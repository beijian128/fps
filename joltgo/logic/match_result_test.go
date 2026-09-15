package logic

import (
	"context"
	"fmt"
	"testing"

	"joltgo/persist"
	accountpb "joltgo/persist/protos"
)

func TestRecordMatchUpdatesBothPlayers(t *testing.T) {
	env := newServiceTestEnv(t)
	ctx := context.Background()
	if err := env.svc.EnsureProfile(ctx, "7"); err != nil {
		t.Fatalf("EnsureProfile(7): %v", err)
	}
	if err := env.svc.EnsureProfile(ctx, "8"); err != nil {
		t.Fatalf("EnsureProfile(8): %v", err)
	}
	// 对手名从 account Hash 只读解析；这里手工塞一条，验证快照落库。
	// 手工塞一条账号 Hash：logic 只读 username 字段，字段号取自生成码。
	env.mr.HSet(persist.AccountKey(8), fmt.Sprintf("%d", accountpb.FieldDBAccount_Username), "bob")

	applied, err := env.svc.RecordMatch(ctx, MatchResult{
		MatchID:    "m1",
		Slots:      []MatchSlot{{UID: "7", Kills: 10, Deaths: 7}, {UID: "8", Kills: 7, Deaths: 10}},
		WinnerSlot: 0, DurationSeconds: 84,
	})
	if err != nil || !applied {
		t.Fatalf("RecordMatch: applied=%v err=%v", applied, err)
	}

	winner, err := env.svc.Profile(ctx, "7")
	if err != nil {
		t.Fatalf("Profile(7): %v", err)
	}
	if winner.Matches != 1 || winner.Wins != 1 || winner.Losses != 0 || winner.Kills != 10 || winner.Deaths != 7 {
		t.Fatalf("胜者统计不对: %+v", winner)
	}
	if winner.XP != 100 { // 50 + 10*5
		t.Fatalf("胜者 XP = %d，期望 100", winner.XP)
	}
	loser, err := env.svc.Profile(ctx, "8")
	if err != nil {
		t.Fatalf("Profile(8): %v", err)
	}
	if loser.Matches != 1 || loser.Wins != 0 || loser.Losses != 1 || loser.XP != 45 { // 10 + 7*5
		t.Fatalf("败者统计不对: %+v", loser)
	}

	if len(winner.Recent) != 1 {
		t.Fatalf("胜者应有一条历史，得到 %d", len(winner.Recent))
	}
	rec := winner.Recent[0]
	if rec.MatchID != "m1" || !rec.Won || rec.Kills != 10 || rec.OpponentKills != 7 ||
		rec.DurationSeconds != 84 || rec.OpponentName != "bob" || rec.EndedAt == 0 {
		t.Fatalf("历史记录不对: %+v", rec)
	}
	if loser.Recent[0].OpponentName != "" {
		t.Fatalf("账号 7 没有 account Hash，对手名应为空串，得到 %q", loser.Recent[0].OpponentName)
	}
}

func TestRecordMatchSkipsIncompleteSlots(t *testing.T) {
	env := newServiceTestEnv(t)
	ctx := context.Background()
	if err := env.svc.EnsureProfile(ctx, "7"); err != nil {
		t.Fatalf("EnsureProfile: %v", err)
	}

	applied, err := env.svc.RecordMatch(ctx, MatchResult{
		MatchID:    "solo",
		Slots:      []MatchSlot{{UID: "7", Kills: 10}, {}},
		WinnerSlot: 0, DurationSeconds: 30,
	})
	if err != nil {
		t.Fatalf("RecordMatch 不应返回错误: %v", err)
	}
	if applied {
		t.Fatal("槽位不齐时不应入账")
	}
	profile, err := env.svc.Profile(ctx, "7")
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if profile.Matches != 0 || profile.XP != 0 || len(profile.Recent) != 0 {
		t.Fatalf("未入账时档案不应变化: %+v", profile)
	}
}

// 中途放弃对局的玩家不进结算：他的档案与历史一行都不写，而**对手照常入账** ——
// 对手确实打完了一局，不能因为有人跑了就白打（spec：放弃者不参与本局结算）。
func TestRecordMatchSkipsPlayerWhoLeft(t *testing.T) {
	env := newServiceTestEnv(t)
	ctx := context.Background()
	for _, id := range []string{"7", "8"} {
		if err := env.svc.EnsureProfile(ctx, id); err != nil {
			t.Fatalf("EnsureProfile(%s): %v", id, err)
		}
	}
	// 对手的名字仍然解析得到（uid 还在，只是带了 left 标记）—— 历史里看得见「跟谁打的」。
	env.mr.HSet(persist.AccountKey(8), fmt.Sprintf("%d", accountpb.FieldDBAccount_Username), "bob")

	applied, err := env.svc.RecordMatch(ctx, MatchResult{
		MatchID: "m1",
		Slots: []MatchSlot{
			{UID: "7", Kills: 10, Deaths: 2},
			{UID: "8", Kills: 2, Deaths: 10, Left: true}, // 中途放弃
		},
		WinnerSlot: 0, DurationSeconds: 90,
	})
	if err != nil || !applied {
		t.Fatalf("RecordMatch: applied=%v err=%v", applied, err)
	}

	winner, err := env.svc.Profile(ctx, "7")
	if err != nil {
		t.Fatalf("Profile(7): %v", err)
	}
	if winner.Matches != 1 || winner.Wins != 1 || winner.Kills != 10 || winner.Deaths != 2 {
		t.Fatalf("在场玩家的战绩应照常入账: %+v", winner)
	}
	if len(winner.Recent) != 1 || winner.Recent[0].OpponentName != "bob" ||
		winner.Recent[0].OpponentKills != 2 {
		t.Fatalf("对手离场前的战绩应留在历史快照里: %+v", winner.Recent)
	}

	leaver, err := env.svc.Profile(ctx, "8")
	if err != nil {
		t.Fatalf("Profile(8): %v", err)
	}
	if leaver.Matches != 0 || leaver.XP != 0 || leaver.Kills != 0 || len(leaver.Recent) != 0 {
		t.Fatalf("放弃对局的玩家不该进结算: %+v", leaver)
	}
}

// 双方都放弃了：没有人的档案要更新，返回未入账（不是错误）。
func TestRecordMatchNotAppliedWhenEveryoneLeft(t *testing.T) {
	env := newServiceTestEnv(t)
	applied, err := env.svc.RecordMatch(context.Background(), MatchResult{
		MatchID: "m1",
		Slots: []MatchSlot{
			{UID: "7", Kills: 3, Left: true},
			{UID: "8", Kills: 4, Left: true},
		},
		WinnerSlot: 1, DurationSeconds: 12,
	})
	if err != nil {
		t.Fatalf("RecordMatch 不该报错: %v", err)
	}
	if applied {
		t.Fatal("一个人都不剩时不该入账")
	}
}

func TestProfileCreatesMissingStatsRow(t *testing.T) {
	env := newServiceTestEnv(t)
	ctx := context.Background()
	if err := env.svc.EnsureProfile(ctx, "7"); err != nil {
		t.Fatalf("EnsureProfile: %v", err)
	}
	// 模拟存量账号：钱包/背包有，档案行被手工删掉。
	env.mr.Del(persist.PlayerProfileKey(7))

	profile, err := env.svc.Profile(ctx, "7")
	if err != nil {
		t.Fatalf("缺档案行时 Profile 应就地补建而不是报错: %v", err)
	}
	if profile.XP != 0 || profile.Matches != 0 {
		t.Fatalf("补建后应是零值档案: %+v", profile)
	}
	if _, ok, err := env.store.GetStats(ctx, 7); err != nil || !ok {
		t.Fatalf("补建后档案行应存在: ok=%v err=%v", ok, err)
	}
}

func TestProfileRejectsUnknownAccount(t *testing.T) {
	env := newServiceTestEnv(t)
	if _, err := env.svc.Profile(context.Background(), "not-a-number"); ReasonOf(err) != ReasonUnauthenticated {
		t.Fatalf("非法 accountID 应返回 unauthenticated，得到 %v", err)
	}
}

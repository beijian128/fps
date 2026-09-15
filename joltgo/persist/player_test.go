package persist

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/alicebob/miniredis/v2"
	redigo "github.com/gomodule/redigo/redis"
	protos "joltgo/persist/protos"
	playerpb "joltgo/persist/protos/player"
)

func newTestPlayerStore(t *testing.T) (*PlayerStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	pool := &redigo.Pool{
		MaxIdle: 2,
		Dial:    func() (redigo.Conn, error) { return redigo.Dial("tcp", mr.Addr()) },
	}
	t.Cleanup(func() { _ = pool.Close() })
	return NewPlayerStore(pool), mr
}

func TestPlayerStoreRoundTrip(t *testing.T) {
	store, mr := newTestPlayerStore(t)
	ctx := context.Background()

	wallet := PlayerWallet{Coins: 1000}
	if err := store.SaveWallet(ctx, 42, wallet); err != nil {
		t.Fatalf("SaveWallet: %v", err)
	}
	bag := PlayerBag{
		Items: []PlayerItem{
			{ItemID: "rifle", Quantity: 2, AcquiredAt: 111},
			{ItemID: "medkit", Quantity: 5, AcquiredAt: 222},
		},
		EquippedPrimaryWeapon: "rifle",
	}
	if err := store.SaveBag(ctx, 42, bag); err != nil {
		t.Fatalf("SaveBag: %v", err)
	}

	if got := PlayerWalletKey(42); got != "REDB#2:42:0" {
		t.Fatalf("PlayerWalletKey = %q", got)
	}
	if got := PlayerBagKey(42); got != "REDB#1:42:0" {
		t.Fatalf("PlayerBagKey = %q", got)
	}
	if !mr.Exists(PlayerWalletKey(42)) || !mr.Exists(PlayerBagKey(42)) {
		t.Fatal("钱包或背包 Hash 未写入")
	}

	gotWallet, ok, err := store.GetWallet(ctx, 42)
	if err != nil || !ok || gotWallet != wallet {
		t.Fatalf("GetWallet: got=%+v ok=%v err=%v", gotWallet, ok, err)
	}
	gotBag, ok, err := store.GetBag(ctx, 42)
	if err != nil || !ok {
		t.Fatalf("GetBag: ok=%v err=%v", ok, err)
	}
	if gotBag.EquippedPrimaryWeapon != "rifle" || len(gotBag.Items) != 2 {
		t.Fatalf("GetBag 不一致: %+v", gotBag)
	}

	if err := store.AddCoins(ctx, 42, 250); err != nil {
		t.Fatalf("AddCoins: %v", err)
	}
	gotWallet, ok, err = store.GetWallet(ctx, 42)
	if err != nil || !ok || gotWallet.Coins != 1250 {
		t.Fatalf("AddCoins 后 GetWallet: got=%+v ok=%v err=%v", gotWallet, ok, err)
	}
	gotBagAfterCoins, ok, err := store.GetBag(ctx, 42)
	if err != nil || !ok || !reflect.DeepEqual(gotBagAfterCoins, gotBag) {
		t.Fatalf("AddCoins 不应修改背包: got=%+v want=%+v ok=%v err=%v", gotBagAfterCoins, gotBag, ok, err)
	}

	conn := store.pool.Get()
	defer conn.Close()
	schema, err := redigo.Int(conn.Do("HGET", PlayerWalletKey(42), playerpb.FieldDBUserWallet_SchemaVersion))
	if err != nil || schema != int(playerpb.DBSchemaVersion_DB_SCHEMA_VERSION_CURRENT) {
		t.Fatalf("schema=%d err=%v", schema, err)
	}

	if err := store.DeleteWallet(ctx, 42); err != nil {
		t.Fatalf("DeleteWallet: %v", err)
	}
	if err := store.DeleteBag(ctx, 42); err != nil {
		t.Fatalf("DeleteBag: %v", err)
	}
	if _, ok, _ := store.GetWallet(ctx, 42); ok {
		t.Fatal("DeleteWallet 后仍存在")
	}
	if _, ok, _ := store.GetBag(ctx, 42); ok {
		t.Fatal("DeleteBag 后仍存在")
	}
}

func TestPlayerStatsAndHistory(t *testing.T) {
	store, mr := newTestPlayerStore(t)
	ctx := context.Background()

	if _, ok, err := store.GetStats(ctx, 7); err != nil || ok {
		t.Fatalf("未建档时 GetStats 应返回 ok=false，得到 ok=%v err=%v", ok, err)
	}
	if err := store.SaveStats(ctx, 7, PlayerStats{}); err != nil {
		t.Fatalf("SaveStats: %v", err)
	}
	if got := PlayerProfileKey(7); got != "REDB#3:7:0" {
		t.Fatalf("PlayerProfileKey = %q", got)
	}
	if got := PlayerHistoryKey(7); got != "playerhist:7" {
		t.Fatalf("PlayerHistoryKey = %q", got)
	}

	delta := PlayerStatsDelta{XP: 55, Kills: 10, Deaths: 3, Matches: 1, Wins: 1}
	if err := store.AddStats(ctx, 7, delta); err != nil {
		t.Fatalf("AddStats: %v", err)
	}
	if err := store.AddStats(ctx, 7, PlayerStatsDelta{XP: 15, Deaths: 2, Matches: 1, Losses: 1}); err != nil {
		t.Fatalf("AddStats 第二次: %v", err)
	}
	stats, ok, err := store.GetStats(ctx, 7)
	want := PlayerStats{XP: 70, Kills: 10, Deaths: 5, Matches: 2, Wins: 1, Losses: 1}
	if err != nil || !ok || stats != want {
		t.Fatalf("GetStats: got=%+v ok=%v err=%v want=%+v", stats, ok, err, want)
	}

	// 历史：写 25 条，只应留下最新 20 条，且新在前。
	for i := 0; i < 25; i++ {
		rec := PlayerMatchRecord{
			MatchID:         fmt.Sprintf("m%02d", i),
			Won:             i%2 == 0,
			Kills:           int32(i),
			Deaths:          int32(i + 1),
			OpponentKills:   int32(i + 2),
			DurationSeconds: 60 + int32(i),
			OpponentName:    fmt.Sprintf("opp%02d", i),
			EndedAt:         int64(1000 + i),
		}
		if err := store.AppendMatchRecord(ctx, 7, rec); err != nil {
			t.Fatalf("AppendMatchRecord(%d): %v", i, err)
		}
	}
	records, err := store.ListMatchRecords(ctx, 7, 20)
	if err != nil {
		t.Fatalf("ListMatchRecords: %v", err)
	}
	if len(records) != 20 {
		t.Fatalf("历史应截断到 20 条，得到 %d", len(records))
	}
	if records[0].MatchID != "m24" || records[19].MatchID != "m05" {
		t.Fatalf("历史应按新在前排列且只保留最近 20 条，得到首=%s 末=%s",
			records[0].MatchID, records[19].MatchID)
	}
	// 截断必须发生在 Redis 侧：只断言「读回来 20 条」抓不住 LTRIM 的 off-by-one
	// （列表里剩 21 条时，LRANGE 0 19 照样返回 20 条）。
	stored, err := mr.List(PlayerHistoryKey(7))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(stored) != 20 {
		t.Fatalf("历史 List 里应只剩 20 条，实际 %d 条", len(stored))
	}
	if !reflect.DeepEqual(records[0], PlayerMatchRecord{
		MatchID: "m24", Won: true, Kills: 24, Deaths: 25, OpponentKills: 26,
		DurationSeconds: 84, OpponentName: "opp24", EndedAt: 1024,
	}) {
		t.Fatalf("历史记录往返不一致: %+v", records[0])
	}

	// 空历史不是错误。
	empty, err := store.ListMatchRecords(ctx, 8, 20)
	if err != nil || len(empty) != 0 {
		t.Fatalf("无历史时应返回空列表与 nil，得到 %v / %v", empty, err)
	}
}

func TestUsernameByID(t *testing.T) {
	store, mr := newTestPlayerStore(t)
	ctx := context.Background()

	if _, ok, err := store.UsernameByID(ctx, 9); err != nil || ok {
		t.Fatalf("账号不存在时应 ok=false，得到 ok=%v err=%v", ok, err)
	}

	// 手工塞一条账号 Hash：logic 只读 username 字段，字段号取自 account 生成码
	// （FieldDBAccount_Username），不手写数字。
	usernameField := fmt.Sprintf("%d", protos.FieldDBAccount_Username)
	mr.HSet(AccountKey(9), usernameField, "alice")
	name, ok, err := store.UsernameByID(ctx, 9)
	if err != nil || !ok || name != "alice" {
		t.Fatalf("UsernameByID: got=%q ok=%v err=%v", name, ok, err)
	}
}

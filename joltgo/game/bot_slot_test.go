package game

import (
	"context"
	"testing"

	"joltgo/bot"
	"joltgo/game/protos"
	"joltgo/sim"
)

func TestNewInstanceMarksBotSlots(t *testing.T) {
	inst := NewInstance(&fakeApp{}, "m1", []string{bot.New("b1"), "10001"})
	defer inst.Stop()

	if !inst.bots[0] {
		t.Fatal("槽位 0 是机器人，应被标记")
	}
	if inst.bots[1] {
		t.Fatal("槽位 1 是真人，不该被标记")
	}
}

func TestPresentUIDsSkipsBotsAndEmptySlots(t *testing.T) {
	inst := NewInstance(&fakeApp{}, "m1", []string{bot.New("b1"), "10001"})
	defer inst.Stop()

	got := inst.presentUIDs()
	if len(got) != 1 || got[0] != "10001" {
		t.Fatalf("推送目标只该有真人，得到 %v", got)
	}

	// 空槽位（单人局 / 对手压根没来）同样不进推送目标。
	empty := NewInstance(&fakeApp{}, "m2", []string{"10001"})
	defer empty.Stop()
	empty.uids = make([]string, sim.MaxPlayers)
	empty.uids[0] = "10001"
	empty.bots[0] = bot.Is(empty.uids[0])
	empty.bots[1] = bot.Is(empty.uids[1])
	if got := empty.presentUIDs(); len(got) != 1 || got[0] != "10001" {
		t.Fatalf("空槽位不该进推送目标，得到 %v", got)
	}
}

func TestCreateSkipsBotRegistryEntries(t *testing.T) {
	comp := New(&fakeApp{})
	reply, err := comp.Create(context.Background(), &protos.CreateGameMsg{
		MatchId: "m-bots",
		Uids:    []string{bot.New("b1"), "10001"},
	})
	if err != nil || reply.Code != 0 {
		t.Fatalf("create: reply=%+v err=%v", reply, err)
	}
	defer comp.Shutdown()

	comp.mu.Lock()
	_, botInRegistry := comp.uidToInst[bot.New("b1")]
	idx, humanInRegistry := comp.uidToIndex["10001"]
	comp.mu.Unlock()

	if botInRegistry {
		t.Fatal("机器人不该进实例注册表：它不会有 game.cmd，也不该被回局查询命中")
	}
	if !humanInRegistry || idx != 1 {
		t.Fatalf("真人应登记在槽位 1，得到 idx=%d ok=%v", idx, humanInRegistry)
	}
}

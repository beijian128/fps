package match

import "testing"

func TestFirstFound(t *testing.T) {
	if _, ok := firstFound(map[string]*RejoinResult{}); ok {
		t.Fatal("没有任何应答时不应命中")
	}
	if _, ok := firstFound(map[string]*RejoinResult{
		"g1": {Found: false},
	}); ok {
		t.Fatal("全部未命中时不应命中")
	}
	got, ok := firstFound(map[string]*RejoinResult{
		"g1": {Found: false},
		"g2": {Found: true, MatchID: "m1", PlayerIdx: 1, GameServerID: "g2"},
	})
	if !ok {
		t.Fatal("有节点命中时应返回 ok")
	}
	if got.MatchID != "m1" || got.PlayerIdx != 1 || got.GameServerID != "g2" {
		t.Fatalf("应命中 g2 的实例，得到 %+v", got)
	}
}

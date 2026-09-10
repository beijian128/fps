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

// 排队期间断线重连会带着同一个 token 再 Join 一次，队列里必须只留最新那条。
func TestRemoveQueued(t *testing.T) {
	q := []queuedPlayer{{uid: "a"}, {uid: "b"}, {uid: "a"}}
	got := removeQueued(q, "a")
	if len(got) != 1 || got[0].uid != "b" {
		t.Fatalf("应只留下 b，得到 %+v", got)
	}
	if len(q) != 3 || q[0].uid != "a" {
		t.Fatalf("removeQueued 不应改动入参，得到 %+v", q)
	}
	if got := removeQueued(q, "zzz"); len(got) != 3 {
		t.Fatalf("没有匹配项时队列应原样返回，得到 %+v", got)
	}
}

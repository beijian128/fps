package game

import "testing"

func TestForgetKeepsUIDsOwnedByAnotherInstance(t *testing.T) {
	c := New(nil)
	old := &Instance{matchID: "m1"}
	fresh := &Instance{matchID: "m2"}
	c.instances = map[string]*Instance{"m1": old, "m2": fresh}
	c.uidToInst = map[string]*Instance{"u": fresh} // 玩家已经匹配进新对局
	c.uidToIndex = map[string]int{"u": 1}

	c.forget(old, "m1", []string{"u"})

	if c.uidToInst["u"] != fresh {
		t.Fatal("旧实例回收不应抹掉新对局的 uid 映射")
	}
	if _, ok := c.instances["m1"]; ok {
		t.Fatal("旧实例应从 instances 里摘掉")
	}
	if _, ok := c.instances["m2"]; !ok {
		t.Fatal("新实例不应受影响")
	}
}

// 正常情况（uid 仍归本实例）必须照常清掉，否则注册表会泄漏。
func TestForgetRemovesOwnUIDs(t *testing.T) {
	c := New(nil)
	inst := &Instance{matchID: "m1"}
	c.instances = map[string]*Instance{"m1": inst}
	c.uidToInst = map[string]*Instance{"u": inst}
	c.uidToIndex = map[string]int{"u": 0}

	c.forget(inst, "m1", []string{"u"})

	if _, ok := c.uidToInst["u"]; ok {
		t.Fatal("属于本实例的 uid 应被摘掉")
	}
	if _, ok := c.uidToIndex["u"]; ok {
		t.Fatal("属于本实例的 uid 索引应被摘掉")
	}
}

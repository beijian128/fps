package replication

import (
	"sort"
	"testing"
)

func newTestStore() *Store {
	s := New()
	s.Declare("Pos", KindVec3)
	s.Declare("Health", KindF32)
	s.Declare("Enemy", KindBool)
	return s
}

func TestSetStoresFinalValueOnly(t *testing.T) {
	s := newTestStore()
	s.Set(7, "Health", F32(10))
	s.Set(7, "Health", F32(20))
	s.Set(7, "Health", F32(30))

	if got := dirtyIDs(s, 7); len(got) != 1 {
		t.Fatalf("同一帧内三次 Set 应只留 1 条脏记录，得到 %d 条", len(got))
	}
	v, ok := s.Get(7, "Health")
	if !ok || v.Floats()[0] != 30 {
		t.Fatalf("应保留终值 30，得到 %v", v.Floats())
	}
}

func TestDestroyClearsValues(t *testing.T) {
	s := newTestStore()
	s.Set(7, "Health", F32(10))

	s.Destroy(7)
	if _, ok := s.Get(7, "Health"); ok {
		t.Fatal("销毁后属性应被清空")
	}
	if !s.dead[7] {
		t.Fatal("销毁应记录 dead 标记")
	}
}

func TestRemoveDropsValue(t *testing.T) {
	s := newTestStore()
	s.Set(7, "Enemy", Bool(true))

	s.Remove(7, "Enemy")
	if _, ok := s.Get(7, "Enemy"); ok {
		t.Fatal("Remove 后属性应消失")
	}
	if got := dirtyIDs(s, 7); len(got) != 0 {
		t.Fatalf("Remove 应同时撤销该属性的脏标记，得到 %v", got)
	}
}

func TestRemoveUndeclaredAttrPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Set/Remove 未声明属性应 panic")
		}
	}()
	newTestStore().Set(1, "Nope", F32(1))
}

func TestDeclareTwicePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("重复声明同名属性应 panic")
		}
	}()
	s := New()
	s.Declare("Pos", KindVec3)
	s.Declare("Pos", KindVec3)
}

func TestResetKeepsDeclarations(t *testing.T) {
	s := newTestStore()
	s.Set(7, "Health", F32(10))
	s.Reset()

	if _, ok := s.Get(7, "Health"); ok {
		t.Fatal("Reset 后应清空终值表")
	}
	s.Set(7, "Health", F32(1)) // 声明还在，不应 panic
}

// ---- 测试辅助（与 store.go 同包，可直接读内部字段） ----

// dirtyIDs 返回实体 id 本帧被标脏的属性 ID（升序，便于比较）。
func dirtyIDs(s *Store, id uint32) []uint32 {
	out := make([]uint32, 0, len(s.dirty[id]))
	for cid := range s.dirty[id] {
		out = append(out, cid)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

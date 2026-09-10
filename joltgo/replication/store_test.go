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
	s.sent[7] = map[uint32]Value{s.attrOf("Health"): F32(10)} // 模拟已下发过

	s.Destroy(7)
	if _, ok := s.Get(7, "Health"); ok {
		t.Fatal("销毁后属性应被清空")
	}
	if !s.dead[7] {
		t.Fatal("销毁应记录 dead 标记")
	}
	if _, ok := s.sent[7]; ok {
		t.Fatal("销毁应同时清掉已下发基线，否则 id 被复用时新实体会因为值相同而发不出去")
	}
}

// id 被回收后立刻重建：新实体必须重新下发，即便它的值和旧实体的相同。
func TestSetAfterDestroyIsDirtyAgain(t *testing.T) {
	s := newTestStore()
	s.Set(7, "Health", F32(10))
	s.sent[7] = map[uint32]Value{s.attrOf("Health"): F32(10)} // 旧实体已下发过 10

	s.Destroy(7)
	s.Set(7, "Health", F32(10)) // 新实体，值恰好相同

	if got := dirtyIDs(s, 7); len(got) != 1 || got[0] != s.attrOf("Health") {
		t.Fatalf("重建的实体必须重新标脏，得到 %v", got)
	}
}

func TestSetKindMismatchPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("值与声明的 Kind 不一致应 panic")
		}
	}()
	newTestStore().Set(1, "Health", Vec3(1, 2, 3)) // Health 声明为 KindF32
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

func TestUndeclaredAttrPanics(t *testing.T) {
	assertPanics := func(name string, fn func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Fatalf("%s 未声明属性应 panic", name)
			}
		}()
		fn()
	}
	s := newTestStore()
	assertPanics("Set", func() { s.Set(1, "Nope", F32(1)) })
	assertPanics("Remove", func() { s.Remove(1, "Nope") })
	assertPanics("Get", func() { s.Get(1, "Nope") })
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
	s.sent[7] = map[uint32]Value{s.attrOf("Health"): F32(10)} // 模拟已下发过

	s.Reset()

	if _, ok := s.Get(7, "Health"); ok {
		t.Fatal("Reset 后应清空终值表")
	}
	if !s.dead[7] {
		t.Fatal("Reset 应保留一条待发的 destroy，否则场景重建后客户端会残留旧实体")
	}
	if _, ok := s.sent[7]; ok {
		t.Fatal("Reset 应同时清掉已下发基线，否则 id 复用后新实体会被静默抑制")
	}
	s.Set(7, "Health", F32(1)) // 声明还在，不应 panic
}

// 场景重建：id 被复用且值恰好与重建前相同时，新实体仍必须重新下发。
func TestSetAfterResetIsDirtyAgain(t *testing.T) {
	s := newTestStore()
	s.Set(7, "Health", F32(10))
	s.sent[7] = map[uint32]Value{s.attrOf("Health"): F32(10)}

	s.Reset()
	s.Set(7, "Health", F32(10)) // 重建，值恰好相同

	if got := dirtyIDs(s, 7); len(got) != 1 || got[0] != s.attrOf("Health") {
		t.Fatalf("Reset 后重建的实体必须重新标脏，得到 %v", got)
	}
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

package replication

import "testing"

func TestFrameDrainIsEmptyWhenNothingChanged(t *testing.T) {
	s := newTestStore()
	s.Set(7, "Health", F32(10))
	s.Drain()
	if f := s.Drain(); len(f.Entities) != 0 {
		t.Fatalf("没有变化的帧应无实体条目，得到 %+v", f.Entities)
	}
}

func TestFrameDrainEmitsRemovedAndDestroy(t *testing.T) {
	s := newTestStore()
	s.Set(7, "Enemy", Bool(true))
	s.Set(8, "Health", F32(5))
	s.Drain()

	s.Remove(7, "Enemy")
	s.Destroy(8)
	f := s.Drain()

	if len(f.Entities) != 2 {
		t.Fatalf("应有 2 个实体条目，得到 %d", len(f.Entities))
	}
	// 升序：7 在前（removed），8 在后（destroy）
	if f.Entities[0].ID != 7 || len(f.Entities[0].Removed) != 1 {
		t.Fatalf("实体 7 应带 1 条 removed，得到 %+v", f.Entities[0])
	}
	if f.Entities[1].ID != 8 || !f.Entities[1].Destroy {
		t.Fatalf("实体 8 应是 destroy，得到 %+v", f.Entities[1])
	}
}

func TestFullCarriesEverythingAndSchema(t *testing.T) {
	s := newTestStore()
	s.Set(7, "Health", F32(10))
	s.Set(9, "Pos", Vec3(1, 2, 3))
	s.Drain()

	f := s.Full()
	if !f.Full {
		t.Fatal("Full() 应带 full 标记")
	}
	if len(f.Schema.Fields) != 3 {
		t.Fatalf("schema 应有 3 个属性，得到 %d", len(f.Schema.Fields))
	}
	if len(f.Entities) != 2 {
		t.Fatalf("全量应包含 2 个实体，得到 %d", len(f.Entities))
	}
	if f.Entities[0].ID != 7 || f.Entities[1].ID != 9 {
		t.Fatalf("全量应按实体 ID 升序，得到 %v / %v", f.Entities[0].ID, f.Entities[1].ID)
	}
	if got := f.Entities[0].Set[0].Value.Floats()[0]; got != 10 {
		t.Fatalf("全量应带终值 10，得到 %v", got)
	}
}

// Full 是发给单个客户端的消息，不能污染增量基线 —— 否则其他在线客户端
// 会再也收不到这些属性的增量。
func TestFullDoesNotResetDeltaBaseline(t *testing.T) {
	s := newTestStore()
	s.Set(7, "Health", F32(10))
	s.Drain() // 基线 = 10

	s.Full() // 某个重连客户端拿走全量

	s.Set(7, "Health", F32(20))
	f := s.Drain()
	if len(f.Entities) != 1 || len(f.Entities[0].Set) != 1 {
		t.Fatalf("Full 之后增量仍应下发 20，得到 %+v", f.Entities)
	}
	if got := f.Entities[0].Set[0].Value.Floats()[0]; got != 20 {
		t.Fatalf("应下发 20，得到 %v", got)
	}
}

func TestSchemaVersionIsStable(t *testing.T) {
	a := newTestStore()
	b := newTestStore()
	if a.Schema().Version != b.Schema().Version {
		t.Fatal("相同的属性表应得到相同的版本哈希")
	}
	c := New()
	c.Declare("Pos", KindVec3)
	if c.Schema().Version == a.Schema().Version {
		t.Fatal("属性表不同应得到不同的版本哈希")
	}
}

func TestOutputIsDeterministic(t *testing.T) {
	s := newTestStore()
	for i := uint32(1); i <= 8; i++ {
		s.Set(i, "Health", F32(float32(i)))
	}
	first := s.Full()
	second := s.Full()
	for i := range first.Entities {
		if first.Entities[i].ID != second.Entities[i].ID {
			t.Fatalf("第 %d 项实体 ID 不稳定：%d vs %d", i, first.Entities[i].ID, second.Entities[i].ID)
		}
	}
}

// 已下发基线的语义：Set 的相等性比较对象是「上一次下发的值」，不是「本帧上一个值」。
// 这两条是「同步系统每 tick 重写全部刚体也不产生流量」的依据。
func TestSetSameAsSentValueIsNotDirty(t *testing.T) {
	s := newTestStore()
	s.Set(7, "Health", F32(10))
	if f := s.Drain(); len(f.Entities) != 1 {
		t.Fatal("首次 Set 应产生 1 条增量")
	}
	s.Set(7, "Health", F32(10)) // 与已下发值相同
	if f := s.Drain(); len(f.Entities) != 0 {
		t.Fatalf("写入相同的值不应产生增量，得到 %+v", f.Entities)
	}
}

func TestSetBackToSentValueCancelsDirty(t *testing.T) {
	s := newTestStore()
	s.Set(7, "Health", F32(10))
	s.Drain() // 10 成为基线

	s.Set(7, "Health", F32(20))
	s.Set(7, "Health", F32(10)) // 改回已下发值 → 撤销脏标记

	if f := s.Drain(); len(f.Entities) != 0 {
		t.Fatalf("改回已下发值应不产生增量，得到 %+v", f.Entities)
	}
}

// id 被回收后同帧重建：destroy 与 set 必须一起下发，客户端才不会漏掉新实体。
func TestDestroyAndRebuildInSameFrameEmitsBoth(t *testing.T) {
	s := newTestStore()
	s.Set(7, "Health", F32(10))
	s.Drain()

	s.Destroy(7)
	s.Set(7, "Health", F32(10)) // 新实体，值恰好与旧实体相同

	f := s.Drain()
	if len(f.Entities) != 1 {
		t.Fatalf("应产出 1 个实体条目，得到 %d", len(f.Entities))
	}
	ed := f.Entities[0]
	if !ed.Destroy {
		t.Fatal("应带 destroy")
	}
	if len(ed.Set) != 1 {
		t.Fatalf("应同时带新实体的 set，得到 %+v", ed.Set)
	}

	// 基线已按新值重建，下一帧不应重复下发。
	if n := s.Drain(); len(n.Entities) != 0 {
		t.Fatalf("重建后不应重复下发，得到 %+v", n.Entities)
	}
}

// removed 下发时必须一并清掉基线，否则之后写回同一个值会被抑制、客户端再也收不到。
func TestRemovedAttrCanBeReSetAndIsSentAgain(t *testing.T) {
	s := newTestStore()
	s.Set(7, "Enemy", Bool(true))
	s.Drain()

	s.Remove(7, "Enemy")
	if f := s.Drain(); len(f.Entities) != 1 || len(f.Entities[0].Removed) != 1 {
		t.Fatalf("移除应下发 1 条 removed，得到 %+v", f.Entities)
	}

	s.Set(7, "Enemy", Bool(true)) // 写回完全相同的值
	f := s.Drain()
	if len(f.Entities) != 1 || len(f.Entities[0].Set) != 1 {
		t.Fatalf("移除后写回同一个值也必须重新下发，得到 %+v", f.Entities)
	}
}

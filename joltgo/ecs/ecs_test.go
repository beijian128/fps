package ecs

import "testing"

type pos struct{ x, y float32 }
type tag struct{}

func TestNewEntityStartsAtLogicBase(t *testing.T) {
	w := New()
	e1 := w.NewEntity()
	e2 := w.NewEntity()
	if e1 != logicEntityBase || e2 != logicEntityBase+1 {
		t.Fatalf("NewEntity 应从 logicEntityBase 起分配，得到 %d, %d", e1, e2)
	}
}

func TestAddGetHasOverwrite(t *testing.T) {
	w := New()
	e := w.NewEntity()

	if Has[pos](w, e) {
		t.Fatal("新实体不应有组件")
	}
	if _, ok := Get[pos](w, e); ok {
		t.Fatal("Get 不存在的组件应返回 false")
	}

	Add(w, e, pos{1, 2})
	if !Has[pos](w, e) {
		t.Fatal("Add 后 Has 应为 true")
	}
	got, ok := Get[pos](w, e)
	if !ok || *got != (pos{1, 2}) {
		t.Fatalf("Get 得到 %v (ok=%v)", *got, ok)
	}

	Add(w, e, pos{3, 4}) // 覆盖
	got, _ = Get[pos](w, e)
	if *got != (pos{3, 4}) {
		t.Fatalf("Add 应覆盖已有组件，得到 %v", *got)
	}
}

func TestGetPointerMutates(t *testing.T) {
	w := New()
	e := w.NewEntity()
	Add(w, e, pos{1, 2})
	if p, ok := Get[pos](w, e); ok {
		p.x = 10
	}
	got, _ := Get[pos](w, e)
	if got.x != 10 {
		t.Fatalf("通过指针修改应写回世界，得到 %v", *got)
	}
}

func TestInvalidEntityIgnoredByAdd(t *testing.T) {
	w := New()
	Add(w, InvalidEntity, pos{1, 2})
	if Count[pos](w) != 0 {
		t.Fatal("Add(InvalidEntity) 应被忽略")
	}
}

func TestPhysicalEntityIDSpace(t *testing.T) {
	// 物理实体直接使用包装层 body id（从 1 起），与逻辑实体空间不重叠。
	w := New()
	Add(w, Entity(1), pos{1, 1})
	Add(w, Entity(2), pos{2, 2})
	le := w.NewEntity()
	if Has[pos](w, le) {
		t.Fatal("新分配的逻辑实体不应与物理实体混淆")
	}
	if Count[pos](w) != 2 {
		t.Fatalf("Count 应为 2，得到 %d", Count[pos](w))
	}
}

func TestEachAndCount(t *testing.T) {
	w := New()
	for i := 0; i < 5; i++ {
		e := w.NewEntity()
		Add(w, e, pos{float32(i), 0})
		if i%2 == 0 {
			Add(w, e, tag{})
		}
	}
	if Count[pos](w) != 5 || Count[tag](w) != 3 {
		t.Fatalf("Count 应为 5/3，得到 %d/%d", Count[pos](w), Count[tag](w))
	}

	sum := float32(0)
	n := 0
	Each(w, func(_ Entity, p *pos) {
		sum += p.x
		n++
	})
	if n != 5 || sum != 10 {
		t.Fatalf("Each 应覆盖全部实体（n=%d, sum=%v）", n, sum)
	}
}

func TestRemoveSwapSemantics(t *testing.T) {
	w := New()
	es := make([]Entity, 4)
	for i := range es {
		es[i] = w.NewEntity()
		Add(w, es[i], pos{float32(i), 0})
	}
	Remove[pos](w, es[0])
	if Has[pos](w, es[0]) {
		t.Fatal("Remove 后 Has 应为 false")
	}
	if Count[pos](w) != 3 {
		t.Fatalf("Remove 后 Count 应为 3，得到 %d", Count[pos](w))
	}
	// swap-remove 会改变顺序，但其余实体必须全部还在。
	seen := map[float32]bool{}
	Each(w, func(_ Entity, p *pos) {
		seen[p.x] = true
	})
	for _, x := range []float32{1, 2, 3} {
		if !seen[x] {
			t.Fatalf("Remove 后剩余实体应包含 x=%v", x)
		}
	}
}

func TestDestroyRemovesAllComponents(t *testing.T) {
	w := New()
	e := w.NewEntity()
	Add(w, e, pos{1, 2})
	Add(w, e, tag{})
	w.Destroy(e)
	if Has[pos](w, e) || Has[tag](w, e) {
		t.Fatal("Destroy 后实体不应再有任何组件")
	}
	if Count[pos](w) != 0 || Count[tag](w) != 0 {
		t.Fatal("Destroy 后各存储应为空")
	}
}

func TestTypesWithSameNameDifferentPackages(t *testing.T) {
	// 两个不同类型（即使结构相同）应各占一个存储。
	type other struct{ v int }
	w := New()
	e := w.NewEntity()
	Add(w, e, tag{})
	Add(w, e, other{7})
	if Count[tag](w) != 1 || Count[other](w) != 1 {
		t.Fatal("不同类型应有独立存储")
	}
}

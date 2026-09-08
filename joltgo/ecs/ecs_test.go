package ecs

import "testing"

type pos struct{ x, y float32 }
type vel struct{ x, y float32 }
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
		t.Fatalf("Each 应覆盖全部 archetype 的实体（n=%d, sum=%v）", n, sum)
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
	// 两个不同类型（即使结构相同）应各占一列。
	type other struct{ v int }
	w := New()
	e := w.NewEntity()
	Add(w, e, tag{})
	Add(w, e, other{7})
	if Count[tag](w) != 1 || Count[other](w) != 1 {
		t.Fatal("不同类型应有独立列")
	}
}

// ---- archetype 特有行为 ----

func TestAddRemoveMovesPreserveData(t *testing.T) {
	// Add/Remove 会让实体在 archetype 间反复搬家，各组件值不能串行或丢失。
	w := New()
	e := w.NewEntity()
	Add(w, e, pos{1, 2})
	Add(w, e, tag{})     // {pos} → {pos,tag}
	Add(w, e, vel{3, 4}) // {pos,tag} → {pos,tag,vel}
	Remove[pos](w, e)    // → {tag,vel}
	if Has[pos](w, e) {
		t.Fatal("Remove 后不应再有 pos")
	}
	if v, ok := Get[vel](w, e); !ok || *v != (vel{3, 4}) {
		t.Fatalf("搬家后 vel 应为 {3,4}，得到 %v (ok=%v)", v, ok)
	}
	if _, ok := Get[tag](w, e); !ok {
		t.Fatal("搬家后 tag 应保留")
	}
	if Count[vel](w) != 1 || Count[tag](w) != 1 {
		t.Fatalf("Count 应为 1/1，得到 %d/%d", Count[vel](w), Count[tag](w))
	}
}

func TestSwapRemoveKeepsNeighborsIntact(t *testing.T) {
	// 一行搬走后，源 archetype 的末行被换到空缺处，其余实体的数据与
	// rec 映射都不能损坏（archetype 实现最易错的点）。
	w := New()
	es := make([]Entity, 4)
	for i := range es {
		es[i] = w.NewEntity()
		Add(w, es[i], pos{float32(i), 0})
	}
	Add(w, es[1], tag{}) // es[1] 搬去 {pos,tag}，源 {pos} 的末行换到它的位置
	if Count[pos](w) != 4 {
		t.Fatalf("Count[pos]=%d, 期望 4", Count[pos](w))
	}
	// 搬走的实体保留全部数据
	if p, ok := Get[pos](w, es[1]); !ok || p.x != 1 {
		t.Fatalf("搬走实体 pos 应为 1，得到 %v (ok=%v)", p, ok)
	}
	if _, ok := Get[tag](w, es[1]); !ok {
		t.Fatal("搬走实体应有 tag")
	}
	// 剩余实体的值与实体一一对应，不能被 swap-remove 破坏
	for _, i := range []int{0, 2, 3} {
		if p, ok := Get[pos](w, es[i]); !ok || p.x != float32(i) {
			t.Fatalf("es[%d] 的 pos 应为 %v，得到 %v (ok=%v)", i, float32(i), p, ok)
		}
	}
}

func TestEachWithRowAccess(t *testing.T) {
	w := New()
	for i := 0; i < 4; i++ {
		e := w.NewEntity()
		Add(w, e, pos{float32(i), 0})
		if i%2 == 0 {
			Add(w, e, tag{})
		}
	}
	n := 0
	EachWith(w, func(_ Entity, p *pos, row Row) {
		n++
		want := p.x == 0 || p.x == 2
		if RowHas[tag](row) != want {
			t.Fatalf("行视图 Has 与组件集合不一致: p.x=%v", p.x)
		}
		if _, ok := RowGet[tag](row); ok != want {
			t.Fatalf("行视图 Get 与组件集合不一致: p.x=%v", p.x)
		}
	})
	if n != 4 {
		t.Fatalf("EachWith 应遍历 4 个实体，得到 %d", n)
	}
}

func TestRowGetPointerMutates(t *testing.T) {
	w := New()
	e := w.NewEntity()
	Add(w, e, pos{1, 2})
	Add(w, e, vel{5, 6})
	EachWith(w, func(_ Entity, p *pos, row Row) {
		if v, ok := RowGet[vel](row); ok {
			v.x = 99
		}
	})
	got, _ := Get[vel](w, e)
	if got.x != 99 {
		t.Fatalf("行视图指针修改应写回，得到 %v", *got)
	}
}

func TestDestroyThenReAdd(t *testing.T) {
	// Destroy 把实体搬回空 archetype，之后可重新挂组件。
	w := New()
	e := w.NewEntity()
	Add(w, e, pos{1, 2})
	Add(w, e, tag{})
	w.Destroy(e)
	Add(w, e, pos{7, 8})
	p, ok := Get[pos](w, e)
	if !ok || *p != (pos{7, 8}) {
		t.Fatalf("Destroy 后重新 Add 应得到新值，得到 %v (ok=%v)", p, ok)
	}
	if Has[tag](w, e) {
		t.Fatal("Destroy 后旧组件不应残留")
	}
	if Count[pos](w) != 1 {
		t.Fatalf("Count 应为 1，得到 %d", Count[pos](w))
	}
}

func TestExternalIDAcrossArchetypes(t *testing.T) {
	// 外部 id（物理刚体）从不登记开始，Add 各组件同样构成 archetype。
	w := New()
	Add(w, Entity(1), pos{1, 1})
	Add(w, Entity(1), vel{2, 2}) // {pos} → {pos,vel}
	Add(w, Entity(1), tag{})     // → {pos,vel,tag}
	if p, ok := Get[pos](w, Entity(1)); !ok || *p != (pos{1, 1}) {
		t.Fatalf("外部 id 搬家后 pos 应保留，得到 %v (ok=%v)", p, ok)
	}
	if Count[vel](w) != 1 {
		t.Fatalf("Count[vel]=%d, 期望 1", Count[vel](w))
	}
	Remove[pos](w, Entity(1))
	if Has[pos](w, Entity(1)) {
		t.Fatal("外部 id Remove 后不应再有 pos")
	}
	if _, ok := Get[vel](w, Entity(1)); !ok {
		t.Fatal("外部 id Remove pos 后 vel 应保留")
	}
}

// ---- Bundle 批量挂载 ----

func TestAdd3BundleSingleMove(t *testing.T) {
	// Bundle 式批量挂载：一次 Add3 只产生一个目标 archetype（依次 Add 会留下
	// {pos}、{pos,vel} 两个中间 archetype），且各组件值正确。
	w := New()
	e := w.NewEntity()
	Add3(w, e, pos{1, 2}, vel{3, 4}, tag{})
	if len(w.archs) != 2 { // empty + {pos,vel,tag}
		t.Fatalf("Add3 应只产生一个目标 archetype，共 %d 个", len(w.archs))
	}
	if p, ok := Get[pos](w, e); !ok || *p != (pos{1, 2}) {
		t.Fatalf("pos 应为 {1,2}，得到 %v (ok=%v)", p, ok)
	}
	if v, ok := Get[vel](w, e); !ok || *v != (vel{3, 4}) {
		t.Fatalf("vel 应为 {3,4}，得到 %v (ok=%v)", v, ok)
	}
	if _, ok := Get[tag](w, e); !ok {
		t.Fatal("tag 应已挂载")
	}
}

func TestAdd3OverwriteAndAppend(t *testing.T) {
	// 已有组件覆盖、缺失组件追加，且不产生中间 archetype。
	w := New()
	e := w.NewEntity()
	Add(w, e, pos{1, 2})
	Add3(w, e, pos{7, 8}, vel{3, 4}, tag{})
	if p, _ := Get[pos](w, e); *p != (pos{7, 8}) {
		t.Fatalf("Add3 应覆盖已有 pos，得到 %v", *p)
	}
	if v, ok := Get[vel](w, e); !ok || *v != (vel{3, 4}) {
		t.Fatalf("vel 应为 {3,4}，得到 %v (ok=%v)", v, ok)
	}
	if len(w.archs) != 3 { // empty + {pos} + {pos,vel,tag}
		t.Fatalf("archs 应为 3，得到 %d", len(w.archs))
	}
}

// ---- 查询缓存与排除过滤 ----

func TestQueryWithout(t *testing.T) {
	w := New()
	for i := 0; i < 4; i++ {
		e := w.NewEntity()
		Add(w, e, pos{float32(i), 0})
		if i%2 == 0 {
			Add(w, e, tag{})
		}
	}
	q := Without[tag](NewQuery[pos]())
	n := 0
	QueryEach(w, &q, func(_ Entity, p *pos, row Row) {
		n++
		if RowHas[tag](row) {
			t.Fatal("排除过滤的查询不应返回带 tag 的行")
		}
		if p.x == 0 || p.x == 2 {
			t.Fatalf("排除过滤应整表跳过带 tag 的实体，得到 p.x=%v", p.x)
		}
	})
	if n != 2 {
		t.Fatalf("QueryEach 应遍历 2 个实体，得到 %d", n)
	}
	// 复用缓存：结果一致
	n = 0
	QueryEach(w, &q, func(_ Entity, _ *pos, _ Row) { n++ })
	if n != 2 {
		t.Fatalf("缓存复用后应仍为 2，得到 %d", n)
	}
}

func TestQueryRebuildsAfterNewArchetype(t *testing.T) {
	w := New()
	e := w.NewEntity()
	Add(w, e, pos{})
	q := Without[tag](NewQuery[pos]())
	n := 0
	QueryEach(w, &q, func(_ Entity, _ *pos, _ Row) { n++ })
	if n != 1 {
		t.Fatalf("初始匹配应为 1，得到 %d", n)
	}
	Add(w, e, tag{}) // 产生新 archetype {pos,tag}，查询缓存应失效重建
	n = 0
	QueryEach(w, &q, func(_ Entity, _ *pos, _ Row) { n++ })
	if n != 0 {
		t.Fatalf("新 archetype 后匹配应为 0，得到 %d", n)
	}
}

// ---- 多列绑定查询 ----

func TestQueryEach2(t *testing.T) {
	w := New()
	e1, e2, e3 := w.NewEntity(), w.NewEntity(), w.NewEntity()
	Add(w, e1, pos{1, 0})
	Add(w, e1, vel{1, 1})
	Add(w, e2, pos{2, 0})
	Add(w, e2, vel{2, 2})
	Add(w, e3, pos{3, 0}) // 只有 pos，不应被双列查询匹配
	q := NewQuery2[pos, vel]()
	n := 0
	QueryEach2(w, &q, func(_ Entity, p *pos, v *vel, _ Row) {
		n++
		if *v != (vel{p.x, p.x}) {
			t.Fatalf("多列直取值不一致: p=%v v=%v", *p, *v)
		}
	})
	if n != 2 {
		t.Fatalf("QueryEach2 应遍历 2 个实体（同时有 pos 和 vel），得到 %d", n)
	}
}

// ---- 实体 id 回收 ----

func TestNewEntityRecyclesDestroyedIDs(t *testing.T) {
	w := New()
	e1 := w.NewEntity()
	Add(w, e1, pos{1, 1})
	w.Destroy(e1)
	if got := len(w.empty.rows); got != 1 {
		t.Fatalf("Destroy 后空 archetype 应留 1 行，得到 %d", got)
	}
	e2 := w.NewEntity()
	if e2 != e1 {
		t.Fatalf("应复用销毁的逻辑实体 id，得到 %d（期望 %d）", e2, e1)
	}
	if Has[pos](w, e2) {
		t.Fatal("复用的实体不应残留旧组件")
	}
	Add(w, e2, pos{9, 9})
	p, _ := Get[pos](w, e2)
	if *p != (pos{9, 9}) {
		t.Fatalf("复用实体应拿到新组件值，得到 %v", *p)
	}
}

func TestNewEntitySkipsResurrectedIDs(t *testing.T) {
	// Destroy 后直接 Add 复活的实体不在空 archetype 里，NewEntity 应跳过它。
	w := New()
	e1 := w.NewEntity()
	w.Destroy(e1)
	Add(w, e1, pos{1, 1}) // 直接复活
	e2 := w.NewEntity()
	if e2 == e1 {
		t.Fatal("被直接 Add 复活的 id 不应再被 NewEntity 复用")
	}
	if Has[pos](w, e2) {
		t.Fatal("新实体不应挂上复活实体的组件")
	}
	p, _ := Get[pos](w, e1)
	if *p != (pos{1, 1}) {
		t.Fatalf("复活实体的数据不应被破坏，得到 %v", *p)
	}
}

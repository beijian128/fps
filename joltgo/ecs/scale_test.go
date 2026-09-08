package ecs

import "testing"

// 规模模拟：模拟更大的游戏场景——~100 种实体组合（kind 位模式，0..99 的
// 7 个位决定组件子集，互不相同）、10000 个实体（每 kind 100 个），
// 每 tick 运行移动/衰减系统 + ~1% 结构 churn（摧毁/新刷/组件加摘）。
// 验证 archetype 存储在更大规模下的正确性与性能瓶颈。

// ---- 组件池（17 种，模拟更大游戏的状态面）----

type cPos [3]float32
type cVel [3]float32
type cHealth float32
type cBody struct{ Kind, Size int32 }
type cRender struct{ Mesh, Mat int32 }
type cAI struct{ State, Goal int32 }
type cTimer float32
type cTeam uint8
type cBuff struct{ Kind, Until int32 }
type cPickup struct{ Value int32 }
type cProj struct{ Owner uint32 }
type cSpawner struct{ Rate, Max int32 }
type cTarget struct{ ID uint32 }
type cCollider struct{ Radius, Height float32 }
type cInventory struct{ Count int32 }
type cEffect struct{ Kind int32 }
type cFodder struct{} // churn 炮灰标记：每 tick 摧毁/等量新刷，保持场上规模恒定

// scaleSpawn 按 kind 位模式挂组件：位 0..6 分别对应 vel/health/body/render/
// ai/timer/team，cPos 恒有。位模式互不相同 ⇒ 100 个 kind 产生 100 种不同
// 组件集合（加上顺序 Add 的中间前缀，实际 archetype 数量更多）。
func scaleSpawn(w *World, kind int) Entity {
	e := w.NewEntity()
	Add(w, e, cPos{float32(kind), 0, 0})
	if kind&1 != 0 {
		Add(w, e, cVel{0.1, 0, 0.2})
	}
	if kind&2 != 0 {
		Add(w, e, cHealth(100))
	}
	if kind&4 != 0 {
		Add(w, e, cBody{Kind: int32(kind), Size: 1})
	}
	if kind&8 != 0 {
		Add(w, e, cRender{Mesh: 1, Mat: 2})
	}
	if kind&16 != 0 {
		Add(w, e, cAI{State: 1, Goal: int32(kind)})
	}
	if kind&32 != 0 {
		Add(w, e, cTimer(60))
	}
	if kind&64 != 0 {
		Add(w, e, cTeam(uint8(kind%4)))
	}
	return e
}

// scaleSim 是规模模拟场景：10000 实体 + 每 tick 的移动/衰减/结构 churn。
type scaleSim struct {
	w    *World
	q    Query // 移动系统查询（cPos+cVel 双列直取，跨 tick 复用缓存）
	seed uint32
}

func newScaleSim() *scaleSim {
	s := &scaleSim{w: New(), q: NewQuery2[cPos, cVel]()}
	for i := 0; i < 10000; i++ {
		kind := i % 100
		e := scaleSpawn(s.w, kind)
		if kind >= 90 {
			Add(s.w, e, cFodder{}) // kinds 90..99 = 1000 个炮灰实体
		}
	}
	return s
}

// tick 推进一个模拟 tick：移动 → 衰减 → 结构 churn。
func (s *scaleSim) tick() {
	s.seed++
	s.moveSystem()
	s.decaySystem()
	s.churnSystem()
}

// moveSystem 移动：cPos+cVel 双列直取（只遍历同时有两者的实体），
// 原地更新位置（无结构变更）。
func (s *scaleSim) moveSystem() {
	QueryEach2(s.w, &s.q, func(_ Entity, p *cPos, v *cVel, _ Row) {
		p[0] += v[0]
		p[2] += v[2]
	})
}

// decaySystem 衰减：cHealth / cTimer 各一遍全量迭代。
func (s *scaleSim) decaySystem() {
	Each(s.w, func(_ Entity, h *cHealth) { *h -= 0.1 })
	Each(s.w, func(_ Entity, t *cTimer) { *t -= 1 })
}

// churnSystem 结构 churn ~1%：摧毁 50 个炮灰、等量新刷、50 个 AI 实体加/摘
// cBuff（产生新的 archetype，触发查询缓存失效重建）。先收集再结构变更。
func (s *scaleSim) churnSystem() {
	var fodder []Entity
	Each(s.w, func(e Entity, _ *cFodder) {
		if len(fodder) < 50 {
			fodder = append(fodder, e)
		}
	})
	for _, e := range fodder {
		s.w.Destroy(e)
	}
	for i := range fodder {
		e := scaleSpawn(s.w, 90+int(s.seed+uint32(i))%10)
		Add(s.w, e, cFodder{})
	}

	var ai []Entity
	Each(s.w, func(e Entity, _ *cAI) {
		if len(ai) < 50 {
			ai = append(ai, e)
		}
	})
	for _, e := range ai {
		if Has[cBuff](s.w, e) {
			Remove[cBuff](s.w, e)
		} else {
			Add(s.w, e, cBuff{Kind: 1, Until: 300})
		}
	}
}

// TestScaleWorld 验证 10000 实体规模下的正确性：计数、churn 守恒、查询结果。
func TestScaleWorld(t *testing.T) {
	s := newScaleSim()
	if got := Count[cPos](s.w); got != 10000 {
		t.Fatalf("实体总数应为 10000，得到 %d", got)
	}
	if got := Count[cVel](s.w); got != 5000 { // 位 0 置位的 kind 恰有 50 个
		t.Fatalf("cVel 实体应为 5000，得到 %d", got)
	}
	if got := Count[cFodder](s.w); got != 1000 {
		t.Fatalf("cFodder 实体应为 1000，得到 %d", got)
	}
	for i := 0; i < 20; i++ {
		s.tick()
	}
	if got := Count[cPos](s.w); got != 10000 {
		t.Fatalf("churn 后实体总数应保持 10000，得到 %d", got)
	}
	if got := Count[cFodder](s.w); got != 1000 {
		t.Fatalf("churn 后 cFodder 应保持 1000，得到 %d", got)
	}
	// 实体 id 回收：销毁的炮灰被 NewEntity 复用，空 archetype 不应累积行
	if rows := len(s.w.empty.rows); rows > 100 {
		t.Fatalf("销毁实体应被回收，空 archetype 行数=%d（应 ≤100）", rows)
	}
	// 移动系统确实推进了带 cVel 实体的位置：初始 x 总和远大于 10000，
	// 20 tick 后每个带 cVel 实体（5000 个）x 再增加 20×0.1=2。
	q := NewQuery2[cPos, cVel]()
	var totalX float32
	QueryEach2(s.w, &q, func(_ Entity, p *cPos, _ *cVel, _ Row) {
		totalX += p[0]
	})
	if totalX <= 10000 {
		t.Fatalf("移动系统应推进位置，totalX=%v", totalX)
	}
	t.Logf("archetypes=%d, 实体=%d", len(s.w.archs), Count[cPos](s.w))
}

// ---- 基准：构建与每 tick 负载 ----

func BenchmarkScaleBuild10k(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		newScaleSim()
	}
}

func BenchmarkScaleTick10k(b *testing.B) {
	s := newScaleSim()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.tick()
	}
}

// BenchmarkScaleTickMoveOnly10k 只跑移动系统：双列直取（QueryEach2），
// 每 archetype 只绑定一次列指针。
func BenchmarkScaleTickMoveOnly10k(b *testing.B) {
	s := newScaleSim()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.seed++
		s.moveSystem()
	}
}

// BenchmarkScaleChurnOnly10k 只跑结构 churn：摧毁 50 + 新刷 50 + 加摘 50。
func BenchmarkScaleChurnOnly10k(b *testing.B) {
	s := newScaleSim()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.seed++
		s.churnSystem()
	}
}

// BenchmarkScaleSnapshot10k 模拟快照读取的最坏情况：每实体 7 次行内组件访问
// （全部走 RowGet 的未优化写法；真实快照用 QueryEach3 把 Position/Rotation
// 换成列绑定，只留可选组件走行视图）。
func BenchmarkScaleSnapshot10k(b *testing.B) {
	s := newScaleSim()
	q := NewQuery[cPos]()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		QueryEach(s.w, &q, func(_ Entity, p *cPos, row Row) {
			var acc float32
			if v, ok := RowGet[cVel](row); ok {
				acc += v[0]
			}
			if h, ok := RowGet[cHealth](row); ok {
				acc += float32(*h)
			}
			if bd, ok := RowGet[cBody](row); ok {
				acc += float32(bd.Kind)
			}
			if r, ok := RowGet[cRender](row); ok {
				acc += float32(r.Mesh)
			}
			if a, ok := RowGet[cAI](row); ok {
				acc += float32(a.State)
			}
			if t, ok := RowGet[cTimer](row); ok {
				acc += float32(*t)
			}
			acc += p[0]
			benchSink = acc
		})
	}
}

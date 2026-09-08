package ecs

import "testing"

// 这些基准用于量化泛型 API 的「接口擦除开销」（reflect.Type map 查找 + 类型
// 断言）。每个基准旁边放一个同等工作的裸切片基线（理论下限），差值即
// API 层开销。行内遍历（Each/EachWith）循环体无反射，重点看 per-op 差值。

// benchSink 防止编译器把无副作用循环整体优化掉。
var benchSink float32

func benchWorld(n int) *World {
	w := New()
	for i := 0; i < n; i++ {
		e := w.NewEntity()
		Add(w, e, pos{float32(i), 0})
		Add(w, e, vel{float32(i * 2), 0})
	}
	return w
}

// ---- 热路径：Add 覆盖（syncSystem 每 tick 对每个刚体调用两次）----

func BenchmarkAddOverwrite(b *testing.B) {
	w := New()
	e := w.NewEntity()
	Add(w, e, pos{1, 1})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Add(w, e, pos{float32(i), 1})
	}
}

func BenchmarkAddOverwriteBaseline(b *testing.B) {
	data := make([]pos, 1)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		data[0] = pos{float32(i), 1}
	}
}

// ---- 热路径：Get（逐实体点查）----

func BenchmarkGet(b *testing.B) {
	w := benchWorld(100)
	var e Entity
	Each(w, func(ee Entity, _ *pos) {
		e = ee
	})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if p, ok := Get[pos](w, e); ok {
			benchSink = p.x
		}
	}
}

func BenchmarkGetBaseline(b *testing.B) {
	data := []pos{{1, 2}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchSink = data[0].x
	}
}

// ---- 热路径：Each / EachWith 遍历 100 实体 ----

func BenchmarkEach100(b *testing.B) {
	w := benchWorld(100)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Each(w, func(_ Entity, p *pos) {
			benchSink = p.x
		})
	}
}

func BenchmarkEach100Baseline(b *testing.B) {
	data := make([]pos, 100)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := range data {
			benchSink = data[j].x
		}
	}
}

func BenchmarkEachWithRow100(b *testing.B) {
	w := benchWorld(100)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EachWith(w, func(_ Entity, p *pos, row Row) {
			if v, ok := RowGet[vel](row); ok {
				benchSink = p.x + v.x
			}
		})
	}
}

// ---- 冷路径：组件集合变化触发 archetype 搬家（spawn/destroy 时，走 reflect）----

func BenchmarkMove(b *testing.B) {
	w := New()
	e := w.NewEntity()
	Add(w, e, pos{1, 2})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Add(w, e, vel{float32(i), 0}) // {pos} → {pos,vel}
		Remove[vel](w, e)             // {pos,vel} → {pos}
	}
}

func BenchmarkMove3Components(b *testing.B) {
	w := New()
	e := w.NewEntity()
	Add(w, e, pos{1, 2})
	Add(w, e, vel{3, 4})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Add(w, e, tag{})  // {pos,vel} → {pos,vel,tag}
		Remove[tag](w, e) // 搬回
	}
}

// ---- Bundle vs 依次 Add（spawn 路径：新实体挂 3 个组件后销毁）----

func BenchmarkSpawnBundle(b *testing.B) {
	w := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e := w.NewEntity()
		Add3(w, e, pos{1, 2}, vel{3, 4}, tag{})
		w.Destroy(e)
	}
}

func BenchmarkSpawnSequential(b *testing.B) {
	w := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e := w.NewEntity()
		Add(w, e, pos{1, 2})
		Add(w, e, vel{3, 4})
		Add(w, e, tag{})
		w.Destroy(e)
	}
}

// ---- 缓存查询（排除过滤）vs EachWith + 行内判断 ----

func benchWorldMixed(n int) *World {
	w := New()
	for i := 0; i < n; i++ {
		e := w.NewEntity()
		Add(w, e, pos{float32(i), 0})
		Add(w, e, vel{float32(i * 2), 0})
		if i%2 == 0 {
			Add(w, e, tag{})
		}
	}
	return w
}

func BenchmarkQueryEach50of100(b *testing.B) {
	w := benchWorldMixed(100) // 一半带 tag，被排除过滤整表跳过
	q := Without[tag](NewQuery[pos]())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		QueryEach(w, &q, func(_ Entity, p *pos, row Row) {
			if v, ok := RowGet[vel](row); ok {
				benchSink = p.x + v.x
			}
		})
	}
}

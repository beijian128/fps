package game

import (
	"testing"

	"joltgo/game/protos"
	"joltgo/replication"
)

// toFrame / toSchema / toAttrValue 是 replication.Frame → protobuf 唯一的手写转换。
// 客户端解码的另一半有 frame_decode_test.gd 专门覆盖，服务端这一半却一直没测：
// 若给 replication.Kind 加了新枚举值却漏加 toAttrValue 的 case，它会静默编成一个
// 空的 AttrValue（Id 带上了、值丢失），而两个端到端冒烟测试只断言 step 与 match
// id/idx，抓不到。所以这里对全部七个 Kind 逐字段往返，把这条路径钉死。

// 七个 Kind 各自应落进哪个 AttrValue 字段。用一个断言函数而不是逐条 if，
// 是为了同时钉住「其余四个字段保持零值」——否则漏加 case 时值虽然没写错字段，
// 但可能残留上一次的值（当前实现每次新建 AttrValue，但测试不该依赖这一点）。
func TestToAttrValueMapsEveryKindToItsOwnField(t *testing.T) {
	cases := []struct {
		name  string
		val   replication.Value
		check func(t *testing.T, pv *protos.AttrValue)
	}{
		{
			name: "KindF32",
			val:  replication.F32(1.5),
			check: func(t *testing.T, pv *protos.AttrValue) {
				assertFloats(t, pv, []float32{1.5})
			},
		},
		{
			name: "KindI32",
			val:  replication.I32(-7),
			check: func(t *testing.T, pv *protos.AttrValue) {
				if pv.I != -7 {
					t.Fatalf("KindI32 应写进 I，得到 %d", pv.I)
				}
				assertOthersZero(t, pv, "I")
			},
		},
		{
			name: "KindBool",
			val:  replication.Bool(true),
			check: func(t *testing.T, pv *protos.AttrValue) {
				if !pv.B {
					t.Fatal("KindBool 应写进 B，得到 false")
				}
				assertOthersZero(t, pv, "B")
			},
		},
		{
			name: "KindStr",
			val:  replication.Str("hello"),
			check: func(t *testing.T, pv *protos.AttrValue) {
				if pv.S != "hello" {
					t.Fatalf("KindStr 应写进 S，得到 %q", pv.S)
				}
				assertOthersZero(t, pv, "S")
			},
		},
		{
			name: "KindVec2",
			val:  replication.Vec2(1, 2),
			check: func(t *testing.T, pv *protos.AttrValue) {
				assertFloats(t, pv, []float32{1, 2})
			},
		},
		{
			name: "KindVec3",
			val:  replication.Vec3(1, 2, 3),
			check: func(t *testing.T, pv *protos.AttrValue) {
				assertFloats(t, pv, []float32{1, 2, 3})
			},
		},
		{
			name: "KindVec4",
			val:  replication.Vec4(1, 2, 3, 4),
			check: func(t *testing.T, pv *protos.AttrValue) {
				assertFloats(t, pv, []float32{1, 2, 3, 4})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pv := toAttrValue(replication.AttrValue{Attr: 42, Value: tc.val})
			if pv.Id != 42 {
				t.Fatalf("Attr 应写进 Id，得到 %d", pv.Id)
			}
			tc.check(t, pv)
		})
	}
}

// assertFloats 断言浮点族值落在 F 且其余字段为零。
func assertFloats(t *testing.T, pv *protos.AttrValue, want []float32) {
	t.Helper()
	if len(pv.F) != len(want) {
		t.Fatalf("F 应有 %d 个分量，得到 %v", len(want), pv.F)
	}
	for i, w := range want {
		if pv.F[i] != w {
			t.Fatalf("F[%d] 应为 %v，得到 %v", i, w, pv.F[i])
		}
	}
	assertOthersZero(t, pv, "F")
}

// assertOthersZero 断言除 keep 之外的类型字段保持零值（漏加 case 时值会静默丢失，
// 这条断言保证它落在「该落的地方」，而不是别处）。
func assertOthersZero(t *testing.T, pv *protos.AttrValue, keep string) {
	t.Helper()
	if keep != "F" && len(pv.F) != 0 {
		t.Fatalf("非浮点值不应写 F，得到 %v", pv.F)
	}
	if keep != "I" && pv.I != 0 {
		t.Fatalf("I 应保持零值，得到 %d", pv.I)
	}
	if keep != "B" && pv.B {
		t.Fatalf("B 应保持零值，得到 true")
	}
	if keep != "S" && pv.S != "" {
		t.Fatalf("S 应保持零值，得到 %q", pv.S)
	}
}

// TestToFrameRoundTripsEveryField 覆盖 toFrame/toSchema 的整条路径：step、full、
// schema（只在 full 帧出现）、以及每个 EntityDelta 的 id/destroy/removed/set。
func TestToFrameRoundTripsEveryField(t *testing.T) {
	f := replication.Frame{
		Step: 42,
		Full: true,
		Schema: replication.Schema{
			Version: 0xABCD,
			Fields: []replication.Attr{
				{ID: 1, Name: "Pos", Kind: replication.KindVec3},
				{ID: 2, Name: "Health", Kind: replication.KindF32},
				{ID: 3, Name: "Enemy", Kind: replication.KindBool},
			},
		},
		Entities: []replication.EntityDelta{
			{
				ID:      7,
				Destroy: true,
				Removed: []uint32{3, 4},
				Set: []replication.AttrValue{
					{Attr: 1, Value: replication.Vec3(1, 2, 3)},
				},
			},
			{
				ID: 9,
				Set: []replication.AttrValue{
					{Attr: 2, Value: replication.F32(10)},
					{Attr: 3, Value: replication.Bool(true)},
				},
			},
		},
	}

	pf := toFrame(f)

	if pf.Step != 42 {
		t.Fatalf("Step 应为 42，得到 %d", pf.Step)
	}
	if !pf.Full {
		t.Fatal("Full 应为 true")
	}

	// schema 只在 full 帧出现，且字段 id/name/kind 与 version 都要还原。
	if pf.Schema == nil {
		t.Fatal("full 帧必须携带 Schema")
	}
	if pf.Schema.Version != 0xABCD {
		t.Fatalf("Schema.Version 应为 0xABCD，得到 %#x", pf.Schema.Version)
	}
	if len(pf.Schema.Fields) != 3 {
		t.Fatalf("Schema 应有 3 个字段，得到 %d", len(pf.Schema.Fields))
	}
	wantFields := []struct {
		id   uint32
		name string
		kind int32
	}{
		{1, "Pos", int32(replication.KindVec3)},
		{2, "Health", int32(replication.KindF32)},
		{3, "Enemy", int32(replication.KindBool)},
	}
	for i, w := range wantFields {
		got := pf.Schema.Fields[i]
		if got.Id != w.id || got.Name != w.name || got.Kind != w.kind {
			t.Fatalf("Schema.Fields[%d] 应为 {%d %q %d}，得到 {%d %q %d}",
				i, w.id, w.name, w.kind, got.Id, got.Name, got.Kind)
		}
	}

	if len(pf.Entities) != 2 {
		t.Fatalf("应有 2 个实体条目，得到 %d", len(pf.Entities))
	}

	// 第一条：destroy + 多条 removed（整表、按序原样搬运）+ 一条 set。
	e0 := pf.Entities[0]
	if e0.Id != 7 {
		t.Fatalf("实体 0 的 Id 应为 7，得到 %d", e0.Id)
	}
	if !e0.Destroy {
		t.Fatal("实体 0 应带 destroy")
	}
	if len(e0.Removed) != 2 || e0.Removed[0] != 3 || e0.Removed[1] != 4 {
		t.Fatalf("实体 0 的 Removed 应完整还原为 [3 4]，得到 %v", e0.Removed)
	}
	if len(e0.Set) != 1 {
		t.Fatalf("实体 0 应有 1 条 set，得到 %d", len(e0.Set))
	}
	if e0.Set[0].Id != 1 {
		t.Fatalf("实体 0 set[0] 的 Id 应为 1，得到 %d", e0.Set[0].Id)
	}
	if len(e0.Set[0].F) != 3 || e0.Set[0].F[2] != 3 {
		t.Fatalf("实体 0 set[0] 应是 Vec3(1,2,3)，得到 %v", e0.Set[0].F)
	}

	// 第二条：只带 set，无 destroy / removed。检查每个 Kind 落到各自字段。
	e1 := pf.Entities[1]
	if e1.Id != 9 || e1.Destroy {
		t.Fatalf("实体 1 应是 id=9 的非 destroy，得到 id=%d destroy=%v", e1.Id, e1.Destroy)
	}
	if len(e1.Removed) != 0 {
		t.Fatalf("实体 1 不应有 removed，得到 %v", e1.Removed)
	}
	if len(e1.Set) != 2 {
		t.Fatalf("实体 1 应有 2 条 set，得到 %d", len(e1.Set))
	}
	if e1.Set[0].I != 0 || e1.Set[0].F[0] != 10 {
		t.Fatalf("实体 1 set[0] 应是 F=10，得到 F=%v I=%d", e1.Set[0].F, e1.Set[0].I)
	}
	if !e1.Set[1].B {
		t.Fatal("实体 1 set[1] 应是 B=true")
	}
}

// 单元素 removed 也要完整搬运（proto3 对 repeated uint32 的 wire 编码可能压成
// packed，但这里到 protobuf 结构体为止没有打包概念，必须整表原样）。
func TestToFrameKeepsSingleRemoved(t *testing.T) {
	pf := toFrame(replication.Frame{
		Entities: []replication.EntityDelta{{ID: 5, Removed: []uint32{8}}},
	})
	if len(pf.Entities) != 1 || len(pf.Entities[0].Removed) != 1 || pf.Entities[0].Removed[0] != 8 {
		t.Fatalf("单元素 removed 应还原为 [8]，得到 %+v", pf.Entities)
	}
}

// schema 恰恰只在 full 帧出现：增量帧的 Schema 必须是 nil，否则客户端会把它当成
// 一次属性表下发（apply_schema 会清空再重建名字表，代价与语义都不对）。
func TestToFrameOmitsSchemaOnDeltaFrames(t *testing.T) {
	pf := toFrame(replication.Frame{
		Step:     1,
		Full:     false,
		Schema:   replication.Schema{Version: 1, Fields: []replication.Attr{{ID: 1, Name: "Pos", Kind: replication.KindVec3}}},
		Entities: []replication.EntityDelta{{ID: 1}},
	})
	if pf.Full {
		t.Fatal("增量帧不应带 full 标记")
	}
	if pf.Schema != nil {
		t.Fatalf("增量帧不应携带 schema，得到 %+v", pf.Schema)
	}
}

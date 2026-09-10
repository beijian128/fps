//go:build joltdll

package physics

// 真实物理（Jolt DLL）下的地图集成测试。
//
// 「走道走不走得上去」「整张图搭出来稳不稳」这类问题 fake 物理测不出来，只能让
// 真引擎跑一遍，因此单独放在这个构建标签后面——没装 DLL 的机器上
// `go test ./...` 依然全绿。本机需要 libjolt_c.dll（在 PATH 或与测试二进制同目录）：
//
//	go test -tags joltdll ./physics
//
// 舷梯参数的选择依据（单级抬升 ≤ 0.4、进深 ≥ 0.5）即来自这里的实测：进深 0.34
// 时 WalkStairs 抬腿后会被再下一级绊住，角色永远卡在最低一级。

import (
	"math"
	"testing"

	"joltgo/sim"
)

// bodyView 是集成测试用到的刚体字段子集。生产路径已改为通用的实体-属性增量
// 同步（见 sim/replicate.go），旧的 sim.Snapshot()/sim.BodyInfo 已删除，
// 这里改为直接从全量帧（FullFrame）按 schema 的属性名取值。
type bodyView struct {
	ID     uint32
	Pos    [3]float32
	Size   [3]float32
	Mat    int
	Static bool
}

// frameBodies 从全量帧里读出玩家可见的刚体（带 Body.Kind 属性的实体）。
//
// 金币传感器球也注册了 Body 且是静态的，但不上屏——旧快照用 Without[Resource]
// 在 archetype 粒度排除了它们（sim_test 亦断言金币不得出现在 Bodies）。全量帧
// 没有 archetype 概念，只能等价地按「同时带 Resource.Kind」跳过：否则金币会在
// 推进中被拾取而消失，静态几何巡检会把它误判成「静态刚体消失了」。
func frameBodies(s *sim.Simulation) []bodyView {
	f := s.FullFrame()
	name := map[uint32]string{}
	for _, a := range f.Schema.Fields {
		name[a.ID] = a.Name
	}
	var out []bodyView
	for _, ed := range f.Entities {
		b := bodyView{ID: ed.ID}
		isBody := false
		hasResource := false
		for _, av := range ed.Set {
			switch name[av.Attr] {
			case "Body.Kind":
				isBody = true
			case "Resource.Kind":
				hasResource = true
			case "Pos":
				copy(b.Pos[:], av.Value.Floats())
			case "Body.Size":
				copy(b.Size[:], av.Value.Floats())
			case "Body.Mat":
				b.Mat = int(av.Value.Int())
			case "Body.Static":
				b.Static = av.Value.Boolean()
			}
		}
		if isBody && !hasResource {
			out = append(out, b)
		}
	}
	return out
}

// framePlayerPos 从全量帧里按玩家槽位（Player.Idx）取脚底位置。
func framePlayerPos(s *sim.Simulation, idx int) [3]float32 {
	f := s.FullFrame()
	name := map[uint32]string{}
	for _, a := range f.Schema.Fields {
		name[a.ID] = a.Name
	}
	for _, ed := range f.Entities {
		pidx := int32(-1)
		var pos [3]float32
		for _, av := range ed.Set {
			switch name[av.Attr] {
			case "Player.Idx":
				pidx = av.Value.Int()
			case "Pos":
				copy(pos[:], av.Value.Floats())
			}
		}
		if pidx == int32(idx) {
			return pos
		}
	}
	return [3]float32{}
}

// snapshotBody 返回指定 id 的刚体视图。
func snapshotBody(s *sim.Simulation, id uint32) (bodyView, bool) {
	for _, b := range frameBodies(s) {
		if b.ID == id {
			return b, true
		}
	}
	return bodyView{}, false
}

// drive 按世界空间水平速度推进若干 tick（与客户端每帧上报输入等价）。
func drive(s *sim.Simulation, vx, vz float32, ticks int) {
	for i := 0; i < ticks; i++ {
		s.ApplyInput(0, [2]float32{vx, vz}, 0, false)
		s.Step()
	}
}

// TestMapCatwalkIsWalkable 让 0 号玩家从出生点走到右舷，再沿舷梯登上高架走道。
// 舷梯的单级抬升/进深一旦被改坏，这里会失败——这是「地图真的能玩」的最终验证。
func TestMapCatwalkIsWalkable(t *testing.T) {
	s := sim.New(New())
	s.Init()
	defer s.Shutdown()

	// 从快照里认出右舷高架走道（材质号 MatCatwalk = 7）：x、面高、z 范围。
	var cw bodyView
	found := false
	for _, b := range frameBodies(s) {
		if b.Mat == 7 && b.Pos[0] > 0 { // int(sim.MatCatwalk)
			cw, found = b, true
		}
	}
	if !found {
		t.Fatal("快照里找不到右舷高架走道（材质号 7）")
	}
	cwX := cw.Pos[0]
	cwTop := cw.Pos[1] + cw.Size[1]

	// 腿 1：从出生点横向走到走道正下方。
	start := framePlayerPos(s, 0)
	targetX := cwX
	stepX := float32(8)
	if start[0] > targetX {
		stepX = -8
	}
	for i := 0; i < 400; i++ {
		if math.Abs(float64(framePlayerPos(s, 0)[0]-targetX)) < 0.3 {
			break
		}
		drive(s, stepX, 0, 1)
	}
	if p := framePlayerPos(s, 0); math.Abs(float64(p[0]-targetX)) >= 0.3 {
		t.Fatalf("没能走到走道正下方：x=%.2f，目标 %.2f", p[0], targetX)
	}

	// 腿 2：朝船中方向走，路过舷梯并拾级而上。
	dirZ := float32(1)
	if start[2] > 0 {
		dirZ = -1
	}
	climbed := false
	for i := 0; i < 400; i++ {
		drive(s, 0, dirZ*8, 1)
		if p := framePlayerPos(s, 0); p[1] >= cwTop-0.15 {
			climbed = true
			t.Logf("登上走道：pos=%v（走道面 %.2f）", p, cwTop)
			break
		}
	}
	if !climbed {
		t.Fatalf("玩家走不上高架走道，最后位置 %v（走道面 %.2f）",
			framePlayerPos(s, 0), cwTop)
	}
}

// TestMapStaticGeometryIsStable 冒烟：真 Jolt 世界能搭出整张图，静态几何在多次
// tick 后不漂移、位置全为有限值（重叠的静态件会让 Jolt 产出 NaN 位置）。
func TestMapStaticGeometryIsStable(t *testing.T) {
	s := sim.New(New())
	s.Init()
	defer s.Shutdown()

	drive(s, 0, 0, 40)

	before := map[uint32][3]float32{}
	for _, b := range frameBodies(s) {
		for i, v := range b.Pos {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				t.Fatalf("刚体 %d 位置出现非有限值：%v", b.ID, b.Pos)
			}
			if b.Static && b.Pos[i] != b.Pos[i] {
				t.Fatalf("刚体 %d 位置为 NaN", b.ID)
			}
		}
		if b.Static {
			before[b.ID] = b.Pos
		}
	}
	if len(before) == 0 {
		t.Fatal("快照里没有静态几何")
	}

	drive(s, 6, 0, 40)
	drive(s, 0, 6, 40)

	for id, pos := range before {
		b, ok := snapshotBody(s, id)
		if !ok {
			t.Fatalf("静态刚体 %d 在推进后消失了", id)
		}
		if b.Pos != pos {
			t.Fatalf("静态刚体 %d 发生了漂移：%v → %v", id, pos, b.Pos)
		}
	}
}

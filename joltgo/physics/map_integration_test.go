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

// snapshotBody 返回快照里指定 id 的刚体。
func snapshotBody(s *sim.Simulation, id uint32) (sim.BodyInfo, bool) {
	for _, b := range s.Snapshot().Bodies {
		if b.ID == id {
			return b, true
		}
	}
	return sim.BodyInfo{}, false
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
	var cw sim.BodyInfo
	found := false
	for _, b := range s.Snapshot().Bodies {
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
	start := s.Snapshot().Players[0].Pos
	targetX := cwX
	stepX := float32(8)
	if start[0] > targetX {
		stepX = -8
	}
	for i := 0; i < 400; i++ {
		if math.Abs(float64(s.Snapshot().Players[0].Pos[0]-targetX)) < 0.3 {
			break
		}
		drive(s, stepX, 0, 1)
	}
	if p := s.Snapshot().Players[0].Pos; math.Abs(float64(p[0]-targetX)) >= 0.3 {
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
		if p := s.Snapshot().Players[0].Pos; p[1] >= cwTop-0.15 {
			climbed = true
			t.Logf("登上走道：pos=%v（走道面 %.2f）", p, cwTop)
			break
		}
	}
	if !climbed {
		t.Fatalf("玩家走不上高架走道，最后位置 %v（走道面 %.2f）",
			s.Snapshot().Players[0].Pos, cwTop)
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
	for _, b := range s.Snapshot().Bodies {
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

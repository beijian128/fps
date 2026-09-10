package sim

// 「运输船」场景的几何不变量测试：这些约束一旦被破坏，地图就会变得不公平或
// 不可玩（出生点卡在掩体里、走道跳不上去、金币刷进集装箱），因此写死成测试，
// 改 map.go 时立刻能发现。

import (
	"math"
	"testing"
)

// 把坐标量化到 1e-4 后再比较，避免浮点噪声让「同一个位置」判不出来。
func q(v float32) int { return int(math.Round(float64(v) * 10000)) }

func boxKey(b shipBox) [7]int {
	return [7]int{q(b.hx), q(b.hy), q(b.hz), q(b.x), q(b.y), q(b.z), int(b.mat)}
}

// TestShipMapIsSymmetric 验证整张图在绕 Y 轴旋转 180° 后与自身重合：
// 每个部件都必须有 (x,y,z) → (-x,y,-z) 的孪生体。这是两个出生点公平的前提。
func TestShipMapIsSymmetric(t *testing.T) {
	have := map[[7]int]int{}
	for _, b := range shipBoxParts {
		have[boxKey(b)]++
	}
	for _, b := range shipBoxParts {
		m := boxKey(shipBox{hx: b.hx, hy: b.hy, hz: b.hz, x: -b.x, y: b.y, z: -b.z, mat: b.mat})
		if have[m] == 0 {
			t.Fatalf("部件 %+v 缺少 180° 旋转孪生体（地图左右不对称）", b)
		}
	}

	caps := map[[5]int]int{}
	for _, c := range shipCapsuleParts {
		caps[[5]int{q(c.radius), q(c.half), q(c.x), q(c.y), q(c.z)}]++
	}
	for _, c := range shipCapsuleParts {
		m := [5]int{q(c.radius), q(c.half), q(-c.x), q(c.y), q(-c.z)}
		if caps[m] == 0 {
			t.Fatalf("胶囊 %+v 缺少 180° 旋转孪生体（地图左右不对称）", c)
		}
	}

	tgt := map[[3]int]int{}
	for _, p := range shipTargets {
		tgt[[3]int{q(p[0]), q(p[1]), q(p[2])}]++
	}
	for _, p := range shipTargets {
		if tgt[[3]int{q(-p[0]), q(p[1]), q(-p[2])}] == 0 {
			t.Fatalf("靶球 %v 缺少 180° 旋转孪生体（地图左右不对称）", p)
		}
	}
}

// TestShipMapHullEnclosesDeck 验证甲板被船体完整围住：外板内侧面贴住甲板边缘
// （重叠而不是留缝），且高度足以挡住玩家（跳不过去）。留缝会让玩家掉出船外。
func TestShipMapHullEnclosesDeck(t *testing.T) {
	var port, starboard, bow, stern *shipBox
	for i := range hullBoxes {
		b := &hullBoxes[i]
		switch {
		case b.x < 0 && b.hz > b.hx:
			port = b
		case b.x > 0 && b.hz > b.hx:
			starboard = b
		case b.z < 0:
			bow = b
		case b.z > 0:
			stern = b
		}
	}
	if port == nil || starboard == nil || bow == nil || stern == nil {
		t.Fatal("船体应有左右舷外板与艏艉横舱壁各一块")
	}

	for _, tc := range []struct {
		name  string
		b     *shipBox
		inner float32 // 内侧面坐标
	}{
		{"左舷", port, port.x + port.hx},
		{"右舷", starboard, starboard.x - starboard.hx},
		{"艏", bow, bow.z + bow.hz},
		{"艉", stern, stern.z - stern.hz},
	} {
		// 内侧面必须落在甲板内（重叠），否则甲板边缘与舱壁之间有掉出去的缝。
		var inside bool
		if tc.b.hz > tc.b.hx { // 舷侧板：比较 x
			inside = math.Abs(float64(tc.inner)) < deckHalfX
		} else {
			inside = math.Abs(float64(tc.inner)) < deckHalfZ
		}
		if !inside {
			t.Fatalf("%s 外板与甲板之间有空隙：内侧面 %.2f 未落进甲板范围", tc.name, tc.inner)
		}
		// 高过玩家跳跃高度（约 1.8 m）才拦得住。
		if top := tc.b.y + tc.b.hy; top < 1.8+1.0 {
			t.Fatalf("%s 外板太矮（顶面 %.2f），玩家能跳出去", tc.name, top)
		}
	}
}

// TestShipMapSpawnsClear 验证两个出生点既不卡在掩体里，也在甲板范围内。
func TestShipMapSpawnsClear(t *testing.T) {
	for i := 0; i < MaxPlayers; i++ {
		x, z := playerSpawnXZ(i)
		if math.Abs(float64(x)) > deckHalfX || math.Abs(float64(z)) > deckHalfZ {
			t.Fatalf("%d 号出生点 %v 落在甲板之外", i, [2]float32{x, z})
		}
		// 玩家胶囊 = 线段（y ∈ [offset-half, offset+half]）扫出半径 characterRadius：
		// 沿线段采样球心即可（不能采到 y=0 的脚底——那里本来就贴着甲板）。
		lo := float32(characterOffsetY - characterHalfHeight)
		hi := float32(characterOffsetY + characterHalfHeight)
		for y := lo; y <= hi+1e-3; y += 0.1 {
			if mapBlocks(x, y, z, characterRadius) {
				t.Fatalf("%d 号出生点 (%v, %v) 的胶囊在 y=%.2f 处卡进掩体", i, x, z, y)
			}
		}
	}
}

// TestShipMapCatwalksReachable 验证两舷走道都走得到：每段舷梯的顶端与走道
// 端头对齐、高度一致；单级抬升与进深都在 Jolt 角色控制器 WalkStairs 能吃下的
// 范围内（见 wrapper 的 ExtendedUpdate 默认设置）——越界就走不上去。
func TestShipMapCatwalksReachable(t *testing.T) {
	const (
		joltMaxStepUp = 0.4 // mWalkStairsStepUp
		minStepDepth  = 0.5 // 踏步进深必须大于角色半径 0.4，否则会被再下一级绊住
	)

	var catwalks []shipBox
	for _, b := range shipBoxParts {
		if b.mat == MatCatwalk {
			catwalks = append(catwalks, b)
		}
	}
	if len(catwalks) != 2 {
		t.Fatalf("两舷应各有 1 条高架走道，得到 %d 条", len(catwalks))
	}

	for _, st := range deckStairs {
		if st.rise > joltMaxStepUp {
			t.Fatalf("舷梯单级抬升 %.2f 超过 Jolt 上限 %.2f，角色走不上去", st.rise, joltMaxStepUp)
		}
		if st.dz < minStepDepth {
			t.Fatalf("舷梯进深 %.2f 小于 %.2f（角色半径），抬腿后会被下一级绊住，走不上去",
				st.dz, minStepDepth)
		}
		if st.steps <= 0 {
			t.Fatalf("舷梯参数不合法：%+v", st)
		}
		// 展开镜像后逐段检查（孪生体只剩一段舷梯可用时，走道就有一端上不去）。
		for _, run := range []shipStairs{st, {x: -st.x, z: -st.z, hx: st.hx, dz: st.dz, rise: st.rise,
			steps: st.steps, dir: -st.dir, mat: st.mat}} {
			topY := run.rise * float32(run.steps)
			topZ := run.z + run.dir*run.dz*float32(run.steps-1)
			var matched bool
			for _, cw := range catwalks {
				if math.Abs(float64(cw.x-run.x)) > 0.01 {
					continue
				}
				if math.Abs(float64(cw.y+cw.hy-topY)) > 0.05 {
					continue // 舷梯顶端高度与走道面不一致
				}
				// 舷梯顶端贴住走道端头：从 -z 侧上来的（dir > 0）对应走道的 -z 端，反之亦然。
				end := cw.z + cw.hz
				if run.dir > 0 {
					end = cw.z - cw.hz
				}
				if math.Abs(float64(end-topZ)) <= float64(run.dz) {
					matched = true
				}
			}
			if !matched {
				t.Fatalf("舷梯（x=%.2f 顶端 z=%.2f y=%.2f）没有接到任何走道端头", run.x, topZ, topY)
			}
		}
	}
}

// TestShipMapEnemiesSpawnInClearDeck 验证刷怪永远落在干净的甲板上：不会卡进
// 集装箱/桅杆，也不会贴着 0 号玩家刷出来。
func TestShipMapEnemiesSpawnInClearDeck(t *testing.T) {
	s, _ := newTestSim(t)
	px, pz := playerSpawnXZ(0)

	seen := 0
	for round := 0; round < 60; round++ {
		before := map[uint32]bool{}
		for _, id := range enemiesOf(s.Snapshot()) {
			before[id] = true
		}
		s.spawnEnemy()
		st := s.Snapshot()
		for _, id := range enemiesOf(st) {
			if before[id] {
				continue // 本波已有的敌人，只看这次新刷出来的
			}
			seen++
			var pos [3]float32
			for _, b := range st.Bodies {
				if b.ID == id {
					pos = b.Pos
				}
			}
			if mapBlocks(pos[0], pos[1], pos[2], enemyRadius) {
				t.Fatalf("怪物刷进了掩体：%v", pos)
			}
			dx, dz := pos[0]-px, pos[2]-pz
			if dx*dx+dz*dz < enemyFreeGap*enemyFreeGap {
				t.Fatalf("怪物刷得离 0 号玩家太近（%.1fm）：%v",
					math.Sqrt(float64(dx*dx+dz*dz)), pos)
			}
			if math.Abs(float64(pos[0])) > deckHalfX || math.Abs(float64(pos[2])) > deckHalfZ {
				t.Fatalf("怪物刷到甲板之外：%v", pos)
			}
		}
	}
	if seen == 0 {
		t.Fatal("没有观察到任何新刷出的怪物")
	}
}

// TestShipMapResourcesNotInsideCover 验证金币不会刷进掩体（否则玩家拾不到）。
func TestShipMapResourcesNotInsideCover(t *testing.T) {
	for round := 0; round < 30; round++ {
		s, _ := newTestSim(t)
		for _, r := range s.Snapshot().Resources {
			if mapBlocks(r.Pos[0], r.Pos[1], r.Pos[2], resourceSensorRadius) {
				t.Fatalf("金币刷进了掩体（拾取不到）：%v", r.Pos)
			}
		}
	}
}

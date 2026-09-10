//go:build joltdll

package physics

// 真实物理（Jolt DLL）下的 PVP 命中链路集成测试。
//
// PVP 的命中判定全部压在两条 Jolt 行为上，而这两条从 Jolt 源码读出来的结论都
// 反直觉，fake 物理（sim/sim_test.go）根本测不到：
//
//  1. 弹丸是 `LinearCast`（CCD）动态球，每 tick 走 3 m。它能不能打中一个**静态胶囊**
//     并在 `ContactListener` 里留下记录？（能——这正是现在打怪物用的路径。）
//  2. 与角色**共位**的命中盒刚体，必须靠 `jolt_character_ignore_body` 才对角色不存在。
//     只关掉 dynamic_push 不够：Jolt 照样把角色速度清零，角色会被自己钉死。
//
// 需要 libjolt_c.dll 在 PATH 或与测试二进制同目录：
//
//	go test -tags joltdll ./physics

import (
	"testing"

	"joltgo/sim"
)

// TestProjectileHitsStaticCapsule 验证 CCD 弹丸能打中静态胶囊（命中盒的形状）。
//
// 关掉重力：这条测的是「sweep 会不会漏掉」，不是弹道。弹丸从 10 m 外以 60 m/s
// 冲向胶囊，若 CCD 不生效它会一帧跨过 3 m、从胶囊里穿过去。
func TestProjectileHitsStaticCapsule(t *testing.T) {
	p := New()
	p.Create()
	defer p.Destroy()
	p.SetGravity(0, 0, 0)

	hitbox := p.AddCapsule(0, 0.9, 0, 0.5, 0.4, sim.MotionStatic) // 与角色同形状的静态胶囊
	proj := p.AddSphere(0, 0.9, 10, 0.08, sim.MotionDynamic)
	if hitbox == 0 || proj == 0 {
		t.Fatal("刚体创建失败")
	}
	p.SetBodyMotionQuality(proj, sim.QualityLinearCast)
	p.SetBodyFriction(proj, 0)
	p.SetBodyRestitution(proj, 0)
	p.SetBodyVelocity(proj, 0, 0, -60) // 正对胶囊飞过去

	hit := false
	for i := 0; i < 20 && !hit; i++ { // 10 m / 60 m/s ≈ 4 tick，留足余量
		p.Step(sim.TickDT, 2)
		for _, c := range p.PollContacts() {
			if (c.BodyA == proj && c.BodyB == hitbox) || (c.BodyA == hitbox && c.BodyB == proj) {
				hit = true
			}
		}
	}
	if !hit {
		t.Fatal("LinearCast 弹丸没有与静态胶囊产生接触事件：玩家的命中盒形同虚设")
	}
}

// TestCharacterIgnoresBodyInItsPath 验证 `jolt_character_ignore_body` 的双向效果：
// 登记忽略的角色能直接穿过刚体，没登记的角色会被挡住。
//
// 为什么必须忽略（**不是**因为共位会把角色钉死 —— 实测共位的静态胶囊不挡人，
// CharacterVirtual 的扫掠会忽略 fraction = 0 的初始重叠）：
//
//   - 对方玩家的命中盒对本地角色就是一堵**隐形墙**：两人贴近时会互相卡住；
//   - 自己的命中盒每 tick 才跟随一次，角色一 tick 的位移（跑动 0.7 m；下落更快）
//     可能超过两个胶囊的接触距离 0.8 m，那一 tick 里它就是角色**正前方**的障碍，
//     Jolt 会把角色速度清零。
//
// 对照组不可省：只测「登记后能穿过去」的话，把忽略功能整个删掉这条测试也照样绿
// （角色本来就穿得过去时才没有阻挡），那就测不出任何东西。
func TestCharacterIgnoresBodyInItsPath(t *testing.T) {
	const (
		speed   = 4.0 // m/s
		ticks   = 25  // 25 × 0.05 s × 4 m/s = 5 m（畅通的话）
		pastX   = 3.0 // 畅通应超过障碍
		blocked = 1.0 // 被挡应低于
		// 障碍放在角色正前方 0.4 m 处：胶囊半径 0.4 + 命中盒半径 0.4 = 0.8，
		// 所以角色最多再走 0.4 m 就会撞上。
		obstacleX = 1.2
	)
	drive := func(ignore bool) float32 {
		p := New()
		p.Create()
		defer p.Destroy()
		p.SetGravity(0, 0, 0) // 悬浮：这条测的是碰撞，不是重力

		p.CreateCharacter(0, 0.5, 0.4, 0.9, 0, 1.0, 0)
		obstacle := p.AddCapsule(obstacleX, 1.0, 0, 0.5, 0.4, sim.MotionStatic)
		if obstacle == 0 {
			t.Fatal("障碍刚体创建失败")
		}
		if ignore {
			p.CharacterIgnoreBody(0, obstacle)
		}
		for i := 0; i < ticks; i++ {
			p.SetCharacterVelocity(0, [3]float32{speed, 0, 0})
			p.UpdateCharacter(0, sim.TickDT)
		}
		return p.CharacterPosition(0)[0]
	}

	if x := drive(true); x < pastX {
		t.Fatalf("登记忽略后角色应穿过该刚体（x > %v），实测 x=%v", pastX, x)
	}
	if x := drive(false); x > blocked {
		t.Fatalf("未登记忽略时该刚体应挡住角色（x < %v），实测 x=%v", blocked, x)
	}
}

// TestCharacterPassesThroughCoLocatedBody 记录一条反直觉的实测结论：与角色**共位**
// 的静态胶囊并不妨碍角色移动 —— CharacterVirtual 的扫掠忽略 fraction = 0 的初始重叠。
//
// 这条不变量有意义：它说明命中盒即使某几 tick 与角色完全重合也不会卡住人，
// 真正需要 `jolt_character_ignore_body` 的是「落在角色前方」的情形（见上一条测试）。
// 若哪天 Jolt 行为变了，这条会先亮。
func TestCharacterPassesThroughCoLocatedBody(t *testing.T) {
	const (
		speed = 4.0
		ticks = 25
		freeX = 3.0
	)
	p := New()
	p.Create()
	defer p.Destroy()
	p.SetGravity(0, 0, 0)

	p.CreateCharacter(0, 0.5, 0.4, 0.9, 0, 1.0, 0)
	if p.AddCapsule(0, 1.0, 0, 0.5, 0.4, sim.MotionStatic) == 0 {
		t.Fatal("共位刚体创建失败")
	}
	for i := 0; i < ticks; i++ {
		p.SetCharacterVelocity(0, [3]float32{speed, 0, 0})
		p.UpdateCharacter(0, sim.TickDT)
	}
	if x := p.CharacterPosition(0)[0]; x < freeX {
		t.Fatalf("共位的静态刚体不应妨碍角色移动（x > %v），实测 x=%v", freeX, x)
	}
}

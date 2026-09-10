package sim

// 系统：每 tick 按固定顺序运行，只通过组件与 s.physics 交互。
// 顺序：输入 → 物理步进 → 变换同步 → 弹丸命中 → 接触伤害 → 弹丸过期 →
// 金币拾取 → 波次推进。
//
// 碰撞判定全部走物理层，不做距离计算：
//   - 贴身伤害：角色控制器的接触事件（CharacterContactListener，每步接触求解
//     触发），碰到的刚体里带 Enemy 组件的才扣血
//   - 金币拾取：金币是 Jolt 传感器球（不与刚体碰撞），角色接触到即拾取
//   - 弹丸命中：刚体接触事件（ContactListener），带 Projectile 组件的才结算
//
// 变换同步（syncSystem）每 tick 只做一次，放在物理步进之后：弹丸掉落位置、
// 刷怪位置、快照读取的都是本 tick 步进后的位置。

import (
	"joltgo/ecs"
	"math"
)

// inputSystem 消费每个玩家的输入并实现角色移动策略（走/跑速度 + 跳跃 + 重力
// 积分），然后让物理层推进每个角色一步。移动规则是游戏逻辑，物理层只负责执行。
func (s *Simulation) inputSystem() {
	for i := 0; i < MaxPlayers; i++ {
		in, ok := ecs.Get[Input](s.world, s.players[i])
		if !ok {
			continue
		}
		jump := in.Jump
		if jump {
			in.Jump = false // 跳跃边沿：只消费一次
		}

		v := s.physics.CharacterVelocity(i)
		if s.physics.CharacterOnGround(i) {
			v[1] = 0
			if jump {
				v[1] = jumpSpeed
			}
		}
		v[1] += gravityY * TickDT
		// 服务端限幅：客户端上报的水平速度只当作"期望方向+期望速率"，封顶到
		// maxPlayerSpeed（走/跑的 8/14 只是客户端约定），防恶意客户端任意超速穿墙。
		mx, mz := in.Move[0], in.Move[1]
		if l := mx*mx + mz*mz; l > maxPlayerSpeed*maxPlayerSpeed {
			k := maxPlayerSpeed / float32(math.Sqrt(float64(l)))
			mx, mz = mx*k, mz*k
		}
		v[0], v[2] = mx, mz // 水平速度来自客户端输入（已限幅）
		s.physics.SetCharacterVelocity(i, v)
		s.physics.UpdateCharacter(i, TickDT)
	}
}

// syncSystem 把物理世界的最新变换写回组件，是 Go 侧与 Jolt 之间唯一的
// 每 tick 枚举点（旧实现每个系统各自枚举一遍）。
func (s *Simulation) syncSystem() {
	s.physics.Sync(func(id uint32, active bool, pos [3]float32, quat [4]float32) {
		e := ecs.Entity(id)
		ecs.Add(s.world, e, Position(pos))
		ecs.Add(s.world, e, Rotation(quat))
		if b, ok := ecs.Get[Body](s.world, e); ok {
			b.Active = active
		}
	})
	for i := 0; i < MaxPlayers; i++ {
		pos := s.physics.CharacterPosition(i)
		ecs.Add(s.world, s.players[i], Position(pos))
	}
}

// projectileSystem 从物理层拿到的「全部接触事件」里挑出与弹丸有关的进行结算：
// 靶球摧毁得分；敌人扣血，血尽死亡并掉落金币；命中其他物体仅移除弹丸。
// 箱子落地、敌人相撞等无关接触在这里被忽略。
func (s *Simulation) projectileSystem() {
	for _, c := range s.physics.PollContacts() {
		a, b := ecs.Entity(c.BodyA), ecs.Entity(c.BodyB)
		var proj, other ecs.Entity
		switch {
		case ecs.Has[Projectile](s.world, a):
			proj, other = a, b
		case ecs.Has[Projectile](s.world, b):
			proj, other = b, a
		default:
			continue // 与弹丸无关的接触
		}
		if !ecs.Has[Body](s.world, proj) {
			continue // 同一弹丸的多条接触记录只结算一次
		}
		if !ecs.Has[Body](s.world, other) {
			continue
		}
		if ecs.Has[Resource](s.world, other) {
			continue // 传感器球（金币）不挡弹丸：弹丸直接穿过，双方保留
		}
		s.destroyBody(proj)
		if ecs.Has[Projectile](s.world, other) {
			continue // 弹丸互撞：移除当前弹丸，无得分
		}
		switch {
		case ecs.Has[Target](s.world, other):
			s.destroyBody(other)
			s.score++
		case ecs.Has[Enemy](s.world, other):
			hp, ok := ecs.Get[Health](s.world, other)
			if !ok {
				continue
			}
			*hp--
			if *hp <= 0 {
				if pos, ok := ecs.Get[Position](s.world, other); ok {
					s.dropResource(*pos) // 击杀掉落金币
				}
				s.destroyBody(other)
				s.score++
			}
		}
	}
}

// enemyDamageSystem 敌人接触伤害：从每个角色的接触事件里找出敌人（接触求解每步
// 触发，同一敌人每 tick 每角色只结算一次），低难度每 tick 每只扣 0.4 点；
// 玩家血尽复活。
func (s *Simulation) enemyDamageSystem(contacts [MaxPlayers][]uint32) {
	for i := 0; i < MaxPlayers; i++ {
		hp, ok := ecs.Get[Health](s.world, s.players[i])
		if !ok {
			continue
		}
		touched := map[uint32]bool{}
		for _, id := range contacts[i] {
			e := ecs.Entity(id)
			if touched[id] || !ecs.Has[Enemy](s.world, e) {
				continue
			}
			touched[id] = true
			*hp -= enemyDamage
		}
		if *hp <= 0 {
			*hp = 100
			// 立即复活到出生点并清零速度；同时把 Position 组件同步为出生点，
			// 避免当 tick 快照出现"满血却还站在死亡点"的不一致（syncSystem 要到
			// 下个 tick 才会回写角色位置）。
			x, z := playerSpawnXZ(i)
			s.physics.SetCharacterPosition(i, x, playerSpawnY, z)
			s.physics.SetCharacterVelocity(i, [3]float32{0, 0, 0})
			ecs.Add(s.world, s.players[i], Position{x, playerSpawnY, z})
		}
	}
}

// expireProjectilesSystem 移除超时的弹丸（20 Hz 下 60 tick = 3 秒）。
func (s *Simulation) expireProjectilesSystem() {
	var dead []ecs.Entity
	ecs.Each(s.world, func(e ecs.Entity, p *Projectile) {
		if s.step-p.SpawnStep > projectileMaxLife {
			dead = append(dead, e)
		}
	})
	for _, e := range dead {
		s.destroyBody(e)
	}
}

// resourceSystem 金币拾取：角色接触到金币传感器球即拾取（物理接触判定，
// 由角色接触事件驱动，不做距离计算）。任一玩家接触都拾取。
func (s *Simulation) resourceSystem(contacts [MaxPlayers][]uint32) {
	for i := 0; i < MaxPlayers; i++ {
		for _, id := range contacts[i] {
			e := ecs.Entity(id)
			r, ok := ecs.Get[Resource](s.world, e)
			if !ok || r.Kind != 0 {
				continue
			}
			s.gold++
			s.destroyBody(e) // 移除传感器刚体并销毁实体
		}
	}
}

// waveSystem 波次推进：场上清空 2 秒后刷下一波。
// 数量规则与文档一致：第 1 波 initialEnemies 只（3），之后每波 +1，封顶
// maxEnemiesPerWave（即 3,4,5,6,6,…）。
func (s *Simulation) waveSystem() {
	if ecs.Count[Enemy](s.world) > 0 {
		s.waveClearStep = 0
		return
	}
	if s.waveClearStep == 0 {
		s.waveClearStep = s.step
		return
	}
	if s.step-s.waveClearStep < waveDelayTicks {
		return
	}
	s.wave++
	n := initialEnemies + (s.wave - 1)
	if n > maxEnemiesPerWave {
		n = maxEnemiesPerWave
	}
	for i := 0; i < n; i++ {
		s.spawnEnemy()
	}
	s.waveClearStep = 0
}

// spawnEnemy 在离 0 号玩家 10m 外的随机甲板位置刷一只怪物。怪物没有移动逻辑：
// 是静态刚体（固定哨兵），不可被推动、不参与重力结算，贴身才造成伤害。
// 刷点必须避开掩体（mapBlocks）——否则怪物会卡在集装箱里，玩家打不到也碰不着。
func (s *Simulation) spawnEnemy() {
	var player [3]float32
	if p, ok := ecs.Get[Position](s.world, s.players[0]); ok {
		player = *p
	}
	spawn := func(x, z float32) {
		id := s.physics.AddCapsule(x, enemySpawnY, z, enemyHalfHeight, enemyRadius, MotionStatic)
		e := s.registerBody(id, BodyCapsule, [3]float32{enemyRadius, enemyHalfHeight, 0}, true, [3]float32{x, enemySpawnY, z}, MatDefault)
		ecs.Add2(s.world, e, Enemy{}, Health(enemyHealth))
	}
	for attempt := 0; attempt < 48; attempt++ {
		x := randRange(-deckHalfX+1, deckHalfX-1)
		z := randRange(-deckHalfZ+1, deckHalfZ-1)
		dx := x - player[0]
		dz := z - player[2]
		if dx*dx+dz*dz < enemyFreeGap*enemyFreeGap {
			continue
		}
		if mapBlocks(x, enemySpawnY, z, enemyRadius) {
			continue
		}
		spawn(x, z)
		return
	}
	spawn(0, -12) // 兜底：甲板中央通道（该处无掩体）
}

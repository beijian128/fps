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

import "joltgo/ecs"

// inputSystem 消费玩家输入并实现角色移动策略（走/跑速度 + 跳跃 + 重力积分），
// 然后让物理层推进角色一步。移动规则是游戏逻辑，物理层只负责执行。
func (s *Simulation) inputSystem() {
	in, ok := ecs.Get[Input](s.world, s.player)
	if !ok {
		return
	}
	jump := in.Jump
	if jump {
		in.Jump = false // 跳跃边沿：只消费一次
	}

	v := s.physics.CharacterVelocity()
	if s.physics.CharacterOnGround() {
		v[1] = 0
		if jump {
			v[1] = jumpSpeed
		}
	}
	v[1] += gravityY * TickDT
	v[0], v[2] = in.Move[0], in.Move[1] // 水平速度直接来自客户端输入
	s.physics.SetCharacterVelocity(v)
	s.physics.UpdateCharacter(TickDT)
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
	pos := s.physics.CharacterPosition()
	ecs.Add(s.world, s.player, Position(pos))
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
		s.destroyBodyLocked(proj)
		if ecs.Has[Projectile](s.world, other) {
			continue // 弹丸互撞：移除当前弹丸，无得分
		}
		switch {
		case ecs.Has[Target](s.world, other):
			s.destroyBodyLocked(other)
			s.score++
		case ecs.Has[Enemy](s.world, other):
			hp, ok := ecs.Get[Health](s.world, other)
			if !ok {
				continue
			}
			*hp--
			if *hp <= 0 {
				if pos, ok := ecs.Get[Position](s.world, other); ok {
					s.dropResourceLocked(*pos) // 击杀掉落金币
				}
				s.destroyBodyLocked(other)
				s.score++
			}
		}
	}
}

// enemyDamageSystem 敌人接触伤害：从角色接触事件里找出敌人（接触求解每步触发，
// 同一敌人每 tick 只结算一次），低难度每 tick 每只扣 0.4 点；玩家血尽复活。
func (s *Simulation) enemyDamageSystem(contacts []uint32) {
	hp, ok := ecs.Get[Health](s.world, s.player)
	if !ok {
		return
	}
	touched := map[uint32]bool{}
	for _, id := range contacts {
		e := ecs.Entity(id)
		if touched[id] || !ecs.Has[Enemy](s.world, e) {
			continue
		}
		touched[id] = true
		*hp -= enemyDamage
	}
	if *hp <= 0 {
		*hp = 100
		s.physics.SetCharacterPosition(0, playerSpawnY, 12)
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
		s.destroyBodyLocked(e)
	}
}

// resourceSystem 金币拾取：角色接触到金币传感器球即拾取（物理接触判定，
// 由角色接触事件驱动，不做距离计算）。
func (s *Simulation) resourceSystem(contacts []uint32) {
	for _, id := range contacts {
		e := ecs.Entity(id)
		r, ok := ecs.Get[Resource](s.world, e)
		if !ok || r.Kind != 0 {
			continue
		}
		s.gold++
		s.destroyBodyLocked(e) // 移除传感器刚体并销毁实体
	}
}

// waveSystem 波次推进：场上清空 2 秒后刷下一波（第 n 波 1+n 只，上限 6 只）。
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
	n := 1 + s.wave
	if n > maxEnemiesPerWave {
		n = maxEnemiesPerWave
	}
	for i := 0; i < n; i++ {
		s.spawnEnemyLocked()
	}
	s.waveClearStep = 0
}

// spawnEnemyLocked 在离玩家 10m 外的随机位置刷一只怪物。怪物没有移动逻辑：
// 是静态刚体（固定哨兵），不可被推动、不参与重力结算，贴身才造成伤害。
func (s *Simulation) spawnEnemyLocked() {
	var player [3]float32
	if p, ok := ecs.Get[Position](s.world, s.player); ok {
		player = *p
	}
	spawn := func(x, z float32) {
		id := s.physics.AddCapsule(x, enemySpawnY, z, enemyHalfHeight, enemyRadius, MotionStatic)
		e := s.registerBodyLocked(id, BodyCapsule, [3]float32{enemyRadius, enemyHalfHeight, 0}, true, [3]float32{x, enemySpawnY, z})
		ecs.Add2(s.world, e, Enemy{}, Health(enemyHealth))
	}
	for attempt := 0; attempt < 24; attempt++ {
		x := randRange(-14, 14)
		z := randRange(-14, 14)
		dx := x - player[0]
		dz := z - player[2]
		if dx*dx+dz*dz >= 10*10 {
			spawn(x, z)
			return
		}
	}
	spawn(-12, -12)
}

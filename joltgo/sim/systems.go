package sim

// 系统：每 tick 按固定顺序运行，只通过组件与 s.physics 交互。
// 顺序：输入 → 命中盒跟随 → 物理步进 → 变换同步 → 弹丸命中 → 弹丸过期 → 对局结算。
//
// 碰撞判定全部走物理层，不做距离计算：弹丸命中靠刚体接触事件（ContactListener），
// 打中的是跟随玩家的**命中盒刚体**——玩家本体是 Jolt 角色控制器（不是刚体），
// 弹丸（动态刚体 + CCD）既看不见它、也不会与它产生刚体接触事件。
//
// 变换同步（syncSystem）每 tick 只做一次，放在物理步进之后：弹丸位置、快照读取
// 的都是本 tick 步进后的位置。

import (
	"joltgo/ecs"
	"joltgo/replication"
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
		// 朝向与积分速度无关，只影响下发：从 Input 拆出来单独写一份，
		// 好让同步层只下发 Facing 而不回灌客户端上行的 Input。
		ecs.Add(s.world, s.players[i], Facing{Yaw: in.Yaw})
		s.rep.Set(uint32(s.players[i]), attrFacing, replication.F32(in.Yaw))
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

// hitboxFollowSystem 把每个玩家的命中盒刚体贴到该玩家的角色位置。
//
// 必须在物理步进**之前**运行：弹丸的接触判定用的是步进开始时的刚体位置，晚一步
// 贴就等于拿上一 tick 的旧位置去判定命中（跑动时角色每 tick 挪 0.7 m，会打空）。
func (s *Simulation) hitboxFollowSystem() {
	for i := 0; i < MaxPlayers; i++ {
		if s.hitboxes[i] == ecs.InvalidEntity {
			continue
		}
		p := s.physics.CharacterPosition(i)
		s.physics.SetBodyPosition(uint32(s.hitboxes[i]), p[0], p[1]+hitboxOffsetY, p[2])
	}
}

// syncSystem 把物理世界的最新变换写回组件与同步 store，是 Go 侧与 Jolt 之间
// 唯一的每 tick 枚举点。
//
// Sync 每 tick 会把全部刚体（含永不变化的静态几何）回调一遍，这里照旧全部 Set：
// Store 与「上一次下发值」比较后不标脏，所以静态几何不会产生任何流量。
//
// 没有 Body 组件的刚体（玩家命中盒）直接跳过：它在 ECS 里只是「一个能被认出的
// 碰撞体」，不是可渲染实体——照发 Pos/Rot 就是每 tick 推一对没人看的属性。
func (s *Simulation) syncSystem() {
	s.physics.Sync(func(id uint32, active bool, pos [3]float32, quat [4]float32) {
		e := ecs.Entity(id)
		b, ok := ecs.Get[Body](s.world, e)
		if !ok {
			return
		}
		ecs.Add(s.world, e, Position(pos))
		ecs.Add(s.world, e, Rotation(quat))
		s.rep.Set(id, attrPos, replication.Vec3(pos[0], pos[1], pos[2]))
		s.rep.Set(id, attrRot, replication.Vec4(quat[0], quat[1], quat[2], quat[3]))
		if b.Active != active {
			b.Active = active
			s.rep.Set(id, attrBodyActive, replication.Bool(active))
		}
	})
	for i := 0; i < MaxPlayers; i++ {
		pos := s.physics.CharacterPosition(i)
		ecs.Add(s.world, s.players[i], Position(pos))
		s.rep.Set(uint32(s.players[i]), attrPos, replication.Vec3(pos[0], pos[1], pos[2]))
	}
}

// projectileSystem 从物理层拿到的「全部接触事件」里挑出与弹丸有关的进行结算：
// 打中别人的命中盒 → 扣血、血尽则结算击杀；命中其他任何刚体 → 仅移除弹丸。
// 箱子落地、命中盒彼此不接触等无关接触在这里被忽略。
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
		p, ok := ecs.Get[Projectile](s.world, proj)
		if !ok {
			continue // 同一弹丸的多条接触记录只结算一次
		}
		hb, isHitbox := ecs.Get[PlayerHitbox](s.world, other)
		if !isHitbox && !ecs.Has[Body](s.world, other) {
			continue // 另一方既不是命中盒也不是实体刚体（例如已销毁）
		}
		// 先把要用的值拷出来再销毁弹丸：ecs.Get 返回的指针与 archetype 存储共用内存，
		// Destroy 的 swap-remove 会把它指的内容挪走（见 AGENTS.md「ECS 使用约束」）。
		owner := p.Owner
		victim := -1
		if isHitbox {
			victim = hb.Idx
		}
		if isHitbox && victim == owner {
			// 打中自己的命中盒：既不扣血，也**不**吃掉这颗弹丸。枪口位置由客户端上报，
			// 贴脸或低头时完全可能落在自己的盒里——那是枪口在体内，不是自伤。
			continue
		}
		s.destroyBody(proj)
		if ecs.Has[Projectile](s.world, other) {
			continue // 弹丸互撞：移除当前弹丸，不计伤害
		}
		if isHitbox {
			s.hitPlayer(victim, owner)
		}
	}
}

// hitPlayer 结算一次命中：victim 扣血；血尽则由 killer 记一次击杀、victim 记一次
// 死亡并在己方出生点满血复活。
func (s *Simulation) hitPlayer(victim, killer int) {
	if victim < 0 || victim >= MaxPlayers {
		return
	}
	hp, ok := ecs.Get[Health](s.world, s.players[victim])
	if !ok {
		return
	}
	*hp -= playerHitDamage
	if *hp > 0 {
		s.rep.Set(uint32(s.players[victim]), attrHealth, replication.F32(float32(*hp)))
		return
	}
	if killer >= 0 && killer < MaxPlayers && killer != victim {
		s.score[killer].Kills++
		s.replicateScore(killer, s.score[killer])
	}
	s.score[victim].Deaths++
	s.replicateScore(victim, s.score[victim])
	s.respawn(victim)
}

// respawn 把玩家就地复活：回满血、回己方出生点、速度清零。
// 组件与同步 store 都立刻写 —— 快照在同一个 tick 里就要反映复活结果，否则客户端
// 会看到「满血却还站在死亡点」。
func (s *Simulation) respawn(idx int) {
	x, z := playerSpawnXZ(idx)
	s.physics.SetCharacterPosition(idx, x, playerSpawnY, z)
	s.physics.SetCharacterVelocity(idx, [3]float32{0, 0, 0})
	ecs.Add(s.world, s.players[idx], Health(playerMaxHealth))
	ecs.Add(s.world, s.players[idx], Position{x, playerSpawnY, z})
	s.rep.Set(uint32(s.players[idx]), attrHealth, replication.F32(playerMaxHealth))
	s.rep.Set(uint32(s.players[idx]), attrPos, replication.Vec3(x, playerSpawnY, z))
	// 命中盒一并搬回出生点：它要到下一 tick 的 hitboxFollowSystem 才跟随角色，而
	// 死者的命中盒若留在原地，本 tick 之后飞来的弹丸还会打中「已经复活在别处的
	// 人」——既多扣一次血，也让命中盒与角色脱节一整帧。
	if s.hitboxes[idx] != ecs.InvalidEntity {
		s.physics.SetBodyPosition(uint32(s.hitboxes[idx]), x, playerSpawnY+hitboxOffsetY, z)
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

// matchSystem 对局结算：某一方击杀数先到 killTarget 即分出胜负（写 Game.Winner），
// 再过 matchOverTicks（5 秒）自动重开一局。重开直接走 Reset —— 整张场景（箱子被
// 推乱、弹丸还在飞）都要一并复位，比「只清计数」多不了几行，但不会留下残局。
func (s *Simulation) matchSystem() {
	if s.winner >= 0 {
		if s.step-s.overAt >= matchOverTicks {
			s.reset()
		}
		return
	}
	for i := 0; i < MaxPlayers; i++ {
		if s.score[i].Kills < killTarget {
			continue
		}
		s.winner = int32(i)
		s.overAt = s.step
		s.syncGameState()
		s.rep.Set(uint32(s.game), attrGameWinner, replication.I32(s.winner))
		return
	}
}

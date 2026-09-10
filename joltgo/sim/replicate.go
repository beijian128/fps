package sim

// 同步层：唯一知道「ECS 组件 ↔ 同步属性」映射的地方。
// replication.Store 本身不依赖 ecs，耦合全部集中在本文件。
//
// 属性名用常量而非裸字符串，避免调用点写错名字 —— 写错会 panic（Store 只
// 接受声明过的属性），但常量能在编译期就挡住。

import (
	"joltgo/ecs"
	"joltgo/replication"
)

// 属性名（与 declareAttributes 的声明一一对应；同时是客户端取值的键）。
const (
	attrPos          = "Pos"
	attrRot          = "Rot"
	attrHealth       = "Health"
	attrFacing       = "Facing"
	attrPlayerIdx    = "Player.Idx"
	attrBodyKind     = "Body.Kind"
	attrBodySize     = "Body.Size"
	attrBodyStatic   = "Body.Static"
	attrBodyActive   = "Body.Active"
	attrBodyMat      = "Body.Mat"
	attrEnemy        = "Enemy"
	attrTarget       = "Target"
	attrProjectile   = "Projectile"
	attrResourceKind = "Resource.Kind"
	attrGameScore    = "Game.Score"
	attrGameWave     = "Game.Wave"
	attrGameGold     = "Game.Gold"
)

// declareAttributes 声明全部同步属性。属性表必须完整稳定 —— 客户端在 full
// 帧里一次拿到，之后靠它解码所有增量，所以不能等到首次 Set 才登记。
func declareAttributes(rep *replication.Store) {
	rep.Declare(attrPos, replication.KindVec3)
	rep.Declare(attrRot, replication.KindVec4)
	rep.Declare(attrHealth, replication.KindF32)
	rep.Declare(attrFacing, replication.KindF32)
	rep.Declare(attrPlayerIdx, replication.KindI32)
	rep.Declare(attrBodyKind, replication.KindI32)
	rep.Declare(attrBodySize, replication.KindVec3)
	rep.Declare(attrBodyStatic, replication.KindBool)
	rep.Declare(attrBodyActive, replication.KindBool)
	rep.Declare(attrBodyMat, replication.KindI32)
	rep.Declare(attrEnemy, replication.KindBool)
	rep.Declare(attrTarget, replication.KindBool)
	rep.Declare(attrProjectile, replication.KindBool)
	rep.Declare(attrResourceKind, replication.KindI32)
	rep.Declare(attrGameScore, replication.KindI32)
	rep.Declare(attrGameWave, replication.KindI32)
	rep.Declare(attrGameGold, replication.KindI32)
}

// DrainFrame 取走本帧增量（填好帧号）。
func (s *Simulation) DrainFrame() replication.Frame {
	f := s.rep.Drain()
	f.Step = s.step
	return f
}

// FullFrame 取走全量（填好帧号），供重连 / 首次进入的客户端整体覆盖。
func (s *Simulation) FullFrame() replication.Frame {
	f := s.rep.Full()
	f.Step = s.step
	return f
}

// replicateBodyMeta 把一个刚体的渲染元数据写进同步 store。
// 只在创建时调用一次（静态属性不会变）；变换由 syncSystem 每 tick 推送。
func (s *Simulation) replicateBodyMeta(e ecs.Entity, b Body, pos [3]float32) {
	id := uint32(e)
	s.rep.Set(id, attrBodyKind, replication.I32(int32(b.Kind)))
	s.rep.Set(id, attrBodySize, replication.Vec3(b.Size[0], b.Size[1], b.Size[2]))
	s.rep.Set(id, attrBodyStatic, replication.Bool(b.Static))
	s.rep.Set(id, attrBodyActive, replication.Bool(b.Active))
	s.rep.Set(id, attrBodyMat, replication.I32(int32(b.Mat)))
	s.rep.Set(id, attrPos, replication.Vec3(pos[0], pos[1], pos[2]))
	s.rep.Set(id, attrRot, replication.Vec4(0, 0, 0, 1))
}

// replicatePlayer 把玩家的槽位/位置/血量/朝向写进同步 store。
func (s *Simulation) replicatePlayer(idx int, pos [3]float32, health float32, yaw float32) {
	id := uint32(s.players[idx])
	s.rep.Set(id, attrPlayerIdx, replication.I32(int32(idx)))
	s.rep.Set(id, attrPos, replication.Vec3(pos[0], pos[1], pos[2]))
	s.rep.Set(id, attrHealth, replication.F32(health))
	s.rep.Set(id, attrFacing, replication.F32(yaw))
}

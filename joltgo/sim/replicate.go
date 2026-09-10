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
	attrPlayerKills  = "Player.Kills"
	attrPlayerDeaths = "Player.Deaths"
	attrBodyKind     = "Body.Kind"
	attrBodySize     = "Body.Size"
	attrBodyStatic   = "Body.Static"
	attrBodyActive   = "Body.Active"
	attrBodyMat      = "Body.Mat"
	attrProjectile   = "Projectile"
	attrGameWinner   = "Game.Winner"
)

// declareAttributes 声明全部同步属性。属性表必须完整稳定 —— 客户端在 full
// 帧里一次拿到，之后靠它解码所有增量，所以不能等到首次 Set 才登记。
//
// 玩家命中盒（PlayerHitbox）**不在这里**：它没有任何需要下发的属性，客户端
// 既看不到也不关心它。同步属性表是「下发给客户端的东西」的清单，不是 ECS 组件表。
func declareAttributes(rep *replication.Store) {
	rep.Declare(attrPos, replication.KindVec3)
	rep.Declare(attrRot, replication.KindVec4)
	rep.Declare(attrHealth, replication.KindF32)
	rep.Declare(attrFacing, replication.KindF32)
	rep.Declare(attrPlayerIdx, replication.KindI32)
	rep.Declare(attrPlayerKills, replication.KindI32)
	rep.Declare(attrPlayerDeaths, replication.KindI32)
	rep.Declare(attrBodyKind, replication.KindI32)
	rep.Declare(attrBodySize, replication.KindVec3)
	rep.Declare(attrBodyStatic, replication.KindBool)
	rep.Declare(attrBodyActive, replication.KindBool)
	rep.Declare(attrBodyMat, replication.KindI32)
	rep.Declare(attrProjectile, replication.KindBool)
	rep.Declare(attrGameWinner, replication.KindI32)
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
// 只在创建时调用一次（静态属性不会变，变换由 syncSystem 每 tick 推送）。
// 初始旋转由调用方传入：写死成单位四元数会和 registerBody 的 Rotation 悄悄脱钩，
// 而「静默不同步」正是本任务要防的东西。
func (s *Simulation) replicateBodyMeta(e ecs.Entity, b Body, pos [3]float32, rot [4]float32) {
	id := uint32(e)
	s.rep.Set(id, attrBodyKind, replication.I32(int32(b.Kind)))
	s.rep.Set(id, attrBodySize, replication.Vec3(b.Size[0], b.Size[1], b.Size[2]))
	s.rep.Set(id, attrBodyStatic, replication.Bool(b.Static))
	s.rep.Set(id, attrBodyActive, replication.Bool(b.Active))
	s.rep.Set(id, attrBodyMat, replication.I32(int32(b.Mat)))
	s.rep.Set(id, attrPos, replication.Vec3(pos[0], pos[1], pos[2]))
	s.rep.Set(id, attrRot, replication.Vec4(rot[0], rot[1], rot[2], rot[3]))
}

// replicatePlayer 把玩家的槽位/血量/朝向写进同步 store。
// 位置也在这里写一次，让实体一建出来就是完整的；此后每 tick 由 syncSystem 推送
// （init 末尾会调一次 syncSystem，所以这两处谁先谁后都不会留下不一致）。
func (s *Simulation) replicatePlayer(idx int, pos [3]float32, health float32, yaw float32) {
	id := uint32(s.players[idx])
	s.rep.Set(id, attrPlayerIdx, replication.I32(int32(idx)))
	s.rep.Set(id, attrPos, replication.Vec3(pos[0], pos[1], pos[2]))
	s.rep.Set(id, attrHealth, replication.F32(health))
	s.rep.Set(id, attrFacing, replication.F32(yaw))
}

// replicateScore 把某个槽位的战绩写进玩家组件与同步 store。调用方把 Go 侧数组
// 传进来 —— 字段是真相，组件与 store 都是它的载体，三者一次写完不留两处真相。
func (s *Simulation) replicateScore(idx int, sc PlayerScore) {
	e := s.players[idx]
	ecs.Add(s.world, e, sc)
	id := uint32(e)
	s.rep.Set(id, attrPlayerKills, replication.I32(sc.Kills))
	s.rep.Set(id, attrPlayerDeaths, replication.I32(sc.Deaths))
}

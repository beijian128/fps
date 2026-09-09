// Package game 是 game 服务（backend）：管理对局实例的生命周期，并把 pitaya 的
// 远端 RPC（game.create / game.input / game.shoot / game.reset）翻译成对局实例的
// 命令调用。每个对局一个 goroutine（Instance），内部顺序执行、无锁。
//
// 分层边界：sim/ 是纯 Go 业务层（单线程所有），本包只做「pitaya 协议 ↔ 实例命令」
// 翻译，不做玩法逻辑。物理实现经 physics 包注入（唯一 cgo 包）。
package game

import (
	"context"
	"log"
	"sync"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/component"
	"joltgo/game/protos"
	"joltgo/sim"
)

const (
	snapRoute    = "onSnapshot" // game → 客户端快照 push 的 route
	frontendType = "gate"       // 前端服务类型（与 gate 服务的 serverType 一致）
)

// Component 是 game 服务的 pitaya 组件：持有实例注册表。
type Component struct {
	component.Base
	app pitaya.Pitaya

	mu         sync.Mutex
	instances  map[string]*Instance // matchId -> 实例
	uidToInst  map[string]*Instance // uid -> 实例
	uidToIndex map[string]int       // uid -> player_idx（槽位 0/1）
}

// New 构造 game 组件（在 main.go 里 RegisterRemote 到 app）。
func New(app pitaya.Pitaya) *Component {
	return &Component{
		app:        app,
		instances:  map[string]*Instance{},
		uidToInst:  map[string]*Instance{},
		uidToIndex: map[string]int{},
	}
}

// Create 是远端 RPC handler（route "game.create"）：match 服务在匹配成功后调用，
// 用 uids（按槽位顺序）创建一个新对局实例。
func (c *Component) Create(ctx context.Context, msg *protos.CreateGameMsg) (*protos.CreateGameReply, error) {
	inst := NewInstance(c.app, msg.MatchId, msg.Uids)
	inst.Start()

	c.mu.Lock()
	c.instances[msg.MatchId] = inst
	for idx, uid := range msg.Uids {
		c.uidToInst[uid] = inst
		c.uidToIndex[uid] = idx
	}
	c.mu.Unlock()

	log.Printf("game: created instance %s with %d players", msg.MatchId, len(msg.Uids))
	return &protos.CreateGameReply{Code: 0}, nil
}

// Input 是远端 RPC handler（route "game.input"）：把玩家输入投给对应实例。
// 玩家槽位由会话 UID 映射（match 创建实例时登记）。
func (c *Component) Input(ctx context.Context, msg *protos.InputMsg) {
	inst, idx, ok := c.lookup(ctx)
	if !ok {
		return
	}
	var move [2]float32
	if len(msg.Move) >= 2 {
		move = [2]float32{msg.Move[0], msg.Move[1]}
	}
	inst.ApplyInput(idx, move, msg.Yaw, msg.Jump)
}

// Shoot 是远端 RPC handler（route "game.shoot"）。
func (c *Component) Shoot(ctx context.Context, msg *protos.ShootMsg) {
	inst, _, ok := c.lookup(ctx)
	if !ok {
		return
	}
	var origin, dir [3]float32
	if len(msg.Origin) >= 3 {
		origin = [3]float32{msg.Origin[0], msg.Origin[1], msg.Origin[2]}
	}
	if len(msg.Dir) >= 3 {
		dir = [3]float32{msg.Dir[0], msg.Dir[1], msg.Dir[2]}
	}
	inst.Shoot(origin, dir)
}

// Reset 是远端 RPC handler（route "game.reset"）：重建对局场景。
func (c *Component) Reset(ctx context.Context) {
	inst, _, ok := c.lookup(ctx)
	if !ok {
		return
	}
	inst.Reset()
}

// lookup 按会话 UID 找到实例与玩家槽位。
func (c *Component) lookup(ctx context.Context) (*Instance, int, bool) {
	s := c.app.GetSessionFromCtx(ctx)
	uid := s.UID()
	c.mu.Lock()
	inst := c.uidToInst[uid]
	idx := c.uidToIndex[uid]
	c.mu.Unlock()
	return inst, idx, inst != nil
}

// Shutdown 停止所有对局实例并释放物理世界。
func (c *Component) Shutdown() {
	c.mu.Lock()
	insts := make([]*Instance, 0, len(c.instances))
	for _, inst := range c.instances {
		insts = append(insts, inst)
	}
	c.instances = map[string]*Instance{}
	c.uidToInst = map[string]*Instance{}
	c.uidToIndex = map[string]int{}
	c.mu.Unlock()

	for _, inst := range insts {
		inst.Stop()
	}
}

// toSnapshot 把 sim 层的纯 Go 快照转成 protobuf 快照（wire 契约在 game/protos/game.proto）。
func toSnapshot(s sim.State) *protos.Snapshot {
	snap := &protos.Snapshot{
		Step:  int32(s.Step),
		Score: int32(s.Score),
		Wave:  int32(s.Wave),
		Gold:  int32(s.Gold),
	}
	for _, ps := range s.Players {
		snap.Players = append(snap.Players, &protos.PlayerState{Pos: ps.Pos[:], Health: ps.Health, Yaw: ps.Yaw})
	}
	for _, b := range s.Bodies {
		snap.Bodies = append(snap.Bodies, &protos.BodyInfo{
			Id:         b.ID,
			Type:       int32(b.Type),
			Static:     b.Static,
			Target:     b.Target,
			Enemy:      b.Enemy,
			Projectile: b.Projectile,
			Pos:        b.Pos[:],
			Quat:       b.Quat[:],
			Size:       b.Size[:],
			Health:     b.Health,
			Active:     b.Active,
		})
	}
	for _, r := range s.Resources {
		snap.Resources = append(snap.Resources, &protos.ResourceInfo{
			Id:   int32(r.ID),
			Pos:  r.Pos[:],
			Kind: int32(r.Kind),
		})
	}
	return snap
}

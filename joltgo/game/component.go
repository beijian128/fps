// Package game 是 game 服务（backend）：管理对局实例的生命周期，并把 pitaya 的
// 远端 RPC（game.create / game.cmd）翻译成对局实例的
// 命令调用。每个对局一个 goroutine（Instance），内部顺序执行、无锁。
//
// 分层边界：sim/ 是纯 Go 业务层（单线程所有），本包只做「pitaya 协议 ↔ 实例命令」
// 翻译，不做玩法逻辑。物理实现经 physics 包注入（唯一 cgo 包）。
package game

import (
	"context"
	"fmt"
	"log"
	"sync"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/component"
	"joltgo/game/protos"
	"joltgo/replication"
	"joltgo/sim"
)

const (
	frameRoute   = "onFrame" // game → 客户端同步帧 push 的 route
	frontendType = "gate"    // 前端服务类型（与 gate 服务的 serverType 一致）
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
	// broadcast 按槽位索引固定大小的 pendingFull[sim.MaxPlayers]，超过上限的名单
	// 会在推进帧时越界 panic。match 目前只会发 1~2 个 uid，但这是外部 RPC 入口，
	// 该 panic 路径是本任务新引入的，必须在建实例前就挡掉。
	if len(msg.Uids) > sim.MaxPlayers {
		log.Printf("game: create %s rejected: %d players exceeds max %d",
			msg.MatchId, len(msg.Uids), sim.MaxPlayers)
		// 返回 error 而非仅 Code=1：match 只看 RPC 是否报错，nil error 会让它以为
		// 创建成功、照推 onMatched，而 lookup 永远查不到实例，game.cmd 静默空转。
		return &protos.CreateGameReply{Code: 1}, fmt.Errorf(
			"game: create %s rejected: %d players exceeds max %d",
			msg.MatchId, len(msg.Uids), sim.MaxPlayers)
	}

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

// Rejoin 是远端 RPC handler（route "game.rejoin"）：报告本节点是否托管着该
// token 的存量实例，以及原本的玩家槽位。match 服务用它在重连时找回对局。
func (c *Component) Rejoin(ctx context.Context, msg *protos.RejoinMsg) (*protos.RejoinReply, error) {
	c.mu.Lock()
	inst := c.uidToInst[msg.Token]
	idx := c.uidToIndex[msg.Token]
	c.mu.Unlock()

	if inst == nil {
		return &protos.RejoinReply{Found: false}, nil
	}
	return &protos.RejoinReply{
		Found:     true,
		MatchId:   inst.MatchID(),
		PlayerIdx: int32(idx),
	}, nil
}

// Cmd 是远端 RPC handler（route "game.cmd"）：一帧上行命令。
// 输入、射击、重置合并成一条消息，减少消息数（帧是最小发送单位）。
func (c *Component) Cmd(ctx context.Context, msg *protos.CommandMsg) {
	inst, idx, ok := c.lookup(ctx)
	if !ok {
		return
	}

	var move [2]float32
	if len(msg.Move) >= 2 {
		move = [2]float32{msg.Move[0], msg.Move[1]}
	}
	inst.ApplyInput(idx, move, msg.Yaw, msg.Jump)

	if msg.Shoot {
		var origin, dir [3]float32
		if len(msg.Origin) >= 3 {
			origin = [3]float32{msg.Origin[0], msg.Origin[1], msg.Origin[2]}
		}
		if len(msg.Dir) >= 3 {
			dir = [3]float32{msg.Dir[0], msg.Dir[1], msg.Dir[2]}
		}
		inst.Shoot(origin, dir)
	}
	// 字段名 reset 与 protoc-gen-go 生成的 Reset() 方法冲突，被重命名为 Reset_
	// （wire 字段号仍是 7，语义不变）。
	if msg.Reset_ {
		inst.Reset()
	}
}

// Resync 是远端 RPC handler（route "game.resync"）：把该玩家的下一帧标为全量。
// 客户端在收到 onMatched 之后主动调用 —— 由客户端驱动就没有「onMatched 与
// 全量帧谁先到」的竞态：客户端在收到 full 帧之前会丢弃一切增量。
func (c *Component) Resync(ctx context.Context) {
	inst, idx, ok := c.lookup(ctx)
	if !ok {
		return
	}
	inst.RequestFull(idx)
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

// toFrame 把 sim 层的同步帧转成 protobuf。帧是通用「实体-属性」结构，
// 新增同步属性不需要改动这里。
func toFrame(f replication.Frame) *protos.Frame {
	pf := &protos.Frame{Step: int32(f.Step), Full: f.Full}
	if f.Full {
		pf.Schema = toSchema(f.Schema)
	}
	for _, ed := range f.Entities {
		pe := &protos.EntityDelta{Id: ed.ID, Destroy: ed.Destroy, Removed: ed.Removed}
		for _, av := range ed.Set {
			pe.Set = append(pe.Set, toAttrValue(av))
		}
		pf.Entities = append(pf.Entities, pe)
	}
	return pf
}

// toSchema 把属性表转成 protobuf。
func toSchema(sc replication.Schema) *protos.Schema {
	ps := &protos.Schema{Version: sc.Version}
	for _, a := range sc.Fields {
		ps.Fields = append(ps.Fields, &protos.SchemaField{
			Id:   a.ID,
			Name: a.Name,
			Kind: int32(a.Kind),
		})
	}
	return ps
}

// toAttrValue 按值的类型标签把值放进对应的字段。
func toAttrValue(av replication.AttrValue) *protos.AttrValue {
	pv := &protos.AttrValue{Id: av.Attr}
	switch av.Value.Kind() {
	case replication.KindF32, replication.KindVec2, replication.KindVec3, replication.KindVec4:
		pv.F = av.Value.Floats()
	case replication.KindI32:
		pv.I = av.Value.Int()
	case replication.KindBool:
		pv.B = av.Value.Boolean()
	case replication.KindStr:
		pv.S = av.Value.Text()
	}
	return pv
}

package gm

import (
	"context"
	"errors"
	"log"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"joltgo/game/protos"
)

// 服务类型名与 route 名：前者与各角色启动时的 serverType 一致，
// 后者是三段式 route（server.service.method）。
const (
	logicServerType = "logic"
	matchServerType = "match"

	grantCoinsRoute = "logic.logic.grantcoins"
	addBotsRoute    = "match.match.addbots"
)

// ErrNoServer 表示集群里没有该类型的节点（服务没起、或刚重启）。
var ErrNoServer = errors.New("gm: no server available")

// RPC 是 gm 到后端的最小客户端：两个能力各一条 RPC。
//
// 它只发不收 —— gm 不注册 handler / remote，因此不存在「客户端能调到 gm」这件事。
//
// 能不能发 app.RPCTo 只取决于「这个进程有没有以 pitaya.Cluster 模式构建 app」，
// 与 frontend / backend 无关：gm 是 backend，照样能主动调别的后端。
// 而 pkg/client/client.go 是 acceptor 客户端（连 frontend 的 WS 口、说 pomelo 握手），
// 它发不出后端 RPC —— 这是两回事。
type RPC struct {
	app pitaya.Pitaya
}

// NewRPC 构造 RPC 客户端。
func NewRPC(app pitaya.Pitaya) *RPC {
	return &RPC{app: app}
}

// GrantCoins 转给任意一个 logic 节点，返回变更后的余额。
func (r *RPC) GrantCoins(ctx context.Context, accountID string, delta int64) (int64, error) {
	serverID, err := r.pick(logicServerType)
	if err != nil {
		return 0, err
	}
	reply := &protos.GrantCoinsReply{}
	if err := r.app.RPCTo(ctx, serverID, grantCoinsRoute, reply, &protos.GrantCoinsMsg{
		AccountId: accountID,
		Delta:     delta,
	}); err != nil {
		return 0, err
	}
	if !reply.GetOk() {
		return 0, errors.New("gm: grant coins rejected: " + reply.GetReason())
	}
	return reply.GetCoins(), nil
}

// AddBots 转给任意一个 match 节点，返回实际入队数量。
func (r *RPC) AddBots(ctx context.Context, count int32) (int32, error) {
	serverID, err := r.pick(matchServerType)
	if err != nil {
		return 0, err
	}
	reply := &protos.AddBotsReply{}
	if err := r.app.RPCTo(ctx, serverID, addBotsRoute, reply, &protos.AddBotsMsg{
		Count: count,
	}); err != nil {
		return 0, err
	}
	if !reply.GetOk() {
		return 0, errors.New("gm: add bots rejected: " + reply.GetReason())
	}
	return reply.GetEnqueued(), nil
}

// pick 从服务发现里取任意一个该类型的节点。这两个服务都无状态（状态在 Redis），
// 挑谁都一样 —— 与 match.startMatch 选 game 节点的做法一致。
func (r *RPC) pick(serverType string) (string, error) {
	servers, err := r.app.GetServersByType(serverType)
	if err != nil {
		log.Printf("gm: discover %s failed: %v", serverType, err)
		return "", err
	}
	for id := range servers {
		return id, nil
	}
	return "", ErrNoServer
}

// 编译期确认 *RPC 满足 Handler 依赖的两个窄接口。
var (
	_ CoinsService   = (*RPC)(nil)
	_ AddBotsService = (*RPC)(nil)
)

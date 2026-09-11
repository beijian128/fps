// Package gate 是 gate 服务（frontend）的路由配置：它持有客户端会话、把业务
// 消息路由到后端，并登记会话归属（见 session.go），自身不注册业务 handler。
//
//   - account.* → 轮询任一 account 节点（服务无状态，状态在 Redis）
//   - match.*   → 轮询任一 match 节点（同上）
//   - game.*    → 读会话数据里的 gameServerId，定点路由到托管该对局的 game 节点
//
// gameServerId 由匹配方（match）经定点 RPC 请本 gate 写入（见 session.go 的
// BindGame），因此 gate 的 game 路由函数能据此定位具体 game 节点。
package gate

import (
	"context"
	"errors"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/cluster"
	"github.com/topfreegames/pitaya/v3/pkg/route"
	"github.com/topfreegames/pitaya/v3/pkg/router"
)

// Configure 在 gate 前端注册 account/match/game 三个路由函数。
func Configure(app pitaya.Pitaya) error {
	if err := app.AddRoute("account", routeAny); err != nil {
		return err
	}
	if err := app.AddRoute("match", routeAny); err != nil {
		return err
	}
	return app.AddRoute("game", routeGame(app))
}

// routeAny 轮询任一同类节点。account 与 match 的服务自身无状态（状态都在
// Redis），所以哪个节点处理都一样，不需要一致性哈希。
func routeAny(
	_ context.Context,
	_ *route.Route,
	_ []byte,
	servers map[string]*cluster.Server,
) (*cluster.Server, error) {
	for _, srv := range servers {
		return srv, nil
	}
	return nil, errors.New("no server available")
}

// routeGame 读会话数据里的 gameServerId，定点路由到对应 game 节点。
func routeGame(app pitaya.Pitaya) router.RoutingFunc {
	return func(
		ctx context.Context,
		_ *route.Route,
		_ []byte,
		servers map[string]*cluster.Server,
	) (*cluster.Server, error) {
		s := app.GetSessionFromCtx(ctx)
		gsid, _ := s.Get("gameServerId").(string)
		if gsid != "" {
			if srv, ok := servers[gsid]; ok {
				return srv, nil
			}
		}
		return nil, errors.New("no game server bound to session")
	}
}

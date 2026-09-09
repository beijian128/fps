// Package gate 是 gate 服务（frontend）的路由配置：它只持有客户端会话、把业务
// 消息路由到 match/game 后端，自身不注册业务 handler。
//
//   - match.* → 轮询任一 match 节点
//   - game.* → 读会话数据里的 gameServerId，定点路由到托管该对局的 game 节点
//
// gameServerId 由 match 服务在匹配成功时写入会话数据（Set + PushToFront 同步到
// 前端），因此 gate 的 game 路由函数能据此定位具体 game 节点。
package gate

import (
	"context"
	"errors"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/cluster"
	"github.com/topfreegames/pitaya/v3/pkg/route"
	"github.com/topfreegames/pitaya/v3/pkg/router"
)

// Configure 在 gate 前端注册 match/game 两个路由函数。
func Configure(app pitaya.Pitaya) error {
	if err := app.AddRoute("match", routeMatch); err != nil {
		return err
	}
	return app.AddRoute("game", routeGame(app))
}

// routeMatch 轮询任一 match 节点。
func routeMatch(
	_ context.Context,
	_ *route.Route,
	_ []byte,
	servers map[string]*cluster.Server,
) (*cluster.Server, error) {
	for _, srv := range servers {
		return srv, nil
	}
	return nil, errors.New("no match server available")
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

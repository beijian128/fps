// Package gate 是 gate 服务（frontend）的路由配置：它持有客户端会话、把业务
// 消息路由到后端，并登记会话归属（见 session.go），自身不注册业务 handler。
//
//   - account.* → 轮询任一 account 节点（服务无状态，状态在 Redis）
//   - match.*   → 轮询任一 match 节点（同上）
//   - logic.*   → 从所有 logic 节点中均匀随机选择一个（服务无状态）
//   - game.*    → 读会话数据里的 gameServerId，定点路由到托管该对局的 game 节点
//
// gameServerId 由匹配方（match）经定点 RPC 请本 gate 写入（见 session.go 的
// BindGame），因此 gate 的 game 路由函数能据此定位具体 game 节点。
//
// **四个路由函数都先过客户端上行白名单**（见 routes.go）：前缀路由不是权限 ——
// 客户端发 match.match.addbots 这种内部 route 也会被前缀匹配上并转发出去，所以
// 只有 allowedRoutes 里的 route 才放行，其余回通用的 route not found。
package gate

import (
	"context"
	"errors"
	"math/rand/v2"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/cluster"
	"github.com/topfreegames/pitaya/v3/pkg/route"
	"github.com/topfreegames/pitaya/v3/pkg/router"
)

// Configure 在 gate 前端注册 account/match/logic/game 四个路由函数。
// 它们只放行 allowedRoutes 里的客户端 route（见 routes.go）。
func Configure(app pitaya.Pitaya) error {
	if err := app.AddRoute("account", routeAny); err != nil {
		return err
	}
	if err := app.AddRoute("match", routeAny); err != nil {
		return err
	}
	if err := app.AddRoute("logic", routeRandom); err != nil {
		return err
	}
	return app.AddRoute("game", routeGame(app))
}

// routeAny 轮询任一同类节点。account 与 match 的服务自身无状态（状态都在
// Redis），所以哪个节点处理都一样，不需要一致性哈希。
//
// 白名单在这里生效：不在 allowedRoutes 里的 route 直接拒绝，转发不发生。
func routeAny(
	_ context.Context,
	rt *route.Route,
	_ []byte,
	servers map[string]*cluster.Server,
) (*cluster.Server, error) {
	if err := allowRoute(rt); err != nil {
		return nil, err
	}
	for _, srv := range servers {
		return srv, nil
	}
	return nil, errors.New("no server available")
}

// routeRandom 从所有 logic 节点中均匀随机选择一个。logic 服务无状态，
// 不需要一致性哈希，也不绑定会话。
func routeRandom(
	_ context.Context,
	rt *route.Route,
	_ []byte,
	servers map[string]*cluster.Server,
) (*cluster.Server, error) {
	if err := allowRoute(rt); err != nil {
		return nil, err
	}
	if len(servers) == 0 {
		return nil, errors.New("no server available")
	}
	ids := make([]string, 0, len(servers))
	for id := range servers {
		ids = append(ids, id)
	}
	return servers[ids[rand.IntN(len(ids))]], nil
}

// routeGame 读会话数据里的 gameServerId，定点路由到对应 game 节点。
//
// 白名单同样先生效：原先它只看会话里的 gameServerId、不看 route 名，
// 所以 game.game.create 这类内部 route 也能被客户端发过来。
func routeGame(app pitaya.Pitaya) router.RoutingFunc {
	return func(
		ctx context.Context,
		rt *route.Route,
		_ []byte,
		servers map[string]*cluster.Server,
	) (*cluster.Server, error) {
		if err := allowRoute(rt); err != nil {
			return nil, err
		}
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

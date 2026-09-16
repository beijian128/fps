package gate

import (
	"errors"

	"github.com/topfreegames/pitaya/v3/pkg/route"
)

// 本文件是客户端**上行白名单**：只有出现在这里的 route 才会被 gate 转发。
//
// 为什么需要它：pitaya 的 AddRoute 是「前缀 → 挑节点函数」，gate 只按前缀
// （account / match / logic / game）转发，**不校验具体 route**。于是「客户端能不能
// 打到某个方法」取决于「该前缀下有没有节点注册了同名 handler」—— 这是个隐式规则。
// match.match.addbots 就是反例：它的签名同时满足 handler 与 remote 的收录条件
// （见内置 component/method.go 的 isHandlerMethod / isRemoteMethod），被注册进了
// 客户端可达的 handler 池，而 match.* 前缀是必须转发的。
//
// 白名单**手写**，只收「gate.proto 里定义过、且我们明确同意转发」的 route。
// 手写的好处是「放行」是一个明确的决定；代价是可能写漏，由 routes_test.go 的
// 一致性测试兜住（白名单里每条都能在 gate.proto 找到对应消息定义）。
//
// 清单里每一条都在 gate/protos/gate.proto 里有对应消息，唯一例外是
// game.game.resync（客户端发空 payload）。
var allowedRoutes = map[string]bool{
	"account.account.register": true,
	"account.account.login":    true,
	"account.account.resume":   true,

	"logic.logic.state":    true,
	"logic.logic.purchase": true,
	"logic.logic.equip":    true,
	"logic.logic.profile":  true,

	"match.match.join":    true, // Notify
	"match.match.pending": true,
	"match.match.abandon": true,
	"match.match.cancel":  true,

	"game.game.cmd":    true, // Notify
	"game.game.resync": true, // Notify，空 payload
}

// errRouteNotFound 与 pitaya 的「这条 route 不存在」在客户端看来**完全一样**：
// 不泄漏「哪些名字存在但你不能用」，攻击者拿不到枚举反馈。
var errRouteNotFound = errors.New("route not found")

// allowRoute 报告这条 route 是否允许客户端发；不允许时返回 errRouteNotFound。
//
// 用 rt.String()（server.service.method）而不是 Short()：后者会丢掉服务类型那段。
func allowRoute(rt *route.Route) error {
	if rt == nil {
		return errRouteNotFound
	}
	if allowedRoutes[rt.String()] {
		return nil
	}
	return errRouteNotFound
}

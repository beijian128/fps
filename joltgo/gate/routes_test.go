package gate

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/cluster"
	"github.com/topfreegames/pitaya/v3/pkg/route"
	"github.com/topfreegames/pitaya/v3/pkg/session"
)

// gameRouteApp 只实现 routeGame 用到的 GetSessionFromCtx，会话只实现 Get。
// 不复用 session_test.go 的 fakeSession：它没有 Get，而给它加会牵动其它测试。
type gameRouteApp struct {
	pitaya.Pitaya
	sess session.Session
}

func (a *gameRouteApp) GetSessionFromCtx(context.Context) session.Session { return a.sess }

type gameRouteSession struct {
	session.Session
	gameServerID string
}

func (s *gameRouteSession) Get(key string) interface{} {
	if key == "gameServerId" {
		return s.gameServerID
	}
	return nil
}

func mustRoute(t *testing.T, s string) *route.Route {
	t.Helper()
	rt, err := route.Decode(s)
	if err != nil {
		t.Fatalf("route.Decode(%q): %v", s, err)
	}
	return rt
}

func TestAllowRouteAcceptsWhitelist(t *testing.T) {
	for _, r := range []string{
		"account.account.register", "account.account.login", "account.account.resume",
		"logic.logic.state", "logic.logic.purchase", "logic.logic.equip", "logic.logic.profile",
		"match.match.join", "match.match.pending", "match.match.abandon", "match.match.cancel",
		"game.game.cmd", "game.game.resync",
	} {
		if err := allowRoute(mustRoute(t, r)); err != nil {
			t.Errorf("白名单内的 %s 被拒了: %v", r, err)
		}
	}
}

func TestAllowRouteRejectsEverythingElse(t *testing.T) {
	// 这些是**真实存在**的 route，只是不该由客户端发：GM 的两条（后端用 RPCTo 调）、
	// 以及 game 的三条内部 RPC。拒绝它们正是白名单存在的理由。
	for _, r := range []string{
		"match.match.addbots",
		"logic.logic.grantcoins",
		"game.game.create",
		"game.game.rejoin",
		"game.game.leave",
		"gm.gm.whatever",
		"account.account.delete",
	} {
		if err := allowRoute(mustRoute(t, r)); err == nil {
			t.Errorf("白名单外的 %s 不该被放行", r)
		}
	}
}

func TestRejectedRouteLooksLikeRouteNotFound(t *testing.T) {
	// 拒绝信息必须与「这条 route 根本不存在」无法区分 —— 不泄漏
	// 「哪些名字存在但你不能用」。
	err := allowRoute(mustRoute(t, "match.match.addbots"))
	if err == nil {
		t.Fatal("应被拒绝")
	}
	if !strings.Contains(err.Error(), "route not found") {
		t.Fatalf("拒绝信息应含 route not found，得到 %q", err.Error())
	}
}

// routeMessages 是「route → 它在 gate.proto 里的请求消息名」的显式映射。
//
// 为什么写死而不是按 method 拼前缀：命名本来就不规则 —— cancel 用的是
// MatchCancelMsg、profile 用的是 PlayerProfileMsg、state/cmd 用的消息名里带服务前缀。
// 猜前缀的测试会因为这些「历史命名」而失去意义；写死则顺带把这份对照关系记录下来。
//
// game.game.resync 不在表里：它发空 payload，proto 里没有对应消息。
var routeMessages = map[string]string{
	"account.account.register": "RegisterMsg",
	"account.account.login":    "LoginMsg",
	"account.account.resume":   "ResumeMsg",
	"logic.logic.state":        "LogicStateMsg",
	"logic.logic.purchase":     "PurchaseMsg",
	"logic.logic.equip":        "EquipMsg",
	"logic.logic.profile":      "PlayerProfileMsg",
	"match.match.join":         "JoinMsg",
	"match.match.pending":      "PendingMatchMsg",
	"match.match.abandon":      "AbandonMatchMsg",
	"match.match.cancel":       "MatchCancelMsg",
	"game.game.cmd":            "CommandMsg",
}

// TestAllowedRoutesAreDefinedInGateProto 盯住一致性：白名单里每一条都必须能在
// gate/protos/gate.proto 里找到对应的消息定义，且映射表本身不能漏项。
//
// 这条测试是「白名单手写」这个取舍的配套：手写意味着可能写漏，测试把它兜住。
func TestAllowedRoutesAreDefinedInGateProto(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("protos", "gate.proto"))
	if err != nil {
		t.Fatalf("读 gate.proto: %v", err)
	}
	msgRe := regexp.MustCompile(`(?m)^\s*message\s+([A-Za-z0-9_]+)`)
	defined := map[string]bool{}
	for _, m := range msgRe.FindAllStringSubmatch(string(raw), -1) {
		defined[m[1]] = true
	}

	// 方向一：白名单里的每条 route 都要有映射，且映射的消息真的定义在 gate.proto 里。
	for r := range allowedRoutes {
		if r == "game.game.resync" {
			continue // 空 payload，没有消息体
		}
		msg, ok := routeMessages[r]
		if !ok {
			t.Errorf("route %s 在白名单里，但 routeMessages 里没有它的消息映射", r)
			continue
		}
		if !defined[msg] {
			t.Errorf("route %s 映射到 %s，但 gate.proto 里没有定义这个 message", r, msg)
		}
	}

	// 方向二：映射表不能有白名单之外的多余项（删 route 时容易忘）。
	for r := range routeMessages {
		if !allowedRoutes[r] {
			t.Errorf("routeMessages 里的 %s 不在白名单里 —— 删 route 时忘了清理映射表", r)
		}
	}
}

func TestRoutingFuncsRejectNonWhitelisted(t *testing.T) {
	// 白名单必须在**转发之前**生效：路由函数自己就要拒绝，而不是靠后面的 handler。
	servers := map[string]*cluster.Server{"m1": {ID: "m1", Type: "match"}}

	if _, err := routeAny(context.Background(), mustRoute(t, "match.match.addbots"), nil, servers); err == nil {
		t.Error("routeAny 应拒绝白名单外的 match.match.addbots")
	}
	if _, err := routeRandom(context.Background(), mustRoute(t, "logic.logic.grantcoins"), nil, servers); err == nil {
		t.Error("routeRandom 应拒绝白名单外的 logic.logic.grantcoins")
	}

	// 白名单内的正常放行。
	if _, err := routeAny(context.Background(), mustRoute(t, "match.match.join"), nil, servers); err != nil {
		t.Errorf("routeAny 应放行 match.match.join，得到 %v", err)
	}
	if _, err := routeRandom(context.Background(), mustRoute(t, "logic.logic.state"), nil, servers); err != nil {
		t.Errorf("routeRandom 应放行 logic.logic.state，得到 %v", err)
	}
}

func TestRouteGameRejectsNonWhitelisted(t *testing.T) {
	// routeGame 原先只看会话里的 gameServerId，不看 route 名 ——
	// 所以 game.game.create 这类内部 route 也能被客户端发过来。现在先过白名单。
	app := &gameRouteApp{sess: &gameRouteSession{gameServerID: "g1"}}
	fn := routeGame(app)
	servers := map[string]*cluster.Server{"g1": {ID: "g1", Type: "game"}}

	if _, err := fn(context.Background(), mustRoute(t, "game.game.create"), nil, servers); err == nil {
		t.Error("routeGame 应拒绝白名单外的 game.game.create")
	}
	if _, err := fn(context.Background(), mustRoute(t, "game.game.cmd"), nil, servers); err != nil {
		t.Errorf("routeGame 应放行 game.game.cmd，得到 %v", err)
	}
}

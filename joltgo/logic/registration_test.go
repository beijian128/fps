package logic

import (
	"strings"
	"testing"

	"github.com/topfreegames/pitaya/v3/pkg/component"
)

// TestLogicComponentRegistersHandlersAndRemotes 钉住 logic 节点的注册表。
//
// 为什么要钉：pitaya 的 remote 表按**服务名**注册（同名报 "remote: service already
// defined"），所以一个节点只能注册一个 "logic" 组件，它必须同时提供两类方法：
//
//   - 客户端经 gate 转发的 handler：state / purchase / equip / profile / grantcoins
//   - 服务之间的 remote：online（account 登录时调）/ recordmatch（game 结算时调）
//
// 我一度把 remote 换成「只有客户端方法」的那个组件，结果 logic.online 从 remotes 表里
// 消失，account 的登录直接拿到 route not found（冷启动冒烟才发现）。这条测试就是
// 那个回归的守卫。
func TestLogicComponentRegistersHandlersAndRemotes(t *testing.T) {
	svc := component.NewService(&Logic{}, []component.Option{
		component.WithName("logic"),
		component.WithNameFunc(strings.ToLower),
	})
	if err := svc.ExtractHandler(); err != nil {
		t.Fatalf("ExtractHandler: %v", err)
	}
	if err := svc.ExtractRemote(); err != nil {
		t.Fatalf("ExtractRemote: %v", err)
	}

	// 客户端 handler（经 gate 转发）。
	for _, name := range []string{"state", "purchase", "equip", "profile", "grantcoins"} {
		if _, ok := svc.Handlers[name]; !ok {
			t.Errorf("handler 表里缺少 %s（客户端经 gate 就是查这张表）", name)
		}
	}
	// 服务间 remote（RPCTo / RPC 查的是这张表）。
	for _, name := range []string{"online", "recordmatch", "grantcoins"} {
		if _, ok := svc.Remotes[name]; !ok {
			t.Errorf("remote 表里缺少 %s（account 的 logic.logic.online 与 game 的 "+
				"logic.logic.recordmatch 都靠它，漏了会让登录/结算静默失败）", name)
		}
	}
}

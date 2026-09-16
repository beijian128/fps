package match

import (
	"strings"
	"testing"

	"github.com/topfreegames/pitaya/v3/pkg/component"
)

// TestGMCommandsAreRegisteredInBothTables 把「pitaya 的收录规则」这件事钉在测试里。
//
// 为什么值得一条测试：`(ctx, *Msg) (*Reply, error)` 这个签名**同时**满足
// isHandlerMethod 与 isRemoteMethod（见内置 component/method.go），所以：
//
//   - AddBots 必须在 remotes 表里 —— gm 是用 app.RPCTo（RPCType_User）调它的；
//   - 它同时**必然**出现在客户端可达的 handlers 表里 —— 客户端经 gate 转发时查的就是
//     这张表。拆组件躲不掉：ExtractHandler 没有任何排除机制。
//
// 所以客户端可达性**只能**由 gate 的转发白名单来兜（见 joltgo/gate/routes.go）。
// 如果将来 pitaya 的行为变了（不再双重收录、或支持排除），这条测试会红 ——
// 那时要重新评估白名单是否还是必需的。
func TestGMCommandsAreRegisteredInBothTables(t *testing.T) {
	svc := component.NewService(&Component{}, []component.Option{
		component.WithName("match"),
		component.WithNameFunc(strings.ToLower),
	})
	if err := svc.ExtractHandler(); err != nil {
		t.Fatalf("ExtractHandler: %v", err)
	}
	if err := svc.ExtractRemote(); err != nil {
		t.Fatalf("ExtractRemote: %v", err)
	}

	if _, ok := svc.Remotes["addbots"]; !ok {
		t.Fatal("AddBots 必须在 remote 表里（gm 用 RPCTo 调它）")
	}
	if _, ok := svc.Handlers["addbots"]; !ok {
		t.Fatal("AddBots 同时也在 handler 表里 —— 这正是 gate 白名单必须存在的原因。" +
			"若这里失败，说明 pitaya 的收录规则变了，白名单的前提要重新评估")
	}
}

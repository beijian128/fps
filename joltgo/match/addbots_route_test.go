package match

import (
	"strings"
	"testing"

	"github.com/topfreegames/pitaya/v3/pkg/component"
)

// TestAddBotsRegistersTheRouteGoMUses 钉住远端 route 的名字。
//
// pitaya 的 route 是**推出来的**：`服务名 + "." + 小写(Go 方法名)`
// （见内置 component/suitableRemoteMethods）。所以把方法从 AddBots 改名成
// QueueBots 不会报错、编译也过，只会让 gm 那边的 RPCTo("match.match.addbots")
// 在运行期拿到 "route not found" —— 这正是本次实现踩过的坑。
//
// 这里按 pitaya 自己的方式抽一遍 remote 表，断言 gm 使用的那个 route 字符串真的
// 存在；同时确认机器人入队与 match.cancel 之类的既有 route 不冲突。
func TestAddBotsRegistersTheRouteGMUses(t *testing.T) {
	svc := component.NewService(&Component{}, []component.Option{
		component.WithName("match"),
		component.WithNameFunc(strings.ToLower),
	})
	if err := svc.ExtractRemote(); err != nil {
		t.Fatalf("ExtractRemote 报错: %v", err)
	}

	if _, ok := svc.Remotes["addbots"]; !ok {
		names := make([]string, 0, len(svc.Remotes))
		for name := range svc.Remotes {
			names = append(names, name)
		}
		t.Fatalf("remote 表里没有 addbots（gm 调用的是 match.addbots）；实际有: %v", names)
	}
}

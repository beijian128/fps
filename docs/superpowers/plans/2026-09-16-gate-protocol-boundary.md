# Gate 协议边界与 GM 登录实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 C/S 协议按归属拆成 `gate.proto`（客户端请求）/ `match.proto`（match 推送）/ `game.proto`（game 推送 + 内部），给 gate 加 route 白名单，并把 GM 的共享密钥换成页面账号登录。

**Architecture:** 协议按「请求按接收方、推送按发送方」拆成三个独立 proto 包，彼此没有 import 关系。gate 的四个路由函数（`RoutingFunc` 拿得到解码后的 `*route.Route`）在转发前校验 route 是否在 13 条白名单里，不在就回 `route not found`。白名单上线与「删掉 match/logic handler 里的会话判据」必须同一个提交，中间不留真空期。

**Tech Stack:** Go 1.26、pitaya v3（内置）、protobuf（`protoc --go_out`）、Gin v1.12.0（GM 页面）。

**Spec:** [docs/superpowers/specs/2026-09-16-gate-protocol-boundary-design.md](../specs/2026-09-16-gate-protocol-boundary-design.md)

## Global Constraints

- 所有 Go 命令的工作目录是 `C:\Users\zhubeijian\Desktop\Projects\fps\joltgo`（模块根，`module joltgo`）；文档路径相对仓库根。
- 提交信息用中文，前缀遵循仓库习惯。
- 测试命令一律带 `-count=1`。`./game` 需要 `libjolt_c.dll` 在 PATH：先 `$env:PATH = "$PWD;$env:PATH"`。
- **消息名与字段号一律不变**，只改归属文件与 Go 包名 —— 客户端 hand-written 编解码因此不需要改字段与消息名。
- **不校验 payload 形状**：白名单只管 route 名；参数校验留在各 handler。
- **推送按发送方归文件**：match 推的在 `match.proto`，game 推的在 `game.proto`。account 目前无推送，**不建** `account/protos`。
- **`SlotResult` 留在 `game.proto`**（`MatchEnded` 与 `RecordMatchMsg` 都在那里，零 import）。
- 不往 `third_party/pitaya` 加业务代码：该目录只允许既有的三处安全脱敏补丁。
- GM 密码为空时 `gm` **拒绝启动**（沿用「默认拒绝服务」原则）。
- 白名单与「删 handler 判据」必须在**同一个提交**里落地。

---

## 消息归属清单（本计划的唯一真相，所有 proto 任务都按它做）

### gate/protos/gate.proto

`package gate`、`option go_package = "joltgo/gate/protos"`、Go 包名 `gatepb`。
内容 = 客户端主动请求 + 对应响应，共 20 个 message：

```text
RegisterMsg  LoginMsg  ResumeMsg  LoginReply
LogicStateMsg  PurchaseMsg  EquipMsg  PlayerProfileMsg
LogicStateReply  PlayerProfileReply  LogicShopItem  MatchRecord
JoinMsg  PendingMatchMsg  PendingMatchReply
AbandonMatchMsg  AbandonMatchReply  MatchCancelMsg  MatchCancelReply
CommandMsg
```

### match/protos/match.proto

`package match`、`option go_package = "joltgo/match/protos"`、Go 包名 `matchpb`。
内容 = match 推给客户端的，共 2 个 message：

```text
MatchResult  MatchStatus
```

### game/protos/game.proto

保持现有 `package game` 与 `option go_package = "joltgo/game/protos"`，Go 包名仍是 `protos`。
内容 = game 推给客户端的 + 服务内部，共 23 个 message：

```text
Frame  EntityDelta  AttrValue  Schema  SchemaField
MatchEnded  SlotResult
CreateGameMsg  CreateGameReply  RejoinMsg  RejoinReply
LeaveMsg  LeaveReply  BindGameMsg  BindGameReply
RecordMatchMsg  RecordMatchReply  UserOnlineMsg  UserOnlineReply
AddBotsMsg  AddBotsReply  GrantCoinsMsg  GrantCoinsReply
```

### 每个服务用哪几个包（迁移时的对照表）

| 文件 | 现在的类型 | 迁移后 |
| --- | --- | --- |
| `account/component.go`、`account/component_test.go` | `RegisterMsg` / `LoginMsg` / `ResumeMsg` / `LoginReply` | `gatepb` |
| `account/logic.go`、`account/logic_test.go` | `UserOnlineMsg` / `UserOnlineReply` | 仍 `protos` |
| `gate/session.go`、`gate/session_test.go` | `BindGameMsg` / `BindGameReply` | 仍 `protos` |
| `logic/component.go` | `LogicStateMsg` / `PurchaseMsg` / `EquipMsg` / `PlayerProfileMsg` / `LogicStateReply` / `PlayerProfileReply` / `LogicShopItem` / `MatchRecord` | `gatepb` |
| `logic/component.go` | `GrantCoinsMsg` / `GrantCoinsReply` | 仍 `protos` |
| `logic/remote.go`、`logic/remote_test.go` | `RecordMatch*` / `UserOnline*` | 仍 `protos` |
| `match/match.go` | `JoinMsg` / `PendingMatch*` / `AbandonMatch*` / `MatchCancel*` | `gatepb` |
| `match/match.go` | `CreateGame*` / `Rejoin*` / `Leave*` / `BindGame*` | 仍 `protos` |
| `match/match.go` | `MatchResult` / `MatchStatus` | `matchpb` |
| `match/addbots.go` | `AddBotsMsg` / `AddBotsReply` | 仍 `protos` |
| `game/component.go` | `CommandMsg` | `gatepb` |
| `game/component.go`、`game/instance.go` | 其余 game 类型 | 仍 `protos` |
| `gm/rpc.go` | `AddBots*` / `GrantCoins*` | 仍 `protos` |

---

## Task 1: 拆分 proto 文件并加归属一致性测试

**Files:**
- Create: `joltgo/gate/protos/gate.proto`、`joltgo/match/protos/match.proto`
- Modify: `joltgo/game/protos/game.proto`（只留 game 推送 + 内部消息）
- Regenerate: 三个 `.pb.go`
- Test: `joltgo/game/protos/proto_layout_test.go`（新建；放这里因为它要同时看三份 proto）

**Interfaces:**
- Consumes: 无
- Produces: 三个 protobuf Go 包 —— `gatepb`（`joltgo/gate/protos`）、`matchpb`（`joltgo/match/protos`）、`protos`（`joltgo/game/protos`，包名不变）

- [ ] **Step 1: 确认 protoc 与 protoc-gen-go 可用，并建目录**

```powershell
$env:PATH = "$(go env GOPATH)\bin;" + $env:PATH
protoc --version
New-Item -ItemType Directory -Force -Path gate\protos, match\protos | Out-Null
```

预期：打印 `libprotoc 3x.x`。`protoc-gen-go` 应从 `$(go env GOPATH)\bin` 找到（本机已有）。

- [ ] **Step 2: 写 `gate/protos/gate.proto`**

把上面清单里 `gate.proto` 的 20 个 message **连同它们上方的注释**整段从 `game/protos/game.proto` 剪切过来。**message 名与字段号一个都不许改**。文件骨架：

```proto
// 客户端主动请求与对应响应（C/S 契约的客户端上行部分）。
//
// 这个文件是 gate 转发白名单的**唯一来源**：gate 只转发这里定义过的 route
// （见 joltgo/gate/routes.go 的 allowedRoutes）。服务端主动推送不在这里 ——
// 它们按发送方归到 match/protos/match.proto 与 game/protos/game.proto。
//
// 线上 route 一律三段式 server.service.method：
//   - 客户端 → gate → account：account.account.register / login / resume（Request/Response）
//   - 客户端 → gate → logic：logic.logic.state / purchase / equip / profile（Request/Response）
//   - 客户端 → gate → match：match.match.join（Notify）/ pending / abandon / cancel
//   - 客户端 → gate → game：game.game.cmd（Notify）/ game.game.resync（Notify，空 payload）
//
// 注：gate.sys.kick 用的是 pitaya 内置的 KickMsg / KickAnswer
// （third_party/pitaya/pkg/protos），不在本文件定义范围内 —— 它同样走 C/S 通道，
// 只是方向是服务端 → 客户端。
//
// 序列化用 pitaya 的 protobuf serializer。

syntax = "proto3";

package gate;

option go_package = "joltgo/gate/protos";

// 下面逐个粘贴 20 个 message 的定义（字段号必须原样）。
```

逐个确认搬到了（20 个）：

```text
RegisterMsg  LoginMsg  ResumeMsg  LoginReply
LogicStateMsg  PurchaseMsg  EquipMsg  PlayerProfileMsg
LogicStateReply  PlayerProfileReply  LogicShopItem  MatchRecord
JoinMsg  PendingMatchMsg  PendingMatchReply
AbandonMatchMsg  AbandonMatchReply  MatchCancelMsg  MatchCancelReply
CommandMsg
```

- [ ] **Step 3: 写 `match/protos/match.proto`**

同样把 `MatchResult` 与 `MatchStatus`（含注释）从 `game.proto` 剪切过来：

```proto
// match 推给客户端的消息。
//
// 归属规则：**推送按发送方** —— 谁发的就定义在谁的 proto 里。客户端主动请求在
// gate/protos/gate.proto，game 推的在 game/protos/game.proto。三份文件之间
// **没有 import 关系**（各自的类型自包含）。

syntax = "proto3";

package match;

option go_package = "joltgo/match/protos";
```

- [ ] **Step 4: 收尾 `game/protos/game.proto`**

删掉已搬走的消息，只留清单里那 23 个。文件顶部那段描述 C/S 路由的注释跟着搬去 `gate.proto`，这里换成：

```proto
// game 的协议定义：game 推给客户端的消息 + 服务内部消息。
//
// 归属规则（与 gate/protos/gate.proto、match/protos/match.proto 一致）：
//   - 客户端主动请求 → gate/protos/gate.proto
//   - match 推给客户端的 → match/protos/match.proto
//   - game 推给客户端的、以及服务之间的内部消息 → 本文件
//
// 内部消息留在这里是因为它们的双方（game ↔ logic / gate、gm → game）不涉及客户端；
// 其中 SlotResult 同时被下发的 MatchEnded 与上报的 RecordMatchMsg 使用，两个消息
// 都在本文件，所以零 import。
//
// 同步不走手写快照，走通用「实体-属性」增量：属性表（Schema）在 full 帧里下发一次，
// 之后所有帧都是 (实体, 属性, 终值) 的 op。新增属性不需要改动本文件的字段定义。
//
// 序列化用 pitaya 的 protobuf serializer。
```

`package game` 与 `option go_package = "joltgo/game/protos"` **保持不变**。

- [ ] **Step 5: 重新生成三个包**

```powershell
$env:PATH = "$(go env GOPATH)\bin;" + $env:PATH
protoc --go_out=. --go_opt=paths=source_relative -I . game/protos/game.proto
protoc --go_out=. --go_opt=paths=source_relative -I . gate/protos/gate.proto
protoc --go_out=. --go_opt=paths=source_relative -I . match/protos/match.proto
```

预期：新增 `gate/protos/gate.pb.go`、`match/protos/match.pb.go`，刷新 `game/protos/game.pb.go`。

- [ ] **Step 6: 写归属一致性测试**

```go
// joltgo/game/protos/proto_layout_test.go
package protos

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// messageRe 抓顶层 `message <Name>` 声明。
var messageRe = regexp.MustCompile(`(?m)^\s*message\s+([A-Za-z0-9_]+)`)

// protoMessages 读一个 .proto 并返回它定义的 message 名（排序去重）。
//
// 这里用极简正则而不是 protoc 的解析器：它要守住的是一条归属规则，
// 规则本身出错的风险远大于解析出错的风险。
func protoMessages(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}
	seen := map[string]bool{}
	for _, m := range messageRe.FindAllStringSubmatch(string(raw), -1) {
		seen[m[1]] = true
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func TestProtoLayoutSplitsMessagesByOwner(t *testing.T) {
	root := filepath.Join("..", "..")
	// 变量名刻意避开 gate / match / game：那三个是本文件的包名同类标识，
	// 虽然这里没有 import 它们，但遮蔽同名字符串变量会让后来读的人犯嘀咕。
	gateMsgs := strings.Join(protoMessages(t, filepath.Join(root, "gate", "protos", "gate.proto")), ",")
	matchMsgs := strings.Join(protoMessages(t, filepath.Join(root, "match", "protos", "match.proto")), ",")
	gameMsgs := strings.Join(protoMessages(t, filepath.Join(root, "game", "protos", "game.proto")), ",")

	for _, want := range []string{
		"RegisterMsg", "LoginMsg", "ResumeMsg", "LoginReply",
		"LogicStateMsg", "PurchaseMsg", "EquipMsg", "PlayerProfileMsg",
		"LogicStateReply", "PlayerProfileReply", "LogicShopItem", "MatchRecord",
		"JoinMsg", "PendingMatchMsg", "PendingMatchReply",
		"AbandonMatchMsg", "AbandonMatchReply", "MatchCancelMsg", "MatchCancelReply",
		"CommandMsg",
	} {
		if !strings.Contains(gateMsgs, want) {
			t.Errorf("客户端契约消息 %s 应该在 gate.proto 里", want)
		}
	}
	for _, want := range []string{"MatchResult", "MatchStatus"} {
		if !strings.Contains(matchMsgs, want) {
			t.Errorf("match 推送消息 %s 应该在 match.proto 里", want)
		}
	}
	for _, want := range []string{"Frame", "MatchEnded", "SlotResult", "CreateGameMsg", "RecordMatchMsg"} {
		if !strings.Contains(gameMsgs, want) {
			t.Errorf("game 推送或内部消息 %s 应该在 game.proto 里", want)
		}
	}
}

func TestProtoLayoutKeepsPushesOutOfGateProto(t *testing.T) {
	root := filepath.Join("..", "..")
	gateMsgs := strings.Join(protoMessages(t, filepath.Join(root, "gate", "protos", "gate.proto")), ",")
	for _, pushOnly := range []string{"MatchResult", "MatchStatus", "MatchEnded", "Frame"} {
		if strings.Contains(gateMsgs, pushOnly) {
			t.Errorf("%s 是服务端推送，不该定义在 gate.proto（推送按发送方归文件）", pushOnly)
		}
	}
}
```

- [ ] **Step 7: 跑测试**

```powershell
go test -count=1 -run TestProtoLayout ./game/protos
```

预期：PASS。若 FAIL，说明某个 message 漏搬或搬错文件，按报错调整。

- [ ] **Step 8: 确认服务层此时**编译失败**（预期）**

```powershell
go build ./...
```

预期：**失败**，报 `undefined: protos.LoginMsg` 之类。这是正常的 —— 服务层还在用旧包，Task 2~5 逐个迁移。**不要在 Task 1 里改服务代码。**

- [ ] **Step 9: 提交（只提交 proto 与生成码）**

```powershell
git add joltgo/gate/protos joltgo/match/protos joltgo/game/protos
git commit -m "refactor: 协议按归属拆成 gate/match/game 三份 proto"
```

> 这个提交之后仓库处于不可编译状态，直到 Task 5 结束。连续实施没问题；不要把这个中间状态推给别人。要避免的话把 Task 1~5 合成一个提交。

---

## Task 2: 迁移 `account` 到新包

**Files:**
- Modify: `joltgo/account/component.go`、`joltgo/account/component_test.go`
- Test: 既有 `account` / `gate` 测试

**Interfaces:**
- Consumes: `gatepb`（Task 1）
- Produces: `account.Component` 的 handler 签名改用 `*gatepb.LoginReply` 等；**route 名不变**（pitaya 用方法名推 route，包名不参与）

- [ ] **Step 1: 改 import 与引用**

`account/component.go` 的 import 块加 `gatepb`、去掉不再需要的 `protos`（`account/logic.go` 仍需要它，那是独立文件）：

```go
import (
	"context"
	"log"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/component"
	pitayaprotos "github.com/topfreegames/pitaya/v3/pkg/protos"
	gatepb "joltgo/gate/protos"
	"joltgo/online"
)
```

然后把该文件里属于客户端契约的引用改掉：

```text
protos.RegisterMsg → gatepb.RegisterMsg
protos.LoginMsg    → gatepb.LoginMsg
protos.ResumeMsg   → gatepb.ResumeMsg
protos.LoginReply  → gatepb.LoginReply
```

`pitayaprotos.KickMsg` / `KickAnswer` 不动（pitaya 内置）。

`account/component_test.go` 同样改这三个请求类型到 `gatepb`。

`account/logic.go` 与 `account/logic_test.go` **不动**（`UserOnlineMsg` / `UserOnlineReply` 仍在 game 包）。

- [ ] **Step 2: 确认 gate 侧无需改动**

```powershell
rg -n 'protos\.' gate
```

预期：只有 `protos.BindGameMsg` / `protos.BindGameReply`（game 包）。若出现客户端契约类型，按对照表改到 `gatepb`。

- [ ] **Step 3: 跑测试**

```powershell
go test -count=1 ./account ./gate
```

预期：全绿（其它包此刻仍编译不过，所以只跑这两个）。

- [ ] **Step 4: 提交**

```powershell
git add joltgo/account
git commit -m "refactor: account 的客户端契约改用 gatepb"
```

---

## Task 3: 迁移 `logic` 并删掉发钱 handler 的鉴权

**Files:**
- Modify: `joltgo/logic/component.go`（换包 + 删鉴权）
- Modify: `joltgo/logic/component_test.go`、`joltgo/logic/grant_coins_component_test.go`
- Modify: `joltgo/game/protos/game.proto`（删两个 `admin_key` 字段）并重新生成
- Test: 既有 `logic` 测试 + 新增两条

**Interfaces:**
- Consumes: `gatepb`（Task 1）
- Produces: `func NewComponent(app pitaya.Pitaya, service *Service) *Component`（**回到两参数**；`NewComponentWithSecret` 删除）；`Component.GrantCoins` 不再检查调用方

> 删判据会让 `logic.logic.grantcoins` 对「任何能发 `logic.*` 的人」开放。白名单（Task 6）是它的替代品；若要严格零真空，把本任务的「删判据」挪到 Task 6 同一个提交（见计划末尾的自查记录）。

- [ ] **Step 1: 改测试（先写新契约）**

`logic/grant_coins_component_test.go` 删掉这三个用例（它们断言的是即将删除的行为）：

```text
TestGrantCoinsHandlerRejectsClientCall
TestGrantCoinsHandlerRejectsWrongKey
TestGrantCoinsHandlerRejectsEmptySecret
```

补两条描述**新**契约的：

```go
func TestGrantCoinsHandlerDoesNotCheckCaller(t *testing.T) {
	// 客户端可达性由 gate 的转发白名单负责（logic.grantcoins 不在白名单里，
	// 客户端发不过来）；handler 本身不再做鉴权 —— 这是 2026-09-16 的明确取舍。
	env, _ := newLockedServiceTestEnv(t)
	if err := env.svc.EnsureProfile(context.Background(), "7"); err != nil {
		t.Fatal(err)
	}
	seedAccount(t, env, 7, "alice")

	// 带会话的调用（客户端经 gate 转发时的样子）现在也照常执行。
	comp := NewComponent(&fakeApp{sess: &fakeSession{uid: "7"}}, env.svc)
	reply, err := comp.GrantCoins(context.Background(), &protos.GrantCoinsMsg{AccountId: "7", Delta: 500})
	if err != nil {
		t.Fatal(err)
	}
	if !reply.Ok || reply.Coins != 1500 {
		t.Fatalf("reply=%+v，期望 ok 且 1500", reply)
	}
}

func TestGrantCoinsHandlerNeedsNoSecret(t *testing.T) {
	// 无会话的后端调用 + 请求里没有任何密钥字段，应照常成功。
	env, _ := newLockedServiceTestEnv(t)
	if err := env.svc.EnsureProfile(context.Background(), "7"); err != nil {
		t.Fatal(err)
	}
	seedAccount(t, env, 7, "alice")
	comp := NewComponent(&fakeApp{}, env.svc)
	reply, err := comp.GrantCoins(context.Background(), &protos.GrantCoinsMsg{AccountId: "7", Delta: 1})
	if err != nil || !reply.Ok || reply.Coins != 1001 {
		t.Fatalf("reply=%+v err=%v，期望 ok 且 1001", reply, err)
	}
}
```

原来的 helper `newGrantCoinsComponentEnv` 要跟着改（`NewComponentWithSecret` 换成 `NewComponent`、去掉 `"s3cret"` 实参）。

- [ ] **Step 2: 跑测试确认失败**

```powershell
go test -count=1 -run TestGrantCoins ./logic
```

预期：编译失败（`AdminKey` 字段还在、`NewComponent` 参数不匹配）。

- [ ] **Step 3: 从 proto 里删掉两个 `admin_key` 字段并重新生成**

`game/protos/game.proto` 删掉两行（连同它们上方的说明）—— 用这两行做定位锚点，**不要按行号找**（文件会被前面的改动挪动）：

```proto
  string admin_key = 2;  // 在 AddBotsMsg 里（count 后面）
  string admin_key = 3;  // 在 GrantCoinsMsg 里（delta 后面）
```

**字段号不要复用、不要重编号** —— 这是内部消息，没有旧客户端需要兼容。

```powershell
$env:PATH = "$(go env GOPATH)\bin;" + $env:PATH
protoc --go_out=. --go_opt=paths=source_relative -I . game/protos/game.proto
```

- [ ] **Step 4: 改 `logic/component.go`**

import 换成「客户端契约走 `gatepb`、GM 消息仍走 `protos`」：

```go
import (
	"context"
	"log"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/component"
	"joltgo/game/protos"
	gatepb "joltgo/gate/protos"
)
```

删掉这些（整块）：

```text
Component.secret 字段（含注释）
UpdateSecret 方法
adminSecret 方法
adminKeyAllowed 函数
NewComponentWithSecret 函数
GrantCoins 里的第一道判据（GetSessionFromCtx + log + forbidden）
GrantCoins 里的第二道判据（adminKeyAllowed + log + forbidden）
```

`GrantCoins` 收敛成：

```go
// GrantCoins 是远端 RPC handler（route "logic.logic.grantcoins"）：GM 请求给账号发钱。
//
// 不做调用方鉴权（2026-09-16 的明确取舍）：客户端发不到这条 route —— 它不在 gate 的
// 转发白名单里（见 joltgo/gate/routes.go 的 allowedRoutes），而 gate 是客户端唯一的
// 入口。集群内部进程本就能调它，这不是鉴权能解决的问题。
// 参数校验与「账号不存在不隐式建档」仍然在 Service.GrantCoins 里。
func (c *Component) GrantCoins(ctx context.Context, msg *protos.GrantCoinsMsg) (*protos.GrantCoinsReply, error) {
	coins, err := c.service.GrantCoins(ctx, msg.GetAccountId(), msg.GetDelta())
	if err != nil {
		return &protos.GrantCoinsReply{Ok: false, Reason: ReasonOf(err)}, nil
	}
	return &protos.GrantCoinsReply{Ok: true, Coins: coins}, nil
}
```

其余 handler 的客户端契约类型改到 `gatepb`：

```text
protos.LogicStateMsg      → gatepb.LogicStateMsg
protos.PurchaseMsg        → gatepb.PurchaseMsg
protos.EquipMsg           → gatepb.EquipMsg
protos.PlayerProfileMsg   → gatepb.PlayerProfileMsg
protos.LogicStateReply    → gatepb.LogicStateReply
protos.PlayerProfileReply → gatepb.PlayerProfileReply
protos.LogicShopItem      → gatepb.LogicShopItem
protos.MatchRecord        → gatepb.MatchRecord
```

`logic/remote.go` **不动**（`RecordMatchMsg/Reply`、`UserOnlineMsg/Reply` 仍在 game 包）。

- [ ] **Step 5: 跑测试确认通过**

```powershell
go test -count=1 ./logic ./persist
```

预期：全绿（含 Step 1 新增的两条）。

- [ ] **Step 6: 提交**

```powershell
git add joltgo/logic joltgo/game/protos
git commit -m "refactor: logic 换用 gatepb，并去掉发钱 handler 的鉴权"
```

---

## Task 4: 迁移 `match`（只换包，不删判据）

**Files:**
- Modify: `joltgo/match/match.go`
- Modify: `joltgo/match/cancel_test.go`、`match_test.go`、`pending_abandon_test.go`、`bot_pairing_test.go`
- Test: 既有 `match` 测试

**Interfaces:**
- Consumes: `gatepb`、`matchpb`（Task 1）
- Produces: route 名不变；`pushMatched` 用 `*matchpb.MatchResult`、`pushMatchStatus` 用 `*matchpb.MatchStatus`

> 本任务**只换包**。`match.AddBots` 里的两道判据与 `match/auth.go` 留到 Task 6（白名单上线后）再删。

- [ ] **Step 1: 改 `match/match.go` 的 import**

```go
import (
	"context"
	"errors"
	"log"
	"sync/atomic"
	"time"

	"github.com/nats-io/nuid"
	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/cluster"
	"github.com/topfreegames/pitaya/v3/pkg/component"
	"joltgo/online"
	"joltgo/game/protos"
	gatepb "joltgo/gate/protos"
	matchpb "joltgo/match/protos"
)
```

（gofmt 会重排 import 顺序，不必手排。）

- [ ] **Step 2: 按对照表换包**

```text
protos.JoinMsg            → gatepb.JoinMsg
protos.PendingMatchMsg    → gatepb.PendingMatchMsg
protos.PendingMatchReply  → gatepb.PendingMatchReply
protos.AbandonMatchMsg    → gatepb.AbandonMatchMsg
protos.AbandonMatchReply  → gatepb.AbandonMatchReply
protos.MatchCancelMsg     → gatepb.MatchCancelMsg
protos.MatchCancelReply   → gatepb.MatchCancelReply
protos.MatchResult        → matchpb.MatchResult
protos.MatchStatus        → matchpb.MatchStatus
```

保持 `protos.` 不变：`CreateGameMsg`、`CreateGameReply`、`RejoinMsg`、`RejoinReply`、`LeaveMsg`、`LeaveReply`、`BindGameMsg`、`BindGameReply`。

`match/addbots.go` 不动（`AddBotsMsg/Reply` 仍在 game 包）。

- [ ] **Step 3: 改测试里的包引用**

```text
match/cancel_test.go          MatchCancelMsg → gatepb；MatchStatus → matchpb
match/match_test.go           JoinMsg → gatepb；BindGameReply 仍 protos
match/pending_abandon_test.go Pending*/Abandon* → gatepb；Leave*/BindGame*/Rejoin* 仍 protos
match/bot_pairing_test.go     BindGame*/CreateGame*/AddBotsMsg 全部仍 protos（无需改）
match/addbots_test.go         AddBotsMsg 仍 protos（无需改）
```

- [ ] **Step 4: 跑测试**

```powershell
go test -count=1 ./match
```

预期：全绿（含 `addbots_route_test.go`；它检查 remote 表里有 `addbots`，与换包无关）。

- [ ] **Step 5: 提交**

```powershell
git add joltgo/match
git commit -m "refactor: match 换用 gatepb 与 matchpb"
```

---

## Task 5: 迁移 `game`，并把迁移完成的仓库恢复成全绿

**Files:**
- Modify: `joltgo/game/component.go`（`CommandMsg` → `gatepb`）
- Modify: `joltgo/game/*_test.go`（`CommandMsg` 引用）
- Test: 既有 `game` / `gm` 测试 + 全量

**Interfaces:**
- Consumes: `gatepb`（Task 1）
- Produces: `game.Component.Cmd(ctx, *gatepb.CommandMsg)`；其余 game 侧类型仍在 `protos`

> `game.Create` / `Rejoin` / `Leave` 里的会话判据**保留** —— spec §5.1 的表格写明了理由：它们除了鉴权还承担入参健全性（例如 `Create` 挡「uids 超过 `sim.MaxPlayers`」，超了会在推进帧时越界 panic），挡的是「任何调用方把节点搞崩」，不是「防玩家」。

- [ ] **Step 1: 改 `game/component.go`**

import 加 `gatepb`：

```go
import (
	"context"
	"fmt"
	"log"
	"sync"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/component"
	"joltgo/bot"
	"joltgo/game/protos"
	gatepb "joltgo/gate/protos"
	"joltgo/replication"
	"joltgo/sim"
)
```

只改一处：

```text
func (c *Component) Cmd(ctx context.Context, msg *protos.CommandMsg)  →  *gatepb.CommandMsg
```

其余（`CreateGameMsg`、`RejoinMsg`、`LeaveMsg`、`Frame`、`Schema`…）保持 `protos`。

- [ ] **Step 2: 确认 `game/instance.go` 无需改动**

```powershell
rg -n 'protos\.' game\instance.go
```

预期：只有 `Frame` / `MatchEnded` / `SlotResult` 等 game 包类型。

- [ ] **Step 3: 改测试**

```powershell
rg -n 'protos\.CommandMsg' game
```

把命中的改成 `gatepb.CommandMsg`，并给这些测试文件加 `gatepb` import。

- [ ] **Step 4: 跑 game 与 gm 测试**

```powershell
$env:PATH = "$PWD;$env:PATH"
go test -count=1 ./game ./gm
```

预期：全绿。

- [ ] **Step 5: 全仓编译 + 全量测试（协议迁移到此结束，必须全绿）**

```powershell
$env:PATH = "$PWD;$env:PATH"
go build ./...
go test -count=1 ./...
```

预期：编译通过、全绿。**若仍有 `undefined: protos.X`，说明某处引用漏改 —— 修掉再提交。**

- [ ] **Step 6: 提交**

```powershell
git add joltgo/game
git commit -m "refactor: game 的上行命令改用 gatepb，协议迁移完成"
```

---

## Task 6: gate 转发白名单 + 删掉 `match` 的鉴权（同一个提交）

**Files:**
- Create: `joltgo/gate/routes.go`、`joltgo/gate/routes_test.go`
- Modify: `joltgo/gate/gate.go`（四个路由函数接入校验）
- Delete: `joltgo/match/auth.go`、`joltgo/match/auth_test.go`
- Modify: `joltgo/match/addbots.go`（删两道判据）、`joltgo/match/match.go`（删 secret 与 `New` 的第四参数）
- Test: `joltgo/match/addbots_test.go`（改）、`joltgo/match/bot_pairing_test.go`（helper 改）

**Interfaces:**
- Consumes: `gate.proto` 的 20 个消息（Task 1）、`match.Component`（Task 4）
- Produces:
  - `var allowedRoutes map[string]bool`（13 条）
  - `func allowRoute(rt *route.Route) error`（不在清单里返回 `errRouteNotFound`）
  - `func New(app pitaya.Pitaya, queue *Queue, onl *online.Store) *Component`（回到三参数）

- [ ] **Step 1: 写失败的白名单测试**

```go
// joltgo/gate/routes_test.go
package gate

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/topfreegames/pitaya/v3/pkg/cluster"
	"github.com/topfreegames/pitaya/v3/pkg/route"
)

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
	// 这些是**真实存在**的 route，只是不该由客户端发：GM 的两条（后端可达），
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
	// 拒绝信息必须与「这条 route 根本不存在」无法区分，不泄漏
	//「哪些名字存在但你不能用」。
	err := allowRoute(mustRoute(t, "match.match.addbots"))
	if err == nil {
		t.Fatal("应被拒绝")
	}
	if !strings.Contains(err.Error(), "route not found") {
		t.Fatalf("拒绝信息应含 route not found，得到 %q", err.Error())
	}
}

// TestAllowedRoutesAreDefinedInGateProto 盯住一致性：白名单里每一条都必须能在
// gate/protos/gate.proto 里找到对应的消息定义。手写白名单的代价是可能写漏，
// 这条测试把它兜住。
func TestAllowedRoutesAreDefinedInGateProto(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "gate", "protos", "gate.proto"))
	if err != nil {
		t.Fatalf("读 gate.proto: %v", err)
	}
	msgRe := regexp.MustCompile(`(?m)^\s*message\s+([A-Za-z0-9_]+)`)
	names := make([]string, 0)
	for _, m := range msgRe.FindAllStringSubmatch(string(raw), -1) {
		names = append(names, m[1])
	}

	// payload 为空的 route：proto 里确实没有对应消息。
	emptyPayload := map[string]bool{"game.game.resync": true}

	for r := range allowedRoutes {
		if emptyPayload[r] {
			continue
		}
		parts := strings.Split(r, ".")
		method := parts[len(parts)-1]
		prefix := strings.ToUpper(method[:1]) + method[1:]
		found := false
		for _, n := range names {
			if strings.HasPrefix(n, prefix) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("白名单里的 %s 在 gate.proto 里找不到 %s* 消息定义", r, prefix)
		}
	}
}

func TestGateRoutesRejectNonWhitelisted(t *testing.T) {
	// 白名单必须在**转发之前**生效：路由函数本身要拒绝。
	servers := map[string]*cluster.Server{"m1": {ID: "m1", Type: "match"}}
	if _, err := routeAny(context.Background(), mustRoute(t, "match.match.addbots"), nil, servers); err == nil {
		t.Fatal("match.* 的转发函数应拒绝白名单外的 addbots")
	}
	if _, err := routeAny(context.Background(), mustRoute(t, "match.match.join"), nil, servers); err != nil {
		t.Fatalf("白名单内的 join 应放行，得到 %v", err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```powershell
go test -count=1 -run 'TestAllowRoute|TestRejectedRoute|TestAllowedRoutes|TestGateRoutesReject' ./gate
```

预期：编译失败（`undefined: allowRoute` / `undefined: allowedRoutes`）。

- [ ] **Step 3: 实现白名单**

```go
// joltgo/gate/routes.go
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
// match.match.addbots 就是反例：它的签名同时满足 handler 与 remote 的收录条件，
// 被注册进了客户端可达的 handler 池，而 match.* 前缀是必须转发的。
//
// 白名单**手写**，只收「gate.proto 里定义过、且我们明确同意转发」的 route。
// 手写的好处是放行是一个明确的决定；代价是可能写漏，由 routes_test.go 的一致性
// 测试兜住（白名单里每条都能在 gate.proto 找到对应消息定义）。
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

// errRouteNotFound 与 pitaya 的「这条 route 不存在」在客户端看来完全一样：
// 不泄漏「哪些名字存在但你不能用」，攻击者拿不到枚举反馈。
var errRouteNotFound = errors.New("route not found")

// allowRoute 报告这条 route 是否允许客户端发；不允许时返回 errRouteNotFound。
//
// 用 rt.String() 而不是 Short()：后者会丢掉服务类型这段。
func allowRoute(rt *route.Route) error {
	if rt == nil {
		return errRouteNotFound
	}
	if allowedRoutes[rt.String()] {
		return nil
	}
	return errRouteNotFound
}
```

- [ ] **Step 4: 接入四个路由函数**

改 `joltgo/gate/gate.go`：`routeAny` 与 `routeRandom` 在函数体第一行加校验，`routeGame` 在返回的闭包第一行加。

```go
// routeAny 轮询任一同类节点。account 与 match 自身无状态（状态都在 Redis），
// 所以哪个节点处理都一样，不需要一致性哈希。
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
```

`routeRandom` 同样在第一行加 `if err := allowRoute(rt); err != nil { return nil, err }`（注意它的参数名要从 `_` 改成 `rt`）。

```go
// routeGame 读会话数据里的 gameServerId，定点路由到对应 game 节点。
//
// 白名单同样生效：原先它只看会话里的 gameServerId、不看 route 名，
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
```

同步更新 `Configure` 的 doc comment：说明它现在「注册四个路由函数，且只放行 allowedRoutes 里的客户端 route」。

- [ ] **Step 5: 删掉 match 侧的鉴权（同一个提交）**

```powershell
git rm joltgo/match/auth.go joltgo/match/auth_test.go
```

`match/addbots.go` 删掉开头这两段：

```go
	if isClientCall(ctx, c.app) {
		log.Printf("match: addbots from client rejected")
		return &protos.AddBotsReply{Ok: false, Reason: "forbidden"}, nil
	}
	if !adminKeyAllowed(c.adminSecret(), msg.GetAdminKey()) {
		log.Printf("match: addbots rejected: bad admin key")
		return &protos.AddBotsReply{Ok: false, Reason: "forbidden"}, nil
	}
```

`AddBots` 的 doc comment 换成：

```go
// AddBots 是远端 RPC handler（route "match.match.addbots"）：GM 请求往匹配队列里
// 塞 N 个机器人凑人数。
//
// **方法名就是 route**：pitaya 按「服务名 + 小写方法名」推出 route，所以这里必须叫
// AddBots（叫 QueueBots 的话 route 会变成 match.queuebots，gm 会拿到 route not found）。
//
// **不做调用方鉴权**（2026-09-16 的明确取舍）：客户端发不到这条 route —— 它不在
// gate 的转发白名单里（见 joltgo/gate/routes.go），而 gate 是客户端唯一的入口；
// 集群内部进程本就能调它，那不是鉴权能解决的问题。
//
// 入队语义留在本包：gm 不碰 match:queue、也不构造 bot uid —— 队列的写入口只有
// Queue.Enqueue 这一条路径。
```

`match/match.go` 删掉 `secret atomic.Value` 字段（含注释）、`UpdateSecret`、`adminSecret`，`New` 回到三参数：

```go
// New 构造 match 组件。
func New(app pitaya.Pitaya, queue *Queue, onl *online.Store) *Component {
	return &Component{app: app, queue: queue, online: onl}
}
```

若 `sync/atomic` 不再被使用，从 import 里删掉。

- [ ] **Step 6: 改 `addbots_test.go`**

删掉这两个用例（断言的是一经删除的行为）：

```text
TestQueueBotsRejectsClientCall
TestQueueBotsRejectsWrongKey
```

把余下用例里的 `AdminKey: "s3cret"` 删掉，helper 改成：

```go
func newBotsTestComponent(t *testing.T, sess *joinTestSession) (*Component, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	app := &joinTestApp{}
	if sess != nil {
		app.sess = sess
	}
	return New(app, NewQueue(rdb), online.NewStore(rdb)), mr
}
```

补一条描述新契约的：

```go
func TestQueueBotsDoesNotCheckCaller(t *testing.T) {
	// 带会话的调用（客户端经 gate 转发时的样子）现在也照常执行：
	// 可达性由 gate 的转发白名单负责，handler 不再判调用方。
	c, _ := newBotsTestComponent(t, &joinTestSession{uid: "7"})
	ctx := context.Background()
	reply, err := c.AddBots(ctx, &protos.AddBotsMsg{Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !reply.Ok || reply.Enqueued != 1 {
		t.Fatalf("reply=%+v，期望 ok 且 1", reply)
	}
}
```

- [ ] **Step 7: 改 `bot_pairing_test.go` 的 helper**

`newRawMatchEnv` 去掉 `secret` 形参与实参，`newBotPairEnv` 里 `New(...)` 的调用同步改。`botPairApp.GetSessionFromCtx`（返回 nil 的显式实现）**保留** —— 它只是让 stub 更像真实后端。

- [ ] **Step 8: 跑测试**

```powershell
go test -count=1 ./gate ./match
```

预期：全绿。

- [ ] **Step 9: 提交（白名单与删判据必须在这一次提交里）**

```powershell
git add joltgo/gate joltgo/match
git commit -m "feat: gate 增加转发白名单，并去掉 match 管理 handler 的鉴权"
```

---

## Task 7: GM 改用账号登录，删掉共享密钥

**Files:**
- Modify: `joltgo/gm/server.go`、`joltgo/gm/rpc.go`、`joltgo/gm/index.html`
- Modify: `joltgo/gm/server_test.go`、`joltgo/gm/page_test.go`
- Modify: `joltgo/main.go`、`joltgo/deploy/start-all.ps1`
- Test: 新建 `joltgo/gm/auth_test.go`

**Interfaces:**
- Consumes: `gm.Handler` / `gm.RPC`（既有）
- Produces:
  - `func NewHandler(coins CoinsService, bots AddBotsService, accounts UsernameResolver, user, pass, secret string) *Handler`
  - `func NewRPC(app pitaya.Pitaya) *RPC`
  - `func (h *Handler) signSession(user string, expiry time.Time) string`
  - `const sessionCookie = "gm_session"`
  - flag：`-gmuser`（默认 `admin`）、`-gmpass`（必填）、`-gmsecret`（为空回落 `-gmpass`）

- [ ] **Step 1: 写失败的登录测试**

```go
// joltgo/gm/auth_test.go
package gm

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func login(t *testing.T, h *Handler, user, pass string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"user":"` + user + `","password":"` + pass + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Router().ServeHTTP(w, req)
	return w
}

func cookieOf(t *testing.T, w *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("响应里没有 cookie %s", name)
	return nil
}

func TestLoginSetsCookieAndGrantsAPI(t *testing.T) {
	h, coins, _ := newTestHandler(t)
	w := login(t, h, "admin", "s3cret")
	if w.Code != http.StatusOK {
		t.Fatalf("登录 = %d，body=%s", w.Code, w.Body.String())
	}
	c := cookieOf(t, w, sessionCookie)
	if !c.HttpOnly {
		t.Error("会话 cookie 应该是 HttpOnly")
	}

	w2 := postJSON(t, h.Router(), "/api/coins", `{"target":"alice","delta":5}`, c.Value)
	if w2.Code != http.StatusOK {
		t.Fatalf("带 cookie 的 /api/coins = %d，body=%s", w2.Code, w2.Body.String())
	}
	if coins.gotID != "10001" {
		t.Fatalf("下游应收到 10001，得到 %q", coins.gotID)
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	h, coins, _ := newTestHandler(t)
	w := login(t, h, "admin", "nope")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("密码错误 = %d，期望 401", w.Code)
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("失败时不该下发 cookie")
	}
	if coins.gotID != "" {
		t.Fatal("登录失败的请求不该触达下游")
	}
}

func TestLoginRejectsWrongUser(t *testing.T) {
	h, _, _ := newTestHandler(t)
	if w := login(t, h, "root", "s3cret"); w.Code != http.StatusUnauthorized {
		t.Fatalf("账号错误 = %d，期望 401", w.Code)
	}
}

func TestAPIsRequireCookie(t *testing.T) {
	h, coins, bots := newTestHandler(t)
	if w := postJSON(t, h.Router(), "/api/coins", `{"target":"alice","delta":1}`, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("无 cookie 访问 /api/coins = %d，期望 401", w.Code)
	}
	if w := postJSON(t, h.Router(), "/api/bots", `{"count":1}`, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("无 cookie 访问 /api/bots = %d，期望 401", w.Code)
	}
	if coins.gotID != "" || bots.gotCount != 0 {
		t.Fatal("未鉴权的请求不该触达下游")
	}
}

func TestForgedCookieIsRejected(t *testing.T) {
	h, _, _ := newTestHandler(t)
	w := postJSON(t, h.Router(), "/api/bots", `{"count":1}`, "admin|9999999999|deadbeef")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("伪造 cookie = %d，期望 401", w.Code)
	}
}

func TestExpiredCookieIsRejected(t *testing.T) {
	h, _, _ := newTestHandler(t)
	expired := h.signSession("admin", time.Now().Add(-time.Hour))
	if w := postJSON(t, h.Router(), "/api/bots", `{"count":1}`, expired); w.Code != http.StatusUnauthorized {
		t.Fatalf("过期 cookie = %d，期望 401", w.Code)
	}
}

func TestLogoutClearsCookie(t *testing.T) {
	h, _, _ := newTestHandler(t)
	w := login(t, h, "admin", "s3cret")
	c := cookieOf(t, w, sessionCookie)

	req := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: c.Value})
	got := httptest.NewRecorder()
	h.Router().ServeHTTP(got, req)
	if got.Code != http.StatusOK {
		t.Fatalf("登出 = %d", got.Code)
	}
	if out := cookieOf(t, got, sessionCookie); out.MaxAge >= 0 {
		t.Fatalf("登出应清掉 cookie（MaxAge<0），得到 %d", out.MaxAge)
	}
}

func TestLoginRateLimit(t *testing.T) {
	h, _, _ := newTestHandler(t)
	for i := 0; i < 5; i++ {
		if w := login(t, h, "admin", "wrong"); w.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次失败登录 = %d", i+1, w.Code)
		}
	}
	// 第 6 次即使密码正确也应被限速挡下。
	if w := login(t, h, "admin", "s3cret"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("限速后 = %d，期望 429", w.Code)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```powershell
go test -count=1 -run 'TestLogin|TestAPIsRequireCookie|TestForged|TestExpired|TestLogout' ./gm
```

预期：编译失败（`undefined: sessionCookie` / `signSession`）。

- [ ] **Step 3: 实现登录与 cookie（`gm/server.go`）**

```go
const (
	// sessionCookie 是 GM 控制台的会话 cookie 名。
	sessionCookie = "gm_session"
	// sessionTTL 是会话有效期。无状态签名 cookie，没有服务端会话存储。
	sessionTTL = 8 * time.Hour
	// 登录失败限速：同一 IP 连续失败 maxLoginAttempts 次后，
	// loginBlockWindow 内一律拒绝（本地工具，不做更复杂的）。
	maxLoginAttempts = 5
	loginBlockWindow = time.Minute
)

// Handler 持有 GM 页面的依赖与登录配置。
type Handler struct {
	coins    CoinsService
	bots     AddBotsService
	accounts UsernameResolver
	user     string
	pass     string
	secret   []byte // cookie 签名密钥
	page     []byte

	loginMu   sync.Mutex
	loginFail map[string]loginAttempts
}

type loginAttempts struct {
	count int
	last  time.Time
}

// NewHandler 构造 GM 处理器。secret 为空时回落到 pass。
//
// pass 为空表示这个进程没有配控制台密码 —— 调用方（main.go）应当在启动时就
// 拒绝这种配置；这里对空密码的请求一律回 401，作为第二重保险。
func NewHandler(coins CoinsService, bots AddBotsService, accounts UsernameResolver, user, pass, secret string) *Handler {
	if secret == "" {
		secret = pass
	}
	return &Handler{
		coins:     coins,
		bots:      bots,
		accounts:  accounts,
		user:      user,
		pass:      pass,
		secret:    []byte(secret),
		page:      indexPage,
		loginFail: map[string]loginAttempts{},
	}
}

// signSession 生成 `user|expiryUnix|hmac` 形式的会话值。
func (h *Handler) signSession(user string, expiry time.Time) string {
	payload := fmt.Sprintf("%s|%d", user, expiry.Unix())
	mac := hmac.New(sha256.New, h.secret)
	mac.Write([]byte(payload))
	return payload + "|" + hex.EncodeToString(mac.Sum(nil))
}

// verifySession 校验签名与过期时间，返回用户名。
func (h *Handler) verifySession(value string) (string, bool) {
	parts := strings.Split(value, "|")
	if len(parts) != 3 {
		return "", false
	}
	payload := parts[0] + "|" + parts[1]
	mac := hmac.New(sha256.New, h.secret)
	mac.Write([]byte(payload))
	want := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(want), []byte(parts[2])) != 1 {
		return "", false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", false
	}
	return parts[0], true
}

// requireSession 是 /api/*（除 login / logout）的中间件。
func (h *Handler) requireSession(c *gin.Context) {
	v, err := c.Cookie(sessionCookie)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"ok": false, "reason": "unauthorized"})
		return
	}
	user, ok := h.verifySession(v)
	if !ok || user != h.user {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"ok": false, "reason": "unauthorized"})
		return
	}
}

func (h *Handler) blocked(c *gin.Context) bool {
	ip := c.ClientIP()
	h.loginMu.Lock()
	defer h.loginMu.Unlock()
	att, ok := h.loginFail[ip]
	if !ok {
		return false
	}
	if time.Since(att.last) > loginBlockWindow {
		delete(h.loginFail, ip)
		return false
	}
	return att.count >= maxLoginAttempts
}

func (h *Handler) noteFailure(c *gin.Context) {
	ip := c.ClientIP()
	h.loginMu.Lock()
	defer h.loginMu.Unlock()
	att := h.loginFail[ip]
	if time.Since(att.last) > loginBlockWindow {
		att.count = 0
	}
	att.count++
	att.last = time.Now()
	h.loginFail[ip] = att
}

func (h *Handler) clearFailures(c *gin.Context) {
	h.loginMu.Lock()
	defer h.loginMu.Unlock()
	delete(h.loginFail, c.ClientIP())
}

// postLogin 校验账号密码并下发签名 cookie。
func (h *Handler) postLogin(c *gin.Context) {
	if h.pass == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"ok": false, "reason": "unauthorized"})
		return
	}
	if h.blocked(c) {
		c.JSON(http.StatusTooManyRequests, gin.H{"ok": false, "reason": "too_many_attempts"})
		return
	}
	var req struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "reason": "bad_request"})
		return
	}
	userOK := subtle.ConstantTimeCompare([]byte(req.User), []byte(h.user)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(req.Password), []byte(h.pass)) == 1
	if !userOK || !passOK {
		h.noteFailure(c)
		c.JSON(http.StatusUnauthorized, gin.H{"ok": false, "reason": "bad_credentials"})
		return
	}
	h.clearFailures(c)
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     sessionCookie,
		Value:    h.signSession(h.user, time.Now().Add(sessionTTL)),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// postLogout 清掉 cookie。
func (h *Handler) postLogout(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
```

`Router()` 改成：

```go
func (h *Handler) Router() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.GET("/", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", h.page)
	})
	r.POST("/api/login", h.postLogin)
	r.POST("/api/logout", h.postLogout)
	api := r.Group("/api", h.requireSession)
	api.POST("/coins", h.postCoins)
	api.POST("/bots", h.postBots)
	return r
}
```

删掉 `requireKey` 整个函数。import 补 `crypto/hmac`、`crypto/sha256`、`encoding/hex`、`fmt`、`strconv`、`sync`、`time`；`crypto/subtle` 保留（账号密码比对用）。

`gm/rpc.go`：删掉 `RPC.secret` 字段与 `NewRPC` 的 `secret` 形参，`GrantCoins` / `AddBots` 不再设 `AdminKey`：

```go
// NewRPC 构造 RPC 客户端。
func NewRPC(app pitaya.Pitaya) *RPC { return &RPC{app: app} }
```

- [ ] **Step 4: 改测试 helper 与既有用例**

`newTestHandler` 改成：

```go
	handler := NewHandler(coins, bots, account.NewStore(rdb, accounts), "admin", "s3cret", "s3cret")
```

`postJSON` 的最后一个参数从「Bearer 密钥」改成「cookie 值」：

```go
func postJSON(t *testing.T, router *gin.Engine, path, body, session string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if session != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}
```

加一个好用的 helper（后面多个用例都要登录）：

```go
// newLoggedInHandler 返回 handler 与一个有效会话 cookie 的值。
func newLoggedInHandler(t *testing.T) (*Handler, string, *stubCoins, *stubBots) {
	t.Helper()
	h, coins, bots := newTestHandler(t)
	w := login(t, h, "admin", "s3cret")
	return h, cookieOf(t, w, sessionCookie).Value, coins, bots
}
```

`server_test.go` 的改动：

- **删掉** `TestCoinsRejectsMissingKey`、`TestCoinsRejectsWrongKey`、`TestBotsRejectsMissingKey`（它们的语义已被 `auth_test.go` 的 `TestAPIsRequireCookie` 覆盖）。
- 其余用例（`TestCoinsByUsername`、`TestCoinsByUsernameIsCaseInsensitive`、`TestCoinsByAccountID`、`TestCoinsUnknownUsername`、`TestCoinsRejectsZeroDelta`、`TestCoinsRejectsEmptyTarget`、`TestCoinsUpstreamFailure`、`TestBotsAddsRequestedCount`、`TestBotsRejectsNonPositiveCount`、`TestBotsUpstreamFailure`）改成用 `newLoggedInHandler` 拿到的 session 值调 `postJSON`。

`page_test.go`：`TestPageContainsBothFormsAndKeyField` 里的 `id="gmkey"` 换成 `id="user"` / `id="password"`；`TestPageSendsBearerHeader` 换成：

```go
func TestPageHasLoginForm(t *testing.T) {
	body := pageBody(t)
	for _, want := range []string{`id="user"`, `id="password"`, `id="loginbtn"`, "/api/login"} {
		if !strings.Contains(body, want) {
			t.Errorf("页面缺少 %s", want)
		}
	}
}
```

- [ ] **Step 5: 改页面（`gm/index.html`）**

把「管理密钥」那块换成登录面板，控制台默认隐藏；`call()` 不再带 `Authorization`（同源 cookie 自动带），401 时切回登录视图并提示重新登录；加一个退出按钮打 `/api/logout`。

```html
<div class="panel" id="loginpanel">
  <h2>登录</h2>
  <label for="user">账号</label>
  <input id="user" autocomplete="username">
  <label for="password">密码</label>
  <input id="password" type="password" autocomplete="current-password">
  <button id="loginbtn" type="button">登录</button>
</div>

<div id="console" hidden>
  <div class="panel">
    <h2>发钱</h2>
    <label for="target">目标（用户名或 accountID）</label>
    <input id="target" placeholder="alice 或 10001">
    <label for="delta">数量（负数表示扣除）</label>
    <input id="delta" type="number" value="500">
    <button id="coinbtn" type="button">发送</button>
  </div>
  <div class="panel">
    <h2>机器人</h2>
    <label for="botcount">数量（单次上限 8）</label>
    <input id="botcount" type="number" value="1" min="1" max="8">
    <button id="botbtn" type="button">加入匹配队列</button>
    <button id="logoutbtn" type="button">退出</button>
    <p class="hint">机器人会与下一位入队的真人配对：进对局后全程不动、不开火，被打死原地复活。
    两个机器人不会互打（那一对会被直接丢弃），所以要等真人入队才会开一局。</p>
  </div>
</div>
```

脚本：删掉 `localStorage` 相关的密钥读写（**密码不再落本地存储**），登录按钮打 `/api/login`：

```js
document.getElementById('loginbtn').addEventListener('click', async () => {
  const body = await post('/api/login', {
    user: document.getElementById('user').value.trim(),
    password: document.getElementById('password').value,
  }, true);
  if (!body) return;
  document.getElementById('loginpanel').hidden = true;
  document.getElementById('console').hidden = false;
  show(true, '已登录');
});

document.getElementById('logoutbtn').addEventListener('click', async () => {
  await post('/api/logout', {}, true);
  document.getElementById('console').hidden = true;
  document.getElementById('loginpanel').hidden = false;
  show(true, '已退出');
});
```

`post()` 里 `credentials: 'same-origin'`，并保留「401 → 收起控制台、提示重新登录」的处理。

- [ ] **Step 6: 改 `main.go` 与 deploy 脚本**

```go
	svType := flag.String("type", "gate", "server type: gate | account | logic | match | game | gm")
	redisAddr := flag.String("redis", kv.DefaultAddr, "redis address (host:port)")
	gmAddr := flag.String("gmaddr", ":8082", "gm http listen address (type=gm)")
	gmUser := flag.String("gmuser", "admin", "gm console account (type=gm)")
	gmPass := flag.String("gmpass", "", "gm console password (type=gm); empty refuses to start")
	gmSecret := flag.String("gmsecret", "", "gm session cookie signing key (type=gm); falls back to -gmpass")
	flag.Parse()

	if *svType == "gm" && *gmPass == "" {
		// 默认拒绝服务：忘配密码的后果是服务起不来，而不是「谁都能登」。
		log.Fatalf("-type gm 需要 -gmpass（GM 控制台密码）")
	}
```

`run` 的签名：`func run(svType *string, builder *pitaya.Builder, redisAddr, gmAddr, gmUser, gmPass, gmSecret string) error`（原来那个 `secret string` 拆成三个）。

`case "gm"` 里：

```go
		handler := gm.NewHandler(rpc, rpc,
			account.NewStore(rdb, persist.NewAccountStore(accountPool)),
			gmUser, gmPass, gmSecret)
```

`logic` 分支：`logic.NewComponentWithSecret(app, service, secret)` → `logic.NewComponent(app, service)`。
`match` 分支：`match.New(app, match.NewQueue(rdb), online.NewStore(rdb), secret)` → 去掉最后一个实参。
`logic` / `match` 两处「handler 与 remote 共用同一实例」的注释保留（那条理由没变）。

`deploy/start-all.ps1`：

```powershell
param(
    [string]$gmPass = $env:GM_PASS
)
if (-not $gmPass) { $gmPass = 'local-dev-pass' }
```

```powershell
Start-Process -FilePath $exe -ArgumentList @('-type', 'gm', '-gmpass', $gmPass) -WorkingDirectory (Split-Path $exe) -RedirectStandardOutput (Join-Path $root 'gm.out.log') -RedirectStandardError (Join-Path $root 'gm.log') -WindowStyle Hidden
```

`logic` / `match` 两行的 `-gmkey` 删掉，顶部那段「三个进程必须配同一个密钥」的注释换成：

```powershell
# GM 控制台登录（-gmuser 默认 admin，密码由 -gmpass 给）。没有密码 gm 会拒绝启动。
# 别的角色不需要任何 GM 相关配置：客户端可达性由 gate 的转发白名单保证。
```

- [ ] **Step 7: 跑测试**

```powershell
go test -count=1 ./gm
```

预期：全绿。

- [ ] **Step 8: 编译 + 全量测试**

```powershell
$env:PATH = "$PWD;$env:PATH"
go build ./...
go test -count=1 ./...
```

预期：全绿。

- [ ] **Step 9: 提交**

```powershell
git add joltgo/gm joltgo/main.go joltgo/deploy joltgo/logic joltgo/match
git commit -m "feat: GM 改为账号登录，删除共享密钥"
```

---

## Task 8: 客户端协议注释与回归

**Files:**
- Modify: `godot_client/scripts/fps_client.gd`

**Interfaces:**
- Consumes: 三份 proto 的最终形状（消息名与字段号都没变）
- Produces: 客户端仍能解出所有应答与推送

> 这个任务**不需要改任何字段与消息名**：客户端是手写编解码、按名字查表，服务端换包对它不可见。产出主要是「更新注释 + 跑一遍确认没破坏」。

- [ ] **Step 1: 确认客户端引用的 push route 仍然存在**

```powershell
cd godot_client
rg -n '"onMatched"|"onFrame"|"onMatchStatus"|"onMatchEnded"' scripts/fps_client.gd
```

预期：四条 route 名不变（它们与 proto 文件位置无关）。

- [ ] **Step 2: 更新顶部注释里的协议归属说明**

把「消息定义见 `joltgo/game/protos/game.proto`」之类改成三份文件的分工（客户端请求在 `gate/protos/gate.proto`、推送按发送方在 `match/protos/match.proto` 与 `game/protos/game.proto`），并补一句：

```gdscript
## 注：gate 只转发**白名单**里的 route（见 joltgo/gate/routes.go）。本地改动若把 route 名写错、
## 或调了一条不属于客户端的 route，会收到 `route not found` —— 那不是客户端编解码的问题，
## 先去 gate 的白名单里核对。
```

- [ ] **Step 3: 跑客户端回归（需要六进程集群）**

```powershell
cd godot_client
Godot_v4.7.2-stable_win64_console.exe --headless --path . --script res://tests/login_smoke.gd
```

预期：注册 → LoginReply → 断线 → resume → onMatched 全通过。若本机 Godot 二进制名不同，按 `docs/DEVELOPMENT.md` 里的实际路径替换。

- [ ] **Step 4: 提交**

```powershell
git add godot_client/scripts/fps_client.gd
git commit -m "docs: 客户端协议注释同步到三份 proto"
```

---

## Task 9: 文档同步

**Files:**
- Modify: `AGENTS.md`、`docs/ARCHITECTURE.md`、`docs/API.md`、`docs/BUILD.md`、`docs/DEVELOPMENT.md`、`joltgo/deploy/README.md`、`README.md`

**Interfaces:**
- Consumes: 全部实现
- Produces: 无代码

- [ ] **Step 1: `AGENTS.md`**

三处改动：

1. §2 目录树：加 `gate/protos/` 与 `match/protos/`，并注明 `game/protos/` 只剩「推送 + 内部」。
2. §3.5 里「wire 契约仍是 `game/protos/game.proto`」改成三份文件的分工。
3. §5 加两条约定：

```markdown
- **协议按归属拆三份**：客户端主动请求 + 响应在 `gate/protos/gate.proto`；服务端推送**按发送方**
  归文件（match 推的在 `match/protos/match.proto`，game 推的在 `game/protos/game.proto`）；
  服务内部消息留在 `game/protos/game.proto`。三份之间没有 import（`SlotResult` 与它的两个使用者
  都在 game.proto）。**新增一条客户端 route 必须同时改两处**：`gate.proto` 定义消息 +
  `gate/routes.go` 的 `allowedRoutes` 加一行；漏改会被 `gate/routes_test.go` 的一致性测试挡住。
- **gate 是唯一入口，且只转发白名单**：pitaya 的 `AddRoute` 是「前缀 → 挑节点」，**不校验具体
  route**；「客户端能不能打到某个方法」取决于目标节点有没有注册同名 handler，这是个隐式规则
  （`match.addbots` 就因为签名同时满足 handler / remote 的收录条件，被注册进了客户端可达的那张表）。
  所以 gate 在四个路由函数里显式校验 `allowedRoutes`，不在清单里的回通用的 `route not found`
  （与「route 不存在」不可区分，不给探测者反馈）。白名单**管不到**服务端推送与集群内部 RPC —— 它们
  不经路由函数。
```

- [ ] **Step 2: `docs/ARCHITECTURE.md`**

新增一节「协议边界与 gate 白名单」：讲清「前缀路由不是权限」这个认知、白名单落在 `RoutingFunc` 里、以及 `match.addbots` 那个真实案例（签名双重收录）。同时更新「数据流」一节的 route 描述，把消息定义的归属指到对应文件。

- [ ] **Step 3: `docs/API.md`**

- 「路由一览」表加一列或一句说明：每条客户端 route 定义在哪份 proto。
- 新增「gate 转发白名单」小节：13 条清单、拒绝语义（`route not found`）、以及「推送与内部 RPC 不在范围内」。
- 「GM 管理指令」小节：删掉 `admin_key` 的描述，改成 **GM 控制台账号登录**（`-gmuser` / `-gmpass` / `-gmsecret`，HMAC cookie，8 小时）+ **客户端可达性由 gate 白名单保证**。

- [ ] **Step 4: `docs/BUILD.md`**

- flag 表：删 `-gmkey`，加 `-gmuser` / `-gmpass` / `-gmsecret`。
- `protoc` 那一节补两条新命令（`gate/protos/gate.proto`、`match/protos/match.proto`），并写明三份文件的归属规则。

- [ ] **Step 5: `docs/DEVELOPMENT.md`**

- 「改 protobuf 消息的完整流程」：新增客户端消息要同时改 `gate.proto` 与 `gate/routes.go`；新增推送按发送方归文件；新增内部消息留在 `game.proto`。
- GM runbook：密钥那套换成账号密码（页面地址、默认账号 `admin`、`-gmpass` 必填、`start-all.ps1` 的默认值）。
- 「已知限制」补一条：gate 白名单只校验 route 名，不校验 payload 形状。

- [ ] **Step 6: `joltgo/deploy/README.md` 与根 `README.md`**

进程清单与 flag 描述同步：`-gmkey` → `-gmpass`，并删掉「三个进程必须配同一个密钥」那段（改成「gate 白名单保证客户端可达性」）。

- [ ] **Step 7: 提交**

```powershell
git add AGENTS.md README.md docs joltgo/deploy/README.md
git commit -m "docs: 同步协议边界、gate 白名单与 GM 登录"
```

---

## Task 10: 端到端验收

**Files:** 无（只跑验证；发现问题回到对应任务修）

**Interfaces:**
- Consumes: 全部
- Produces: 一次可复现的验收记录

- [ ] **Step 1: 起集群**

```powershell
cd joltgo; .\build.ps1
cd deploy; .\stop-infra.ps1; .\start-all.ps1
Start-Sleep -Seconds 6
Get-Content .\gate.log -Tail 3
Get-Content .\gm.log -Tail 3
```

预期：六进程起来、无启动错误；`gm.log` 里有 HTTP 监听记录。

- [ ] **Step 2: 正常链路没被换包破坏**

```powershell
cd godot_client
Godot_v4.7.2-stable_win64_console.exe --headless --path . --script res://tests/login_smoke.gd
```

预期：注册 → 登录 → resume → onMatched 全通过。

- [ ] **Step 3: 白名单挡住 GM 的两条 route**

用客户端会话（或临时改一个脚本的 route 字符串）发 `match.match.addbots`，然后：

```powershell
cd joltgo\deploy
Get-Content gate.log -Tail 20 | Select-String -Pattern 'route not found'
Get-Content match.log -Tail 10 | Select-String -Pattern 'addbots'
```

预期：客户端收到 `route not found`；**`match.log` 里没有任何 `addbots` 处理记录**（说明消息根本没被转发）。把 `logic.logic.grantcoins` 同样验一遍。

- [ ] **Step 4: 白名单放行正常 route**

同一个连接发 `match.match.join` → 正常入队（`match.log` 里能看到 enqueue，或队列里有自己）。

- [ ] **Step 5: GM 登录与鉴权**

```powershell
# 无 cookie -> 401
Invoke-RestMethod -Uri http://localhost:8082/api/bots -Method Post -ContentType 'application/json' -Body '{"count":1}'

# 登录后带 cookie -> 成功
$s = New-Object Microsoft.PowerShell.Commands.WebRequestSession
Invoke-RestMethod -Uri http://localhost:8082/api/login -Method Post -WebSession $s -ContentType 'application/json' -Body '{"user":"admin","password":"local-dev-pass"}'
Invoke-RestMethod -Uri http://localhost:8082/api/bots -Method Post -WebSession $s -ContentType 'application/json' -Body '{"count":1}'

# 错密码 -> 401
Invoke-RestMethod -Uri http://localhost:8082/api/login -Method Post -ContentType 'application/json' -Body '{"user":"admin","password":"wrong"}'
```

预期：401 → 登录成功 → `enqueued=1`（**后端此时已无任何密钥校验**，能成功说明链路通）→ 401。

- [ ] **Step 6: 发钱链路仍可用**

```powershell
Invoke-RestMethod -Uri http://localhost:8082/api/coins -Method Post -WebSession $s -ContentType 'application/json' -Body '{"target":"gmtest","delta":100}'
```

预期：`ok` 且余额正确（若 `gmtest` 已被清掉，换成任意真实账号）。

- [ ] **Step 7: 全量回归**

```powershell
cd joltgo; $env:PATH = "$PWD;$env:PATH"; go test -count=1 ./...
```

预期：全绿。

---

## Self-Review 记录

**1. Spec 覆盖**

| spec 章节 | 承担它的 Task |
| --- | --- |
| §3 协议文件布局（含 §3.3 两个定位决定） | Task 1 |
| §4 gate 白名单（含 §4.2 拒绝语义） | Task 6 |
| §4.3 白名单管不到什么 | Task 9（写进文档） |
| §5.1 删密钥（含「同提交」硬约束） | Task 3（logic）+ Task 6（match 与白名单同提交） |
| §5.2 GM 页面登录 | Task 7 |
| §6 代码改动清单 | Task 1~8 |
| §7 错误处理 | Task 6（`route not found`）+ Task 7（401 / 429） |
| §8 测试 | 各 Task 的 Step + Task 10 |

**2. 占位符扫描**：无 TBD / TODO / 「实现细节待定」；每个代码步骤都给了可粘贴的实现。

**3. 一致性检查**

- 包名全程只用三个：`gatepb`（`gate/protos`）、`matchpb`（`match/protos`）、`protos`（`game/protos`，包名不变）。
- 签名跨任务一致：`allowRoute(*route.Route) error` 与 `allowedRoutes map[string]bool`（Task 6 定义与使用一致）；`NewHandler(coins, bots, accounts, user, pass, secret)`（Task 7 定义，测试与 main.go 都按六个参数调）；`NewComponent(app, service)`（Task 3 回到两参数，Task 7 的 main.go 按两参数调）；`New(app, queue, onl)`（Task 6 回到三参数，Task 7 的 main.go 按三参数调）。
- route 名与消息名在全部 Task 里保持与 spec 的清单一致。

**4. 已知取舍与风险（实施时留意）**

- **Task 1 与 Task 5 之间仓库不可编译**。连续实施没问题；要每个提交都可编译就把 Task 1~5 压成一个提交（代价是 diff 巨大）。
- **Task 3 删判据时白名单还没上线**，从提交边界看 `logic.logic.grantcoins` 会短暂对「能发 `logic.*` 的人」开放。要严格零真空，把 Task 3 的「删判据」挪到 Task 6 一起做（白名单同一个提交上线）。两种顺序都能用，Task 6 之后都做一次全量验证。
- **`-gmkey` 相关的历史文档**（`docs/superpowers/` 下的旧 spec / plan）不回改 —— 那是历史记录。
- **`gm` 的 `-gmuser` 默认值 `admin`**：本地开发零配置能登；生产化时应该显式配一个非默认账号名（这不在本次范围内）。

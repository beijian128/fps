# Gate 协议边界与 GM 登录设计

- 日期：2026-09-16
- 状态：设计已确认，待实现
- 范围：把 C/S 协议收口到**按发送方/接收方归属的文件**、给 gate 加**转发白名单**、删掉 GM 的共享密钥改为**页面账号登录**

## 1. 背景与目标

现状有一个结构性问题：**`game/protos/game.proto` 一个文件里混着三种东西** —— 客户端上行消息（`CommandMsg` / `LoginMsg`…）、服务端推送（`Frame` / `MatchEnded`…）、以及服务之间的内部 RPC（`CreateGameMsg` / `BindGameMsg` / `RecordMatchMsg` / GM 的两条）。后果是：

- 「客户端能发什么」这件事**没有单一来源**，只能靠翻代码和文档；
- gate 按**前缀**转发（`account` / `match` / `logic` / `game`），不校验具体 route。「客户端能不能打到某个方法」取决于「有没有节点注册了那个前缀 + 那个节点的 handler 池里有没有同名方法」，这是个隐式规则 —— 我们刚在 `match.addbots` 上踩到过：它因为签名同时满足 handler 与 remote 的收录条件，**被注册进了客户端可达的 handler 池**，而 `match.*` 前缀是 gate 必须转发的。

本次要做三件事：

1. **协议按归属拆文件**：客户端主动请求收进 `gate.proto`；服务端推送按**发送方**归到各服务自己的 proto；服务内部 RPC 留在原处。
2. **gate 加转发白名单**：客户端消息的 route 必须出现在 `gate.proto` 定义的清单里，否则 gate 直接拒绝转发（回通用的 `route not found`）。
3. **GM 鉴权从共享密钥改为页面登录**：删掉 `-gmkey` / `admin_key` 那一套，GM 页面加账号密码登录。

目标状态：**「客户端能发什么」只有一处定义（`gate.proto` + gate 白名单），且由代码强制**；新增一条客户端 route 必须显式改这两处，漏改会被测试和运行期同时挡住。

## 2. 非目标

- **不校验 payload 形状**：白名单只管 route 名。gate 拿到的只有 route 字符串与序列化后的字节，它并不认识 route 对应的消息类型（那句话在目标节点的 handler 池里）；要在 gate 做 schema 校验就得维护 `route → 消息类型 → 反序列化` 的表，成本与收益不成比例。参数校验继续留在各 handler。
- **不引入 GM 多账号体系**：只有一个启动参数里的账号，不做增删改、不做权限分级、不做审计（沿用既有取舍）。
- **不动 `persist/protos`**：那是 `protoc-gen-redis` 的持久化模型，与 wire 契约无关。
- **不动 pitaya 内置消息**（`sys.kick` 的 `KickMsg` / `KickAnswer`）：它不属于我们的协议面，只在文档里标注。
- **不给 account 建推送包**：account 目前没有任何下行推送，空文件没有价值（见 §3.3）。
- **不改同步属性机制**：`Frame` 的实体-属性增量协议一行不动，只是换了定义所在的文件。

## 3. 协议文件布局（已确认）

### 3.1 归属规则

| 文件 | 装什么 | Go 包 |
| --- | --- | --- |
| `joltgo/gate/protos/gate.proto` | **客户端主动请求 + 对应响应**（白名单的唯一来源） | `gatepb` |
| `joltgo/match/protos/match.proto` | **match 推给客户端的** | `matchpb` |
| `joltgo/game/protos/game.proto` | **game 推给客户端的** + game 的服务内部消息（含既有内部 RPC） | `gamepb`（保持现有包名 `protos` 与本目录） |

一句话规则：**请求按接收方（gate 是入口），推送按发送方**。

### 3.2 具体内容

**`gate.proto`（客户端主动请求 + 响应）**

| route | 请求 | 响应 |
| --- | --- | --- |
| `account.account.register` | `RegisterMsg` | `LoginReply` |
| `account.account.login` | `LoginMsg` | `LoginReply` |
| `account.account.resume` | `ResumeMsg` | `LoginReply` |
| `logic.logic.state` | `LogicStateMsg` | `LogicStateReply` |
| `logic.logic.purchase` | `PurchaseMsg` | `LogicStateReply` |
| `logic.logic.equip` | `EquipMsg` | `LogicStateReply` |
| `logic.logic.profile` | `PlayerProfileMsg` | `PlayerProfileReply` |
| `match.match.pending` | `PendingMatchMsg` | `PendingMatchReply` |
| `match.match.abandon` | `AbandonMatchMsg` | `AbandonMatchReply` |
| `match.match.cancel` | `MatchCancelMsg` | `MatchCancelReply` |
| `match.match.join` | `JoinMsg`（Notify，**无响应**） | — |
| `game.game.cmd` | `CommandMsg`（Notify，**无响应**） | — |
| `game.game.resync` | 空 payload（Notify，**无响应**） | — |

附带定义：`LogicShopItem`、`MatchRecord`。

**`match.proto`（match 推送）**：`MatchResult`（`onMatched`）、`MatchStatus`（`onMatchStatus`）

**`game.proto`（game 推送 + 内部）**

- 推送：`Frame`（`onFrame`）+ `EntityDelta` / `AttrValue` / `Schema` / `SchemaField`；`MatchEnded`（`onMatchEnded`）
- 内部：`CreateGameMsg/Reply`、`RejoinMsg/Reply`、`LeaveMsg/Reply`、`BindGameMsg/Reply`、`RecordMatchMsg/Reply`、`UserOnlineMsg/Reply`、GM 的 `AddBotsMsg/Reply`、`GrantCoinsMsg/Reply`

### 3.3 两个定位决定

- **`SlotResult` 留在 `game.proto`**。它被 `MatchEnded`（game 推给客户端）与 `RecordMatchMsg`（game → logic）同时引用，而这两个消息都在 `game.proto`，所以零 import；它只是 `{uid, kills, deaths, left}`，不是跨服务通用概念，不值得单开一个包。检查过另外两个文件的引用面：`MatchResult` / `MatchStatus` 自包含，**本次拆分不会产生任何跨 proto 的 import**。
- **暂不建 `account/protos/account.proto`**。account 目前没有任何下行推送；空文件没有价值。将来 account 要推东西时，按同一条规则建包即可 —— 归属规则会写进 `docs/ARCHITECTURE.md`，不是只写在本 spec 里。
- **`game.game.resync` 没有消息体**（客户端发空 payload），所以 `gate.proto` 里没有对应请求消息，白名单里只登记这个 route 字符串。

## 4. gate 转发白名单（本次的核心）

### 4.1 机制

pitaya 的 `AddRoute` 是 `前缀 → RoutingFunc`，而 `RoutingFunc` 的签名是：

```go
func(ctx context.Context, route *route.Route, payload []byte, servers map[string]*cluster.Server) (*cluster.Server, error)
```

它拿得到**解码后的完整 route**（`route.SvType` / `Service` / `Method`）。所以白名单落在每个路由函数的第一行：不在清单里就返回错误，转发不发生。

清单（= 客户端所有合法上行 route，13 条，与 §3.2 的表一一对应）：

```text
account.account.register   account.account.login      account.account.resume
logic.logic.state          logic.logic.purchase       logic.logic.equip
logic.logic.profile
match.match.join           match.match.pending        match.match.abandon
match.match.cancel
game.game.cmd              game.game.resync
```

清单**手写**在 gate 里（不从 proto 自动生成：proto 里允许有「定义了但暂时不发」的东西，比如客户端目前没调 `logic.logic.profile`，自动生成会把这类也放行；而白名单要的是「我明确同意转发哪些」）。配一个测试盯住一致性：**白名单里的每一条都必须能在 `gate.proto` 里找到对应的请求/通知消息**（`game.game.resync` 这条空 payload 的写在测试的例外表里）。

### 4.2 拒绝时返回什么（已确认：通用 route not found）

回 pitaya 的 `ErrNotFoundCode` + 消息 `route not found` —— 与「这条 route 根本不存在」在客户端看完全一样，不泄漏「哪些名字存在但你无权使用」。

### 4.3 白名单管不到什么（必须写清楚）

- **服务端推送不经路由函数**：`onMatched` / `onMatchStatus` / `onMatchEnded` / `onFrame` 由后端 `SendPushToUsers` 经 NATS 发给 gate，不经过 `AddRoute`，所以白名单天然管不到它们。它们的安全性来自「只有持会话的那个 gate 会转给对应连接」。
- **集群内部 RPC**：`match` → `game`、`game` → `logic` 等走 `RPCTo`，也不经 gate 的路由函数。白名单只约束「客户端消息」。
- **参数形状**：见 §2 非目标。

## 5. 删除 GM 的共享密钥，改为页面登录（已确认）

### 5.1 删掉的东西

| 位置 | 删除内容 |
| --- | --- |
| `game.proto` | `AddBotsMsg.admin_key`、`GrantCoinsMsg.admin_key` 两个字段 |
| `match` | `Component.secret` / `UpdateSecret` / `adminSecret`、`adminKeyAllowed`、`New` 的 `secret` 参数、handler 里的两道判据 |
| `logic` | 同上（`NewComponentWithSecret` / `UpdateSecret` / `adminSecret` / `adminKeyAllowed`、handler 里的两道判据） |
| `gm` | 页面的密钥输入框、`Authorization: Bearer`、`RPC.secret`、`NewHandler` / `NewRPC` 的 `secret` 参数 |
| `main.go` | `-gmkey` flag 与 `GM_KEY` 环境变量、`secret` 的传递 |
| `deploy/start-all.ps1` | `-gmkey` 参数与三处传参 |

**连带影响（可接受，但要记录）**：删掉两道判据后，`match.match.addbots` 与 `logic.logic.grantcoins` **对集群内任何进程都不再设防**（它们本来就能调）。这是明确接受的取舍：边界是内网，不是应用层。

> **落地顺序是硬约束**：白名单与「删判据」**必须在同一个提交里**。删判据之前，当前唯一挡着「玩家直达 `match.addbots` / `logic.grantcoins`」的就是那道会话判据；白名单是它的替代品，两者之间不能有真空期。

### 5.2 GM 页面登录（启动参数单账号 + HMAC cookie）

新增 flag：

| flag | 说明 |
| --- | --- |
| `-gmuser` | GM 账号（默认 `admin`） |
| `-gmpass` | GM 密码。**为空则 gm 拒绝启动**（与原来「没配密钥就拒绝启动」同一条原则：默认拒绝服务） |
| `-gmsecret` | cookie 签名密钥。为空则回落到 `-gmpass`（本地开发够用） |

页面与接口：

| 方法 | 路径 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| GET | `/` | 无 | 登录页 + 控制台（同一个页面，按 cookie 切显） |
| POST | `/api/login` | 无 | `{"user","password"}` → 校验通过则 `Set-Cookie`（HttpOnly、SameSite=Lax、有效期 8h），失败 401 |
| POST | `/api/logout` | cookie | 清 cookie |
| POST | `/api/coins` | cookie | 同现在的语义 |
| POST | `/api/bots` | cookie | 同上 |

cookie 用 HMAC-SHA256 签 `user|expiry`；校验时验签 + 查过期。**不引入 session 存储**（无状态、gm 重启后 cookie 仍有效直到过期）。密码比对用常数时间（`subtle.ConstantTimeCompare`），与原来比密钥同一套做法。

登录失败要**限速**（复用项目里既有的按用户名的做法太绕，这里简单起见：同一 IP 连续失败 5 次后 60 秒内直接拒绝）—— 本地工具，不做更复杂的东西，但要有，否则密码可暴力枚举。

## 6. 代码改动清单

| 文件 | 动作 |
| --- | --- |
| `joltgo/gate/protos/gate.proto` + 生成码 | 新建（`gatepb`） |
| `joltgo/match/protos/match.proto` + 生成码 | 新建（`matchpb`） |
| `joltgo/game/protos/game.proto` + 生成码 | 收敛到「推送 + 内部」，删 `admin_key` |
| `joltgo/gate/gate.go` | 四个路由函数加白名单 + 一个 `allowedRoutes` 表 |
| `joltgo/gate/gate_test.go` | 白名单一致性测试 + 拒绝路径测试 |
| `joltgo/account/component.go` | handler 签名换 `gatepb` |
| `joltgo/logic/component.go` | 同上；删密钥与会话判据 |
| `joltgo/logic/remote.go` | 同上（`UserOnline` 等） |
| `joltgo/match/match.go`、`addbots.go`、`auth.go` | handler 与推送载荷换包；**删 `auth.go`**（两道判据都删） |
| `joltgo/game/component.go`、`instance.go` | handler / 推送载荷换包 |
| `joltgo/gm/server.go`、`rpc.go`、`page.go`、`index.html` | 登录 + cookie；删密钥 |
| `joltgo/main.go` | 删 `-gmkey`，加 `-gmuser` / `-gmpass` / `-gmsecret` |
| `joltgo/deploy/start-all.ps1` | 删 `-gmkey`，加 GM 账号参数 |
| `godot_client/scripts/fps_client.gd` | 上行编码 / 应答解码 / 推送解码按新包名同步（消息名不变，只有归属与注释变） |

## 7. 错误处理与边界

| 情形 | 行为 |
| --- | --- |
| 客户端发不在白名单里的 route | gate 回 `route not found`（与不存在的 route 无法区分） |
| 客户端发白名单里的 route，但目标节点没注册该 handler | 目标节点的 handler 池回 `route not found`（既有行为，不变） |
| 未登录访问 `/api/*` | `401 unauthorized` |
| 登录失败 | `401`；同 IP 连续 5 次失败后 60 秒冷却 |
| `-gmpass` 为空启动 gm | 拒绝启动 |
| 集群内任意进程调 `match.addbots` / `logic.grantcoins` | **放行**（已知取舍，见 §5.1） |
| `game.game.create` 之类的内部 route 被客户端发 | gate 白名单拒绝（它不在清单里） |

## 8. 测试

**单测**

- `gate`：白名单一致性（每条都能在 `gate.proto` 找到对应消息，`resync` 例外）；清单外的 route 被拒且**不返回任何节点**；清单内的 route 正常返回节点（服务发现用假实现）。
- `match` / `logic`：删掉判据后的行为 —— 发 `addbots` / `grantcoins` **不再**要求密钥（断言这两条路径在无密钥情况下成功），同时确认既有功能测试仍绿。
- `match` / `logic` / `game` / `account`：换包后所有既有测试编译并通过（这是本次最大的回归面）。
- `gm`：登录成功发 cookie、密码错误 401、无 cookie 访问 `/api/*` 401、cookie 过期拒绝、限速生效、页面含登录表单。
- 生成的三个包：`go build ./...` + `go vet`。

**端到端冒烟**

1. 六进程起来，客户端能正常登录 → 匹配 → 对局（验证换包没破坏正常链路）。
2. 用一个原始 pomelo 连接发 `match.match.addbots`（白名单外）→ 收到 `route not found`，且 `match.log` 里**没有**这条处理的记录。
3. 同一个连接发 `match.match.join`（白名单内）→ 正常入队。
4. GM 页面：错误密码 401；正确密码登录后能发钱 / 加机器人（此时后端已经没有任何密钥校验）。

## 9. 已知取舍

- **gate 白名单是「route 级」而不是「payload 级」**：形状错误（比如 `CommandMsg.move` 传 100 个元素）仍然只在 handler 里挡。换来的是 gate 不需要认识任何消息类型。
- **白名单手写 + 一致性测试**，不是自动生成：宁可多一处需要人维护的清单，也要保证「放行」是一个明确的决定。
- **两条 GM route 在集群内不再设防**：这是删掉密钥的直接后果。真要有人进了内网，能做的事远不止加机器人；本机开发场景下这个取舍成立，生产化时要重新评估（前提是先有审计）。
- **GM 是单账号 + 无状态 cookie**：没有改密、没有多账号、没有登出全部设备的机制。cookie 有效期 8 小时，`-gmsecret` 不变则旧 cookie 一直有效到过期。

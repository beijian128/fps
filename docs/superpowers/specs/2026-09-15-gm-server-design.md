# GM 服务设计

- 日期：2026-09-15
- 状态：设计已确认，待实现
- 范围：新增第六个服务角色 `gm`（GM 指令入口 + Web 操作页）、机器人陪练（往匹配队列里塞机器人凑人数）

## 1. 背景与目标

现状里服务端有五个角色（gate / account / logic / match / game），任何「给玩家发钱」「凑个人陪我打一局」这类运维诉求都没有入口：

- 局外数据（钱包）只有 `logic` 通过客户端 `logic.logic.*` 这条路能改，而客户端能拿到的东西全部要过账号鉴权与商城规则，没有「直接加一笔钱」的能力。
- 匹配队列只能由客户端 `match.match.join` 入队，服务端把「凑不满 2 人就一直等」当硬规则（单人兜底已删除），所以本地想验证一局必须开第二个客户端。

本子项目的目标是补上这两条运维通路，并让它们**完全不进玩家协议**：

- 新增 `-type gm` 角色，起一个 **Gin HTTP 服务** + 内嵌 Web 操作页；管理流量不经过 gate、不进 8080、不占任何 pitaya 端口。
- GM 页面能「按用户名或 accountID 给玩家发钱」「往匹配队列里塞 N 个机器人」。
- 机器人是**队列里的普通一员**：能跟真人在公开队列里配成一对，进对局后全程站在出生点不动、不开火，被打死原地满血复活，直到真人拿到 10 杀结束这一局。真人的战绩照常结算，机器人不进任何档案。

## 2. 非目标

- **不做审计**（已确认）：GM 操作不落 Redis 的审计 List，只写服务日志（`gm.log`）。这是本次唯一被显式砍掉的一环，将来要追账再补。
- **机器人不走路、不寻路、不射击**：只承担「凑人数的靶子」。移动策略、行为树、难度分级都不在范围内。
- 不做「机器人中途退出」、「机器人自动填满队列维持在线人数」这类常驻调度 —— 一次指令只塞一次。
- 不做 GM 的多用户体系（角色 / 权限 / 操作记录分离）；只有一个共享密钥。
- 不做「机器人互打」：两个机器人配成的对局直接丢弃。
- Web 页面不做国际化、不做移动端适配、不做权限分级；它是本机运维工具。
- 不改玩家可见协议：客户端一行不用动，`game.proto` 的既有字段与语义不变。

## 3. 已确认决策

1. **形态**：`gm` 是第六个常驻角色（`-type gm`，backend，注册进 etcd），不是一次性 CLI，也不是独立于 pitaya 的服务。
2. **接入方式**：GM 自己监听 HTTP 端口 + 提供 Web 操作页，**不通过 gate 转发**。gate 的 `AddRoute` 表不加任何 `gm.*` 路由。
3. **机器人怎么进队列**：由 `match` 自己完成入队 —— GM 发后端 RPC `match.match.addbots`，`match` 收到后构造 bot uid 并调它自己的 `queue.Enqueue`。**`gm` 里不出现 `match:queue` 这个键名，也不构造 bot uid。**
4. **发钱怎么改数据**：由 `logic` 完成 —— GM 发后端 RPC `logic.logic.grantcoins`，`logic` 校验后走 `persist.PlayerStore.AddCoins`（`HINCRBY`）。`gm` 不直接写钱包 Hash。
5. **发钱目标**：**用户名与 accountID 都支持**。纯十进制字符串按 accountID 处理，否则按用户名查 `acct:name:*` 反向映射。
6. **鉴权**：单一共享密钥。`gm` 侧 `-gmkey`（或 `GM_KEY` 环境变量），**没配就拒绝启动**；每条请求带 `Authorization: Bearer <key>`。被 RPC 调用的 `logic` / `match` 也各自校验请求里携带的同一个密钥。
7. **全机器人配对丢弃**：队列里弹出的两人如果都是机器人，直接丢弃这一对（不放回队列），继续处理后面的排队者。
8. **不审计**：见 §2。
9. **HTTP 框架用 Gin**（`github.com/gin-gonic/gin`，已验证 `v1.12.0` 可在本机拉取，走 `goproxy.cn`；Gin 是纯 Go，不引入 cgo）。

## 4. 架构

### 4.1 角色与端口

```text
┌────────────────────┐        HTTP（gin，:8082）        ┌──────────────┐
│ 运维 / 开发者浏览器 │ ───────────────────────────────▶ │  gm（backend）│
└────────────────────┘                                  │  index.html  │
                                                        │  /api/coins  │
                                                        │  /api/bots   │
                                                        └───┬──────┬───┘
                                          RPCTo(match.*) │      │ RPCTo(logic.*)
                                                         ▼      ▼
                                                 ┌──────────┐ ┌────────┐
                                                 │  match   │ │ logic  │
                                                 │ 队列入队  │ │ 钱包   │
                                                 └──────────┘ └────────┘
```

`gm` 的两条腿互不干扰：

- **HTTP 腿**：Gin 起在 `-gmaddr`（默认 `:8082`），只对运维可见。
- **RPC 腿**：`gm` 以 `pitaya.Cluster` 模式构建 app（注册进 etcd、挂上 NATS），用 `app.RPCTo` 主动调 `match` / `logic`。

**关键约束（本次确认的技术前提）**：能不能发 `app.RPCTo` 取决于「进程有没有以 Cluster 模式构建 app」，跟它是 frontend 还是 backend 无关。`pkg/app.go` 的 `GetServersByType`、`pkg/rpc.go` 的 `RPC` / `RPCTo` 都是 app 能力，`match` 调 `game`、`game` 调 `logic` 走的就是这条路。内置的 `pkg/client/client.go` 是**acceptor 客户端**（连 frontend 的 WS 口、说 pomelo 握手），它发不出 `RPCTo` —— 这是两回事，不要混。因此 `gm` **不需要**：

- 不需要 acceptor、不需要监听任何 pitaya 端口；
- 不需要另搭一套 etcd 发现 + NATS RPC 的客户端；
- 不需要把管理消息伪装成玩家消息经 gate 转发。

`gm` 是 backend，所以它**不注册 handler**；它也不注册 remote handler（没有任何调用方），因此 `gm` 只发不收，攻击面就只有那个 HTTP 端口。

### 4.2 新增包

| 包 | 职责 | 依赖 |
| --- | --- | --- |
| `joltgo/bot/` | 机器人身份的唯一真相：uid 前缀 `bot:`、构造与识别 | 无（纯字符串函数，可单测） |
| `joltgo/gm/` | GM 服务：Gin HTTP 服务 + 内嵌页面 + 两个接口的实现 + RPC 转发 | `account`（用户名解析）、`persist`、pitaya |

`bot` 包单独存在，是因为有两个服务需要对同一份约定达成一致：

- `match` 需要在 `startMatch` 里认出「这一个是机器人，跳过在线探活与会话数据写入」；
- `game` 需要在建实例时认出「这一个槽位没有客户端」，从而不推帧、不进注册表、不参与回局。

把前缀判据写两遍就是两份真相，改一处漏一处。`bot` 包只有几十行，值得为它单独开一个包。

**用户名 → accountID 复用现成实现，不新写反查**：账号服务里的 `account.Store.LookupByName(ctx, username)` 就是干这个的（`acct:name:<规范化用户名>` → 十进制 ID），建号时用的规范化规则是 `account.NormalizeUsername`。`gm` 直接调这一对函数，保证「页面里填的 alice」与「注册时写的 Alice」落在同一个 key 上；自己拼 `acct:name:...` 会踩大小写不敏感的坑。

### 4.3 `main.go` 的装配

```text
joltgo.exe -type gm -redis localhost:6379 -gmaddr :8082 -gmkey <key>
```

- `gm` 需要 Redis（查用户名 → accountID 要走 `persist`），所以进 `main.go` 里「非 game 角色统一连 Redis 并 Ping」那一档 —— 连不上直接启动失败。
- 没有 `-gmkey`（且 `GM_KEY` 也为空）时 `log.Fatalf` 退出，不监听端口。**默认拒绝服务比默认开放安全**：忘记配置的后果是服务起不来，而不是「谁都能发指令」。
- 启动顺序：先 `go httpd.ListenAndServe()`（失败时 `log.Fatalf`），最后 `app.Start()` 阻塞（它自己处理 SIGINT/SIGTERM，与现有五个角色一致）。
- `deploy/start-all.ps1` 增加第六个进程，日志落 `deploy/gm.log` / `gm.out.log`。

## 5. Wire 协议

全部加在 `joltgo/game/protos/game.proto`（该文件已是各服务共用的契约文件，里面既有客户端消息也有 `BindGameMsg` / `CreateGameMsg` / `LeaveMsg` 这类服务间消息），改完重跑 `protoc --go_out`。

```proto
// AddBotsMsg 是 gm → match 的 RPC（route "match.match.addbots"）：往匹配队列里塞 N 个机器人。
// admin_key 是共享密钥 —— 这两条 route 会被 gate 按前缀路由到 match/logic，客户端也能发到，
// 所以服务端不能靠「route 是内部的」来假设调用方可信。
message AddBotsMsg {
  int32 count = 1;      // 期望入队的机器人数量，服务端另有上限
  string admin_key = 2;
}

// AddBotsReply 是 match.match.addbots 的应答。
message AddBotsReply {
  bool ok = 1;
  int32 enqueued = 2;   // 实际入队数量
  string reason = 3;    // ok=false 时的原因码
}

// GrantCoinsMsg 是 gm → logic 的 RPC（route "logic.logic.grantcoins"）：
// 给指定账号加（delta>0）或扣（delta<0）金币。
message GrantCoinsMsg {
  string account_id = 1; // 十进制账号 ID（用户名解析在 gm 侧完成）
  int64 delta = 2;
  string admin_key = 3;
}

// GrantCoinsReply 是 logic.logic.grantcoins 的应答。
message GrantCoinsReply {
  bool ok = 1;
  int64 coins = 2;      // 变更后的余额（ok=true 时有效，页面直接显示）
  string reason = 3;
}
```

**为什么密钥写在 payload 里而不是走 metadata**：pitaya 的 `RPCTo` 没有便捷的自定义 metadata 通道，要带就得改内置 pitaya（`third_party/pitaya` 里现在只有三处安全脱敏的本地补丁，不宜再加入口）。payload 里的字段简单、可测，服务端日志只打长度与原因码，不会把密钥写进日志。

**为什么 `GrantCoinsMsg` 只收 accountID**：用户名 → accountID 的解析放在 `gm`，因为 `gm` 本来就要连 Redis 读 `persist` 的数据，而且解析失败要在页面上给出「没有这个玩家」的明确提示。这样 `logic` 的 handler 只做一件事：给这个 ID 加钱。

## 6. GM 服务（`joltgo/gm/`）

### 6.1 HTTP 接口

页面与接口同源，都挂 `-gmaddr`：

| 方法 | 路径 | 请求 | 响应 |
| --- | --- | --- | --- |
| GET | `/` | — | 内嵌的单页 HTML（`go:embed`，不额外打包静态文件） |
| POST | `/api/coins` | `{"target":"alice","delta":500}`（`target` 也可写 accountID） | `{"ok":true,"account_id":"10001","coins":1500}` |
| POST | `/api/bots` | `{"count":1}` | `{"ok":true,"enqueued":1}` |

统一约定：

- 鉴权：`Authorization: Bearer <gmkey>`；不匹配或缺失 → `401` + `{"ok":false,"reason":"unauthorized"}`。这一层用 Gin 中间件包住所有 `/api/*`（`/` 放行 —— 页面本身不含数据，密钥由用户在页面上填一次）。
- 请求体非法 JSON、`delta` 为 0、`count <= 0` → `400` + `reason: "bad_request"`。
- 集群侧失败（`GetServersByType` 为空、RPC 不通）→ `503` + `reason: "no_server"` / `"upstream_failed"`（**不假装成功** —— 与项目里 `match.Abandon` 的失败处置一致）。
- 业务失败（用户名不存在 / 账号不存在）→ `404` + `reason: "player_not_found"`。

### 6.2 页面的最小信息架构

单页三段，够用即可：

1. **顶部**：密钥输入框（存 `localStorage`，刷新不用重填）+ 连接状态。
2. **发钱**：目标（用户名或 accountID）+ 数量（允许负数表示扣除）+「发送」→ 结果区显示「alice（10001）余额 1500」或失败原因。
3. **机器人**：数量 +「加入队列」→ 结果区显示实际入队数量 + 一句提示「机器人会与下一位入队的真人配对，全程不动」。

页面是内嵌字符串，样式内联，不引入前端构建链路。

### 6.3 转发实现

```go
// 发钱：解析目标 → 挑一个 logic 节点 → RPCTo("logic.logic.grantcoins")
func (c *Component) grantCoins(ctx context.Context, target string, delta int64) (uint64, int64, error)

// 加机器人：挑一个 match 节点 → RPCTo("match.match.addbots")
func (c *Component) addBots(ctx context.Context, count int32) (int32, error)
```

两处都用 `app.GetServersByType(...)` 取节点，从 map 里取任意一个（服务无状态，挑谁都一样 —— 与 `match.startMatch` 现有做法一致）。返回值与错误原因码直接由 HTTP 层翻译成状态码，不做二次包装。

## 7. 匹配服务（`joltgo/match/`）

### 7.1 新增 remote handler `AddBots`

route `match.match.addbots`，用 `RegisterRemote` 注册（与 `game.game.create` 同款；`Register` 会让客户端消息进 handler 池，而这里刻意要走 remote 表）。

**两道入口检查，缺一不可**：

1. `isClientCall(ctx, app)`（即 `app.GetSessionFromCtx(ctx) != nil`）→ 拒绝。客户端经 gate 发出的 `match.match.addbots` 会被 `routeAny` 送到某个 match 节点，这条路径带会话；后端 `RPCTo` 不带会话。判据与 `game` 里已验证的那套一致。
2. 密钥比对（`subtle.ConstantTimeCompare`）→ 拒绝。仅靠第 1 条不够：任何**后端**都能调这条 route，密钥是第二道闸。

通过后：

- `count` 超过单次上限（8）→ 截到上限（页面填错数字不该塞爆队列，也不该让 RPC 超时）；`count <= 0` → 空操作。
- 为每个机器人生成 uid：`bot.New(nuid.New().Next())` —— uid 里的 n 由内置 `github.com/nats-io/nuid` 生成（已是直接依赖）。**不用 Redis 计数器**：少一个共享状态、少一次往返，uid 本来就是不透明的。前缀让「它是不是机器人」在 `match` 与 `game` 两侧都能 O(1) 判断。
- 逐个 `c.queue.Enqueue(ctx, uid)`，失败只记日志并从计数里剔除（与 `requeue` 的容错口径一致：这里没有补偿机会，返回给页面的数量必须诚实）。
- 收尾调一次 `c.tryMatch(ctx)`，不必等下一秒的 ticker —— 机器人通常跟一个正在排队的真人立刻配上。
- 返回 `{ok: true, enqueued: n}`。

### 7.2 `startMatch` 区分机器人与真人

现在 `startMatch` 对每个 uid 做三件事：读 `online:` 登记 → 请 gate 写会话数据（`bindGameOn`，顺带探活）→ 放进 `alive`。机器人没有 gate、没有会话、没有在线登记，照现有逻辑会被 **100% 当成掉线剔除**，这是必须改的地方。

改法：对 `bot.Is(uid)` 为真的 uid **跳过前两步**，直接放进存活名单；真人路径原样不动。槽位仍然是「uid 在名单里的下标」，所以：

- `CreateGameMsg.Uids` 里机器人的位置就是它的 `player_idx`，`game` 侧、协议、客户端全都不用改；
- 真人拿到的 `player_idx` 可能变成 1（机器人排在前面），这是既有协议本来就支持的（`MatchResult.player_idx` 早就是 0/1 两值）。

回滚路径（建局失败时 `bindGameOn(..., "", "", i)` + `requeue`）只对真人生效；机器人直接丢掉即可。

### 7.3 全机器人配对丢弃

队列弹出的两人可能都是机器人（一次加 2 个、或多次加之后恰好凑到一起）。这种对局没有意义：两个木头人对站，谁都不动、谁都不开枪，只能空转到 30 分钟空闲回收。

规则：`startMatch` 在探活阶段统计存活名单里的**真人**数量，若为 0 则直接返回 —— 这一对已经被原子弹出队列，按已确认决策**不放回**（放回会让它们反复被弹出、每次都重置等待时间，与当初删除单人兜底时的取舍一致）。

**丢弃后立刻重试一次**：队列里若还排着别的真人，不该白等下一秒的 ticker。`tryMatch` 在「这一对全是机器人」这条分支里再调一次自己（递归收敛，队列每轮至少少两人，不会无限递归）—— 与 `startMatch` 建局失败时「放回去等下一轮」是两种处置，但同样是为了不让玩家静默等待。

### 7.4 `pushMatchStatus` 跳过机器人

`pushMatchStatus` 每 tick 给队列里每个成员推一次 `onMatchStatus`。机器人不在任何 gate 上，推送必然失败 —— 每 tick 一条错误日志，队列里有几个机器人就刷几倍。

改法：遍历 entries 时，`bot.Is(entry.UID)` 直接跳过。**人数仍要算机器人**（`total` 不做过滤）：对等待中的真人来说，队列里有几个机器人确实是「排队人数」，那正是他想知道的信息。

## 8. 对局服务（`joltgo/game/`）

机器人进对局后是「无客户端的槽位」。要做的改动只有三处，**都在实例内部**，不改协议、不改 sim：

1. **实例构造时标记机器人槽位**：`NewInstance` 里按 `bot.Is(uids[slot])` 填一份 `bots [sim.MaxPlayers]bool`。
2. **不推帧**：`broadcast` 与 `presentUIDs` 跳过 `bots[slot]`（与现有的 `uid == ""` / `left[slot]` 同一个位置、同一套判据）。机器人收不到帧、也不在推送目标里。
3. **不进实例注册表**：`Component.Create` 写 `uidToInst` / `uidToIndex` 时跳过机器人 uid —— 否则 `game.rejoin` 会拿 `bot:xxx` 去查，`match.findInstance` 的 fan-out 也会多一次无意义的往返。

**结算不用改**：`matchEnded` / `reportMatch` 里机器人槽位的 `SlotResult.Uid` 是空串，`logic.RecordMatch` 现在的规则已经会跳过「空槽位」（既不计战绩、也不当错误），而真人那一边照常入账 —— 因为这一局对真人来说就是「对手是靶子，我打完了」。`logic` 在这条路径上**一行都不用改**。

**为什么不复用 `left` 标记**：`left` 的语义是「本来有这个人，他中途放弃了」，`logic` 靠它区分「放弃」与「空槽位」。把机器人标成 `left` 会让日志与结算语义出现「机器人在中途放弃」这种假话。实例内单独一份 `bots` 标记更诚实，且只在 `game` 内部使用。

## 9. 逻辑服务（`joltgo/logic/`）

新增 remote handler `GrantCoins`（route `logic.logic.grantcoins`），与 `AddBots` 同款两道检查（拒绝带会话的调用 + 校验 `admin_key`）。

通过后：

- 校验 `account_id` 可解析、非 0；
- 校验目标账号存在：走 `persist.NewAccountStore(pool).Get(ctx, id)`（`EXISTS acct:1:<id>:0` + 读字段），拿不到就回 `account_not_found` —— **不隐式建档**：给一个不存在的账号发钱应当报错，而不是凭空创建钱包行；
- 调 `persist.PlayerStore.AddCoins(ctx, id, delta)`（`HINCRBY`，天然原子），返回变更后余额。

**为什么不复用 `logic.Service.Purchase`**：发钱不涉及商品、背包与装备，不需要账号级分布式锁（`user:lock:<accountID>`）与补偿路径 —— 那套锁是为「扣钱包 + 写背包」这个多命令提交准备的。单条 `HINCRBY` 本身就是原子操作，加锁等于把原子操作降级，这也是 `AGENTS.md` §3.10 已经写下的原则。

`logic.Service` 增加一个薄方法 `GrantCoins(ctx, accountID, delta) (int64, error)` 承载「校验存在 + 调用 store + 返回余额」，这样 handler 只剩协议翻译，逻辑可单测。

## 10. 错误处理与边界

| 情形 | 行为 |
| --- | --- |
| `-gmkey` 未配置 | 启动即失败，不监听端口 |
| 请求缺 / 错密钥 | `401 unauthorized`，不触达 `match` / `logic` |
| `GetServersByType` 取不到 logic 或 match 节点 | `503 no_server` |
| RPC 超时 / 不通 | `503 upstream_failed`，页面提示重试 |
| 用户名不存在 | `404 player_not_found` |
| accountID 不存在 | `logic` 返回 `account_not_found`，页面透传 |
| `count > 8` | 截到 8，返回实际入队数量 |
| `count <= 0` | `400 bad_request` |
| 机器人与真人配成一对 | 正常开局，机器人不动 |
| 两个机器人配成一对 | 丢弃这一对，不放回 |
| 机器人那局的真人中途放弃 | 实例照常跑（机器人不会打人），30 分钟空闲回收 |
| `match` / `logic` 重启 | 无状态，无影响；页面报 `503` 让运维重试 |

## 11. 测试

**单测（不需要真集群）**

- `bot`：`New` 生成的前缀、`Is` 对真人 accountID（纯十进制、空串、含其它字符）的判定、与前缀相似的字符串（如 `bot`、`bots:1`）不误判。
- `match.AddBots`（`miniredis` 跑真队列）：入队数量、超上限截断、`count<=0` 不写队列、密钥错误与「带会话的调用」两条拒绝路径、收尾触发配对。
- `match.startMatch`：机器人不被探活剔除；真人 + 机器人时的槽位分配（真人可能拿 1）；全机器人配对被丢弃且不出现在队列里；`pushMatchStatus` 不把帧推给 `bot:` 开头的成员、但 `queued_players` 仍计入。
- `match` 队列的既有用例（`queue_nx_test` / `queue_test`）必须保持绿：机器人只是新增成员，不改变队列语义。
- `logic.GrantCoins`：成功返回新余额、账号不存在、密钥错误、带会话的调用被拒（复用 `logic/service_test.go` 的 fake store 风格）。
- `gm`（`httptest` + Gin 测试模式，注入假 `pitaya.Pitaya`）：密钥中间件、`target` 是用户名与纯十进制两条解析路径、用户名大小写不敏感（`Alice` 与注册时的 `alice` 命中同一个 ID）、`delta=0` / `count<=0` 的 400、上游不可用时 503、`/` 能返回 HTML。

**端到端冒烟（需要真集群）**

`deploy/start-all.ps1` 起六进程后：

1. 客户端登录，页面上给该账号发 500 金币 → 客户端进商城看到余额变化；
2. 页面加 1 个机器人 → 客户端点开始匹配 → 秒配成一对 → 客户端看到对面有个站着不动的对手 → 打死它 → 它回出生点复活 → 客户端拿 10 杀结束这局 → 结果面板正常；
3. 这局之后查该账号档案：场次 / 胜 / 击杀正常累加，**没有**「对手名字是 bot」这类脏记录（机器人槽位是空 uid，被 `RecordMatch` 跳过）；
4. 页面加 2 个机器人（无真人排队）→ 两个机器人不被配对（`match.log` 能看到丢弃日志），随后真人入队时能拿到其中一个当对手。

## 12. 要同步更新的文档

| 文件 | 更新内容 |
| --- | --- |
| `AGENTS.md` | 角色列表加 `gm`；新的 `-gmaddr` / `-gmkey` flag；`bot:` uid 前缀约定；「GM 指令走 HTTP + 后端 RPC，不经 gate、不进玩家协议」；机器人槽位在 `game` 侧的三条处置 |
| `docs/ARCHITECTURE.md` | 六角色拓扑图里加 `gm`；一条新的数据流（页面 → gm → match/logic）；机器人配对规则与「全机器人丢弃」 |
| `docs/API.md` | GM 的 HTTP 接口（鉴权、请求/响应、错误码）；`match.match.addbots` 与 `logic.logic.grantcoins` 两条后端 route 及其入口检查 |
| `docs/BUILD.md` | 新增依赖 `github.com/gin-gonic/gin`（纯 Go，不涉及 cgo 与 UCRT64 工具链） |
| `docs/DEVELOPMENT.md` | 本地怎么用 GM 页面：起服务、配密钥、发钱、加机器人，以及机器人互打被丢弃的行为 |
| `joltgo/deploy/README.md` + `start-all.ps1` | 第六个进程与 `gm.log`；`-gmkey` 怎么配 |

## 13. 已知取舍

- **`gm` 常驻占一个集群节点**：它不做业务、平时没有任何流量，但在本地开发里多一个进程与一个日志文件。这是「不对外开放的管理口」应付的代价。
- **密钥是共享的、写在 payload 里**：单一密钥没有轮换、没有归属，泄漏即全权限。本次明确不做审计与多用户，因此没有更细的边界可言；要收紧时先补审计，再谈分级。
- **机器人是靶子不是对手**：它不反击，所以这一局的战绩对真人来说含金量低。它解决的是「本地凑不满人」的验证问题，不解决「练枪对手」的问题。
- **全机器人配对是丢弃、不是放回**：与匹配队列「弹出来就不再放回（放回等于重置等待时间）」的既有取舍一致。代价是裸加 2 个机器人时它们不会自己动起来，必须等真人入队。

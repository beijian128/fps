# 账号体系：用户名密码登录 + 会话凭证

> 状态：设计已确认，待实施
> 日期：2026-09-11

> ### ⚠️ 实施偏差记录（2026-09-11，实施后补记）
>
> 本文是**设计时**的记录，下面这条与最终落地的代码不一致。原文照留（它是当时的
> 设计依据），偏差在此统一说明，正文相关处另有 `【偏差】` 指回本节。
>
> **限流键从 IP 改为用户名：`rl:ip:{ip}` → `rl:user:{规范化用户名}`。**
> 原因是 account 服务**拿不到客户端 IP**：它是 backend 服务，会话上的 agent 是
> pitaya 的 `Remote`，`RemoteAddr()` 直接返回 nil
> （`third_party/pitaya/pkg/agent/agent_remote.go`），会话也没有暴露 frontend 地址
> 或 frontendID 的 getter。原设计的「IP 取 `s.RemoteAddr()`」（§5）无法实现。
> 改后粒度是「每账号每分钟 10 次」，对分布式暴力破解其实更强（换 IP 无效），代价是
> 同一出口 IP 的不同账号互不影响。实现见 `joltgo/account/store.go` 的 `keyRate`。
>
> 附带一处：`Resume` 实际**没有**做限流检查（§5 的 Resume 步骤 1 未落地）——
> resume 消息里只有 token、没有用户名，按用户名的限流键无从构造。token 本身是
> 32 字节随机串，猜不出来，所以未补。

## 1. 背景与目标

### 现状

服务端目前**没有任何账号体系**。客户端首次运行生成一个 UUID 存进
`user://client_id.txt`，把它当作 `JoinMsg.token` 发上来，`match.Join` 无条件
`s.Bind(ctx, token)` 把它设成 pitaya 会话 UID：

```go
uid := msg.Token
if !persisted { uid = nuid.New().Next() }
if err := s.Bind(ctx, uid); err != nil { ... }
```

没有注册、没有密码、没有服务端签发的凭证、没有任何一处校验。
`docs/ARCHITECTURE.md` 自己记着这条取舍：

> `JoinMsg.token` 是持有即可冒用的一次性身份，且被直接当作会话 UID；
> 生产环境应换成服务端签发、可吊销、带过期的凭证。本 demo 不做。

后果很具体：拿到别人的 `client_id.txt` 就等于拿到别人的账号——可以顶掉他的会话
（`sessionsByUID` 逻辑会 Close 旧连接），并通过 `game.rejoin` 接管他的对局槽位和击杀数。

### 目标

1. **真实账号**：用户名 + 密码注册，bcrypt 哈希后持久化；登录校验后签发凭证。
2. **凭证可吊销、带过期**：不能是持有即可冒用的字符串。
3. **服务无状态、可水平扩容**：account / match / gate / game 的**同类型节点**都无状态，
   加机器即扩容。所有共享状态（账号、凭证、会话归属、配对队列）都在 Redis。
4. **缓存与持久化数据都放 Redis**：不引入第二个存储（不加 MySQL/SQLite）。
5. **端到端可玩**：Godot 客户端有登录界面，登录后进匹配、对局、断线回局的现有链路不变。
6. **保持现有玩法链路零回归**：`game.rejoin`、实体-属性帧、重连回局全部照常工作。

### 非目标

- 对局历史、玩家档案（累计战绩）——本次不做，见 §13。
- OAuth / 第三方登录、邮箱验证、密码找回、验证码。
- 多端并存（同一账号同时多端在线）——明确选择「后登录顶号」，见 §13。

## 2. 架构总览

新增第 4 个服务角色 `account`（同二进制 `joltgo.exe -type account`），与
gate / match / game 对称：

```
                    ┌────────────────────────────────────────────┐
客户端 ──WS──▶ gate │ account.* → 轮询任一 account 节点            │
                    │ match.*   → 轮询任一 match 节点              │
                    │ game.*    → 按会话数据定点                   │
                    │ gate.gate.bindgame → 本节点（会话归属）       │
                    └───────────────┬────────────────────────────┘
                                    │ NATS RPC (RPCType_Sys)
                                    ▼
              account 服务（-type account）
                ├ register：SETNX 占名 → 写账号 → 签发 token → Bind
                ├ login   ：查账号 → bcrypt 校验 → 轮换 token → Bind → 定点顶号
                └ resume  ：查 token → 续期 → Bind

              match 服务：Join 只读会话 UID；配对队列在 Redis
              game  服务：毫不知情（uid 仍是 uidToInst 等表的键）

                    ┌──────── Redis ────────┐
                    │ 账号 / 凭证 / 会话归属 / 配对队列 │
                    └───────────────────────┘
```

### 关键设计决定

| 决定 | 理由 |
|---|---|
| **独立 `account` 服务**，不并入 match、不做 gate 本地 handler | 鉴权边界唯一（只有它碰密码），match/game 完全不知道 Redis 存在；gate 保持纯路由器 |
| **登录即绑定会话**（`s.Bind`） | 用 pitaya 现成语义；从 backend 绑前端会话的路径 match 服务今天就在走，已验证可用 |
| **register/login/resume 用 Request/Response** | 见 §3——NATS 下 uid 未绑定时 `Push` 直接失败，登录结果无法用 Push 回传 |
| **会话 UID = accountID** | `game.rejoin`、`uidToInst`、`SendPushToUsers` 全部原样工作，改动面最小 |
| **在线登记 + 定点踢**（不广播） | 消除同 gate 顶号竞态，见 §6、§8 |
| **配对队列搬 Redis（ZSET + Lua）** | match 队列原本是进程内数组，多节点下永久配对不上，见 §7 |

## 3. 协议（`game/protos/game.proto`）

### 新增消息

```proto
// RegisterMsg 是注册请求。用户名 3-16 位 [a-zA-Z0-9_]，密码 6-64 位。
message RegisterMsg {
  string username = 1;
  string password = 2;
}

// LoginMsg 是登录请求。
message LoginMsg {
  string username = 1;
  string password = 2;
}

// ResumeMsg 是凭证恢复请求：客户端持有 token，换回会话。
message ResumeMsg {
  string token = 1;
}

// LoginReply 是 register / login / resume 的统一应答。
// 三者都返回它，客户端用同一段代码处理。
message LoginReply {
  bool ok = 1;
  string token = 2;      // ok=true 时有效
  string username = 3;   // 用于 UI 显示（保留用户输入时的原始大小写）
  string account_id = 4; // 十进制字符串，仅供诊断
  string reason = 5;     // ok=false 时的原因，枚举见下（不是给人看的自由文本）
}
```

`reason` 取值（客户端据此显示文案，服务端只给 code）：

| reason | 含义 | 客户端文案 |
|---|---|---|
| `""` | ok=true | — |
| `bad_credentials` | 用户名不存在 **或** 密码错误 | 用户名或密码错误 |
| `name_taken` | 注册时用户名已占用 | 该用户名已被注册 |
| `bad_username` | 用户名格式非法 | 用户名需 3-16 位字母/数字/下划线 |
| `bad_password` | 密码长度非法 | 密码需 6-64 位 |
| `rate_limited` | 触发限流 | 操作过于频繁，请稍后再试 |
| `token_invalid` | resume 的 token 不存在或已过期 | 登录已过期，请重新登录 |
| `internal` | Redis 故障等 | 服务暂时不可用 |

**`bad_credentials` 同时覆盖"用户名不存在"和"密码错误"**：不区分是为了不泄露账号是否存在。

### 修改的消息

```proto
// JoinMsg 原本带 token；身份现在来自会话绑定，字段删除。
// 保留为空消息而不是删掉消息本身：route 与 handler 签名不变，改动面最小。
message JoinMsg {}
```

### 新增 route

| route | 类型 | 方向 | 说明 |
|---|---|---|---|
| `account.account.register` | Request | 客户端 → account | 注册 |
| `account.account.login` | Request | 客户端 → account | 登录 |
| `account.account.resume` | Request | 客户端 → account | 凭证恢复 |
| `gate.gate.bindgame` | RPC (User) | match → gate | 写会话数据 `gameServerId`（§6） |
| `gate.sys.kick` | RPC (User) | account → gate | pitaya 内置（`remote.Sys.Kick`），定点踢旧会话（§5） |

`gate.sys.kick` 不是本设计新增的——它是 pitaya 自带的 remote（每个前端由
`initSysRemotes` 注册为 `sys.kick`），本设计只是**第一次定点调用它**而不是广播。
服务名由 `component.WithName(...)` 决定（`pkg/component/service.go:73`）：
account 服务注册为 `"account"`、gate 新增的 handler 注册为 `"gate"`、pitaya 内置的是 `"sys"`。

`match.match.join` / `game.game.cmd` / `game.game.resync` 保持 Notify；`onMatched` /
`onFrame` 保持 Push。

### 为什么是 Request/Response 而不是 Notify/Push

`pkg/agent/agent_remote.go:107`：

```go
func (a *Remote) Push(route string, v interface{}) error {
	if reflect.TypeOf(a.rpcClient) == reflect.TypeOf(&cluster.NatsRPCClient{}) &&
		a.Session.UID() == "" {
		return constants.ErrNoUIDBind
	}
```

NATS 实现下，**uid 未绑定就 push 不了**。而登录失败时 uid 恰恰是空的（用户名密码错了
当然不绑）——所以「登录失败」的结果无法用 Push 回传。Request 路径按 mid 回包，
完全不依赖 uid，是唯一可行的选择。

整条链路（已逐行核对）：

1. 客户端发 Request（mid=N）
2. gate `processMessage`：SvType `account` ≠ `gate` → `remoteProcess` → `remoteCall(RPCType_Sys)`
3. account `handleRPCSys` → `ProcessHandlerMessage` → handler
4. `handleRPCSys` 把返回值放进 `protos.Response` 回给 gate
5. gate `remoteProcess` 的 `case message.Request:` → `a.GetSession().ResponseMID(ctx, msg.ID, res.Data)`

handler 的签名决定消息类型：`suitableHandlerMethods` 里 `NumOut() == 0` → Notify，
否则 → Request。三个 handler 都返回 `(*protos.LoginReply, error)`，因此自动是 Request。

### Response 帧的 wire 格式

```
Response 帧 = flag(0x04) + mid(LEB128 变长) + payload
```

- `flag = Type(Response=2) << 1 = 0x04`
- `flag |= 0x20`（`errorMask`）表示 pitaya 层错误，此时 payload 是错误字符串
- **没有 route 字段**（只有 Request/Push 是可路由类型），比 Push 帧还简单

> ⚠️ 本项目至今**只发过 Notify、只收过 Push**（`docs/API.md` 明写）。Response 路径
> 是全新代码，服务端也是第一次走到 `ResponseMID`。§11 里针对它的测试不是可选项。

## 4. Redis 键空间

```
-- account 域 --
acct:name:{用户名小写}  → accountID                 # SETNX 占名（原子）
acct:{accountID}        → Hash{username, pass_hash, created_at}
acct:seq                → INCR 计数器                # accountID 分配
sess:{token}            → accountID, TTL 7d          # 会话缓存
sess:acct:{accountID}   → 当前 token, TTL 7d         # 单会话强制（轮换）
online:{accountID}      → gateServerID, TTL 24h      # 会话归属（定点踢 / 定点 bindgame）
rl:ip:{ip}              → 计数器, TTL 60s            # 限流【偏差：实为 rl:user:{用户名}】

-- match 域 --
match:queue             → ZSET(member=uid, score=入队毫秒)
```

### 约定

- **accountID 是十进制字符串**（`"1"`、`"2"`…），直接当 pitaya 会话 UID。
  `game.uidToInst` 等 map 只把它当字符串用，不需要改。
  用 INCR 而非 UUID：日志里 `uid=1` 比 `uid=9f3a…` 好认。代价是 ID 可枚举——
  但 ID 不是凭证（凭证是 token），枚举不出任何东西。
- **用户名规范化**：`acct:name:{u}` 的键用**小写**（大小写不敏感，防「Alice/alice」抢注）；
  `acct:{id}` 的 `username` 字段保留**用户输入的原始大小写**用于 UI 显示。
- **token**：32 字节 `crypto/rand` → base64url 无填充（43 字符）。
  签发用 `SET sess:{token} {accountID} EX 604800`。
- **密码**：`bcrypt` DefaultCost(10)，约 50ms/次。Redis 里**绝不存明文、不存 token 明文**。
- **resume 续期**：`GET sess:{token}` 命中后 `EXPIRE` 续满 7 天——活跃用户凭证不过期。
- **登出**：`DEL sess:{token}`（本次 UI 不提供登出按钮，但接口与键位留好）。

## 5. account 服务

```
joltgo/account/
├── component.go   # pitaya handler（Register/Login/Resume）+ 业务规则（校验、限流、顶号决策）
├── store.go       # Redis 读写：账号 CRUD、token 签发/查询/轮换/删除、在线登记
└── token.go       # token 生成与密码哈希（纯函数，无 I/O）
```

分层意图：`store.go` 是纯 I/O，可被 miniredis 独立测试；`component.go` 只做规则与
pitaya 绑定；`token.go` 是纯函数。`New(app, store)` 接受 `store` 接口，
组件测试可以塞假实现。

### Register

```
1. 限流检查（rl:ip:{ip}）
2. 校验用户名/密码格式 → bad_username / bad_password
3. SETNX acct:name:{u} "" → 失败则 name_taken
4. id := INCR acct:seq
5. SET acct:name:{u} id；HSET acct:{id} {username, pass_hash, created_at}
   （任一步失败 → 回滚 DEL acct:name:{u}，避免占名却无账号）
6. token := issue(accountID)
7. s.Bind(ctx, accountID)
8. → LoginReply{ok:true, token, username, account_id}
```

### Login

```
1. 限流检查
2. id := GET acct:name:{u}；不存在 → bad_credentials
3. HGETALL acct:{id}；bcrypt 校验失败 → bad_credentials
4. old := GET online:{accountID}
5. token := 轮换（签发新 token，SET sess:acct:{id} = 新 token，DEL 旧 token）
6. s.Bind(ctx, accountID)     ← 同 gate 的旧会话在这里被框架同步关闭
7. me := GET online:{accountID}   ← 绑定后由 gate 写入，见 §6
8. if old != "" && old != me { RPCTo(old, "gate.sys.kick", &KickMsg{UserId: accountID}) }
9. → LoginReply{ok:true, token, username, account_id}
```

### Resume

```
1. 限流检查
2. id := GET sess:{token}；不存在 → token_invalid
3. EXPIRE sess:{token} 604800   # 续期
4. s.Bind(ctx, accountID)
5. → LoginReply{ok:true, token, username, account_id}
```

### 限流

> 【偏差】本小节整节按 IP 的设计未落地：账号服务拿不到客户端 IP，实际按用户名限流，
> 且 Resume 未做限流。见文首「实施偏差记录」。

`rl:ip:{ip}` INCR + 首次 EXPIRE 60s，超过 10 次/分钟返回 `rate_limited`。
IP 取 `s.RemoteAddr()`。

**不做账号锁定**：锁定会被用来恶意锁死他人账号。bcrypt 本身约 50ms/次，配合 IP 限流
在 demo 量级足够。

`Login`/`Register`/`Resume` 三者成功后共用同一条收尾路径（轮换 token + Bind）。

## 6. gate：会话归属登记

gate 是**唯一**知道"某账号的会话在哪个节点"的地方，登记由 pitaya 的两个回调驱动：

```go
builder.SessionPool.OnAfterSessionBind(func(ctx context.Context, s session.Session) error {
    if id := s.UID(); id != "" {
        rdb.Set(ctx, "online:"+id, app.GetServerID(), 24*time.Hour)  // 失败只记日志
    }
    return nil
})
builder.SessionPool.OnSessionClose(func(s session.Session) { /* 同理，DEL */ })
```

- `OnAfterSessionBind` 在 `Bind` 内部、**所有 sessionBindCallbacks 之后**触发
  （`pkg/session/session.go:477`），此时 uid 已确定。
- `OnSessionClose` 由 `agentImpl.onSessionClosed` 在连接关闭时逐个调用
  （`pkg/agent/agent.go:507`）。
- `app.GetServerID()` 拿到本节点 id（`pkg/app.go:231`）。

### 为什么 gate 连 Redis 是可接受的

上一版设计反对「gate 连 Redis」，理由是鉴权依赖会让 Redis 抖动卡死连接受理。
这里接受了，但把边界划清楚——**反对的是鉴权依赖，接受的是 best-effort 归属登记**：

- **写入失败只记日志，不阻塞 Bind**：连接照常建立，只是这一轮去重降级。
- **登记陈旧无害**：陈旧条目指向一个已无该会话的 gate → 定点踢过去 → `Sys.Kick`
  本地查不到 → 返回 `ErrSessionNotFound` → 忽略。
- **登记缺失是降级不是错误**：漏写只少踢一次，正确性由 token 轮换兜底（§8）。

所以 TTL 可以给得很宽（24h，与 token 同量级）：陈旧不造成伤害，只有「有会话却没记录」
才会漏踢。

### bindgame：让前端改自己的会话数据

配对的 match 节点可能不是玩家连接的那个 gate——它手里只有 `uid` 字符串，没有会话对象。
三条出路只有一条通：

- `app.GetSessionByUID(uid)` 在 backend 上恒返回 nil（池里只有前端会话）✗
- 自己造 Remote 会话 → `agent.NewRemote` 需要 rpcClient/serializer/serviceDiscovery
  等内部件，接口上没暴露 ✗
- **让玩家所属的 gate 自己去改** ✓ —— `online:{accountID}` 正好给出了地址

```
match                                        gate（玩家所属）
  RPCTo(online[uid], "gate.gate.bindgame",
        {uid, game_server_id, match_id, player_idx})
                                             → s := GetSessionByUID(uid)
                                             → s.Set("gameServerId", gsid)
```

这比原有的 `PushToFront` 更正确：前端拥有自己的会话，后端请它改，而不是隔着 NATS
去改别人进程里的状态。"gate 是纯路由器"原则不受损——这不是路由也不是业务，是会话归属。

⚠️ **安全坑**：在前端注册 handler 意味着**客户端可以直接发 `gate.gate.bindgame`**，
把自己的会话绑到任意 game 节点。挡法：`handleRPCSys` 建的 agent 用 Remote 会话
（`IsFrontend() == false`），客户端消息走 `localProcess`、会话是前端会话
（`IsFrontend() == true`）。handler 里 `if s.GetIsFrontend() { 拒绝 }` 即可把客户端挡在门外。

## 7. match：配对队列搬 Redis

### 现状的 bug

`match.Component.queue` 是**进程内数组**，而 `routeMatch` 从 map 里取第一个节点
（Go map 迭代随机）。多 match 节点下：两个玩家大概率落到**不同** match 节点 →
各自队列永远凑不满 2 人 → 双双等满 10s 单人兜底 → **每人各开一局**。
排队期断线重连的 `removeQueued` 去重也跨不了节点。

### 新设计

```
match:queue → ZSET(member=uid, score=入队毫秒)
```

ZSET 天然解决两件事：**按 uid 去重**（重连再 `ZADD` 就是更新 score，
`removeQueued` 那套手写去重可以删掉）、**按等待时长排序**（score 就是入队时间）。

两个 Lua 脚本负责原子性（Redis 串行执行脚本，多节点同时抢只有一个成功）：

```lua
-- pair.lua：不足 2 人时不动（直接 ZPOPMIN 会把人白白弹出队）
if redis.call('ZCARD', KEYS[1]) < 2 then return {} end
return redis.call('ZPOPMIN', KEYS[1], 2)

-- popstale.lua：原子取出「最早且已等待超时」的那一个
local r = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1], 'LIMIT', 0, 1)
if #r == 0 then return nil end
redis.call('ZREM', KEYS[1], r[1])
return r[1]
```

每个 match 节点都跑 1s 的 ticker 抢这两个脚本，**不需要选主**——谁抢到谁干活。

### 开局顺序（连带解决「幽灵排队者」）

排队者可能已经掉线（match 侧没有断线钩子），所以**先探活再建局**：

1. 挑 game 节点（沿用现有逻辑：取第一个；生产应做负载均衡，见 §12）
2. 逐人 `RPCTo(online[uid], "gate.gate.bindgame")` —— **RPC 失败 = 这人没了**，剔除
3. 一个都不剩 → 放弃（条目已弹出，玩家重连会重新入队）
4. `game.create` 用**剔除后的名单**建局；建局失败 → 回滚（逐人再发一次 `bindgame`
   带空 `game_server_id` 清掉）→ 放弃
5. `SendPushToUsers("onMatched", …)` 只推给活着的

第 2 步失败即剔除，比「建完局再发现人不在」干净：不会留下幽灵玩家占着槽位，
也不会出现「客户端收到 onMatched 但 resync 路由不到」的死局。

### 简化

`removeQueued`、`queue []queuedPlayer`、`tryMatch` / `tryMatchTimeout` 的切片操作
全部删除，换成 4 个薄函数：`enqueue` / `popPair` / `popStale` / `startMatch`。
`match.go` 会比现在更短。

### Join

```go
func (c *Component) Join(ctx context.Context, msg *protos.JoinMsg) {
    s := c.app.GetSessionFromCtx(ctx)
    uid := s.UID()
    if uid == "" {                    // 未登录
        log.Printf("match: join rejected: session not bound")
        return
    }
    if c.tryRejoin(ctx, s, uid) { return }   // 回局优先
    c.enqueue(ctx, uid)                       // ZADD（重连自动去重）
}
```

`JoinMsg` 已为空消息，但 handler 签名保留 `msg` 参数——删掉参数会让 route 仍是
Notify 但语义不明确，保留更清楚。

## 8. 多节点正确性

| 关注点 | 机制 | 节点本地状态 |
|---|---|---|
| account 服务自身 | 状态全在 Redis | 无 |
| 注册占名 | `SETNX` 原子 | 无 |
| 会话凭证 | `sess:{token}` 在 Redis，任意节点可校验 | 无 |
| **顶号** | 在线登记 + 定点 RPC 踢（§6、§8） | 无 |
| **配对队列** | ZSET + Lua（§7） | 无 |
| 会话数据 `gameServerId` | `online:` 定位 gate，定点 RPC 写（§6） | 无 |
| 对局归属 | 实例归属写在会话数据 + `game.rejoin` fan-out | 无（注册表已按 uid 索引） |
| 跨 gate 重连 | 新 gate 重新 `bindgame` 写会话数据 | 无 |

### 顶号竞态为什么消失

关键是：**竞态只在同 gate 时存在**——跨 gate 时我们自己的会话还没绑定，
kick 打过来也找不到人。而「同 gate」恰恰**不需要踢**，因为 `Bind` 内部的
`sessionsByUID` 会同步把旧的 Close 掉。

流程：

```
1. old := GET online:{accountID}      # 上次登录在哪个 gate
2. s.Bind(ctx, accountID)             # 同 gate 的旧会话在这里被框架同步关闭
                                      # gate 的 after-bind 回调写入 online:{accountID}
3. me := GET online:{accountID}       # Bind 返回时 gate 必然已写完（跨节点 RPC，天然有序）
4. if old != "" && old != me { 定点踢 }
```

第 4 步只在 `old != me` 时执行，意味着旧会话在**另一个 gate** 上。定点 RPC 只投给
那个 gate，**我们自己的 gate 全程没参与**。同 gate 的情况走第 2 步的框架同步关闭。

第 3 步能读到正确值，是因为 `Bind` 对远端会话走 `bindInFront` 跨节点 RPC——
**RPC 返回时 gate 侧的回调已经执行完了**，不需要 sleep 或轮询。

### 两个机制的分工（都要）

| 机制 | 作用 | 失效后果 |
|---|---|---|
| **token 轮换**（登录时 `DEL` 旧 token） | **权威**：旧客户端断了就再也回不来 | 被踢的客户端重连后 resume 成功 → 又变两个会话 |
| **在线登记 + 定点踢** | **及时**：让旧连接立刻停下，不再抢发 `game.cmd` | 旧连接苟活到下次断线；正确性仍由 token 兜底 |

### 推送双发（分析结论：不要动）

`subscribeToUserMessages` 用的是**没有 queue group**的 `conn.Subscribe`
（`pkg/cluster/nats_rpc_server.go:193`），同一 uid 在两个 gate 上都有会话时，
**两个 gate 都收到推送、两个客户端都收帧**。

加 queue group **更糟**：会把每条帧随机投给其中一个 gate，等于两个客户端各收到一半
帧流，画面残缺。不加则是旧端收全量冗余帧（它反正马上被踢掉），新端始终正确。
**维持现状，靠顶号把窗口压到一次登录的毫秒级**——这条要写进文档，
避免以后有人「顺手优化」成 queue group。

## 9. 客户端

`godot_client/scripts/fps_client.gd` 与 `main.gd`。

### 新增的编解码

```gdscript
# 发 Request：flag(0x00) + mid(LEB128) + route 长度 + route + payload
func _send_request(route: String, payload: PackedByteArray) -> int

# _on_data 新增分支：flag 低 3 位 == MSG_RESPONSE
#   mid = LEB128 变长读出 → 查 _pending[mid] 得是哪条请求
#   注意 errorMask = 0x20：pitaya 层错误会置位，此时 payload 是错误字符串
```

`_pending: Dictionary[mid → route]` 做请求关联；登录类请求 5s 超时 → 退回登录面板并提示。

### 登录流程

```
连接成功 → 握手 → 握手响应里发 HandshakeAck
  ├ 本地有 token → 自动发 resume
  │    ├ ok:true  → 存新 token → send_match_join()
  │    ├ ok:false → 显示登录面板（提示"登录已过期"）
  │    └ 超时     → 显示登录面板
  └ 本地无 token → 显示登录面板
                    ├ 登录 → ok:true → 存 token → join
                    └ 注册 → ok:true → 存 token → join
```

**没有 token 就绝不发 `match.match.join`**——未绑定的 join 会被服务端忽略，
发了只会让人困惑。

- Token 落盘仍用 `user://`，改名 `auth_token.txt`。**不复用 `client_id.txt`**：
  里面躺的是旧的自造 UUID，拿它去 resume 必然失败，不如不认。
  附带存 `last_username` 预填输入框。
- `main.gd` 新增登录面板：`CanvasLayer` + 两个 `LineEdit`（用户名/密码，
  密码 `secret=true`）+ 登录/注册按钮 + 错误标签；登录成功才隐藏面板、复位 `_matched` 状态。
- 现有的断线重连、看门狗、`_store.clear()` 等逻辑**不动**——登录失败后的重试
  走现成的重连路径。
- 断线重连时自动 resume（token 未变）→ 回到同一局，用户无感。

## 10. 部署

- `redis-server.exe` 放进 `deploy/`（与 `etcd.exe`/`nats-server.exe` 同规格），监听 `6379`。
- **Redis 数据目录不清空**（与 etcd 相反）：etcd 里只有服务发现这种瞬时状态，清掉无妨；
  Redis 里是**账号**，清了就真没了。这条差异要写进 `deploy/README.md`，
  否则下次有人照抄 etcd 那套 `Remove-Item` 就把账号连锅端了。
- `start-infra.ps1` 加起 Redis（已在跑则沿用，同 nats 的处理方式）；
  `start-all.ps1` 加第 4 个进程 `-type account`。
- **持久化开 AOF**（`--appendonly yes`）：不开的话 Redis 一重启**所有账号消失**。
  代价是本地多一个 `appendonlydir/`。
- `main.go` 新增 `-redis` flag，默认 `localhost:6379`；三个角色按需取用。

### 新增代码结构

```
joltgo/
├── kv/            # 新增：Redis 连接管理（薄封装，只有 *redis.Client 构造）
├── account/       # 新增：component.go / store.go / token.go
├── gate/          # 改：account.* 路由 + bindgame handler + 会话归属回调
├── match/         # 改：Join 读会话 UID；队列搬 Redis
└── main.go        # 改：第 4 个角色 + -redis flag + 各角色的 Redis 装配
```

## 11. 测试

分层，各层独立可跑：

| 层 | 文件 | 依赖 | 覆盖 |
|---|---|---|---|
| 存储 | `account/store_test.go` | miniredis | 占名冲突、ID 分配、密码校验、token 签发/轮换/续期/删除、key 空间与 TTL |
| 组件 | `account/component_test.go` | fake Session + miniredis | 三个 handler 的成功/失败路径、限流（【偏差】按用户名而非 IP，见文首）、顶号决策（`old == me` 不踢）、`Bind` 失败时的错误返回 |
| 队列 | `match/queue_test.go` | miniredis | 原子取双人、不足 2 人不弹、超时兜底、重连去重、幽灵玩家剔除、建局失败回滚 |
| 客户端 | `godot_client/tests/login_reply_decode_test.gd` | 无 | Response 帧解码 + `errorMask` + `LoginReply` 各字段（**首次覆盖 Response 路径**） |
| 冒烟 | `godot_client/tests/login_smoke.gd` | **活集群** | 注册 → 拿 token → 断线 → resume → 收到 `onMatched` |
| 回归 | 现有 4 个无头测试 + `rejoin_smoke.gd` | 混合 | 必须继续全绿 |

`account` / `match` 的纯 Redis 测试**不需要集群**（miniredis 是进程内的）——
这是把存储与组件分层的主要收益：登录逻辑的主体测试可以在 `go test` 里秒级跑完，
不用起 etcd/NATS/四进程。

`rejoin_smoke.gd` 是回局链路的唯一端到端验证，本次改了 `bindPlayer` **必须重跑**。

依赖（都已在本地模块缓存，无需联网）：`github.com/redis/go-redis/v9`、
`golang.org/x/crypto/bcrypt`、`github.com/alicebob/miniredis`（测试用）。

## 12. 已知取舍

1. **推送双发窗口**（§8）：旧连接在新登录后的毫秒级窗口内会多收几帧。
   不影响正确性，不修。
2. **game 节点选择仍是「取第一个」**：`match.startMatch` 沿现有逻辑。
   多 game 节点下负载不均，且 `GetServersByType` 可能返回已死的旧节点
   （`AGENTS.md` 已记录此风险）。本次不改，但队列搬 Redis 后这个位置更显眼了。
3. **account 服务不参与 etcd 的健康检查之外的任何协调**：无限流之外的保护，
   无防重放（token 是 bearer，拿到即用——但现在是服务端签发的，且可吊销）。
4. **无对局历史/玩家档案**：见 §13。
5. **Redis 是单点**：demo 用单实例；生产应上哨兵/集群。键空间设计（无跨键事务、
   只有单键原子操作与 Lua）对 Redis Cluster 是友好的，但**未实测**。

## 13. 不在本次范围

- 对局历史、玩家档案（累计战绩）——需要新增数据模型与查询接口。
- 多端并存（同一账号多端同时在线）——会要求 `game` 的 `uidToInst` 从账号维度改成
  会话维度，回局与匹配槽位都要重做。
- 密码找回、邮箱验证、验证码、OAuth。

## 14. 文档同步清单

仓库硬约定：改动代码必须同步文档。

- `AGENTS.md`：新增 `kv/` 与 `account/` 目录、改数据流一节、
  把 `JoinMsg.token` 那条「已知安全取舍」替换为新设计、新增 Redis 到「多进程部署」。
- `docs/ARCHITECTURE.md`：新增「账号与会话」一节（含多节点正确性）、
  更新数据流（登录 → 匹配 → 对局）、替换「已知安全取舍」段落。
- `docs/API.md`：Response 帧格式、三条 account route、`gate.gate.bindgame`、
  `JoinMsg` 变更、登录时序图。
- `joltgo/deploy/README.md`：Redis 的角色、不清数据目录的理由、AOF。
- `godot_client/README.md`：登录界面、`auth_token.txt`、新增测试。

## 15. 实施顺序（供 writing-plans 参考）

1. `kv/` + `account/`（store → token → component）+ 单测（miniredis，无集群）。
2. proto 变更 + 重新生成 Go 码。
3. `main.go` 第 4 个角色 + gate 路由 + `account.*` 端到端跑通（需集群）。
4. gate：会话归属回调 + `bindgame` handler + 守卫。
5. `match`：Join 改读会话 UID；`bindPlayer` 拆分；队列搬 Redis + 单测。
6. 部署脚本（Redis + 第 4 进程）。
7. 客户端：Response 编解码 + 登录流程 + 登录面板。
8. 测试补齐（`login_reply_decode_test.gd` / `login_smoke.gd`）+ 回归全绿。
9. 文档同步（§14）。

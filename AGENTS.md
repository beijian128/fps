# AGENTS.md

> AI 引导文件：让任何 AI / agent 在最小上下文下快速建立正确心智模型。
> 读本文件后，按需深入 `docs/ARCHITECTURE.md`、`docs/API.md`、`docs/BUILD.md`、`docs/DEVELOPMENT.md`。
> **改动代码时同步更新对应文档与本文件**（过期文档比没有更糟）。

## 1. 项目一句话定位

基于 [Jolt Physics](https://github.com/jrouwe/JoltPhysics) 的服务端权威第一人称 PVP demo：
- **服务端** `joltgo/`：Go + cgo 调用 Jolt（C ABI 包装层），**pitaya 框架**（内置源码，
  **Cluster 模式**）做**分布式微服务**：gate（前端接入）/ account（账号与凭证）/ logic（局外钱包、背包与商城）/
  match（匹配）/ game（对局逻辑），共享状态放 **Redis**。
  ECS 架构，20 Hz 固定 tick，WebSocket 推送**实体-属性增量帧**（重连 / 首次进入推全量帧）。
- **客户端** `godot_client/`：Godot 4.7（gl_compatibility），瘦客户端，60 Hz 渲染 + 实体-属性增量累积 + 影子跟随插值，纯程序化美术/音效。
- **JoltPhysics/** 是 gitignored 第三方依赖（需单独 `git clone`），不属于本仓库代码。

## 2. 目录与入口

```
fps/
├── joltgo/                  # 服务端（Go，单二进制六角色）
│   ├── main.go              # 入口：解析 -type(gate|account|logic|match|game|gm) / -redis / -gmaddr / -gmkey，按角色装配 pitaya app
│   ├── bot/                 # ★ 机器人 uid 前缀的唯一真相（bot:），match 与 game 共用
│   ├── gm/                  # ★ GM 服务：Gin HTTP 端口 + 内嵌 Web 操作页 + 两个后端 RPC 客户端（不经过 gate）
│   ├── gate/                # gate 服务：AddRoute 路由（account.*/match.* 轮询 / logic.* 均匀随机 / game.* 按会话数据定点）
│   │   └── session.go       # ★ 会话归属登记（online:{accountID} → 本 gate）+ 远端 remote gate.bindgame
│   ├── account/             # account 服务：注册 / 登录 / 凭证恢复 + distlock 注册临界区（token.go / store.go / component.go）
│   ├── logic/               # logic 服务：玩家档案 / 钱包 / 背包 / 商城 / 装备 + logic.online 远端
│   ├── kv/                  # Redis 连接（go-redis Open + redigo 持久化池 + 启动时 Ping）
│   ├── online/              # 会话归属读写（账号当前在线于哪个 gate）
│   ├── persist/             # ★ protoc-gen-redis 数据模型 + Hash 仓储（账号与玩家持久化）
│   │   ├── protos/           # account.proto + 生成的 account.redis.go（独立 Go 包）
│   │   │   └── player/       # player.proto + 生成的 player.redis.go（独立 Go 包）
│   │   └── store.go / player.go # redigo 池适配、Hash 读写与领域记录转换
│   ├── third_party/distlock/ # 固定提交内置的 Redis 分布式锁（MIT）
│   ├── match/               # match 服务：配对队列（queue.go，队列在 Redis）+ 分配 game 节点（RPC game.create）
│   ├── game/                # game 服务：对局实例生命周期 + 远端 handler
│   │   ├── instance.go      # ★ 每个对局一条 goroutine，顺序执行、无锁；每 tick 按槽位下发增量/全量帧
│   │   ├── component.go     # game.create/cmd/resync/rejoin handler + 同步帧 → protobuf 转换
│   │   └── protos/          # protobuf 消息定义 + 生成码（wire 契约；新增同步属性无需改此文件）
│   ├── replication/         # 与 ECS 解耦的实体-属性同步层：终值表 + 本帧脏集（不 import ecs）
│   ├── physics/             # ★ 唯一 cgo 包：sim.Physics 的 Jolt 实现 + id 翻译
│   ├── sim/                 # ECS 玩法层（纯 Go，无 cgo，单线程所有）
│   │   ├── map.go           # ★ 运输船场景：部件表（甲板/船体/集装箱/走道/舷梯/桅杆）+ 材质号
│   │   ├── simulation.go    # 组装世界、按部件表搭场景、持有同步 store
│   │   ├── components.go    # 组件定义（Body 带 Kind/Size/Static/Mat）
│   │   ├── systems.go       # 每 tick 系统（输入/命中盒跟随/变换同步/弹丸命中/对局结算）+ 各处 rep.Set 变更点
│   │   ├── replicate.go     # ★ ECS ↔ 同步属性的唯一映射（declareAttributes + 变更点 rep.Set）
│   │   └── replicate_test.go# oracle 测试：从 ECS 世界独立推期望属性，漏写的 rep.Set 在此失败
│   ├── ecs/                 # 零依赖 archetype ECS 核心（纯 Go，可单测）—— 零依赖、可单测
│   ├── wrapper/jolt_c.{h,cpp}  # 纯物理桥（extern "C"，无任何业务概念）
│   ├── deploy/              # 本地集群基础设施：etcd/nats/redis 二进制 + 启动脚本
│   ├── CMakeLists.txt       # 把 Jolt 作为子项目编译 libjolt_c.dll
│   └── build.ps1            # 一键构建（UCRT64 + go build + 拷贝 DLL）
├── godot_client/            # 客户端（Godot 4）
│   ├── scenes/main.tscn     # Main(Node3D) + FpsClient + Sfx + UI（CanvasLayer，挂 screen_manager）
│   ├── theme/               # ★ 界面主题：tokens.gd（颜色/字号/间距唯一真相）+ build_theme.gd + tactical_theme.tres（生成物）
│   ├── ui/                  # ★ 界面层：screen_manager（唯一状态源）+ backdrop（全局底图）+ settings（本机偏好）+ shell（大厅入口）/login/profile/shop/bag/item_card/match_status_bar/result_overlay/hud/pause_screen/rejoin_prompt（进大厅时问「回到对局 / 放弃对局」）
│   ├── assets/ui/           # ★ 界面美术（程序化生成物，tools/gen_art.py 产出）：底图 / 头像 / 道具与入口图标
│   ├── scripts/main.gd      # 输入/相机/双玩家渲染/插值/玩法反馈（受击红闪 + 命中音）+ 把 FpsClient 事件派发给 UI
│   ├── scripts/world_store.gd # 本地世界状态：实体-属性增量累积成完整世界（按名字取值）
│   ├── scripts/fps_client.gd  # 传输层（pomelo 握手/登录/匹配/心跳/Frame 编解码 + Logic Request/Response + 重连/看门狗）
│   ├── scripts/body_entity.gd # 每个服务端刚体一个渲染节点（程序化模型）
│   ├── scripts/sfx.gd       # 程序化合成 WAV
│   └── tests/               # headless 回归/冒烟测试（.gd，见 §6）
├── docs/                    # ARCHITECTURE / API / BUILD / DEVELOPMENT
└── AGENTS.md                # 本文件
```

**先读顺序**：本文件 → `docs/ARCHITECTURE.md`（分层与数据流）→ `joltgo/main.go` →
`joltgo/account/component.go` + `joltgo/gate/gate.go` + `joltgo/gate/session.go` +
`joltgo/account/store.go` + `joltgo/persist/store.go` + `joltgo/persist/protos/account.proto` +
`joltgo/match/match.go` + `joltgo/game/instance.go` + `joltgo/game/component.go` →
`joltgo/replication/`（`store.go` 终值表/脏集 + `frame.go` 帧结构）→ `joltgo/sim/replicate.go`
（ECS↔属性映射）→ `joltgo/sim/simulation.go`（tick 顺序 + Physics 接口）→
`godot_client/scripts/world_store.gd` + `godot_client/scripts/main.gd`。

## 3. 关键不变量（改代码前必须理解）

1. **实体 ID 双空间**：物理刚体 id = ECS 实体 id，由物理桥从 1 递增发放；纯逻辑实体（玩家）由 `ecs.NewEntity` 从 `1<<24` 起分配，两空间不重叠。
2. **`physics/` 是唯一允许 cgo 的包**；`sim/` 与 `ecs/` 必须是纯 Go，只依赖 `sim.Physics` 接口（可用 fake 物理单测）。Jolt 原生 BodyID 不透传（0=失败；合法 id 带序号位）。**每个对局实例各建一个 Jolt 世界**（`physics.New()` 独立实例）。
3. **包装层是纯物理桥**：只暴露 Jolt 原生能力，不含玩家/弹丸/血量/移动策略/任何调参；所有游戏调参集中在 `joltgo/sim/simulation.go` 常量区。多角色（charIdx 0/1）是纯物理能力，不带业务。
4. **服务端权威 + 固定 tick**：`sim.Simulation` 每 tick 顺序固定：
   `inputSystem → hitboxFollowSystem → physics.Step → step++ → syncSystem → projectileSystem → expireProjectilesSystem → matchSystem`。
   命中全部靠**刚体接触事件**，不做距离判定。`hitboxFollowSystem` 必须在 `Step` **之前**：弹丸的接触判定用的是步进开始时的刚体位置，晚一步贴命中盒就是拿上一 tick 的旧位置判命中。
   **玩家不是刚体**（是 `CharacterVirtual`），弹丸看不见它 —— 每个玩家配一个跟随角色的**命中盒刚体**（`PlayerHitbox`）承担「可被击中体积」，并让两个角色都忽略两个命中盒（`CharacterIgnoreBody`），细节见 §3.9 与 `docs/ARCHITECTURE.md`。
5. **同步协议是通用「实体-属性」帧**：wire 契约仍是 `game/protos/game.proto`（protobuf），但**新增一个同步属性不需要改 proto、不需要重生成 Go 码、也不需要动客户端解码**——只有三步：
   - 在 `sim/replicate.go` 的 `declareAttributes` 里加一行 `Declare`（属性表必须完整稳定，见 §5）；
   - 在每个「值会变的地方」**就近** `rep.Set`（同步层只做终值去重，不做任何 ECS 遍历）；
   - 客户端按**属性名**取值（`WorldStore.attr(id, "名字")`）。
   属性名是扁平字符串（约定 `组件.字段`，如 `Body.Mat` / `Player.Idx` / `Player.Kills`）。属性表（Schema）**只随 full 帧**下发（不单独发消息），客户端据此把属性 ID 还原成名字与 Kind；**不认识的属性照常存下、只是不渲染**（这就是前后端可独立演进的原因）。`replication.Store` **不 import `ecs`**，耦合全部集中在 `sim/replicate.go`。帧内实体/属性按 ID **升序**输出（字节稳定、可测试）。上行 route 三段式 `server.service.method`（`account.account.login` / `match.match.join` / `game.game.cmd` / `game.game.resync`）。
6. **并发/锁（核心变化）**：`sim.Simulation` **无锁**——由对局实例 goroutine（`game/instance.go`）独占驱动，输入经命令 channel 投递、同一 goroutine 顺序执行，不需要任何互斥。`replication.Store` 与 `sim.Simulation` 一样由该 goroutine 独占，**非并发安全**（package 注释已声明）。`game.Component` 的实例注册表（uid→实例）用一把 `sync.Mutex` 保护（跨 RPC handler 共享）。`ecs.World` 自身不加锁。
7. **ECS 使用约束**：`Each` / `QueryEach*` 回调内**禁止** Add/Remove/Destroy（swap-remove 打乱迭代），需要增删时"先收集再处理"；`Get` 返回的指针仅本次调用有效；物理实体 Destroy 后彻底注销（id 不复用），逻辑实体 id 走 free list 复用。
8. **地图是对称的**：`sim/map.go` 的部件表在绕 Y 轴旋转 180°（`(x,y,z) → (-x,y,-z)`）下自映射——带 `mirror` 的部件由代码自动补孪生体，两个出生点（也就是死亡后的复活点）必须完全等价。改图只写半边；`sim/map_test.go` 会验证对称性、出生点与开局命中盒都不卡掩体。舷梯参数有硬约束（单级抬升 ≤ 0.4、进深 ≥ 0.5，均为角色半径/`WalkStairs` 决定），改动前先看 `map_test.go` 里的说明。
9. **玩家命中盒**：玩家是 `CharacterVirtual`（不是刚体），弹丸（动态 + CCD）既看不见它也打不中它——Jolt 的 CCD **明确忽略传感器**，所以「把玩家做成 sensor」也走不通。每个玩家因此额外配一个与角色同形状的**静态胶囊刚体**（`PlayerHitbox`），每 tick 用 `SetBodyPosition` 贴到角色身上（`jolt_set_body_position`），弹丸靠最普通的刚体接触命中它。配套不变量：
   - **命中盒不进渲染路径**：它没有 `Body` 组件，`syncSystem` 跳过没有 `Body` 的刚体，因此不产生同步流量、客户端不渲染、`declareAttributes` 里也没有它的属性。`replicate_test.go` 的反向断言（store 里不许有世界里不存在的实体）会挡住「给命中盒加 rep.Set」。
   - **两个角色都必须忽略两个命中盒**（`CharacterIgnoreBody` → Jolt `OnContactValidate` 返回 false）。注意**共位**的静态刚体本身并不挡人（CharacterVirtual 的扫掠忽略 fraction = 0 的初始重叠，实测见 `physics/pvp_hit_integration_test.go`）；真正需要忽略的是命中盒**落在角色前方**的情形 —— 对方玩家的命中盒对本地角色就是一堵隐形墙，自己的命中盒每 tick 才跟随一次、会被角色甩到身前（跑动 0.7 m/tick，下落更快）。
   - **死亡复活时命中盒要一起搬回出生点**（`respawn`）：它要到下一 tick 的 `hitboxFollowSystem` 才跟随角色，留在旧位置会让之后飞来的弹丸打中一个「已经复活在别处的人」。
   - **枪口不能落在自己的命中盒里**：命中盒是实体刚体、会挡住弹丸，而第三人称的枪口正好在角色中轴上。`shoot` 会把出生点沿射向推到盒外（`pushOutsideOwnHitbox`），并且「弹丸 vs 自己的命中盒」的接触被忽略（不扣血、也不吃掉弹丸）。
   - **每个对局各建一份**：命中盒在 `init()` 里随场景创建（**排在场景几何之后**，让场景刚体 id 仍从 1 开始）。**世界生命周期 = 实例生命周期**：`Init()` 只在实例创建时调用一次，**场景重置功能已删除**（连同 `Simulation.Reset` / `Instance.Reset` / `replication.Store.Reset` 与 `CommandMsg.reset`），因此同一实例内刚体 id 不会复用。
10. **持久化与分布式锁边界**：`persist/` 是结构化持久化层，账号与玩家模型由固定版本 `protoc-gen-redis` 分包生成（`account.proto` → `account.redis.go`；`player/player.proto` → `player.redis.go`），同一账号 Hash key 为 `acct:1:<accountID>:0`，玩家钱包/背包 Hash 为 `REDB#2:<accountID>:0` / `REDB#1:<accountID>:0`，**玩家档案（累计战绩）为 `REDB#3:<accountID>:0`，最近 20 场历史为 List `playerhist:<accountID>`**（历史是 List 不是 Hash 行，所以不套 `REDB#` 前缀）。`distlock` 只包住「占名 → 分配 ID → 写 Hash → 落名字映射」这类多命令业务临界区；凭证轮换、匹配队列已经由 Redis Lua 原子脚本保证，战绩计数累加是一条 `MULTI/EXEC` + `HINCRBY`（纯计数不需要锁），**不要**再用锁替换那些更强的原子操作。

## 4. 数据流（分布式链路）

```
客户端 ──WS(pomelo+protobuf)──▶ gate(frontend)
   gate 按 route 路由：account.* / match.* → 对应后端轮询；logic.* → 均匀随机；game.* → 定点 game 节点（读会话数据 gameServerId）
   account 校验用户名/密码（bcrypt）→ distlock 串行同名注册 → 写账号 Hash + SETNX 名字映射 → RPC logic.logic.online（随机 logic）→ logic 确保钱包/背包 Hash → 签发 token 存 Redis → Bind(accountID) → 回 LoginReply
   gate → logic.logic.*（随机 logic 节点）→ logic 读写 Redis 钱包/背包 Hash
   gate 绑定成功后写 online:{accountID} → 本 gate（会话归属登记）
   match 从 Redis 队列（match:queue，ZSET，**入队用 ZADD NX**）配对 2 人 —— **没有单人兜底**：凑不满就一直等，玩家可发 `match.match.cancel` 取消 → GetServersByType("game") 挑节点 → RPCTo "game.game.create"
   game 节点创建 Instance（每条 goroutine）→ 每 tick SendPushToUsers("onFrame", …, uids, "gate")
   game 判出胜负 → 推 onMatchEnded + RPC logic.logic.recordmatch（game → logic，不重试不去重）→ 终结实例并摘注册表 → 玩家回大厅重新匹配
   match 每 tick 把 `onMatchStatus{queued_players, waited_seconds}` 推给队列里每个等待者
   gate 收到 push → 经 NATS 用户频道转发给客户端会话
```

- **会话 UID = accountID**（不再是客户端自己生成的 UUID）。握手（Handshake → Ack）之后**必须先登录**：
  客户端发 `account.account.register` / `.login` / `.resume`（**Request/Response**，不是 Notify/Push），
  服务端回 `LoginReply`（含 token + account_id）并把 pitaya 会话 `Bind` 到 accountID；
  拿到 `LoginReply.ok=true` 之后才允许发 `match.match.join`（`JoinMsg` 是**空消息**，身份来自会话）。
- 客户端本地存 token（`user://auth_token.txt`），下次启动直接 `.resume` 免登录；token 7 天过期、每次 resume 续期，
  一次 `.login` 会**轮换** token 并删掉旧的（同一账号只允许一个活跃会话，顶号）。
- 上行：输入 + 射击**合并成一条** `game.game.cmd`（Notify，帧是最小发送单位）；不再有单独的 `input`/`shoot` 消息，射击等即时操作也不额外补推，统一等下一 tick 的帧。（场景重置已整体删除，字段 7 在 `CommandMsg` 里 `reserved`。）
- **增量帧**每 tick 推 `onFrame`，只含本帧变化的 `(实体, 属性, 终值)`；同一属性一帧内改多次只发终值，值没变的写入不产生流量（连静态几何也每 tick 写、但不下发）。
- **重连回局**：会话 UID（accountID）就是回局的钥匙 → `match.join` 先向所有 game 节点 fan-out RPC `game.game.rejoin` → 命中则走 `bindGameOn`（RPC 请持有该会话的 gate 把 `gameServerId` 写进会话数据）+ `pushMatched`（推 `onMatched`），与首次匹配同一条收尾路径 → 客户端收到 `onMatched` 后先清空本地世界、再主动发 `game.game.resync` → 服务端把该槽位的**下一帧**标为全量，单独下发 full 帧（含 Schema）。因为 full 帧是**先清空再整体覆盖**，即使中间先到了几帧增量也会被整帧盖掉——不存在「onMatched 与 full 帧谁先到」的竞态。
- **一局结束**：`matchSystem` 判出胜负即产出结算快照（`sim.DrainOutcome`，取走即清），`game` 先广播本 tick 增量帧、再推 `onMatchEnded`、再异步上报战绩，最后终结实例。客户端收到 `onMatchEnded` 必须离开对局态 —— 实例一终结帧流就断，否则 2.5s 接收看门狗会把「本局结束」误判成掉线。
- **登录进大厅先问一句「有没有没打完的局」**：客户端登录/resume 成功后立刻发 `match.match.pending`（Request/Response，服务端只查询：fan-out `game.rejoin`，**不写会话数据、不推 onMatched、不入队**）。命中就弹 `ui/rejoin_prompt`（「回到对局 / 放弃对局」），ESC = 稍后决定（点「开始匹配」仍走回局分支）。不许替他自动选：服务端权威、对局不因掉线暂停，自动重连会把他从大厅拽进战场，自动放弃等于替他改战绩。
- **放弃对局 = 释放这一个玩家，不是终结对局实例**：`match.match.abandon` → match 找到托管节点 → `RPCTo("game.game.leave")` → `Instance.Release(slot)`。四件事同时成立：① 摘掉 `uidToInst/uidToIndex`（他的 `game.cmd`/回局查询都不再命中，可立刻重新匹配）；② `Instance.left[slot]=true`（不再给他推帧）；③ 结算槽位带 `left` 标记、logic **跳过他的战绩**、`onMatchEnded` 不推给他；④ **实例照常跑**，对手那一局继续打到分出胜负并正常入账（只在最后一个在场玩家也离开时终结）。`SlotResult.left` 就是为了区分「中途放弃」与「压根没这个槽位」——后者（练习局）照旧不入账。
- 断线：客户端 1s 重连；**接收看门狗 2.5s 只在匹配后生效**（匹配等待期无帧流属正常）。实例的空闲回收超时是 **30 分钟**，同时也是掉线回局窗口。
- 服务间通信走 etcd（服务发现）+ NATS（RPC）+ Redis（共享状态：账号、凭证、会话归属、匹配队列、钱包、背包），见 `joltgo/deploy/`。

## 5. 约定与坑

- **C 包装层**：函数 `extern "C"`，只用 C 类型/定长数组/opaque 指针，禁止跨边界传 `std::string`/`vector`/C++ 对象/异常。
- **cgo**：显式 `C.float(...)` / `C.uint32_t(...)` 转换；`physics/` 之外的 Go 代码不出现 `import "C"`。
- **pitaya**：框架源码内置在 `joltgo/third_party/pitaya/`（`go.mod` 用 `replace` 指向本地目录），不在其上改业务。**Cluster 模式**需本地 etcd（服务发现）+ nats-server（RPC），共享状态另需 redis-server，见 `deploy/`。协议是 pomelo 帧 + **protobuf** payload（不是 raw JSON 文本帧），客户端编解码在 `fps_client.gd`。改动 wire 契约 `game.proto` 后重跑 `protoc --go_out`;持久化 proto 则跑 `gen-redis.ps1`，两者不要混用。
  - **⚠️ 本地改动（重新 vendor / 升级 pitaya 时必须重新打上，否则凭证会重新明文进日志）**：上游把**原始请求载荷**打进日志（`logger.Debugf("SID=%d, Data=%s", session.ID(), data)`），而 account 的 register/login 载荷里是**明文密码**、resume 里是 **bearer token**；日志级别又硬编码为 debug（`pkg/logger/logger.go`），`deploy/*.log` 里因此直接躺着玩家密码。已把这三处改为只打**长度**（`DataLen=%d`，保留「载荷到没到、形状对不对」的排查能力，不牺牲 debug 流的其余价值）：
    - `pkg/service/handler_pool.go`（后端 RPC 的 handler 调用路径）
    - `pkg/service/util.go`（同一条日志的本地副本）
    - `pkg/acceptorwrapper/rate_limiter.go`（限流丢弃帧时打整条 pomelo 帧；本项目未启用 acceptor 包装器，属预防）
    这是**安全脱敏**，不是业务逻辑——该目录仍然不放业务代码。改动只影响日志内容，不影响任何协议行为。
- **Request/Response vs Notify/Push**：pitaya 靠 handler **有没有返回值**来判定类型（有返回值 = Request，无返回值 = Notify）。
  `account.*` 三条路必须走 Request/Response —— 登录**失败**时会话根本没有 uid，而 NATS 后端推送要求 uid 已绑定
  （`ErrNoUIDBind`），Push 发不出去。这是本项目第一处用 Request/Response 的地方。
- **Response 帧没有 route 字段**：下行 Push 是 `flag + route + payload`，而 Response 是 `flag + mid(LEB128) + payload`，
  客户端靠自己发出去时记的 mid 认领响应。`flag & 0x20`（errorMask）置位时 payload 不是 `LoginReply` 而是 pitaya 的错误串。
  客户端解码见 `fps_client.gd` 的 `_on_response`。
- **`RegisterRemote` vs `Register`**：`app.RPCTo` 走 `RPCType_User` → `handleRPCUser` → `remotes` 表，那张表**只由
  `RegisterRemote` 填充**。`gate.gate.bindgame` 注册成 handler 的话 match 会拿到 `ErrNotFoundCode`。反过来这也关掉了
  攻击面：客户端发的 `gate.gate.bindgame` 会被路由到 handler 池、找不到而报错。
- **账号与凭证**：客户端不再自带身份；用户名/密码注册登录（bcrypt DefaultCost，用户名 `^[a-zA-Z0-9_]{3,16}$` 且**大小写不敏感**、
  密码 6–64），服务端签发 32 字节随机 base64url token（`sess:{token}` / `sess:acct:{id}`，TTL 7 天、resume 时续期）。
  **会话 UID = accountID**，所以 `game.rejoin` / 实例注册表 / 推送目标这些按 uid 索引的地方一行没改。
  限流是**按用户名**（`rl:user:{name}`，1 分钟 10 次）而不是按 IP —— account 是 backend，pitaya 的 `Remote` agent 的
  `RemoteAddr()` 返回 nil，它看不见客户端 IP。
- **持久化模型（`persist/protos/`）**：账号 `DBAccount`、玩家 `DBUserWallet` / `DBUserBag` / `DBUserProfile` / `DBMatchRecord` 都由 `protoc-gen-redis` 生成独立包；账号 key 为 `acct:1:<accountID>:0`，钱包/背包/档案 key 为 `REDB#2:<accountID>:0` / `REDB#1:<accountID>:0` / `REDB#3:<accountID>:0`，历史 key 为 `playerhist:<accountID>`。生成物必须提交，改字段后跑 `joltgo/gen-redis.ps1`，不要手改 `.redis.go`；生成包与 `game/protos` 分开，避免枚举/消息类型重复声明。
- **分布式锁（`distlock`）**：源码按固定 commit 内置在 `third_party/distlock/`；唯一本地补丁是把其 `go.mod` 的短 module path `distlock` 改成 `github.com/beijian128/distlock`，根模块再用本地 `replace` 引入（离线可构建且 `go mod verify` 通过）。锁只用于多命令业务提交；单条脚本能原子完成的事继续走 Lua，避免把强原子性降级成锁。
- **Redis 数据目录不清空**：`deploy/start-infra.ps1` 每次都清 `etcd-data`（见下条），但 `redis-data` **永不清空** ——
  里面是账号、密码哈希、凭证、钱包与背包；清掉就是把所有账号和局外进度删光。两者对待方式相反，别把 etcd 的习惯套过去。
- **route 三段式**：`server.service.method`（如 `account.account.login`、`match.match.join`、`game.game.create`）。RPC 调用（`RPCTo`）也必须是三段式，否则报 `no server type chosen for sending RPC`。
- **etcd 租约与残留**：租约只在 etcd 运行期间倒计时，etcd 重启会把上一轮的孤儿租约按 checkpoint 恢复并**重新计时**（v3.5.14 实测：进程已死 + etcd 停机 66s 后重启，注册项仍带 19s TTL 复活；TTL 内反复重启会反复续命）。所以本地 `start-infra.ps1` 每次启动都清空 `etcd-data`（详见 `deploy/README.md`），整套重启后服务列表一定干净；生产则要调小 TTL 并给选节点加健康检查/重试——`GetServersByType` 可能返回已死的旧节点，`match.startMatch` 选中它就会把玩家静默丢出队列。
- **构建**：Jolt 必须经 CMake `add_subdirectory` 编译（保证 NDEBUG/指令集宏与静态库一致）；UCRT64 与 MINGW64 不能混用；`build.ps1` 硬编码 `C:\msys64`；运行需 `joltgo.exe` 与 `libjolt_c.dll` 同目录。
- **Godot 4**：材质属性用 `metallic`（不是 Godot 3 的 `metalness`）；命令行运行用 `preload` 而非 `class_name`；typed for 循环需 4.2+。
- **客户端界面分层**：`screen_manager` 是**唯一状态源**（`boot → login → lobby → matching → in_match → result → lobby`），界面只做「渲染 `state()` + 发意图（`intent_*`）」；协议在 `fps_client.gd`、世界渲染与输入在 `main.gd`。登录成功后**不自动匹配**，要玩家在大厅点「开始匹配」。
- **进大厅的询问框是浮层，不进 `State` 枚举**：`ui/rejoin_prompt`（回到对局 / 放弃对局）与 `pause_screen` 同一性质 —— 由 `screen_manager._rejoin_prompt_open` 控制，只在大厅（`_page == ""`）显示；`_set_state()` 里一旦离开 LOBBY/MATCHING 就自动收起。放弃的结果以服务端应答为准（失败**保留**框并提示重试，不假装成功）；`intent_escape()` 只收起它（= 稍后决定），绝不顺手放弃 —— 破坏性动作不该有一个「顺手按到」的键。新增可点控件要同步补 `tests/ui_feedback_test.gd` 的三张清单（手型 / 提示行 / tooltip）。
- **大厅是入口，子界面整屏铺开**：`_page == ""` 时显示大厅（`shell`：三张入口卡 + 底部匹配条），否则显示那一个子界面（`profile` / `shop` / `bag` 各是一整屏，带「返回大厅」）。大厅与子界面**互斥**、由 `screen_manager._apply_visibility()` 统一切换 —— 子界面不是嵌在大厅里的一块内容，所以它们互不知道对方存在，也不会出现「大厅露出半屏、子界面缩在右边」。返回大厅一律走 `intent_close_page()`。
- **每个可点的东西都要有「能点 + 点完有回音」**（`tests/ui_feedback_test.gd` 会挡）：① 交互控件 `mouse_default_cursor_shape = 2`（手型）；② 每个主界面有一行操作提示（`HUD` 底部一行按键、子界面「ESC 返回大厅」、大厅「点卡片 / Tab / Enter」）；③ 设置项与关键按钮有 `tooltip_text`；④ 动作结果走通知栈 `ui/toast`（购买 / 装备 / 匹配 / 取消 / 掉线重连…），`screen_manager.notify()` 是唯一入口；⑤ `ESC` 统一走 `intent_escape()`：对局内开合设置层、子界面里返回大厅 —— 同一个键只做一件事，不会出现「在大厅按 ESC 毫无反应」。没有回音的动作等于没做完。
- **界面美术是程序化生成物**：`godot_client/assets/ui/*.png` 由 `godot_client/tools/gen_art.py`（Pillow，4 倍超采样抗锯齿）生成，改图改脚本再重跑。这与项目「纯程序化美术」一致（模型来自 `body_entity.gd`、音效来自 `sfx.gd`），也让图标可复现、可 diff、无素材授权问题。要换成 AI 出图时只替换同名 png，界面代码一行不用改；`.import` 文件必须一起提交（新增图片后跑一次 `godot --headless --path godot_client --import`）。
- **界面根节点必须铺满父级**（`anchors_preset = 15`，即 `anchor_right = anchor_bottom = 1.0`）：屏幕的直接父级是 `CanvasLayer` 或内容插槽，**都不是容器**，不会替子节点摆位置。漏掉这一步，锚点全为 0 的根节点尺寸就是 `0×0`，表现为「整个大厅塌进左上角一小块」「锚在右下角的血条跑到屏幕外」——这类塌陷不报错、不崩，只会静默错位，**改完界面要用真实窗口看一眼**（无头断言测不到它）。
- **对局内 ESC = 设置层，不是暂停**：`ui/pause_screen` 打开时**服务端照常推进**（对手还在打），所以文案必须是「设置 / 返回战场 + 对局仍在继续」，不能写「已暂停」。它不进 `State` 枚举（是对局态上的浮层），离开 `IN_MATCH` 时自动收起 —— 否则鼠标会卡在「未捕获」状态，结算层上点不动按钮。
- **本机偏好走 `ui/settings.gd`，不要塞进 tokens**：tokens 是**所有玩家共享的视觉真相**（改它要重生成主题），`settings.gd` 是**每个玩家各自的本机开关**（灵敏度 / 界面缩放 / 受击反馈强度 / HUD 安全区，存 `user://settings.cfg`）。改偏好的入口只有 `screen_manager.intent_set_setting()`，由 `apply_settings()` 一次性推给窗口（`content_scale_factor`）与 HUD（安全区）；灵敏度与受击强度由 `main.gd` 现读。
- **HUD 面板挂在 `%SafeArea/Field` 下**：安全区留白 = 视口尺寸 × 偏好百分比（默认 5%，即标题安全区 90%；电视/投影会裁掉边缘 3%–10%，玩家可拉到 0 贴边）。`%SafeArea` 是 `MarginContainer`（负责四边留白），里面**再垫一层普通 `Control`（`Field`）当锚点参照系** —— `MarginContainer` 是容器，面板直接挂进去会被拉成同一个满格矩形叠在一起。
- **界面按技能清单走**：**本项目自己的规范沉淀在个人技能 `fps-ui-rules`**（分层 / 八条不可协商 / 改界面的固定动作 / 踩过的坑 + `references/theme-vocabulary.md` 主题词汇表），改 `godot_client/` 界面时先加载它；通用技巧再往下看 godot-prompter 的 `godot-ui`（Control / 容器 / 锚点 / 焦点导航）、`responsive-ui`（分辨率 / 安全区）、`hud-system`（对局内 HUD）。可执行底线只有三条：交互控件有**可见焦点**并在屏幕打开时 `grab_focus()`；可点区域高度 ≥ `tokens.HIT_MIN`；HUD 整层 `mouse_filter = IGNORE`。
- **主题取值集中在 `theme/tokens.gd`**：`theme/build_theme.gd` 把它写成 `theme/tactical_theme.tres`，`tests/theme_test.gd` 校验两者一致（改了 tokens 要重跑生成，否则测试红）。界面优先用 `theme_type_variation` 挑样式；只有必须参与运算的取值（按血量取色、按用户名取头像色）才 import tokens。要加新样式就在 `build_theme.gd` 里加变体，别在界面代码里 `duplicate()` 出界面私有的样式盒。
- **`ui/*.tscn` 是手写的**：结构与样式直接在编辑器里改，没有生成器要同步。布局用锚点 + Container；HUD 的四个贴边面板用「锚点 + `offset_*` 贴角」（`offset_*` 在贴边元素上是正确工具，在普通内容里会被窗口拉伸拉坏）。
- **多进程部署**：单二进制 `joltgo.exe -type gate|account|logic|match|game|gm` 六角色（flag 是 `-type` / `-redis` /
  `-gmaddr` / `-gmkey`，gate 的 WS 端口 8080 仍写死），先起 etcd + nats + redis（`deploy/start-all.ps1` 一把梭）；
  服务日志在 `deploy/gate.log` / `account.log` / `logic.log` / `match.log` / `game.log` / `gm.log`
  （pitaya 写 stderr，`*.out.log` 是 stdout 基本为空）。
  `gate` / `account` / `logic` / `match` / `gm` 启动时 Ping 一次 Redis，连不上直接退出；`game` 不碰 Redis。
- **GM 指令不进玩家协议**：`gm` 是第六个角色（backend），自己起 Gin HTTP 端口（默认 `:8082`）+ 内嵌 Web 操作页，
  gate 的路由表里没有 `gm.*`，它也不监听任何 pitaya 端口、不注册 handler / remote。它只**主动**发后端 RPC
  （`match.match.addbots` / `logic.logic.grantcoins`），能这么做的前提是它以 `pitaya.Cluster` 构建 app ——
  **能不能发 `app.RPCTo` 取决于 app 的模式，与 frontend/backend 无关**（`pkg/client/client.go` 是 acceptor
  客户端，它发不出后端 RPC，别混）。两条 route 都要求「调用方没有会话」+ 共享密钥（`-gmkey` / `GM_KEY`）：
  前者挡客户端经 gate 发来的调用（**route 名不是权限**），后者挡其它后端；密钥为空 = 不提供管理入口。
  三个进程（`gm` / `logic` / `match`）必须配同一个密钥，`start-all.ps1` 统一传。GM 操作**不审计**，只写 `gm.log`。
- **机器人是队列里的普通成员**：uid 前缀 `bot:`（唯一真相在 `joltgo/bot/`），由 `match` 自己入队
  （`gm` 不碰 `match:queue`，也不构造 bot uid）。配对时机器人跳过「读在线登记 + 请 gate 写会话数据」；
  全机器人配对直接丢弃且**不放回**（放回会让它们反复被弹出、反复重置等待时间）；「机器人 + 掉线真人」这一对
  则保留机器人回队列等下一个真人。对局内它是不收帧、不进注册表的「无客户端槽位」，结算靠既有的「空槽位」
  规则跳过 —— **logic 不需要认识机器人**。机器人全程不动、不开火（没有客户端上报输入），被打死在出生点复活。
- **多节点正确性**：`match` 的队列在 Redis（`match:queue` ZSET + Lua 原子脚本），所以 match 节点无状态、可横向扩；
  `gate` 在会话绑定时写 `online:{accountID}` → 本节点、断开时清除，account 靠它把顶号踢到**正确的那个 gate**，
  match 靠它找到玩家所在 gate 去写会话数据（`gate.gate.bindgame`）。这个登记是 **best-effort**：Redis 写失败只会漏踢一次，
  不会挡住连接或登录；真正的权威是**凭证轮换**（旧 token 被删，旧连接下一次 resume 必然失败）。
- **同步属性（`sim/replicate.go`）**：加同步字段 = `declareAttributes` 加一行 `Declare` + 在每个变更点 `rep.Set`。**属性表必须在 `Simulation.New()` 里一次声明完整**——`Set`/`Remove`/`Get` 遇到未声明属性直接 panic（`Declare` 重复同名也 panic）；客户端在 full 帧里一次拿到完整属性表，之后靠它解码所有增量，所以不能等首次 `Set` 才登记。
- **就近 `rep.Set` 漏写不会在运行时暴露**：Store 不反查 ECS 世界，漏写只会让客户端**静默停在旧值**（没有报错、没有日志）。唯一能抓住它的是 `sim/replicate_test.go` 的 oracle 测试（`expectedAttrs` 从 ECS 世界独立推期望值，与 store 全量逐项比对）。所以加同步字段时**先补 `rep.Set`、再在 `expectedAttrs` 里补断言**——这是「变更点显式 Set」这套设计的固有代价。
- **full 帧不得修改增量基线**：`Store.Full()` 刻意不碰 `sent`（已下发基线）——全量是发给**单个**客户端（重连 / resync）的消息，其他在线客户端的基线不受影响；又因为所有 op 携带的是**终值**而非相对增量，无论基线如何，客户端都会收敛到同一状态。
- **实体 id 在同一实例内不再复用**（场景重置已删除，世界随实例创建一次）：`replication.Store` 不再有 `Reset`，`Store.Destroy` 仍会清掉该实体自己的已下发基线（`sent`）。
- **服务端 20Hz tick 无条件运行**（实例创建后无论客户端是否在线都推进）。
- **Git 流程：本仓库直接提交到 `main`**，不要开 feature branch、不要走 PR——`main` 就是集成分支，历史保持线性（`git push origin main` 即可）。提交信息用 `<type>: <subject>` 前缀（`feat` / `fix` / `docs` / `refactor` / `test`），AI 提交在结尾加 `Co-Authored-By` 尾注；提交前先把 §6 里对应的测试跑绿。

## 6. 构建 / 测试命令

```powershell
# 服务端构建（MSYS2 UCRT64 + Go 1.26+）
# 改了 persist/protos/account.proto 或 persist/protos/player/player.proto 时重建生成的 Redis 代码
cd joltgo; .\gen-redis.ps1
cd joltgo; .\build.ps1

# 本地起分布式服务端（etcd + nats + redis + gate/account/logic/match/game/gm 六进程）
cd joltgo\deploy; .\start-all.ps1     # 停：.\stop-infra.ps1

# 客户端（Godot 4.7）—— 命令行要用 _console.exe 才看得到 stdout
Godot_v4.7.2-stable_win64.exe --path godot_client        # F5 运行
```

```bash
# 下面的命令在 Git Bash 里跑（`cd joltgo && ...`）。注意：`./game` 与 `./physics` 的
# 测试要加载 libjolt_c.dll（joltgo/ 下已构建），必须把 joltgo/ 放进 PATH。

# gofmt / vet（纯 Go 包，无 cgo，最快）
# 注：存量文件是 CRLF，gofmt -l 会把它们整文件标记出来——不是格式错误，忽略即可
cd joltgo
gofmt -l gate account logic kv online match game physics sim replication ecs persist
go vet ./gate ./account ./logic ./kv ./online ./match ./game ./physics ./sim ./replication ./ecs ./persist

# 单元测试（账号/凭证、持久化、会话归属、匹配队列、ecs 存储语义 / sim 系统+oracle / replication 存储与帧）
# 注：./account ./persist ./online ./match ./gate ./kv 里的 Redis 用例走 miniredis，不需要真 Redis
PATH="$PWD:$PATH" go test -count=1 ./account ./persist ./logic ./online ./match ./gate ./kv ./ecs ./sim ./replication

# 全部包（含 cgo 编译检查与 ./game，需已构建出 libjolt_c.dll）
PATH="$PWD:$PATH" go test -count=1 ./...

# 真 Jolt 集成测试：地图可玩性（舷梯真能走上去、静态几何不漂移）
# + PVP 命中链路（CCD 弹丸真能打中静态胶囊、角色忽略表真的有阻挡差异）
PATH="$PWD:$PATH" go test -count=1 -tags joltdll ./physics
```

客户端测试（`_console.exe` 变体；`--path` 后接项目目录）：

```bash
# 无头回归：无需服务端，可单独跑
Godot_..._console.exe --headless --path godot_client --script res://tests/world_store_test.gd       # 世界存储语义（full/removed/destroy/未知属性）
Godot_..._console.exe --headless --path godot_client --script res://tests/frame_decode_test.gd      # Frame / Schema protobuf 解码
Godot_..._console.exe --headless --path godot_client --script res://tests/game_frame_test.gd        # 渲染路径（喂合成帧，不碰 WebSocket）
Godot_..._console.exe --headless --path godot_client --script res://tests/reconnect_cleanup_test.gd # 断线清理本地世界与插值状态
Godot_..._console.exe --headless --path godot_client --script res://tests/login_reply_decode_test.gd # Response 帧（无 route）+ LoginReply / errorMask 解码
Godot_..._console.exe --headless --path godot_client --script res://tests/logic_state_decode_test.gd # LogicStateReply / 商城状态解码
Godot_..._console.exe --headless --path godot_client --script res://tests/logic_panel_test.gd # Logic 面板 UI
Godot_..._console.exe --headless --path godot_client --script res://tests/match_ended_test.gd # 对局结束（onMatchEnded）：停看门狗 / 清世界 / 不自动重排
Godot_..._console.exe --headless --path godot_client --script res://tests/theme_test.gd        # 主题与 tokens 一致（改了 tokens 必须重跑 build_theme.gd）
Godot_..._console.exe --headless --path godot_client --script res://tests/screen_flow_test.gd  # 屏幕状态机：登录不自动匹配 / 匹配 / 取消 / 进局 / 结算
Godot_..._console.exe --headless --path godot_client --script res://tests/profile_screen_test.gd # 个人信息页（等级/经验条/统计派生/战绩列表/空态）
Godot_..._console.exe --headless --path godot_client --script res://tests/item_grid_test.gd    # 商城与背包卡片（数量 1–99、买不起禁用、装备/卸下、空态）
Godot_..._console.exe --headless --path godot_client --script res://tests/hud_test.gd          # 对局内 HUD（血条 / K/D / 回合进度 / 击杀播报 / 准星命中）
Godot_..._console.exe --headless --path godot_client --script res://tests/ui_feedback_test.gd  # 交互反馈（手型光标 / 操作提示 / 工具提示 / 通知栈 / ESC）
Godot_..._console.exe --headless --path godot_client --script res://tests/pause_settings_test.gd # 设置层：ESC 开合 / 离开对局自动收起 / 四项偏好落到位
Godot_..._console.exe --headless --path godot_client --script res://tests/pending_match_decode_test.gd # 进大厅询问 / 放弃对局的 Response 解码与 mid 认领
Godot_..._console.exe --headless --path godot_client --script res://tests/rejoin_prompt_test.gd # 进大厅的询问框：弹框 / 回到对局 / 放弃 / 稍后决定（ESC）

# 冒烟：需要活集群（etcd + NATS + redis + gate/account/logic/match/game/gm 六进程）
Godot_..._console.exe --headless --path godot_client --script res://tests/login_smoke.gd            # 注册 → LoginReply → 断线 → resume → onMatched（两客户端配对）
Godot_..._console.exe --headless --path godot_client --script res://tests/ws_smoke.gd               # 登录 + 匹配 + 20Hz 增量帧
Godot_..._console.exe --headless --path godot_client --script res://tests/rejoin_smoke.gd           # 登录 + 断线回同一局
Godot_..._console.exe --headless --path godot_client --script res://tests/logic_smoke.gd            # 注册 + Logic 状态 / 购买 / 装备
Godot_..._console.exe --headless --path godot_client --script res://tests/profile_smoke.gd          # logic.logic.profile 端到端（档案查询）
Godot_..._console.exe --headless --path godot_client --script res://tests/abandon_smoke.gd          # 进大厅问「有没有没打完的局」→ 放弃 → 对手那一局照常继续
```

> `rejoin_smoke.gd` 是新协议下**唯一**端到端验证「重连回到同一对局」的测试（同一
> match_id + 同一 player_idx + 重连后收到 full 帧），改匹配 / 回局 / resync 链路后必跑；
> 它的断言在载荷解析失败时显式判失败（不会假通过）。冒烟测试里 `login_smoke` / `ws_smoke` /
> `rejoin_smoke` 都要**先登录再 join**，而且**各自都需要两个客户端真配对**（`tests/pair_helper.gd`
> 起第二个真实客户端）—— 单人兜底删除后，一个客户端永远匹配不上。其余八个回归测试互相独立、
> 无需服务端。

## 7. 变更 runbook（改什么就动哪里）

- 改玩法逻辑 → 只动 `joltgo/sim/`（组件 + 系统 + 调参），跑 `go test ./ecs ./sim`。
- 改局外钱包、背包、商城与装备 → 只动 `joltgo/logic/` 及对应客户端面板/编解码，跑 `go test ./logic ./persist` 与 Logic Godot 测试。
- **加/改一个同步字段** → 只动 `joltgo/sim/replicate.go`：`declareAttributes` 加一行 +
  每个变更点 `rep.Set`，再在 `sim/replicate_test.go` 的 `expectedAttrs` 里补一条断言，
  跑 `go test ./sim ./replication`。客户端按属性名读，取新值只需一句
  `_store.attr(id, "名字")` —— **不需要**改 proto、生成码或客户端解码器（§3 第 5 条）。
- **加/改持久化字段** → 改 `joltgo/persist/protos/account.proto`（账号）或 `joltgo/persist/protos/player/player.proto`（玩家数据），跑 `joltgo/gen-redis.ps1`，再改 `persist/store.go` / `persist/player.go` 与调用方；生成文件必须一起提交。账号改动跑 `go test ./persist ./account`，玩家局外数据另跑 `go test ./logic ./persist`。
- 改地图/场景 → 只动 `joltgo/sim/map.go`（部件表 + 材质号），跑 `go test ./sim`；
  涉及"走不走得上去"这类几何手感，再跑 `go test -tags joltdll ./physics`。
  新增材质号同时改 `godot_client/scripts/body_entity.gd` 的 `MATS` 表（只能追加编号）。
- 改 ECS 核心 → 只动 `joltgo/ecs/`（必须零依赖、可单测）。
- 改物理接口 → 动 `joltgo/wrapper/` + `joltgo/physics/`（`sim.Physics` 接口同步），重跑 `build.ps1`，再跑 `go test -tags joltdll ./physics`（命中盒/忽略表这类结论只有真 Jolt 能验证）。
- 改**协议结构**（新增/删除消息或 route、改 Frame/Schema 字段号）→ 动 `joltgo/game/protos/game.proto`（重跑 `protoc --go_out`）+ `joltgo/game/` + `joltgo/match/` + `docs/API.md` + `godot_client/scripts/fps_client.gd`（protobuf 编解码同步改）。只加同步**属性**不走这条路（见上）。
- 改服务路由/匹配 → 动 `joltgo/gate/` / `joltgo/match/`。
- 改**账号/登录/凭证** → 动 `joltgo/account/`（`token.go` 纯函数、`store.go` Redis、`component.go` handler）
  + `joltgo/game/protos/game.proto`（`RegisterMsg`/`LoginMsg`/`ResumeMsg`/`LoginReply`）
  + `godot_client/scripts/fps_client.gd`（Request/Response 编解码）+ `godot_client/scripts/main.gd`（登录面板），
  账号本体字段改动还要跑 `go test ./persist ./account` 与 `.\gen-redis.ps1`；再跑 `login_reply_decode_test.gd` 与（有集群时）`login_smoke.gd`。
- 改**会话归属 / 多节点行为** → 动 `joltgo/online/` + `joltgo/gate/session.go`（+ 调用方 `account/` `match/`），
  跑 `go test ./online ./gate ./account ./match`。
- 改客户端渲染/输入 → 只动 `godot_client/`，Godot 直接 F5。
- **改客户端界面** → 只动 `godot_client/ui/`（结构与逻辑）与 `godot_client/theme/`（配色与圆角/字号/间距）：改颜色先改 `tokens.gd` 再跑 `build_theme.gd` 重生成主题，改布局直接改对应 `.tscn`；跑 `theme_test` + `screen_flow_test` + `hud_test` + 受影响屏幕自己的测试。界面**不得**绕过 `screen_manager` 直接改状态或直接调 `fps_client`。
## Logic 局外数据边界

- `logic` 是无状态 backend：客户端经 gate 随机路由到任意 logic 节点，节点只把钱包/背包写入 Redis；账号登录成功后由 account 通过 `logic.logic.online` 确保档案存在。
- 钱包与背包分别使用 `REDB#2:<accountID>:0` 与 `REDB#1:<accountID>:0`，玩家档案（xp/击杀/死亡/场次/胜负）使用 `REDB#3:<accountID>:0`，最近 20 场历史使用 List `playerhist:<accountID>`；新账号初始金币 1000，默认商城为 rifle/pistol/shotgun/medkit。
- **战绩上报不重试、不去重**（`game` 用 `app.RPC` 单发一条 `logic.logic.recordmatch`，2 秒超时，失败只记日志）：logic 不可达时那一场会静默少记一条，这是 spec 明确接受的取舍；载荷带 `match_id`，将来要加幂等键不用改协议。
- **`logic` 只读 account Hash 的 username**（`persist.UsernameByID`）用于给历史记对手名：只读、不写别人的键，缺失就当空串。
- 购买不是请求幂等接口，客户端不得自动重试；余额预检查、账号锁内复查与钱包/背包补偿共同保证并发一致性。game 不读取局外背包或装备。

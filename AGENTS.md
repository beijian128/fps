# AGENTS.md

> AI 引导文件：让任何 AI / agent 在最小上下文下快速建立正确心智模型。
> 读本文件后，按需深入 `docs/ARCHITECTURE.md`、`docs/API.md`、`docs/BUILD.md`、`docs/DEVELOPMENT.md`。
> **改动代码时同步更新对应文档与本文件**（过期文档比没有更糟）。

## 1. 项目一句话定位

基于 [Jolt Physics](https://github.com/jrouwe/JoltPhysics) 的服务端权威第一人称 PVP demo：
- **服务端** `joltgo/`：Go + cgo 调用 Jolt（C ABI 包装层），**pitaya 框架**（内置源码，
  **Cluster 模式**）做**分布式微服务**：gate（前端接入）/ account（账号与凭证）/
  match（匹配）/ game（对局逻辑），共享状态放 **Redis**。
  ECS 架构，20 Hz 固定 tick，WebSocket 推送**实体-属性增量帧**（重连 / 首次进入推全量帧）。
- **客户端** `godot_client/`：Godot 4.7（gl_compatibility），瘦客户端，60 Hz 渲染 + 实体-属性增量累积 + 影子跟随插值，纯程序化美术/音效。
- **JoltPhysics/** 是 gitignored 第三方依赖（需单独 `git clone`），不属于本仓库代码。

## 2. 目录与入口

```
fps/
├── joltgo/                  # 服务端（Go，单二进制四角色）
│   ├── main.go              # 入口：解析 -type(gate|account|match|game) 与 -redis，按角色装配 pitaya app
│   ├── gate/                # gate 服务：AddRoute 路由（account.*/match.* 轮询 / game.* 按会话数据定点）
│   │   └── session.go       # ★ 会话归属登记（online:{accountID} → 本 gate）+ 远端 remote gate.bindgame
│   ├── account/             # account 服务：注册 / 登录 / 凭证恢复（token.go 纯函数、store.go Redis、component.go handler）
│   ├── kv/                  # Redis 连接（Open + 启动时 Ping）
│   ├── online/              # 会话归属读写（账号当前在线于哪个 gate）
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
│   ├── ecs/                 # 零依赖 archetype ECS 核心（纯 Go，可单测）—— 本次改造完全未动
│   ├── wrapper/jolt_c.{h,cpp}  # 纯物理桥（extern "C"，无任何业务概念）
│   ├── deploy/              # 本地集群基础设施：etcd/nats/redis 二进制 + 启动脚本
│   ├── CMakeLists.txt       # 把 Jolt 作为子项目编译 libjolt_c.dll
│   └── build.ps1            # 一键构建（UCRT64 + go build + 拷贝 DLL）
├── godot_client/            # 客户端（Godot 4）
│   ├── scenes/main.tscn     # Main(Node3D) + FpsClient + Sfx
│   ├── scripts/main.gd      # 输入/相机/双玩家渲染/登录面板/按属性名查询与插值/HUD/音效（核心）
│   ├── scripts/world_store.gd # 本地世界状态：实体-属性增量累积成完整世界（按名字取值）
│   ├── scripts/fps_client.gd  # 传输层（pomelo 握手/登录/匹配/心跳/Frame 编解码 + 重连/看门狗）
│   ├── scripts/body_entity.gd # 每个服务端刚体一个渲染节点（程序化模型）
│   ├── scripts/sfx.gd       # 程序化合成 WAV
│   └── tests/               # headless 回归/冒烟测试（.gd，见 §6）
├── docs/                    # ARCHITECTURE / API / BUILD / DEVELOPMENT
└── AGENTS.md                # 本文件
```

**先读顺序**：本文件 → `docs/ARCHITECTURE.md`（分层与数据流）→ `joltgo/main.go` →
`joltgo/account/component.go` + `joltgo/gate/gate.go` + `joltgo/gate/session.go` +
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
   - **每个对局各建一份**：命中盒在 `init()` 里随场景创建（**排在场景几何之后**，让场景刚体 id 仍从 1 开始），`reset()` 走 `physics.Destroy()` + 重建，`CharacterIgnoreBody` 的忽略表也随之重建。

## 4. 数据流（分布式链路）

```
客户端 ──WS(pomelo+protobuf)──▶ gate(frontend)
   gate 按 route 路由：account.account.* → account 服务（Request/Response）；
                       match.match.join → match 服务；game.game.* → 定点 game 节点（读会话数据 gameServerId）
   account 校验用户名/密码（bcrypt）→ 签发 token 存 Redis → Bind(accountID) → 回 LoginReply
   gate 绑定成功后写 online:{accountID} → 本 gate（会话归属登记）
   match 从 Redis 队列（match:queue，ZSET）配对 2 人（或 10s 兜底单人）→ GetServersByType("game") 挑节点 → RPCTo "game.game.create"
   game 节点创建 Instance（每条 goroutine）→ 每 tick SendPushToUsers("onFrame", …, uids, "gate")
   gate 收到 push → 经 NATS 用户频道转发给客户端会话
```

- **会话 UID = accountID**（不再是客户端自己生成的 UUID）。握手（Handshake → Ack）之后**必须先登录**：
  客户端发 `account.account.register` / `.login` / `.resume`（**Request/Response**，不是 Notify/Push），
  服务端回 `LoginReply`（含 token + account_id）并把 pitaya 会话 `Bind` 到 accountID；
  拿到 `LoginReply.ok=true` 之后才允许发 `match.match.join`（`JoinMsg` 是**空消息**，身份来自会话）。
- 客户端本地存 token（`user://auth_token.txt`），下次启动直接 `.resume` 免登录；token 7 天过期、每次 resume 续期，
  一次 `.login` 会**轮换** token 并删掉旧的（同一账号只允许一个活跃会话，顶号）。
- 上行：输入 + 射击 + 重置**合并成一条** `game.game.cmd`（Notify，帧是最小发送单位）；不再有单独的 `input`/`shoot`/`reset` 消息，重置等即时操作也不额外补推，统一等下一 tick 的帧。
- **增量帧**每 tick 推 `onFrame`，只含本帧变化的 `(实体, 属性, 终值)`；同一属性一帧内改多次只发终值，值没变的写入不产生流量（连静态几何也每 tick 写、但不下发）。
- **重连回局**：会话 UID（accountID）就是回局的钥匙 → `match.join` 先向所有 game 节点 fan-out RPC `game.game.rejoin` → 命中则走 `bindPlayer`（写会话数据 `gameServerId` + 推 `onMatched`，与首次匹配同一条收尾路径）→ 客户端收到 `onMatched` 后先清空本地世界、再主动发 `game.game.resync` → 服务端把该槽位的**下一帧**标为全量，单独下发 full 帧（含 Schema）。因为 full 帧是**先清空再整体覆盖**，即使中间先到了几帧增量也会被整帧盖掉——不存在「onMatched 与 full 帧谁先到」的竞态。
- 断线：客户端 1s 重连；**接收看门狗 2.5s 只在匹配后生效**（匹配等待期无帧流，10s 兜底属正常）。
- 服务间通信走 etcd（服务发现）+ NATS（RPC）+ Redis（共享状态：账号、凭证、会话归属、匹配队列），见 `joltgo/deploy/`。

## 5. 约定与坑

- **C 包装层**：函数 `extern "C"`，只用 C 类型/定长数组/opaque 指针，禁止跨边界传 `std::string`/`vector`/C++ 对象/异常。
- **cgo**：显式 `C.float(...)` / `C.uint32_t(...)` 转换；`physics/` 之外的 Go 代码不出现 `import "C"`。
- **pitaya**：框架源码内置在 `joltgo/third_party/pitaya/`（`go.mod` 用 `replace` 指向本地目录），不在其上改业务。**Cluster 模式**需本地 etcd（服务发现）+ nats-server（RPC），共享状态另需 redis-server，见 `deploy/`。协议是 pomelo 帧 + **protobuf** payload（不是 raw JSON 文本帧），客户端编解码在 `fps_client.gd`。改动 proto 后重跑 `protoc --go_out` 重新生成 Go 码。
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
- **Redis 数据目录不清空**：`deploy/start-infra.ps1` 每次都清 `etcd-data`（见下条），但 `redis-data` **永不清空** ——
  里面是账号、密码哈希、凭证；清掉就是把所有玩家账号删光。两者对待方式相反，别把 etcd 的习惯套过去。
- **route 三段式**：`server.service.method`（如 `account.account.login`、`match.match.join`、`game.game.create`）。RPC 调用（`RPCTo`）也必须是三段式，否则报 `no server type chosen for sending RPC`。
- **etcd 租约与残留**：租约只在 etcd 运行期间倒计时，etcd 重启会把上一轮的孤儿租约按 checkpoint 恢复并**重新计时**（v3.5.14 实测：进程已死 + etcd 停机 66s 后重启，注册项仍带 19s TTL 复活；TTL 内反复重启会反复续命）。所以本地 `start-infra.ps1` 每次启动都清空 `etcd-data`（详见 `deploy/README.md`），整套重启后服务列表一定干净；生产则要调小 TTL 并给选节点加健康检查/重试——`GetServersByType` 可能返回已死的旧节点，`match.startMatch` 选中它就会把玩家静默丢出队列。
- **构建**：Jolt 必须经 CMake `add_subdirectory` 编译（保证 NDEBUG/指令集宏与静态库一致）；UCRT64 与 MINGW64 不能混用；`build.ps1` 硬编码 `C:\msys64`；运行需 `joltgo.exe` 与 `libjolt_c.dll` 同目录。
- **Godot 4**：材质属性用 `metallic`（不是 Godot 3 的 `metalness`）；命令行运行用 `preload` 而非 `class_name`；typed for 循环需 4.2+。
- **多进程部署**：单二进制 `joltgo.exe -type gate|account|match|game` 四角色（**只有 `-type` 与 `-redis` 两个 flag**，
  gate 的 WS 端口 8080 写死），先起 etcd + nats + redis（`deploy/start-all.ps1` 一把梭）；服务日志在
  `deploy/gate.log` / `account.log` / `match.log` / `game.log`（pitaya 写 stderr，`*.out.log` 是 stdout 基本为空）。
  `gate` / `account` / `match` 启动时 Ping 一次 Redis，连不上直接退出；`game` 不碰 Redis。
- **多节点正确性**：`match` 的队列在 Redis（`match:queue` ZSET + Lua 原子脚本），所以 match 节点无状态、可横向扩；
  `gate` 在会话绑定时写 `online:{accountID}` → 本节点、断开时清除，account 靠它把顶号踢到**正确的那个 gate**，
  match 靠它找到玩家所在 gate 去写会话数据（`gate.gate.bindgame`）。这个登记是 **best-effort**：Redis 写失败只会漏踢一次，
  不会挡住连接或登录；真正的权威是**凭证轮换**（旧 token 被删，旧连接下一次 resume 必然失败）。
- **同步属性（`sim/replicate.go`）**：加同步字段 = `declareAttributes` 加一行 `Declare` + 在每个变更点 `rep.Set`。**属性表必须在 `Simulation.New()` 里一次声明完整**——`Set`/`Remove`/`Get` 遇到未声明属性直接 panic（`Declare` 重复同名也 panic）；客户端在 full 帧里一次拿到完整属性表，之后靠它解码所有增量，所以不能等首次 `Set` 才登记。
- **就近 `rep.Set` 漏写不会在运行时暴露**：Store 不反查 ECS 世界，漏写只会让客户端**静默停在旧值**（没有报错、没有日志）。唯一能抓住它的是 `sim/replicate_test.go` 的 oracle 测试（`expectedAttrs` 从 ECS 世界独立推期望值，与 store 全量逐项比对）。所以加同步字段时**先补 `rep.Set`、再在 `expectedAttrs` 里补断言**——这是「变更点显式 Set」这套设计的固有代价。
- **full 帧不得修改增量基线**：`Store.Full()` 刻意不碰 `sent`（已下发基线）——全量是发给**单个**客户端（重连 / resync）的消息，其他在线客户端的基线不受影响；又因为所有 op 携带的是**终值**而非相对增量，无论基线如何，客户端都会收敛到同一状态。
- **实体 id 会被复用**：`Store.Destroy` / `Reset` 必须一并清掉已下发基线（`sent`），否则重建后刚体 id 从头复用、新实体的 `Set` 会因「与旧实体值相同」被静默抑制——客户端只收到 destroy、再也收不到重建（`Body.*` 这类只在创建时 Set 一次的属性就永久丢了）。
- **服务端 20Hz tick 无条件运行**（实例创建后无论客户端是否在线都推进）。
- **Git 流程：本仓库直接提交到 `main`**，不要开 feature branch、不要走 PR——`main` 就是集成分支，历史保持线性（`git push origin main` 即可）。提交信息用 `<type>: <subject>` 前缀（`feat` / `fix` / `docs` / `refactor` / `test`），AI 提交在结尾加 `Co-Authored-By` 尾注；提交前先把 §6 里对应的测试跑绿。

## 6. 构建 / 测试命令

```powershell
# 服务端构建（MSYS2 UCRT64 + Go 1.26+）
cd joltgo; .\build.ps1

# 本地起分布式服务端（etcd + nats + redis + gate/account/match/game 四进程）
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
gofmt -l gate account kv online match game physics sim replication ecs
go vet ./gate ./account ./kv ./online ./match ./game ./physics ./sim ./replication ./ecs

# 单元测试（账号/凭证、会话归属、匹配队列、ecs 存储语义 / sim 系统+oracle / replication 存储与帧）
# 注：./account ./online ./match ./gate ./kv 里的 Redis 用例走 miniredis，不需要真 Redis
PATH="$PWD:$PATH" go test -count=1 ./account ./online ./match ./gate ./kv ./ecs ./sim ./replication

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

# 冒烟：需要活集群（etcd + NATS + redis + gate/account/match/game 四进程）
Godot_..._console.exe --headless --path godot_client --script res://tests/login_smoke.gd            # 注册 → LoginReply → 断线 → resume → onMatched
Godot_..._console.exe --headless --path godot_client --script res://tests/ws_smoke.gd               # 登录 + 匹配 + 20Hz 增量帧
Godot_..._console.exe --headless --path godot_client --script res://tests/rejoin_smoke.gd           # 登录 + 断线回同一局
```

> `rejoin_smoke.gd` 是新协议下**唯一**端到端验证「重连回到同一对局」的测试（同一
> match_id + 同一 player_idx + 重连后收到 full 帧），改匹配 / 回局 / resync 链路后必跑；
> 它的断言在载荷解析失败时显式判失败（不会假通过）。`ws_smoke.gd` / `rejoin_smoke.gd`
> 现在都要**先登录再 join**（各自随机注册一个账号）。其余五个回归测试互相独立、无需服务端。

## 7. 变更 runbook（改什么就动哪里）

- 改玩法逻辑 → 只动 `joltgo/sim/`（组件 + 系统 + 调参），跑 `go test ./ecs ./sim`。
- **加/改一个同步字段** → 只动 `joltgo/sim/replicate.go`：`declareAttributes` 加一行 +
  每个变更点 `rep.Set`，再在 `sim/replicate_test.go` 的 `expectedAttrs` 里补一条断言，
  跑 `go test ./sim ./replication`。客户端按属性名读，取新值只需一句
  `_store.attr(id, "名字")` —— **不需要**改 proto、生成码或客户端解码器（§3 第 5 条）。
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
  跑 `go test ./account`，再跑 `login_reply_decode_test.gd` 与（有集群时）`login_smoke.gd`。
- 改**会话归属 / 多节点行为** → 动 `joltgo/online/` + `joltgo/gate/session.go`（+ 调用方 `account/` `match/`），
  跑 `go test ./online ./gate ./account ./match`。
- 改客户端渲染/输入 → 只动 `godot_client/`，Godot 直接 F5。

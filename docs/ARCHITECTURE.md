# 架构说明

## 分层（分布式微服务）

```text
┌──────────────────────────────────────────────┐
│  Godot 客户端（godot_client/）                │
│  main.gd      输入/相机/双玩家渲染/HUD/登录面板│
│  fps_client.gd  传输层：pomelo 握手/登录/编解码│
│  body_entity.gd 每个刚体一个渲染节点          │
└──────────────────────┬───────────────────────┘
                       │ WebSocket（pomelo 帧 + protobuf payload）
┌──────────────────────▼───────────────────────┐
│  gate（frontend）                             │
│    pitaya acceptor / agent / session          │
│    gate.go：AddRoute 路由                     │
│      account.account.* → account 节点（轮询） │
│      match.match.join → match 节点（轮询）    │
│      game.game.* → 定点 game 节点（会话数据） │
│    session.go：会话绑定/断开 → 写/清在线登记  │
│      + remote gate.gate.bindgame（供 match 调）│
└───┬──────────┬──────────────────┬────────────┘
    │ NATS RPC │ NATS RPC         │ NATS RPC
┌───▼────────┐ │            ┌─────▼────────────────────┐
│ account    │ │            │  game（backend）          │
│ (backend)  │ │            │  实例注册表（uid→Instance）│
│ 注册/登录/ │ │            │  instance.go：★每局一 goroutine│
│ resume     │ │            │   顺序执行、无锁          │
│ bcrypt+token│ │           │  sim/ ECS 模拟层（双玩家）│
└─────┬──────┘ │            │  ecs/ ECS 核心            │
      │  ┌─────▼──────────┐ │  physics/ cgo 物理桥      │
      │  │ match（backend）│ └──────────┬───────────────┘
      │  │ 队列在 Redis    │            │ cgo
      │  │ 分配 game 节点  │ ┌──────────▼───────────────┐
      │  │ RPC game.create │ │  C 包装层（wrapper/）      │
      │  └─────┬──────────┘ │  纯物理桥（双角色）        │
      │        │            └──────────┬───────────────┘
┌─────▼────────▼─────┐                 │ C++ API
│  Redis（共享状态）  │      ┌──────────▼───────────────┐
│  账号 / 凭证        │      │  Jolt Physics（libjolt_c.dll）│
│  会话归属 / 匹配队列│      └──────────────────────────┘
└────────────────────┘
```

四个服务由**单二进制** `joltgo.exe` 用 `-type gate|account|match|game` 区分角色，经
**etcd**（服务发现）+ **NATS**（RPC）互相通信，共享状态放 **Redis**（`-redis`，默认
`localhost:6379`），见 `joltgo/deploy/`。`game` 是纯计算节点，不连 Redis。

## 对局实例模型（★ 无锁核心）

`game/instance.go` 是本次架构的核心：**每个对局一个 goroutine，内部顺序执行、不加锁**。

- 一个匹配（matchId）对应一个 `Instance`，构造时 `sim.New(physics.New())` 各建一个
  独立 Jolt 世界（互不共享）。
- `Instance.run()` 是唯一访问 sim 的 goroutine：一个 select 循环消费
  - **命令 channel**（`cmds`）：输入/射击/重置，由 RPC handler 经 `enqueue` 投递；
  - **20 Hz ticker**：`sim.Step()` + 广播同步帧（多数槽位发增量，待全量槽位发 full）；
  - **stop**：退出并释放物理世界。
- 因为只有这一条 goroutine 访问 sim，`sim.Simulation` **去掉了 `sync.Mutex`**——并发
  安全由「单线程所有」这一模型保证，输入命令在 channel 上自然串行化。
- `game.Component` 的实例注册表（uid → Instance）由 RPC handler 跨 goroutine 共享，
  用一把 `sync.Mutex` 保护（这只是注册表查找，不是游戏逻辑）。

## 服务端 ECS 架构

服务端游戏逻辑用「实体-组件-系统」（ECS）组织，核心在 `ecs/`（零依赖、可单测），
玩法层在 `sim/`：

- **实体（Entity）**：只是 `uint32` ID，由 Go 桥（`physics/physics.go`）从 1 递增发放，
  桥层维护实体 id ↔ Jolt BodyID 的双向映射（Jolt 原生 BodyID 会与 ECS 的
  InvalidEntity 约定冲突，不能直接透传）；纯逻辑实体（玩家）由 ECS 世界
  从 `1<<24` 起分配，两个 ID 空间不重叠。销毁的逻辑实体 id 进入回收池供
  `NewEntity` 复用，销毁行不在空 archetype 里累积
- **组件（Component）**：纯数据，按 archetype（原型）存储——组件集合（类型集）
  相同的实体归入同一 archetype，组件数据按列（SoA）连续存放、行号即实体下标；
  组件类型注册为整数 ComponentID，列按下标索引（热路径无 reflect.Type 哈希
  查找），archetype 按组件 ID 序列键 O(1) 查找。
  Add/Remove 组件时实体整体搬到目标 archetype（公共列复制、目标列补零、源行
  swap-remove，代价 O(组件数)）；`Add2/Add3/Add4` 批量挂载只搬一次家、不产生
  中间 archetype（spawn 路径用）。ecs 核心另提供按 archetype 粒度的缓存查询
  （`Without[U]` 追加排除、`QueryEach2/3/4` 多列行内直取、`RowHas`/`RowGet` 行视图），
  由 `ecs` 自己的测试与基准覆盖；但**玩法层现在没有生产调用点**——旧的快照路径
  `Simulation.snapshot` 已随实体-属性同步改造删除，同步改由 `replication/` 的终值表
  驱动，客户端按属性名取值。查询族仍留在 ecs 核心中备用。
  规模模拟（`ecs/scale_test.go`）验证到 100+ archetype、10000 实体：
  `Position` / `Rotation` / `Body`（形状/尺寸/静态/活跃/材质，同步给客户端的渲染元数据）、
  `Player` / `Input` / `Health` / `PlayerScore` / `PlayerHitbox`、
  `Projectile`（命中盒是**纯物理装置**：有 ECS 实体、没有 `Body` 组件）
- **系统（System）**：每 tick 按固定顺序运行，只通过组件和 `Physics` 接口交互：
  输入 → 命中盒跟随 → 物理步进 → 变换同步 → 弹丸命中 → 弹丸过期 → 对局结算。
  碰撞判定全部走物理层（弹丸用刚体接触事件），不做距离计算
- **物理抽象**：`sim.Physics` 接口隔离 Jolt cgo 调用；`physics/` 包
  是唯一允许 cgo 的包。因此 `sim` 层用 fake 物理即可单元测试，
  系统行为不依赖真实物理引擎。包装层本身不含任何业务（见下），
  所有游戏调参（重力/跳跃/胶囊尺寸/弹丸配置/对局规则）都在 `sim/` 常量区

### 同步协议（实体-属性帧）

服务端不再手写快照，而是把「给客户端同步什么」从 ECS 里**完全拆出来**，做成独立包
`replication/`（不 import `ecs`）；`sim/replicate.go` 是唯一知道「组件 ↔ 属性」映射的地方。
玩法层在每个**变更点就近**调用 `rep.Set(实体, 属性, 终值)`，同步层只负责去重与打包：

```text
sim/ 各系统（变更点）──rep.Set(实体, 属性, 终值)──▶ replication.Store
                                                     ├ values : [实体][属性] = 终值
                                                     ├ dirty  : 本帧脏集
                                                     └ dead/gone : 销毁 / 移除
每 tick：Drain() ─增量帧─▶ 局内多数槽位        Full() ─全量帧(带 Schema)─▶ 重连 / resync 槽位
```

- **属性表（Schema）**：属性名是扁平字符串（约定 `组件.字段`，如 `Body.Mat`），在
  `sim.Simulation.New()` 里由 `declareAttributes` **一次性声明**（当前 14 个，清单见
  [API.md](API.md)）。Schema **只随 full 帧下发**（full 帧自带一份，避免「schema 与全量帧
  分两条消息、顺序可能颠倒」的竞态）；`version` 是属性表（名字 + Kind）的 FNV-1a 哈希，
  随 Schema 一起携带、**仅供诊断**，客户端**不比对**它——属性表有差异也不会崩（不认识
  的属性会被忽略），比对没有意义。**新增一个属性不需要改 proto / 重生成 Go 码 / 改
  客户端解码** —— 客户端只按属性名取值，不认识的属性照常存下、只是不渲染。
- **值类型**：`KindF32` / `KindI32` / `KindBool` / `KindStr` / `KindVec2` / `KindVec3` /
  `KindVec4`；wire 上按族塞进 `AttrValue` 的 `f` / `i` / `b` / `s`。以后加一个 int 属性，
  协议一个字都不用动。类型与 `Declare` 时声明的不符会 panic（防「写错属性名或值的
  构造器」导致客户端按错误 Kind 解码）。
- **增量 = 终值 + 脏集**：`Set` 与「上一次下发给客户端的值」比较，不同才标脏；同一帧内
  改多次只留终值（A→B→A 则撤销脏标记，不产生流量）。因此 `syncSystem` 每 tick 给全部
  刚体（含永不变化的静态船体）写 `Pos`/`Rot` 也不会产生任何流量。`Drain()` 每 tick
  **恰好调用一次**——它负责清脏并推进「已下发基线」。
- **全量 = 终值表整表**：`Full()` 与 `Drain()` 产出**同一个 `Frame` 类型、同一套编码**，
  区别只有 `full=true` + 携带 Schema。「把断线期间的所有帧补上」在这里的形态就是把净效果
  压缩成终值表一次性下发。`Full()` **刻意不修改增量基线**（全量只发给单个客户端）。
- **属性存在性**：缺省即不存在。标记属性（`Projectile`）就是值恒为
  `Bool(true)` 的属性 —— 存在即有该组件；`removed` / `destroy` 是属性存在性的终点。
- **实体 id 会被复用**：`Destroy` / `Reset` 必须把已下发基线（`sent`）一并清掉，否则重建后
  刚体 id 从头复用、新实体的 `Set` 会因「与旧实体值相同」被静默抑制 —— 客户端只收到
  destroy、再也收不到重建（`Body.*` 这类只在创建时 Set 一次的属性就永久丢了）。
- **就近 `Set` 的风险**：漏写一处 `rep.Set` 不会报错、没有日志，客户端只会静默停在旧值
  （store 不反查 ECS 世界）。这是「变更时显式 Set」换 O(变化量) 的固有代价，由
  `sim/replicate_test.go` 的 oracle 测试兜底（`expectedAttrs` 从 ECS 世界独立推期望值，
  与 store 全量逐项比对）——加同步字段时先补 `rep.Set`、再补断言。
- **身份与同步的关系**：同步帧不含任何身份信息，客户端是谁完全由**会话 UID**（= 账号 ID）
  决定，见下文「账号与会话」。安全取舍也集中在那里，不在同步层。

### 每 tick 只同步一次变换

旧实现里每个系统各自枚举 C++ 刚体（构建同步帧、伤害判定、计数、命中查找，
每 tick 多达 4~5 遍全量扫描）。重构后 `syncSystem` 每 tick 只枚举一次，
把位置/旋转/活跃状态写回组件并就近 `rep.Set`，其余系统全部读写 Go 侧组件。
顺序语义与旧实现完全一致：伤害判定与下发的同步帧读的都是**本 tick 步进后**的位置
（命中盒跟随则必须在步进**之前**，否则弹丸拿的是上一 tick 的旧位置）。

`syncSystem` 跳过没有 `Body` 组件的刚体（玩家命中盒）：它只在物理世界里存在，
不是可渲染实体。
- 两次 tick 之间创建的实体（如 RPC handler 投递的射击命令）在创建时就写入已知的
  初始位置，立即出现在同步帧里，不会闪现在原点

### 命中结算与物理桥的职责边界

- 包装层是**纯物理桥**：只暴露 Jolt 原生能力（世界/刚体/角色控制器/接触事件/
  射线/刚体瞬移/角色忽略表），不携带任何游戏业务概念——没有玩家/弹丸/血量，没有角色
  移动策略与调参，接触事件原样上报所有刚体对
- 血量是 Go 侧 `Health` 组件；弹丸命中由弹丸系统从通用接触流中筛选（至少一侧带
  `Projectile` 组件，另一侧是玩家的 `PlayerHitbox`）
- Go 桥（`physics/physics.go`）负责 id 翻译：Jolt 原生 BodyID 透传会与 ECS 的
  InvalidEntity（0）约定冲突，桥层维护双向映射，向 sim 发放从 1 递增的实体 id
- **同步层与 ECS 解耦**：旧实现里 `bodyInfo` 的每个字段都要在这里由组件重建（`type` ←
  `Body.Kind`、`health` ← `Health`）再整体下发；
  现在改成通用属性名，客户端**按名字**取值（`Body.Kind` / `Projectile` / `Pos` /
  `Rot`…）。`replication/` 是独立包、不 import `ecs`，`sim/replicate.go` 是唯一知道
  「ECS 组件 ↔ 属性名」映射的地方 —— 详见上文「同步协议（实体-属性帧）」。

## 为什么用 cgo + C ABI

cgo 只能直接调用 C ABI，不能调用 C++ 类/重载/模板。因此所有 Jolt C++ 调用都被
封装成 `extern "C"` 的普通函数，参数和返回值只用：

- 整数 / 浮点数
- 定长数组（`float pos[3]` 等）
- 不透明指针（`JoltWorld *`）

禁止跨边界传递 `std::string`、`std::vector`、C++ 对象、引用或异常。

包装层是**纯物理桥**：函数只对应 Jolt 原生能力（`jolt_add_sphere` /
`jolt_set_body_velocity` / `jolt_poll_contacts` / `jolt_character_update` 等），
不带任何游戏语义。玩家/弹丸/血量、角色移动策略、所有数值调参都属于
Go 侧（sim）；包装层内部只保留两个物理层自身的状态：刚体 id 登记表（供枚举）
与接触事件队列（线程安全转发）。

## 数据流

游戏状态由服务端持有，客户端是「输入 + 展示」的瘦客户端，模拟节奏由服务端驱动：

1. 客户端连接 gate（WS）→ pomelo 握手 → **先登录**：发 `account.account.register` /
   `.login` / `.resume`（Request/Response）拿到 `LoginReply`，会话被 `Bind` 到账号 ID；
   然后才发 `match.match.join`（`JoinMsg` 是空消息，身份取自会话）进入匹配。
2. match 服务配对（2 人，或 10s 兜底单人）→ `GetServersByType("game")` 挑一个 game
   节点 → `RPCTo("game.game.create")` 让该节点创建对局实例。
3. game 节点的实例 goroutine 以固定 **20 Hz** tick 推进：消费最新输入 → 更新两个
   角色控制器 → 命中盒跟随 → 步进物理 → 处理弹丸命中与对局结算。
4. 每个 tick 结束，实例把本帧**增量**（route `onFrame`，只含变化的 (实体, 属性, 终值)）
   经 `SendPushToUsers` 通过 NATS 转发给 gate，gate 再推给局内客户端；被标记为待全量的
   槽位（重连 / resync）这一 tick 改推 **full 帧**（终值表整表 + Schema）。射击 / 重置
   不再单独补推，统一等下一 tick 的帧。
5. 客户端把增量累积进本地 `WorldStore`，60 Hz 渲染时对运动刚体和玩家位置做**影子跟随
   插值**（位置 lerp、四元数 slerp、玩家朝向 lerp_angle），让 20 Hz 数据在 60 Hz 屏幕上
   平滑。服务端不会重复推送同一 tick，客户端也不再靠快照 diff 推断「谁消失了」。
6. 客户端每渲染帧上报一条 `game.game.cmd`（输入 + 射击 + 重置**合并成一条**），
   由 gate 定点路由到托管该对局的 game 节点。
7. 连接断开后客户端每秒自动重连：重新握手 → 用本地保存的凭证 `account.account.resume`
   （会话重新 `Bind` 到**同一个账号 ID**）→ 发 `match.match.join`；
   match 先向各 game 节点 fan-out `game.rejoin`，命中存量实例则走与首次匹配相同的收尾
   （写会话数据 + 推 `onMatched`），客户端回到**同一对局、同一槽位**、不入匹配队列；
   客户端随后发 `game.resync` 请求 full 帧把本地世界整体重建（未命中则按新玩家重新匹配）。

这种「服务端权威 + 固定 tick + 推送 + 客户端插值」让物理/游戏逻辑只存在于一处，
模拟快慢与客户端数量/帧率无关，客户端换引擎也不影响逻辑。

### 账号与会话

客户端不再自带身份（旧版是首次运行生成一个 UUID 存在本地，谁拿到谁就是谁）。
现在身份由 `account` 服务签发，三条路都以同一个 `LoginReply` 收尾：

```text
register(username, password) ─▶ 校验格式 → 限流 → bcrypt 哈希 → 占名 → 建账号 ─┐
login(username, password)    ─▶ 限流 → 查名 → bcrypt 校验 ────────────────────┤
resume(token)                ─▶ 解析 token（Redis sess:{token}）─────────────┤
                                                                             ▼
                                          finishLogin：记下旧 gate → 轮换 token
                                          → session.Bind(accountID) → 定点踢旧连接
                                          → LoginReply{ok, token, username, account_id}
```

- **传输是 Request/Response，不是 Notify/Push**。这不是风格选择：pitaya 的 NATS 后端 agent
  在 uid 未绑定时 `Push` 直接返回 `ErrNoUIDBind`，而登录**失败**时 uid 恰恰是空的，
  结果根本推不回去。pitaya 靠 handler 有没有返回值判定类型（有返回值 = Request），
  所以三个 handler 都返回 `(*protos.LoginReply, error)`。这是本项目第一处用 Request/Response。
- **会话 UID = accountID**。这一条是「`game.rejoin` 为何零改动」的全部答案：
  `game.Component` 的实例注册表、`SendPushToUsers` 的目标、`match` 的回局 fan-out，
  历来都按会话 UID 索引，只是那个字符串以前来自客户端、现在来自账号表。
  `RejoinMsg.token` 这个 wire 字段名是历史遗留，它装的是 accountID（改名要动 proto 与客户端，
  收益为零）。
- **凭证形态**：32 字节 `crypto/rand` + base64url（43 字符），存 `sess:{token}` → accountID，
  反向 `sess:acct:{id}` → token，TTL 7 天、每次 resume 两边一起续期。
  一次 `login`/`register` 会**轮换** token 并在同一个事务里删掉旧的 —— 一个账号只有一个活凭证。
- **密码**：bcrypt DefaultCost（约 50 ms，这也是为什么限流必须在校验之前）。用户名
  `^[a-zA-Z0-9_]{3,16}$`，占名与查名都按小写归一，密码 6–64 字节。
  「用户名不存在」与「密码错误」返回同一个 `bad_credentials`，不泄露账号是否存在。
- **已知安全取舍**（诚实说明，本 demo 不做）：token 是 **bearer 凭证** —— 拿到即可使用，
  服务端无法区分持有者。当前是明文 WS + 7 天 TTL，生产环境应上 TLS（否则凭证在链路上裸奔）
  并把 TTL 缩到分钟级 + 配一条刷新路径。
- **限流按用户名，不按 IP**（`rl:user:{name}`，1 分钟 10 次）。不是偷懒：account 是 backend，
  它拿到的 pitaya agent 是 `Remote`，`RemoteAddr()` 返回 nil —— 它**看不见客户端 IP**。
  要按 IP 限流得让 gate 把地址透传进来，那是另一层改造。

### 多节点正确性

每个角色都可以起多份。哪些状态是节点本地的、因此必须靠路由或共享存储兜住：

| 角色 | 节点本地状态 | 共享状态（Redis） | 多节点怎么正确 |
| --- | --- | --- | --- |
| gate | pitaya session / agent（就是那条 TCP 连接，天然本地） | `online:{accountID}` → 本节点 id | 连接在哪就是哪；别人要找它靠在线登记 |
| account | 无 | `acct:*`、`sess:*`、`rl:user:*` | 完全无状态，随便扩 |
| match | 只有 10s 兜底的 ticker | `match:queue`（ZSET + Lua 原子脚本） | 队列在 Redis，两个 match 节点能互相配对 |
| game | **对局实例**（`uid→Instance`、`matchId→Instance`），有状态且不可迁移 | 无（game 不连 Redis） | 靠 gate 会话数据里的 `gameServerId` 定点路由 |

三个机制把它们串起来：

- **在线登记**：gate 在 `OnAfterSessionBind` 写 `online:{accountID}` → 本节点 id，
  `OnSessionClose` 清除（TTL 24h 兜底）。account 靠它把顶号踢到**正确的那个 gate**
  （`gate.sys.kick`）；match 靠它找到玩家所在的 gate，请那个 gate 写会话数据
  （remote `gate.gate.bindgame`，顺带探活）。
- **定点踢 + 凭证轮换**：`finishLogin` 的顺序是「先记下旧 gate → 轮换 token → Bind → 再踢」。
  `Bind` 本身会关掉**同一个 gate 上**的旧会话（框架的 `sessionsByUID`），所以那一脚只用来处理
  「旧会话在另一个 gate」，且绝不会打到自己。
  **权威是凭证轮换，不是踢**：旧 token 在 Bind 之前就已经从 Redis 删掉了，旧客户端断线后
  resume 必然失败。踢只是让它早点闭嘴。因此在线登记是 **best-effort** —— Redis 读写失败
  最多漏踢一次（旧连接多活一会儿），既不会挡住新连接，也不会让顶号失效。
  读不到当前归属时代码宁可**不踢**：误踢会打掉刚建立的会话，而此时凭证已经轮换，
  那个客户端连 resume 都回不来。
- **推送双发窗口（明确不修）**：从旧连接被踢到它真正关闭之间有个短窗口，同一账号在两个 gate
  上都有会话，pitaya 的 NATS 用户频道没有 queue group，两个 gate 都会收到同一份 push、
  各自推给自己那条连接。加 queue group 能消掉双发，但代价是**两个客户端各收一半帧流** ——
  那是彻底坏掉，比多发一份糟得多。所以这里保留双发：旧连接反正马上就断，
  而它期间收到的帧只是被丢弃。

## 核心机制

### 场景地图（运输船）

参考穿越火线「运输船」的一艘长甲板货船，坐标 x 为船宽（±13）、z 为船长
（±22）、甲板面 y = 0。布局全部写在 `sim/map.go` 的**部件表**里，`Simulation.init`
照表搭物理刚体：

- **船体**：甲板钢板 + 左右舷外板 + 艏艉横舱壁（围板的内侧面落在甲板范围内，
  不留可掉出去的缝；高度高过跳跃高度）
- **艏艉两个出生区**：玩家分别出生在 z = +18 / -18，开局面朝船中，
  出生区前方有掩体墙（中间留中央通道、两侧留舷侧绕后路线）
- **中部集装箱堆**：1.4 m → 2.4 m 的两级台阶，靠跳跃（跳跃高度约 1.8 m）就能爬上去
- **两舷高架走道**：面高 2.4 m、通长 26 m，各有一段舷梯上去，走道外侧有栏杆
  （内侧敞开便于跳下甲板）
- **甲板掩体**：舷侧长集装箱、木箱（动态）、烟囱、桅杆、系缆桩

两条不变量由代码与测试共同保证：

- **180° 旋转自映射**：部件表里带 `mirror` 的部件会自动补上 `(x,y,z) → (-x,y,-z)`
  的孪生体，改图只写半边；`sim/map_test.go` 逐件验证对称性，两个出生点因而完全等价
- **出生/复活点与命中盒不卡进掩体**：`mapBlocks(x,y,z,margin)` 是纯 Go 的几何判定
  （不依赖物理引擎，可单测），`map_test.go` 用它验证出生点（也就是死亡后的复活点）
  与开局命中盒都在干净的甲板上——命中盒埋进集装箱意味着「人站在明处却打不中」

舷梯参数有硬约束：单级抬升 ≤ 0.4 m（Jolt `ExtendedUpdateSettings::mWalkStairsStepUp`）、
踏步进深 ≥ 0.5 m（必须大于角色半径 0.4 m，否则 `WalkStairs` 抬腿后会被再下一级绊住，
角色永远卡在最低一级）。这两条写进了 `map_test.go`，并有跑真引擎的
集成测试 `physics/map_integration_test.go`（`go test -tags joltdll ./physics`）
真的把角色从出生点走到走道上，防止改参数改坏手感。

同步属性 `Body.Mat` 给每个刚体一个材质号（`sim/map.go` 的 `Material`），客户端
（`body_entity.gd` 的 `MATS` 表）据此配色：甲板/船体/四色集装箱/走道格栅/栏杆/
木箱/钢构件/踏步。它只是配色提示，不参与任何物理或玩法判定；客户端遇到不认识的
材质号会退回默认配色，因此新增材质号向后兼容。

### 玩家角色控制器

- 每局两个玩家，各用一个 Jolt `CharacterVirtual`（形状是「胶囊 + 向上平移」，charIdx
  0/1 区分）：
  - 胶囊半径 0.4 m，圆柱半高 0.5 m，总高 1.8 m
  - 用 `RotatedTranslatedShape` 上移 0.9 m，使形状底部位于脚底（`GetPosition()` 即脚底位置）
  - 形状、出生点（两人分别出生在船的艏/艉）、重力、跳跃速度全部是 Go 侧（sim）的调参，
    通过 `jolt_character_create` 与 `jolt_set_gravity` 传入；包装层不携带任何角色参数
- 每 tick 由输入系统（`sim/inputSystem`）遍历两个玩家实现移动策略后交给物理层执行：
  - 水平速度直接来自各自客户端输入（走 8 m/s，跑 14 m/s）
  - 垂直速度：着地时归零、跳跃设 8.5 m/s、再叠加重力（-20 m/s²，比真实重力大，
    跳起/落地更快、手感更利落）
  - `ExtendedUpdate`（`jolt_character_update`）负责碰撞、贴地、上台阶
- 角色不可被推动/挤开：这是游戏规则，由 sim 通过 `SetCharacterDynamicPush(false)`
  配置——包装层只暴露「动态刚体能否推动角色」这个物理级开关
  （对应 Jolt `CharacterContactSettings::mCanPushCharacter`）。
  禁用后动态刚体通过接触冲量（`mCanReceiveImpulses`）被挡开，玩家照常能推箱子；
  静态几何保持默认，不影响贴地与上台阶
- 角色与本局的所有**命中盒**都互不接触（`jolt_character_ignore_body` → Jolt
  `CharacterContactListener::OnContactValidate` 返回 false）。它只对弹丸有意义，
  对角色必须完全不存在（理由见上文「玩家命中盒」）
- 客户端只保留偏航/俯仰（鼠标视角），相机位置 = 服务端返回的本地玩家脚底位置 + 1.6 m 眼高

### 弹丸

- 客户端下发 `shoot` 消息 → 服务端生成一枚小球（物理配置全部由 sim 设置）：
  - 半径 0.08 m，初速 60 m/s
  - `EMotionQuality::LinearCast`（`jolt_set_body_motion_quality`），高速下不会穿透薄墙
  - 摩擦/弹性置 0；「这是弹丸」是 Go 侧 `Projectile` 组件，物理层不区分
- 命中判定依赖 `PhysicsSystem` 的 `ContactListener`：
  - 包装层把**所有**刚体接触对原样写入受互斥锁保护的队列（纯物理事实，
    不区分弹丸/箱子/玩家命中盒）
  - 每 tick 结束后服务端 `jolt_poll_contacts` 排空队列，弹丸系统挑出至少一侧
    带 `Projectile` 组件的接触对进行结算，其余（箱子落地等）忽略
- 命中后处理：命中**别人的**玩家命中盒 → 扣血、血尽结算击杀；命中其他 → 移除弹丸
- 弹丸最多存活 60 tick（20 Hz 下 3 秒），超时自动移除

### 玩家命中盒（弹丸怎么才能打中人）

玩家本体是 Jolt 的**角色控制器**（`CharacterVirtual`），不是刚体。这带来一个必须
正面解决的问题：**弹丸是动态刚体，它看不见角色**。

三条看似可行的路都走不通：

- 刚体接触监听器（`ContactListener`）只报刚体对，角色不在其中；
- 角色自己的接触监听器只在角色 `Update` 时做一次重叠/扫掠查询，而弹丸每 tick 走
  3 m（60 m/s ÷ 20 Hz），远超角色约 1 m 的检测窗口 —— 会直接穿过去；
- 「把玩家做成传感器」也不行：Jolt 的 CCD 明确忽略传感器
  （`PhysicsSystem.cpp`：`// TODO: For now we ignore sensors`），而本项目弹丸必然是
  CCD 体质（每 tick 3 m ≫ 0.06 m 的线性投射阈值）。

所以每个玩家额外配一个**命中盒**：一个与角色胶囊同形状的**静态胶囊刚体**
（半高 0.5、半径 0.4，中心在脚底 + 0.9）。`hitboxFollowSystem` 每 tick 在物理步进
**之前**把它瞬移到角色所在处（`jolt_set_body_position`），弹丸就用「动态球 vs 静态
胶囊」这条最普通的接触路径命中它 —— 与打中场景几何走的是同一条路。

两处配套的物理能力（都在包装层，且都不含业务概念）：

- `jolt_set_body_position`：把刚体瞬移到指定位置（命中盒跟随）
- `jolt_character_ignore_body`：让角色**彻底忽略**某个刚体。这一条不可省：命中盒每
  tick 才跟随一次，角色一 tick 的位移（跑动 0.7 m，下落更快）可能超过两者 0.8 m 的
  接触距离 —— 那一 tick 命中盒就落在角色**正前方**，Jolt 会把角色速度清零；而对方
  玩家的命中盒对本地角色更是一堵隐形墙。两个角色都要忽略**两个**命中盒。
  （实测细节：**共位**的静态刚体本身并不挡人 —— `CharacterVirtual` 的扫掠会忽略
  fraction = 0 的初始重叠。这条反直觉结论由
  `physics/pvp_hit_integration_test.go` 的对照组钉住。）

命中盒不进渲染路径：它没有 `Body` 组件，因此不产生任何同步流量、客户端也不会渲染它
（`syncSystem` 跳过没有 `Body` 的刚体）。

**自伤**：命中盒是实体刚体、会挡住弹丸，而第三人称的枪口正好在角色中轴上 —— 弹丸
会生成在盒内被卡住。所以开火时服务端把出生点沿射向挪到自己的盒外
（`pushOutsideOwnHitbox`，第一人称的枪口本就在盒外、不会被挪动）；同时对「弹丸 vs
自己的命中盒」的接触直接忽略（不摧毁弹丸），作双保险。

### 对局结算

- 击杀数先到 `killTarget`（10）即分出胜负，写入全局单例的 `Game.Winner`
- 分出胜负 5 秒后 `matchSystem` 直接调 `Reset()` 重开一局 —— 整张场景（被推乱的
  箱子、还在飞的弹丸）都要复位，重建比「只清计数」多不了几行，但不会留下残局
- 被击杀者**立即**满血回己方出生点：血量、位置、命中盒三者都在同一个 tick 内写齐。
  命中盒尤其不能漏 —— 它要到下一 tick 的 `hitboxFollowSystem` 才跟随角色，留在
  死亡点的话，之后飞来的弹丸还会打中一个「已经复活在别处的人」

## 关键设计决策与坑

### Jolt 必须和包装层用同一套编译参数

Jolt 的 Release 构建定义 `NDEBUG`、`JPH_DEBUG_RENDERER`、`JPH_PROFILE_ENABLED`
以及 CPU 指令集宏（`JPH_USE_AVX2` 等）。如果包装层自己手写编译命令、漏掉这些宏，
会出现两类问题：

- 缺 `NDEBUG` → `JPH_DEBUG` 被开启 → 引用 `JPH::AssertFailed` 但库内没定义 → 链接失败
- 指令集宏不一致 → 内联函数 ABI 不一致 → 链接或运行期错误

因此 `joltgo/CMakeLists.txt` 用 `add_subdirectory` 把 Jolt 作为子项目引入，
编译宏和指令集标志自动继承，避免手工维护。

### DLL 静态链接 C++ 运行库

`libjolt_c.dll` 编译时加了 `-static`，把 libstdc++ / libgcc / winpthread 静态链入，
所以运行时只需 `libjolt_c.dll` 一个文件，不需要 MSYS2 的 DLL 伴生。

### UCRT64 与 MINGW64 不能混用

项目统一使用 UCRT64 工具链（`C:\msys64\ucrt64\bin`）。`build.ps1` 会在编译包装层时
把 PATH 切到 `/ucrt64/bin`，并在 `go build` 时显式设置 `CC`/`CXX` 指向 UCRT64 编译器。
混用 MINGW64 会因 C 运行时不同（ucrtbase vs msvcrt）导致链接或运行异常。

### 服务端锁与广播（分布式模型）

- `sim.Simulation` **无锁**：由对局实例 goroutine（`game/instance.go`）独占驱动。
  输入/射击/重置经命令 channel 投递，同一 goroutine 顺序消费；20 Hz tick 也在同一
  goroutine 里，因此不需要任何互斥。并发安全由「单线程所有」模型保证。
- `ecs.World` 自身不加锁，由实例 goroutine 串行化。
- `game.Component` 的实例注册表（`uid → Instance`、`matchId → Instance`）跨 RPC
  handler 共享，用一把 `sync.Mutex` 保护——这只是注册表查找，不含游戏逻辑。
- 广播：实例每 tick 先 `sim.DrainFrame()`（**恰好一次**，它负责清脏并推进已下发基线），
  再按槽位分发 —— 多数槽位收增量帧，`pendingFull` 槽位（重连 / resync）收 `FullFrame()`。
  两路都调 `app.SendPushToUsers("onFrame", frame, uids, "gate")`，pitaya 经 NATS 用户
  频道转给 gate，gate 再推给对应会话。每个对局的 uids 在 `game.create` 时固定，实例
  退出即停止广播。
- 实例自退：所有槽位 60s 无上行消息即结束（`instanceIdleTimeout`，远大于客户端 1s 重连 +
  2.5s 看门狗）；`forget` 只在 `uidToInst[uid]` 仍指向该实例时才摘除映射 —— 玩家可能已经
  匹配进新对局，无脑删会把新对局的 uid 映射一起抹掉。
- 慢客户端：pitaya agent 每连接一个写者 + 有界发送缓冲，写不出去则断开连接，
  不会拖慢 20 Hz 模拟。
- 服务间通信：etcd（服务发现，60s 租约）+ NATS（RPC）+ Redis（共享状态）。route 三段式
  `server.service.method`（`account.account.login` / `match.match.join` / `game.game.cmd` /
  `game.game.resync`）；服务间 RPC 同样三段式（`game.game.create` / `game.game.rejoin` /
  `gate.gate.bindgame` / `gate.sys.kick`）。

### 客户端插值

- 世界状态在 `WorldStore`（`world_store.gd`）里按需累积：实体 ID → { 属性名: 值 }，
  按**名字**取值，不认识的新属性照常存下、只是不渲染。full 帧先清空再整体覆盖，
  因此「首次进入 / 重连 / 乱序」在应用层没有区别。
- 插值状态由渲染层自己持有（`main.gd`）：保留每个刚体的**上一帧变换**作为插值起点
  （`_prev_body_xform`，`id → {pos, quat}`），收到新帧时把当前变换整份拷进去（`_body_xform`），
  渲染时在「上一帧 → 本帧」之间按 alpha = 距本帧到达时间 / 0.05s（`TICK`）做 lerp / slerp
  （渲染滞后一个 tick）。
- 玩家位置与远端朝向同样是 prev→target 插值（`_prev_player_pos` / `_player_pos_target`…），
  显示值与服务端下发值**分开存**：`_render_interpolated` 一帧内会被调两次（`_process`
  一次、`_on_frame` 收尾一次），显示值不能同时充当插值输入（否则第二次会拿结果再插一次）。
- 旧实现的双缓冲（`_prev_snap` / `_next_snap`）与 `_snap_sig` / `_frame_keeps_bodies`
  去重逻辑**整块删除**：增量协议下服务端不会重复推送，也不需要靠快照 diff 推断「谁消失了」
  —— 消失由 `destroy` / `removed` op 显式下发，销毁事件还带着消失前的属性快照，渲染层
  据此做弹丸爆闪与命中音。
- 新生成 / 移除的刚体不参与插值：按最新帧直接创建或删除（弹丸消失有爆闪特效兜底）。
- 双玩家：按属性 `Player.Idx` 找槽位；本地玩家（player_idx）第一人称视角，
  远端玩家渲染 avatar。
- `FpsClient`（传输层）与渲染层通过信号解耦（`frame_received` / `matched_received` /
  `connection_changed`）。

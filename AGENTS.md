# AGENTS.md

> AI 引导文件：让任何 AI / agent 在最小上下文下快速建立正确心智模型。
> 读本文件后，按需深入 `docs/ARCHITECTURE.md`、`docs/API.md`、`docs/BUILD.md`、`docs/DEVELOPMENT.md`。
> **改动代码时同步更新对应文档与本文件**（过期文档比没有更糟）。

## 1. 项目一句话定位

基于 [Jolt Physics](https://github.com/jrouwe/JoltPhysics) 的服务端权威第一人称 PVE demo：
- **服务端** `joltgo/`：Go + cgo 调用 Jolt（C ABI 包装层），**pitaya 框架**（内置源码，
  **Cluster 模式**）做**分布式微服务**：gate（前端接入）/ match（匹配）/ game（对局逻辑）。
  ECS 架构，20 Hz 固定 tick，WebSocket 推送**实体-属性增量帧**（重连 / 首次进入推全量帧）。
- **客户端** `godot_client/`：Godot 4.7（gl_compatibility），瘦客户端，60 Hz 渲染 + 实体-属性增量累积 + 影子跟随插值，纯程序化美术/音效。
- **JoltPhysics/** 是 gitignored 第三方依赖（需单独 `git clone`），不属于本仓库代码。

## 2. 目录与入口

```
fps/
├── joltgo/                  # 服务端（Go，单二进制三角色）
│   ├── main.go              # 入口：解析 -type(gate|match|game)，按角色装配 pitaya app
│   ├── gate/                # gate 服务：AddRoute 路由（match.* 轮询 / game.* 按会话数据定点）
│   ├── match/               # match 服务：配对队列 + 分配 game 节点（RPC game.create）
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
│   │   ├── systems.go       # 每 tick 系统（输入/变换同步/命中/伤害/拾取/波次）+ 各处 rep.Set 变更点
│   │   ├── replicate.go     # ★ ECS ↔ 同步属性的唯一映射（declareAttributes + 变更点 rep.Set）
│   │   └── replicate_test.go# oracle 测试：从 ECS 世界独立推期望属性，漏写的 rep.Set 在此失败
│   ├── ecs/                 # 零依赖 archetype ECS 核心（纯 Go，可单测）—— 本次改造完全未动
│   ├── wrapper/jolt_c.{h,cpp}  # 纯物理桥（extern "C"，无任何业务概念）
│   ├── deploy/              # 本地集群基础设施：etcd/nats 二进制 + 启动脚本
│   ├── CMakeLists.txt       # 把 Jolt 作为子项目编译 libjolt_c.dll
│   └── build.ps1            # 一键构建（UCRT64 + go build + 拷贝 DLL）
├── godot_client/            # 客户端（Godot 4）
│   ├── scenes/main.tscn     # Main(Node3D) + FpsClient + Sfx
│   ├── scripts/main.gd      # 输入/相机/双玩家渲染/按属性名查询与插值/HUD/音效（核心）
│   ├── scripts/world_store.gd # 本地世界状态：实体-属性增量累积成完整世界（按名字取值）
│   ├── scripts/fps_client.gd  # 传输层（pomelo 握手/匹配/心跳/Frame 编解码 + 重连/看门狗）
│   ├── scripts/body_entity.gd # 每个服务端刚体一个渲染节点（程序化模型）
│   ├── scripts/sfx.gd       # 程序化合成 WAV
│   └── tests/               # headless 回归/冒烟测试（.gd，见 §6）
├── docs/                    # ARCHITECTURE / API / BUILD / DEVELOPMENT
└── AGENTS.md                # 本文件
```

**先读顺序**：本文件 → `docs/ARCHITECTURE.md`（分层与数据流）→ `joltgo/main.go` →
`joltgo/gate/gate.go` + `joltgo/match/match.go` + `joltgo/game/instance.go` + `joltgo/game/component.go` →
`joltgo/replication/`（`store.go` 终值表/脏集 + `frame.go` 帧结构）→ `joltgo/sim/replicate.go`
（ECS↔属性映射）→ `joltgo/sim/simulation.go`（tick 顺序 + Physics 接口）→
`godot_client/scripts/world_store.gd` + `godot_client/scripts/main.gd`。

## 3. 关键不变量（改代码前必须理解）

1. **实体 ID 双空间**：物理刚体 id = ECS 实体 id，由物理桥从 1 递增发放；纯逻辑实体（玩家）由 `ecs.NewEntity` 从 `1<<24` 起分配，两空间不重叠。
2. **`physics/` 是唯一允许 cgo 的包**；`sim/` 与 `ecs/` 必须是纯 Go，只依赖 `sim.Physics` 接口（可用 fake 物理单测）。Jolt 原生 BodyID 不透传（0=失败；合法 id 带序号位）。**每个对局实例各建一个 Jolt 世界**（`physics.New()` 独立实例）。
3. **包装层是纯物理桥**：只暴露 Jolt 原生能力，不含敌人/弹丸/靶球/血量/移动策略/任何调参；所有游戏调参集中在 `joltgo/sim/simulation.go` 常量区。多角色（charIdx 0/1）是纯物理能力，不带业务。
4. **服务端权威 + 固定 tick**：`sim.Simulation` 每 tick 顺序固定：
   `inputSystem → 双角色接触排空 → physics.Step → step++ → syncSystem → projectileSystem → enemyDamageSystem → expireProjectilesSystem → resourceSystem → waveSystem`。
   命中/伤害/拾取全部靠**接触事件**，不做距离判定。
5. **同步协议是通用「实体-属性」帧**：wire 契约仍是 `game/protos/game.proto`（protobuf），但**新增一个同步属性不需要改 proto、不需要重生成 Go 码、也不需要动客户端解码**——只有三步：
   - 在 `sim/replicate.go` 的 `declareAttributes` 里加一行 `Declare`（属性表必须完整稳定，见 §5）；
   - 在每个「值会变的地方」**就近** `rep.Set`（同步层只做终值去重，不做任何 ECS 遍历）；
   - 客户端按**属性名**取值（`WorldStore.attr(id, "名字")`）。
   属性名是扁平字符串（约定 `组件.字段`，如 `Body.Mat` / `Player.Idx` / `Game.Score`）。属性表（Schema）**只随 full 帧**下发（不单独发消息），客户端据此把属性 ID 还原成名字与 Kind；**不认识的属性照常存下、只是不渲染**（这就是前后端可独立演进的原因）。`replication.Store` **不 import `ecs`**，耦合全部集中在 `sim/replicate.go`。帧内实体/属性按 ID **升序**输出（字节稳定、可测试）。上行 route 三段式 `server.service.method`（`match.match.join` / `game.game.cmd` / `game.game.resync`）。
6. **并发/锁（核心变化）**：`sim.Simulation` **无锁**——由对局实例 goroutine（`game/instance.go`）独占驱动，输入经命令 channel 投递、同一 goroutine 顺序执行，不需要任何互斥。`replication.Store` 与 `sim.Simulation` 一样由该 goroutine 独占，**非并发安全**（package 注释已声明）。`game.Component` 的实例注册表（uid→实例）用一把 `sync.Mutex` 保护（跨 RPC handler 共享）。`ecs.World` 自身不加锁。
7. **ECS 使用约束**：`Each` / `QueryEach*` 回调内**禁止** Add/Remove/Destroy（swap-remove 打乱迭代），需要增删时"先收集再处理"；`Get` 返回的指针仅本次调用有效；物理实体 Destroy 后彻底注销（id 不复用），逻辑实体 id 走 free list 复用。
8. **地图是对称的**：`sim/map.go` 的部件表在绕 Y 轴旋转 180°（`(x,y,z) → (-x,y,-z)`）下自映射——带 `mirror` 的部件由代码自动补孪生体，两个出生点必须完全等价。改图只写半边；`sim/map_test.go` 会验证对称性、出生点不卡掩体、刷怪不刷进掩体。舷梯参数有硬约束（单级抬升 ≤ 0.4、进深 ≥ 0.5，均为角色半径/`WalkStairs` 决定），改动前先看 `map_test.go` 里的说明。

## 4. 数据流（分布式链路）

```
客户端 ──WS(pomelo+protobuf)──▶ gate(frontend)
   gate 按 route 路由：match.match.join → match 服务；game.game.* → 定点 game 节点（读会话数据 gameServerId）
   match 配对 2 人（或 10s 兜底单人）→ GetServersByType("game") 挑节点 → RPCTo "game.game.create"
   game 节点创建 Instance（每条 goroutine）→ 每 tick SendPushToUsers("onFrame", …, uids, "gate")
   gate 收到 push → 经 NATS 用户频道转发给客户端会话
```

- 客户端握手（Handshake → Ack）后发 `match.match.join`（带持久化 `token`）进匹配；收到 `onMatched`（含 player_idx）后进入对局。
- 上行：输入 + 射击 + 重置**合并成一条** `game.game.cmd`（Notify，帧是最小发送单位）；不再有单独的 `input`/`shoot`/`reset` 消息，重置等即时操作也不额外补推，统一等下一 tick 的帧。
- **增量帧**每 tick 推 `onFrame`，只含本帧变化的 `(实体, 属性, 终值)`；同一属性一帧内改多次只发终值，值没变的写入不产生流量（连静态几何也每 tick 写、但不下发）。
- **重连回局**：`token` 即会话 UID → `match.join` 先向所有 game 节点 fan-out RPC `game.game.rejoin` → 命中则走 `bindPlayer`（写会话数据 `gameServerId` + 推 `onMatched`，与首次匹配同一条收尾路径）→ 客户端收到 `onMatched` 后先清空本地世界、再主动发 `game.game.resync` → 服务端把该槽位的**下一帧**标为全量，单独下发 full 帧（含 Schema）。因为 full 帧是**先清空再整体覆盖**，即使中间先到了几帧增量也会被整帧盖掉——不存在「onMatched 与 full 帧谁先到」的竞态。
- 断线：客户端 1s 重连；**接收看门狗 2.5s 只在匹配后生效**（匹配等待期无帧流，10s 兜底属正常）。
- 服务间通信走 etcd（服务发现）+ NATS（RPC），见 `joltgo/deploy/`。

## 5. 约定与坑

- **C 包装层**：函数 `extern "C"`，只用 C 类型/定长数组/opaque 指针，禁止跨边界传 `std::string`/`vector`/C++ 对象/异常。
- **cgo**：显式 `C.float(...)` / `C.uint32_t(...)` 转换；`physics/` 之外的 Go 代码不出现 `import "C"`。
- **pitaya**：框架源码内置在 `joltgo/third_party/pitaya/`（`go.mod` 用 `replace` 指向本地目录），不在其上改业务。**Cluster 模式**需本地 etcd（服务发现）+ nats-server（RPC），见 `deploy/`。协议是 pomelo 帧 + **protobuf** payload（不是 raw JSON 文本帧），客户端编解码在 `fps_client.gd`。改动 proto 后重跑 `protoc --go_out` 重新生成 Go 码。
- **route 三段式**：`server.service.method`（如 `match.match.join`、`game.game.create`）。RPC 调用（`RPCTo`）也必须是三段式，否则报 `no server type chosen for sending RPC`。
- **etcd 租约与残留**：租约只在 etcd 运行期间倒计时，etcd 重启会把上一轮的孤儿租约按 checkpoint 恢复并**重新计时**（v3.5.14 实测：进程已死 + etcd 停机 66s 后重启，注册项仍带 19s TTL 复活；TTL 内反复重启会反复续命）。所以本地 `start-infra.ps1` 每次启动都清空 `etcd-data`（详见 `deploy/README.md`），整套重启后服务列表一定干净；生产则要调小 TTL 并给选节点加健康检查/重试——`GetServersByType` 可能返回已死的旧节点，`match.startMatch` 选中它就会把玩家静默丢出队列。
- **构建**：Jolt 必须经 CMake `add_subdirectory` 编译（保证 NDEBUG/指令集宏与静态库一致）；UCRT64 与 MINGW64 不能混用；`build.ps1` 硬编码 `C:\msys64`；运行需 `joltgo.exe` 与 `libjolt_c.dll` 同目录。
- **Godot 4**：材质属性用 `metallic`（不是 Godot 3 的 `metalness`）；命令行运行用 `preload` 而非 `class_name`；typed for 循环需 4.2+。
- **多进程部署**：单二进制 `joltgo.exe -type gate|match|game` 三角色，先起 etcd+nats（`deploy/start-all.ps1`）；服务日志在 `deploy/gate.log` / `match.log` / `game.log`（pitaya 写 stderr，`*.out.log` 是 stdout 基本为空）。
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

# 本地起分布式服务端（etcd + nats + gate/match/game 三进程）
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
gofmt -l gate match game physics sim replication ecs
go vet ./gate ./match ./game ./physics ./sim ./replication ./ecs

# 单元测试（ecs 存储语义 / sim 系统+oracle / replication 存储与帧）
PATH="$PWD:$PATH" go test -count=1 ./ecs ./sim ./replication

# 全部包（含 cgo 编译检查与 ./game ./match，需已构建出 libjolt_c.dll）
PATH="$PWD:$PATH" go test -count=1 ./...

# 地图可玩性集成测试（跑真 Jolt：验证舷梯真能走上去、静态几何不漂移）
PATH="$PWD:$PATH" go test -count=1 -tags joltdll ./physics
```

客户端测试（`_console.exe` 变体；`--path` 后接项目目录）：

```bash
# 无头回归：无需服务端，可单独跑
Godot_..._console.exe --headless --path godot_client --script res://tests/world_store_test.gd       # 世界存储语义（full/removed/destroy/未知属性）
Godot_..._console.exe --headless --path godot_client --script res://tests/frame_decode_test.gd      # Frame / Schema protobuf 解码
Godot_..._console.exe --headless --path godot_client --script res://tests/game_frame_test.gd        # 渲染路径（喂合成帧，不碰 WebSocket）
Godot_..._console.exe --headless --path godot_client --script res://tests/reconnect_cleanup_test.gd # 断线清理本地世界与插值状态

# 冒烟：需要活集群（etcd + NATS + gate/match/game 三进程）
Godot_..._console.exe --headless --path godot_client --script res://tests/ws_smoke.gd               # 匹配 + 20Hz 增量帧
Godot_..._console.exe --headless --path godot_client --script res://tests/rejoin_smoke.gd           # 断线回同一局
```

> `rejoin_smoke.gd` 是新协议下**唯一**端到端验证「重连回到同一对局」的测试（同一
> match_id + 同一 player_idx + 重连后收到 full 帧），改匹配 / 回局 / resync 链路后必跑；
> 它的断言在载荷解析失败时显式判失败（不会假通过）。其余四个回归测试互相独立、
> 无需服务端。

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
- 改物理接口 → 动 `joltgo/wrapper/` + `joltgo/physics/`（`sim.Physics` 接口同步），重跑 `build.ps1`。
- 改**协议结构**（新增/删除消息或 route、改 Frame/Schema 字段号）→ 动 `joltgo/game/protos/game.proto`（重跑 `protoc --go_out`）+ `joltgo/game/` + `joltgo/match/` + `docs/API.md` + `godot_client/scripts/fps_client.gd`（protobuf 编解码同步改）。只加同步**属性**不走这条路（见上）。
- 改服务路由/匹配 → 动 `joltgo/gate/` / `joltgo/match/`。
- 改客户端渲染/输入 → 只动 `godot_client/`，Godot 直接 F5。

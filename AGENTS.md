# AGENTS.md

> AI 引导文件：让任何 AI / agent 在最小上下文下快速建立正确心智模型。
> 读本文件后，按需深入 `docs/ARCHITECTURE.md`、`docs/API.md`、`docs/BUILD.md`、`docs/DEVELOPMENT.md`。
> **改动代码时同步更新对应文档与本文件**（过期文档比没有更糟）。

## 1. 项目一句话定位

基于 [Jolt Physics](https://github.com/jrouwe/JoltPhysics) 的服务端权威第一人称 PVE demo：
- **服务端** `joltgo/`：Go + cgo 调用 Jolt（C ABI 包装层），**pitaya 框架**（内置源码，
  **Cluster 模式**）做**分布式微服务**：gate（前端接入）/ match（匹配）/ game（对局逻辑）。
  ECS 架构，20 Hz 固定 tick，WebSocket 主动推送状态。
- **客户端** `godot_client/`：Godot 4.7（gl_compatibility），瘦客户端，60 Hz 渲染 + 快照插值，纯程序化美术/音效。
- **JoltPhysics/** 是 gitignored 第三方依赖（需单独 `git clone`），不属于本仓库代码。

## 2. 目录与入口

```
fps/
├── joltgo/                  # 服务端（Go，单二进制三角色）
│   ├── main.go              # 入口：解析 -type(gate|match|game)，按角色装配 pitaya app
│   ├── gate/                # gate 服务：AddRoute 路由（match.* 轮询 / game.* 按会话数据定点）
│   ├── match/               # match 服务：配对队列 + 分配 game 节点（RPC game.create）
│   ├── game/                # game 服务：对局实例生命周期 + 远端 handler
│   │   ├── instance.go      # ★ 每个对局一条 goroutine，顺序执行、无锁
│   │   ├── component.go     # game.create/input/shoot/reset handler + State→Snapshot 转换
│   │   └── protos/          # protobuf 消息定义 + 生成码（wire 契约）
│   ├── physics/             # ★ 唯一 cgo 包：sim.Physics 的 Jolt 实现 + id 翻译
│   ├── sim/                 # ECS 玩法层（纯 Go，无 cgo，单线程所有）
│   │   ├── map.go           # ★ 运输船场景：部件表（甲板/船体/集装箱/走道/舷梯/桅杆）+ 材质号
│   │   ├── simulation.go    # 组装世界、按部件表搭场景、快照构建
│   │   ├── components.go    # 组件定义（Body 带 Kind/Size/Static/Mat）
│   │   ├── systems.go       # 每 tick 系统（输入/同步/命中/伤害/拾取/波次）
│   │   └── state.go         # 快照内部结构（wire 契约在 game/protos/game.proto）
│   ├── ecs/                 # 零依赖 archetype ECS 核心（纯 Go，可单测）
│   ├── wrapper/jolt_c.{h,cpp}  # 纯物理桥（extern "C"，无任何业务概念）
│   ├── deploy/              # 本地集群基础设施：etcd/nats 二进制 + 启动脚本
│   ├── CMakeLists.txt       # 把 Jolt 作为子项目编译 libjolt_c.dll
│   └── build.ps1            # 一键构建（UCRT64 + go build + 拷贝 DLL）
├── godot_client/            # 客户端（Godot 4）
│   ├── scenes/main.tscn     # Main(Node3D) + FpsClient + Sfx
│   ├── scripts/main.gd      # 输入/相机/双玩家渲染/快照插值/HUD/音效（核心）
│   ├── scripts/fps_client.gd  # 传输层（pomelo 握手/匹配/心跳/编解码 + 重连/看门狗）
│   ├── scripts/body_entity.gd # 每个服务端刚体一个渲染节点（程序化模型）
│   ├── scripts/sfx.gd       # 程序化合成 WAV
│   └── tests/               # headless 回归/冒烟测试（.gd）
├── docs/                    # ARCHITECTURE / API / BUILD / DEVELOPMENT
└── AGENTS.md                # 本文件
```

**先读顺序**：本文件 → `docs/ARCHITECTURE.md`（分层与数据流）→ `joltgo/main.go` →
`joltgo/gate/gate.go` + `joltgo/match/match.go` + `joltgo/game/instance.go` + `joltgo/game/component.go` →
`joltgo/sim/simulation.go`（tick 顺序 + Physics 接口）→ `godot_client/scripts/main.gd`。

## 3. 关键不变量（改代码前必须理解）

1. **实体 ID 双空间**：物理刚体 id = ECS 实体 id，由物理桥从 1 递增发放；纯逻辑实体（玩家）由 `ecs.NewEntity` 从 `1<<24` 起分配，两空间不重叠。
2. **`physics/` 是唯一允许 cgo 的包**；`sim/` 与 `ecs/` 必须是纯 Go，只依赖 `sim.Physics` 接口（可用 fake 物理单测）。Jolt 原生 BodyID 不透传（0=失败；合法 id 带序号位）。**每个对局实例各建一个 Jolt 世界**（`physics.New()` 独立实例）。
3. **包装层是纯物理桥**：只暴露 Jolt 原生能力，不含敌人/弹丸/靶球/血量/移动策略/任何调参；所有游戏调参集中在 `joltgo/sim/simulation.go` 常量区。多角色（charIdx 0/1）是纯物理能力，不带业务。
4. **服务端权威 + 固定 tick**：`sim.Simulation` 每 tick 顺序固定：
   `inputSystem → 双角色接触排空 → physics.Step → step++ → syncSystem → projectileSystem → enemyDamageSystem → expireProjectilesSystem → resourceSystem → waveSystem`。
   命中/伤害/拾取全部靠**接触事件**，不做距离判定。
5. **快照协议是客户端契约**（`sim/state.go` 内部结构；wire 契约在 `game/protos/game.proto`，protobuf）：改动 proto 字段必须重生成 Go 码并同步改 Godot 客户端。快照 `players` 按槽位 0/1 顺序；刚体/金币按 id **升序**输出。上行 route 三段式 `server.service.method`（`match.match.join` / `game.game.*`）。
6. **并发/锁（核心变化）**：`sim.Simulation` **无锁**——由对局实例 goroutine（`game/instance.go`）独占驱动，输入经命令 channel 投递、同一 goroutine 顺序执行，不需要任何互斥。`game.Component` 的实例注册表（uid→实例）用一把 `sync.Mutex` 保护（跨 RPC handler 共享）。`ecs.World` 自身不加锁。
7. **ECS 使用约束**：`Each` / `QueryEach*` 回调内**禁止** Add/Remove/Destroy（swap-remove 打乱迭代），需要增删时"先收集再处理"；`Get` 返回的指针仅本次调用有效；物理实体 Destroy 后彻底注销（id 不复用），逻辑实体 id 走 free list 复用。
8. **地图是对称的**：`sim/map.go` 的部件表在绕 Y 轴旋转 180°（`(x,y,z) → (-x,y,-z)`）下自映射——带 `mirror` 的部件由代码自动补孪生体，两个出生点必须完全等价。改图只写半边；`sim/map_test.go` 会验证对称性、出生点不卡掩体、刷怪不刷进掩体。舷梯参数有硬约束（单级抬升 ≤ 0.4、进深 ≥ 0.5，均为角色半径/`WalkStairs` 决定），改动前先看 `map_test.go` 里的说明。

## 4. 数据流（分布式链路）

```
客户端 ──WS(pomelo+protobuf)──▶ gate(frontend)
   gate 按 route 路由：match.match.join → match 服务；game.game.* → 定点 game 节点（读会话数据 gameServerId）
   match 配对 2 人（或 10s 兜底单人）→ GetServersByType("game") 挑节点 → RPCTo "game.game.create"
   game 节点创建 Instance（每条 goroutine）→ 每 tick SendPushToUsers("onSnapshot", …, uids, "gate")
   gate 收到 push → 经 NATS 用户频道转发给客户端会话
```

- 客户端握手（Handshake → Ack）后发 `match.match.join` 进匹配；收到 `onMatched`（含 player_idx）后进入对局。
- 上行输入 route 三段式（`game.game.input` 等）；`shoot`/`reset` 会**立即补推**一帧。
- 断线：客户端 1s 重连；**接收看门狗 2.5s 只在匹配后生效**（匹配等待期无快照流，10s 兜底属正常）。
- 服务间通信走 etcd（服务发现）+ NATS（RPC），见 `joltgo/deploy/`。

## 5. 约定与坑

- **C 包装层**：函数 `extern "C"`，只用 C 类型/定长数组/opaque 指针，禁止跨边界传 `std::string`/`vector`/C++ 对象/异常。
- **cgo**：显式 `C.float(...)` / `C.uint32_t(...)` 转换；`physics/` 之外的 Go 代码不出现 `import "C"`。
- **pitaya**：框架源码内置在 `joltgo/third_party/pitaya/`（`go.mod` 用 `replace` 指向本地目录），不在其上改业务。**Cluster 模式**需本地 etcd（服务发现）+ nats-server（RPC），见 `deploy/`。协议是 pomelo 帧 + **protobuf** payload（不是 raw JSON 文本帧），客户端编解码在 `fps_client.gd`。改动 proto 后重跑 `protoc --go_out` 重新生成 Go 码。
- **route 三段式**：`server.service.method`（如 `match.match.join`、`game.game.create`）。RPC 调用（`RPCTo`）也必须是三段式，否则报 `no server type chosen for sending RPC`。
- **etcd 租约 60s**：服务进程被强杀后旧租约要 ~60s 才过期，期间会短暂残留（etcd 语义，不是 bug）；`GetServersByType` 可能返回已死的旧节点，生产要做健康检查/重试。
- **构建**：Jolt 必须经 CMake `add_subdirectory` 编译（保证 NDEBUG/指令集宏与静态库一致）；UCRT64 与 MINGW64 不能混用；`build.ps1` 硬编码 `C:\msys64`；运行需 `joltgo.exe` 与 `libjolt_c.dll` 同目录。
- **Godot 4**：材质属性用 `metallic`（不是 Godot 3 的 `metalness`）；命令行运行用 `preload` 而非 `class_name`；typed for 循环需 4.2+。
- **多进程部署**：单二进制 `joltgo.exe -type gate|match|game` 三角色，先起 etcd+nats（`deploy/start-all.ps1`）。
- **服务端 20Hz tick 无条件运行**（实例创建后无论客户端是否在线都推进）。

## 6. 构建 / 测试命令

```powershell
# 服务端构建（MSYS2 UCRT64 + Go 1.26+）
cd joltgo; .\build.ps1

# 单元测试（纯 Go，无 cgo，最快）
cd joltgo; gofmt -l .; go vet ./gate ./match ./game ./physics ./sim; go test ./ecs ./sim

# 全部包（含 cgo 编译检查，需已构建出 libjolt_c.dll 导入库）
go test ./...

# 地图可玩性集成测试（跑真 Jolt：验证舷梯真能走上去、静态几何不漂移；需 libjolt_c.dll 在 PATH）
cd joltgo; go test -tags joltdll ./physics

# 本地起分布式服务端（etcd + nats + gate/match/game 三进程）
cd joltgo\deploy; .\start-all.ps1     # 停：.\stop-infra.ps1

# 客户端（Godot 4.7）
Godot_v4.7.2-stable_win64.exe --path godot_client        # F5 运行
# 无头冒烟（需分布式服务端已启动）：验证匹配 + 20Hz 推送
Godot_..._console.exe --headless --path godot_client --script res://tests/ws_smoke.gd
# 无头回归（无需服务端）：快照去重 / 断线清理
Godot_..._console.exe --headless --path godot_client --script res://tests/snapshot_same_step_test.gd
Godot_..._console.exe --headless --path godot_client --script res://tests/reconnect_cleanup_test.gd
```

## 7. 变更 runbook（改什么就动哪里）

- 改玩法逻辑 → 只动 `joltgo/sim/`（组件 + 系统 + 调参），跑 `go test ./ecs ./sim`。
- 改地图/场景 → 只动 `joltgo/sim/map.go`（部件表 + 材质号），跑 `go test ./sim`；
  涉及"走不走得上去"这类几何手感，再跑 `go test -tags joltdll ./physics`。
  新增材质号同时改 `godot_client/scripts/body_entity.gd` 的 `MATS` 表（只能追加编号）。
- 改 ECS 核心 → 只动 `joltgo/ecs/`（必须零依赖、可单测）。
- 改物理接口 → 动 `joltgo/wrapper/` + `joltgo/physics/`（`sim.Physics` 接口同步），重跑 `build.ps1`。
- 改协议 → 动 `joltgo/game/protos/game.proto`（重跑 `protoc --go_out`）+ `joltgo/game/` + `joltgo/match/` + `docs/API.md` + `godot_client/scripts/`（protobuf 编解码同步改）。
- 改服务路由/匹配 → 动 `joltgo/gate/` / `joltgo/match/`。
- 改客户端渲染/输入 → 只动 `godot_client/`，Godot 直接 F5。

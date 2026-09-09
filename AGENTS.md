# AGENTS.md

> AI 引导文件：让任何 AI / agent 在最小上下文下快速建立正确心智模型。
> 读本文件后，按需深入 `docs/ARCHITECTURE.md`、`docs/API.md`、`docs/BUILD.md`、`docs/DEVELOPMENT.md`。
> **改动代码时同步更新对应文档与本文件**（过期文档比没有更糟）。

## 1. 项目一句话定位

基于 [Jolt Physics](https://github.com/jrouwe/JoltPhysics) 的服务端权威第一人称 PVE demo：
- **服务端** `joltgo/`：Go + cgo 调用 Jolt（C ABI 包装层），ECS 架构，20 Hz 固定 tick，WebSocket 主动推送状态。
- **客户端** `godot_client/`：Godot 4.7（gl_compatibility），瘦客户端，60 Hz 渲染 + 快照插值，纯程序化美术/音效。
- **JoltPhysics/** 是 gitignored 第三方依赖（需单独 `git clone`），不属于本仓库代码。

## 2. 目录与入口

```
fps/
├── joltgo/                  # 服务端（Go）
│   ├── main.go              # 入口：组装 sim + ws hub + 20Hz tick + 优雅退出
│   ├── ws.go                # 手写 RFC6455 WebSocket + 广播 hub（单写者通道）
│   ├── physics.go           # ★ 唯一 cgo 文件：sim.Physics 的 Jolt 实现 + id 翻译
│   ├── sim/                 # ECS 玩法层（纯 Go，无 cgo）：components/systems/simulation/state
│   ├── ecs/                 # 零依赖 archetype ECS 核心（纯 Go，可单测）
│   ├── wrapper/jolt_c.{h,cpp}  # 纯物理桥（extern "C"，无任何业务概念）
│   ├── CMakeLists.txt       # 把 Jolt 作为子项目编译 libjolt_c.dll
│   └── build.ps1            # 一键构建（UCRT64 + go build + 拷贝 DLL）
├── godot_client/            # 客户端（Godot 4）
│   ├── scenes/main.tscn     # Main(Node3D) + FpsClient + Sfx
│   ├── scripts/main.gd      # 输入/相机/快照插值渲染/HUD/音效（约 800 行，核心）
│   ├── scripts/fps_client.gd  # WebSocket 传输层（连接/重连/收发 + 接收看门狗）
│   ├── scripts/body_entity.gd # 每个服务端刚体一个渲染节点（程序化模型）
│   ├── scripts/sfx.gd       # 程序化合成 WAV
│   └── tests/               # headless 回归/冒烟测试（.gd）
├── docs/                    # ARCHITECTURE / API / BUILD / DEVELOPMENT
└── AGENTS.md                # 本文件
```

**先读顺序**：本文件 → `docs/ARCHITECTURE.md`（分层与数据流）→ `joltgo/main.go` → `joltgo/sim/simulation.go`（tick 顺序 + Physics 接口）→ `godot_client/scripts/main.gd`（`_process` / `_store_snapshot` / `_render_interpolated`）。

## 3. 关键不变量（改代码前必须理解）

1. **实体 ID 双空间**：物理刚体 id = ECS 实体 id，由物理桥从 1 递增发放；纯逻辑实体（玩家）由 `ecs.NewEntity` 从 `1<<24` 起分配，两空间不重叠。
2. **`physics.go` 是唯一允许 cgo 的文件**；`sim/` 与 `ecs/` 必须是纯 Go，只依赖 `sim.Physics` 接口（可用 fake 物理单测）。Jolt 原生 BodyID 不透传（0=失败；合法 id 带序号位）。
3. **包装层是纯物理桥**：只暴露 Jolt 原生能力，不含敌人/弹丸/靶球/血量/移动策略/任何调参；所有游戏调参集中在 `joltgo/sim/simulation.go` 常量区。
4. **服务端权威 + 固定 tick**：`simulation.stepLocked` 顺序固定：
   `inputSystem → PollCharacterContacts → physics.Step → step++ → syncSystem → projectileSystem → enemyDamageSystem → expireProjectilesSystem → resourceSystem → waveSystem`。
   只有 `syncSystem` 每 tick 枚举物理世界并写回组件；命中/伤害/拾取全部靠**接触事件**，不做距离判定。
5. **快照协议是客户端契约**（`sim/state.go`）：改动 JSON 字段必须同步改 Godot 客户端。刚体/金币按 id **升序**输出。
6. **并发/锁**：`sim.Simulation` 用一把 `s.mu` 串行一切，公开方法内部加锁，`xLocked` 方法要求调用方已持锁；`ecs.World` 自身不加锁。`ws.go` hub 用独立互斥锁，每连接单写者。
7. **ECS 使用约束**：`Each` / `QueryEach*` 回调内**禁止** Add/Remove/Destroy（swap-remove 打乱迭代），需要增删时"先收集再处理"；`Get` 返回的指针仅本次调用有效；物理实体 Destroy 后彻底注销（id 不复用），逻辑实体 id 走 free list 复用。

## 4. 数据流（核心链路）

```
客户端 60Hz 输入(input/shoot/reset) ──WebSocket 文本帧──▶ ws.go hub
   hub 读线程 → sim.ApplyInput/Shoot/Reset（s.mu）
   tick goroutine(50ms) → sim.Step() → Jolt 步进 → Snapshot
   encodeState → broadcast → 客户端 _store_snapshot
   客户端：_store_snapshot（同 step 按内容去重）→ _render_interpolated（lerp/slerp 影子跟随）
```

- 客户端输入 `move` 是世界空间水平期望速度；跳跃边沿触发（服务端下一 tick 消费）。
- `shoot`/`reset` 会**立即补推**一帧（可能与前帧同 step 但内容已变——客户端按内容去重，不是按 step）。
- 断线：客户端每 1s 重连；接收看门狗 2.5s 无数据强制重连；重连清空渲染实体与弹丸状态。

## 5. 约定与坑

- **C 包装层**：函数 `extern "C"`，只用 C 类型/定长数组/opaque 指针，禁止跨边界传 `std::string`/`vector`/C++ 对象/异常。
- **cgo**：显式 `C.float(...)` / `C.uint32_t(...)` 转换；`physics.go` 之外的 Go 代码不出现 `import "C"`。
- **构建**：Jolt 必须经 CMake `add_subdirectory` 编译（保证 NDEBUG/指令集宏与静态库一致），不要手写编译命令；UCRT64 与 MINGW64 不能混用；`build.ps1` 硬编码 `C:\msys64`；运行需 `joltgo.exe` 与 `libjolt_c.dll` 同目录；重建前若 exe 在运行会锁 DLL。
- **Godot 4**：材质属性用 `metallic`（不是 Godot 3 的 `metalness`）；命令行运行用 `preload` 而非 `class_name`；typed for 循环需 4.2+。
- **多客户端 = 共享同一个玩家**（服务端单角色、广播同一状态）：这是当前设计，不是 bug；做真正的多人需 per-client 玩家/房间。
- **服务端 20Hz tick 无条件运行**（无客户端也在推进）。

## 6. 构建 / 测试命令

```powershell
# 服务端构建（MSYS2 UCRT64 + Go 1.26+）
cd joltgo; .\build.ps1

# 运行服务端（监听 ws://localhost:8080/）
.\joltgo.exe

# 单元测试（纯 Go，无 cgo，最快）
cd joltgo; gofmt -l .; go vet ./ecs ./sim; go test ./ecs ./sim

# 全部包（含 cgo 编译检查，需已构建出 libjolt_c.dll 导入库）
go test ./...

# 客户端（Godot 4.7）
Godot_v4.7.2-stable_win64.exe --path godot_client        # F5 运行
# 无头冒烟（需服务端已启动）：验证 20Hz 推送
Godot_..._console.exe --headless --path godot_client --script res://tests/ws_smoke.gd
# 无头回归（无需服务端）：快照去重 / 断线清理
Godot_..._console.exe --headless --path godot_client --script res://tests/snapshot_same_step_test.gd
Godot_..._console.exe --headless --path godot_client --script res://tests/reconnect_cleanup_test.gd
```

## 7. 变更 runbook（改什么就动哪里）

- 改玩法逻辑 → 只动 `joltgo/sim/`（组件 + 系统 + 调参），跑 `go test ./ecs ./sim`。
- 改 ECS 核心 → 只动 `joltgo/ecs/`（必须零依赖、可单测）。
- 改物理接口 → 动 `joltgo/wrapper/` + `joltgo/physics.go`（`sim.Physics` 接口同步），重跑 `build.ps1`。
- 改协议 → 动 `joltgo/sim/state.go` + `docs/API.md` + `godot_client/scripts/`（同步改）。
- 改客户端渲染/输入 → 只动 `godot_client/`，Godot 直接 F5。

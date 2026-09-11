# fps

基于 [Jolt Physics](https://github.com/jrouwe/JoltPhysics) 的第一人称 FPS demo。
物理引擎通过 Go cgo 调用，客户端使用 **Godot 4**。全部业务代码位于 `joltgo/`（服务端）
与 `godot_client/`（Godot 客户端）。

## 特性

- 第一人称视角：鼠标捕获、WASD 移动、跳跃、奔跑；**V 键切换第一/第三人称**（Godot 客户端）
- **运输船场景**（参考穿越火线）：长甲板货船，艏艉两个对称出生区、中部可跳跃攀爬的集装箱堆、
  两舷通长高架走道（舷梯上下，可俯瞰全船）、舷侧集装箱/木箱掩体与桅杆烟囱；
  整张图绕 Y 轴 180° 自映射，两个出生点完全等价
- **PVP 对枪**：两名玩家互相射击，**击杀数先到 10 者获胜**（分出胜负 5 秒后自动重开
  一局）；每发命中扣 34 血（满血 100，3 发击杀），被击杀后**立即**在己方出生点满血复活
- **卡通模型（全部程序化生成，无外部美术资源）**：
  玩家持枪第一人称 viewmodel（枪口闪光/后坐）、第三人称玩家人形 Avatar（大头/棒球帽/
  双肩包，远端玩家换色区分）
- 玩家是 Jolt `CharacterVirtual` 胶囊角色控制器；**不可被推动/挤开**（撞来的怪物/箱子被弹开）
- 真实弹丸：`LinearCast` 小球飞行，通过接触监听（`ContactListener`）判定命中
- 可阻挡弹丸的箱子；玩家另有跟随角色的**命中盒**刚体（角色控制器不是刚体，弹丸靠它判定命中）
- 服务端以 **20 Hz** 固定 tick 推进模拟（服务器权威），状态变化经 WebSocket 主动推送
  **实体-属性增量帧**（只发本帧变化的 `(实体, 属性, 终值)`；值没变不发、同帧只留终值）
- **账号体系**：用户名 + 密码注册/登录（bcrypt 存哈希），服务端签发 token 凭证
  （7 天有效、每次恢复续期）；一个账号同时只有一个活会话，异地登录顶掉旧连接
- **断线重连回到同一对局**：客户端持久化服务端签发的 token，重连后自动登回同一账号、
  找回原实例与原槽位，服务端单独下发一份全量帧把本地世界整体重建
- 服务端采用 **pitaya 分布式游戏服务端框架**（内置源码，**Cluster 模式**）拆成四个微服务：
  - **gate**（前端接入，WS 直连客户端，路由业务消息，登记会话归属）
  - **account**（注册/登录/凭证恢复，签发与轮换 token）
  - **match**（对局匹配，配对 2 人 / 超时单人兜底，分配 game 节点；队列在 Redis）
  - **game**（对局逻辑，每个对局一个 goroutine 顺序执行、无锁）
  - 四服务经 etcd（服务发现）+ NATS（RPC）通信，共享状态（账号/凭证/会话归属/匹配队列）
    放 Redis；协议为 pomelo 帧 + protobuf payload
- **双玩家**：每局两个玩家各自独立位置/血量/战绩，击杀与死亡分别记在各自身上；客户端第一人称视角 + 远端玩家 avatar
- 服务端业务层采用 **ECS（实体-组件-系统）架构**：物理实体 id 即实体 id，每 tick 只同步一次
  变换到组件，各系统（输入/命中盒跟随/弹丸命中/对局结算）全部读写 Go 侧组件；同步层独立成
  `replication/` 包（与 ECS 解耦的终值表 + 脏集），映射集中在 `sim/replicate.go`
- 客户端 **60 Hz** 渲染：把服务端增量累积成本地世界 + 对运动物体做**影子跟随插值**，
  输入 60 Hz 上报
- 音效：客户端程序化合成（Godot 端生成 WAV），无外部音频文件

## 快速开始

### 1. 准备依赖

本仓库不包含 Jolt Physics 源码，需要先在仓库根目录单独克隆：

```bash
git clone https://github.com/jrouwe/JoltPhysics.git
```

最终目录结构应为：

```text
fps/
├── joltgo/          # 本仓库代码（服务端，pitaya 已内置在 joltgo/third_party/）
└── JoltPhysics/     # 第三方依赖（gitignored）
```

### 2. 准备工具链（Windows + MSYS2 UCRT64）

需要 Go 1.26+ 以及 MSYS2 的 UCRT64 工具链，另外需要 Godot 4.7+（客户端）：

```bash
pacman -S --needed \
  mingw-w64-ucrt-x86_64-gcc \
  mingw-w64-ucrt-x86_64-cmake \
  mingw-w64-ucrt-x86_64-ninja \
  mingw-w64-ucrt-x86_64-make
```

> 详细环境要求见 [docs/BUILD.md](docs/BUILD.md)。

### 3. 构建服务端

```powershell
cd joltgo
.\build.ps1
```

### 4. 运行（分布式四服务）

先起基础设施（etcd + nats-server + redis-server），再起四个服务：

```powershell
cd joltgo\deploy
.\start-all.ps1     # 内含 etcd + nats + redis + gate/account/match/game 四进程
```

或用四个终端手动起（单二进制按 `-type` 区分角色）：

```powershell
cd joltgo
.\joltgo.exe -type gate      # frontend，监听 ws://localhost:8080
.\joltgo.exe -type account   # 账号注册/登录/凭证恢复
.\joltgo.exe -type match     # 匹配服务
.\joltgo.exe -type game      # 游戏逻辑（对局实例）
```

只有 `-type` 和 `-redis`（默认 `localhost:6379`）两个 flag；gate/account/match 启动时会
Ping 一次 Redis，连不上就退出。

然后用 Godot 4.7 打开 `godot_client/project.godot` 按 F5，或命令行运行：

```bash
Godot_v4.7.2-stable_win64.exe --path godot_client
```

首次运行会看到登录面板：注册一个账号后进入匹配，点击画面锁定鼠标即可游玩
（支持两个客户端一起匹配双人局；**同一台机器**上跑两个客户端时，要用**两个不同的账号**，
并且两个实例各自使用独立的 user 目录（否则第二个会读到第一个的凭证、登成同一个账号而
互相顶下线）—— 做法见 [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) 的冒烟测试一节）。
gate 监听 `ws://localhost:8080/`；基础设施见 `joltgo/deploy/README.md`。

## 玩法

| 操作 | 说明 |
|---|---|
| 鼠标 | 瞄准（锁定鼠标后） |
| 左键 | 射击 |
| W / A / S / D | 移动 |
| Space | 跳跃 |
| Shift | 奔跑 |
| V | 切换第一/第三人称 |
| ESC | 释放鼠标（可点 Reset 重开） |

规则：双人对枪，每发命中扣 34 血（满血 100），击杀数先到 10 者获胜；被击杀后立即
在己方出生点满血复活。分出胜负 5 秒后服务端自动重开一局。

## 项目结构

```text
fps/
├── joltgo/                  # Go 服务端（单二进制四角色）
│   ├── third_party/pitaya/  # 内置 pitaya 框架源码（pkg/ + go.mod + go.sum）
│   ├── wrapper/
│   │   ├── jolt_c.h         # C ABI 声明（extern "C"，纯物理桥、无业务）
│   │   └── jolt_c.cpp       # Jolt C++ 原生 API → C ABI 实现
│   ├── main.go              # 入口：解析 -type(gate|account|match|game) 与 -redis，按角色装配
│   ├── gate/                # gate 服务：AddRoute 路由（account.* / match.* / game.*）+ 会话归属登记
│   ├── account/             # account 服务：注册/登录/resume，bcrypt + token 轮换
│   ├── match/               # match 服务：Redis 配对队列 + 分配 game 节点
│   ├── game/                # game 服务：对局实例（instance.go 每局一 goroutine 无锁）
│   │   └── protos/          # protobuf 消息定义 + 生成码（wire 契约；新增同步属性无需改此文件）
│   ├── kv/                  # Redis 连接（共享状态的唯一入口）
│   ├── online/              # 会话归属登记：accountID → 所在 gate 节点
│   ├── physics/             # cgo 物理桥（sim.Physics 接口的 Jolt 实现，唯一 cgo 包）
│   ├── replication/         # 与 ECS 解耦的实体-属性同步层：终值表 + 本帧脏集（不 import ecs）
│   ├── sim/                 # ECS 模拟层：组件/系统/场景 + ECS↔属性映射（replicate.go）+ oracle 单测
│   ├── ecs/                 # ECS 核心：实体 + archetype 存储/查询 + Bundle（零依赖、可单测）
│   ├── deploy/              # 本地集群基础设施：etcd/nats/redis 二进制 + 启动脚本
│   ├── CMakeLists.txt       # 将 Jolt 作为子项目，编译 libjolt_c.dll
│   └── build.ps1            # 一键构建脚本
├── godot_client/            # Godot 4 客户端
│   ├── scenes/main.tscn     # 主场景（Main + FpsClient + Sfx 子节点）
│   ├── scripts/
│   │   ├── main.gd          # 输入/相机/登录面板/双玩家渲染/按属性名查询与插值/HUD/音效
│   │   ├── world_store.gd   # 本地世界状态：实体-属性增量累积成完整世界（按名字取值）
│   │   ├── fps_client.gd    # 传输层（pomelo 握手/登录/匹配/心跳 + Frame 编解码 + 重连）
│   │   ├── body_entity.gd   # 每个服务端刚体一个渲染节点
│   │   └── sfx.gd           # 程序化音效
│   └── tests/               # 无头回归/冒烟测试（.gd；见 AGENTS.md §6）
└── JoltPhysics/             # 第三方依赖（gitignored，需单独克隆）
```

## 文档

- [架构说明](docs/ARCHITECTURE.md)：分层、数据流、核心机制、设计决策
- [构建与环境](docs/BUILD.md)：环境准备、构建流程、常见问题
- [网络协议](docs/API.md)：WebSocket 消息格式（唯一通信通道）
- [开发与维护](docs/DEVELOPMENT.md)：如何扩展功能、代码约定、调试

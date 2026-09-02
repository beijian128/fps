# fps

基于 [Jolt Physics](https://github.com/jrouwe/JoltPhysics) 的第一人称 FPS demo。
物理引擎通过 Go cgo 调用，客户端使用 **Godot 4**。全部业务代码位于 `joltgo/`（服务端）
与 `godot_client/`（Godot 客户端）。

## 特性

- 第一人称视角：鼠标捕获、WASD 移动、跳跃、奔跑；**V 键切换第一/第三人称**（Godot 客户端）
- **PVE 打怪**：怪物按波刷新（第 1 波 3 只，之后每波 +1，上限 6），清波 2 秒后刷下一波；低难度（怪物慢、伤害低）
- **卡通资源**：金币走近自动拾取、击杀怪物掉落金币；全部模型程序化生成，无外部美术资源
- **卡通模型**：玩家持枪第一人称 viewmodel（枪口闪光/后坐）、第三人称玩家人形 Avatar（大头/棒球帽/双肩包）、大眼睛小角短腿的圆滚滚怪物
- 玩家是 Jolt `CharacterVirtual` 胶囊角色控制器；**不可被推动/挤开**（撞来的怪物/箱子被弹开）
- 真实弹丸：`LinearCast` 小球飞行，通过接触监听（`ContactListener`）判定命中
- 可破坏的红色靶球、可阻挡弹丸的箱子
- 服务端以 **20 Hz** 固定 tick 推进模拟（服务器权威），状态变化经 WebSocket 主动推送
- 客户端 **60 Hz** 渲染：对运动物体做影子跟随插值（快照 lerp/slerp），输入 60 Hz 上报
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
├── joltgo/          # 本仓库代码（服务端）
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

### 4. 运行

```powershell
.\joltgo.exe
```

然后用 Godot 4.7 打开 `godot_client/project.godot` 按 F5，或命令行运行：

```bash
Godot_v4.7.2-stable_win64.exe --path godot_client
```

点击画面锁定鼠标即可游玩。服务端监听 `ws://localhost:8080/`。

## 玩法

| 操作 | 说明 |
|---|---|
| 鼠标 | 瞄准（锁定鼠标后） |
| 左键 | 射击 |
| W / A / S / D | 移动 |
| Space | 跳跃 |
| Shift | 奔跑 |
| ESC | 释放鼠标（可点 Reset 重开） |

## 项目结构

```text
fps/
├── joltgo/                  # Go 服务端
│   ├── wrapper/
│   │   ├── jolt_c.h         # C ABI 声明（extern "C"）
│   │   └── jolt_c.cpp       # Jolt C++ API → C ABI 实现
│   ├── main.go              # 入口：启动 WebSocket 服务 + 20 Hz tick
│   ├── game.go              # 游戏状态与玩法逻辑（物理、敌人 AI、弹丸、伤害）
│   ├── ws.go                # WebSocket 实现（纯标准库 RFC 6455）+ 广播 hub
│   ├── CMakeLists.txt       # 将 Jolt 作为子项目，编译 libjolt_c.dll
│   └── build.ps1            # 一键构建脚本
├── godot_client/            # Godot 4 客户端
│   ├── scenes/main.tscn     # 主场景（Main + FpsClient + Sfx 子节点）
│   ├── scripts/
│   │   ├── main.gd          # 输入/相机/快照插值渲染/HUD/音效
│   │   ├── fps_client.gd    # WebSocket 传输层（连接/重连/收发）
│   │   ├── body_entity.gd   # 每个服务端刚体一个渲染节点
│   │   └── sfx.gd           # 程序化音效
│   └── tests/ws_smoke.gd    # 无头冒烟测试（20 Hz 推送速率）
└── JoltPhysics/             # 第三方依赖（gitignored，需单独克隆）
```

## 文档

- [架构说明](docs/ARCHITECTURE.md)：分层、数据流、核心机制、设计决策
- [构建与环境](docs/BUILD.md)：环境准备、构建流程、常见问题
- [网络协议](docs/API.md)：WebSocket 消息格式（唯一通信通道）
- [开发与维护](docs/DEVELOPMENT.md)：如何扩展功能、代码约定、调试

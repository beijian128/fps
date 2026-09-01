# fps

基于 [Jolt Physics](https://github.com/jrouwe/JoltPhysics) 的浏览器 FPS 游戏 demo。
物理引擎通过 Go cgo 调用，前端使用 Three.js。全部业务代码位于 `joltgo/`。

## 特性

- 第一人称视角：指针锁定、WASD 移动、跳跃、奔跑
- 玩家是 Jolt `CharacterVirtual` 胶囊角色控制器，会与地板、墙、箱子碰撞（不会穿墙）
- 真实弹丸：`LinearCast` 小球飞行，通过接触监听（`ContactListener`）判定命中
- 敌人 AI：胶囊敌人持续追击玩家，可被击杀并在地图内重生
- 可破坏的红色靶球、可阻挡弹丸的箱子
- 音效：Web Audio 实时合成，无外部音频文件
- 前端零外部运行时依赖（Three.js 已本地化到仓库内）

## 快速开始

### 1. 准备依赖

本仓库不包含 Jolt Physics 源码，需要先在仓库根目录单独克隆：

```bash
git clone https://github.com/jrouwe/JoltPhysics.git
```

最终目录结构应为：

```text
fps/
├── joltgo/          # 本仓库代码
└── JoltPhysics/     # 第三方依赖（gitignored）
```

### 2. 准备工具链（Windows + MSYS2 UCRT64）

需要 Go 1.26+ 以及 MSYS2 的 UCRT64 工具链：

```bash
pacman -S --needed \
  mingw-w64-ucrt-x86_64-gcc \
  mingw-w64-ucrt-x86_64-cmake \
  mingw-w64-ucrt-x86_64-ninja \
  mingw-w64-ucrt-x86_64-make
```

> 详细环境要求见 [docs/BUILD.md](docs/BUILD.md)。

### 3. 构建

```powershell
cd joltgo
.\build.ps1
```

### 4. 运行

```powershell
.\joltgo.exe
```

浏览器打开 <http://localhost:8080>，点击画面锁定鼠标即可游玩。

## 玩法

| 操作 | 说明 |
|---|---|
| 鼠标 | 瞄准（锁定鼠标后） |
| 左键 | 射击 |
| W / A / S / D | 移动 |
| Space | 跳跃 |
| Shift | 奔跑 |
| Reset 按钮 | 重建场景 |

## 项目结构

```text
fps/
├── joltgo/
│   ├── wrapper/
│   │   ├── jolt_c.h          # C ABI 声明（extern "C"）
│   │   └── jolt_c.cpp        # Jolt C++ API → C ABI 实现
│   ├── main.go               # HTTP 服务 + 游戏逻辑 + 敌人 AI
│   ├── CMakeLists.txt        # 将 Jolt 作为子项目，编译 libjolt_c.dll
│   ├── build.ps1             # 一键构建脚本
│   └── web/                  # Three.js 前端（vendor 目录已本地化）
└── JoltPhysics/              # 第三方依赖（gitignored，需单独克隆）
```

## 文档

- [架构说明](docs/ARCHITECTURE.md)：分层、数据流、核心机制、设计决策
- [构建与环境](docs/BUILD.md)：环境准备、构建流程、常见问题
- [HTTP API](docs/API.md)：服务端接口参考
- [开发与维护](docs/DEVELOPMENT.md)：如何扩展功能、代码约定、调试

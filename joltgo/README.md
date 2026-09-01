# joltgo

一个通过 cgo 调用 [Jolt Physics](https://github.com/jrouwe/JoltPhysics) 的示例，附带一个
Three.js 3D FPS 网页游戏 demo。

## 目录结构

- `wrapper/jolt_c.h` / `wrapper/jolt_c.cpp`：把 Jolt 的 C++ API 封装成 `extern "C"` 的 C ABI
- `CMakeLists.txt`：把 Jolt 作为子项目，编译出 `libjolt_c.dll`（自带静态 C++ 运行库）
- `main.go`：HTTP 服务，管理物理世界、角色控制器、弹丸、敌人 AI 并暴露 JSON API
- `web/`：Three.js 前端（本地已内置，无需联网）

## 构建

前提：已安装 Go、MSYS2 UCRT64 工具链（gcc/g++/cmake/ninja）。

```powershell
cd joltgo
.\build.ps1
```

脚本依次：编译包装层 DLL → 用 UCRT64 编译器 `go build` → 把 `libjolt_c.dll` 拷贝到 exe 旁。

## 运行

```powershell
.\joltgo.exe
```

然后浏览器打开 <http://localhost:8080>，点击画面锁定鼠标即可游玩。
`joltgo.exe` 与 `libjolt_c.dll` 必须在同一目录。

## 玩法

- WASD 移动，鼠标瞄准，左键射击，空格跳跃，Shift 奔跑
- 玩家是 Jolt `CharacterVirtual` 胶囊角色控制器，会与地板、墙、箱子发生碰撞
- 射击发射真实弹丸（`LinearCast` 小球），通过接触监听检测命中
- 红色靶球与追击玩家的敌人（胶囊 + 简单 AI）可被击杀，命中箱子会被击飞

## API

- `GET  /api/state`   返回当前所有刚体的状态
- `POST /api/step`    body `{"frames": N, "move": [x,z], "jump": bool}`
- `POST /api/shoot`   body `{"origin": [...], "dir": [...]}`，发射一枚弹丸
- `POST /api/add`     body `{"kind":"box"|"sphere","pos":[...],"size":[...],"radius":...,"vel":[...],"static":...}`
- `POST /api/reset`   重建场景
- `POST /api/gravity` body `{"g":[x,y,z]}`

## 扩展

在 `wrapper/jolt_c.cpp` 加 `extern "C"` 函数、在 `wrapper/jolt_c.h` 声明，然后在 `main.go`
里调用。跨边界只传 POD 类型，不要传 `std::string`、`std::vector` 或 C++ 对象。

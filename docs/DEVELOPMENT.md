# 开发与维护

## 给新加入者的最短路径

1. 按 [BUILD.md](BUILD.md) 跑通构建与运行
2. 读 [ARCHITECTURE.md](ARCHITECTURE.md) 理解分层和数据流
3. 改前端：只动 `joltgo/web/`，不需要重编译 Go；刷新浏览器即可（改动会被 `go:embed` 打包，发布前需重跑 `build.ps1`）
4. 改服务端逻辑：只动 `joltgo/main.go`，重跑 `build.ps1` 的第 2 步（`go build`）
5. 改物理接口：动 `wrapper/`，必须重跑完整 `build.ps1`

## 如何扩展一个功能

新增物理能力时，按「从下到上」的顺序改，每一层都容易验证：

1. **C 包装层** `wrapper/jolt_c.cpp` + `wrapper/jolt_c.h`
   - 加一个 `extern "C"` 函数，参数/返回值只用 POD 类型
   - 头文件里加声明
2. **Go 服务** `main.go`
   - 在 cgo 注释块里已经 `#include "jolt_c.h"`，直接调用新函数即可
   - 如需新接口，加一个 `handleXxx` 并在 `main()` 的 mux 注册
3. **前端** `web/app.js`
   - 在对应按钮/事件里调用接口，根据返回的 `state` 更新渲染

示例：想加「玩家蹲下」——C 包装层加切换胶囊形状的函数；Go 在 `/api/step`
请求里接受 `crouch` 字段并调用；前端监听 Ctrl 键并在 `doStep` 的 body 里带上。

## 代码约定

- C 包装层所有函数 `extern "C"`，头文件里统一用 `uint32_t`/`float`/`int` 等 C 类型
- Go 里 C 类型用 `C.float(...)`、`C.uint32_t(...)` 显式转换，避免 float32 与 `_Ctype_float` 的赋值错误
- 服务端对 `world` 和游戏状态的所有读写都在 `mu`（`sync.Mutex`）保护下
- 前端渲染用 `meshes` Map 按 `id` 缓存 Three.js 网格，只在形状签名变化时重建几何体
- 新增 gitignored 的产物时，同步更新根目录 `.gitignore`

## 调试

### 服务端

- 看服务日志：启动时用重定向，或直接在终端前台运行 `.\joltgo.exe`
- 快速验证接口，不启动前端：

  ```powershell
  curl.exe http://localhost:8080/api/state
  curl.exe -X POST http://localhost:8080/api/step -H "Content-Type: application/json" -d "{\"frames\":10,\"move\":[0,0],\"jump\":false}"
  ```

- Jolt 的断言/日志默认关闭；如需排查物理问题，可在 CMake 里开 `USE_ASSERTS=ON`（Debug）重编

### 前端

- 浏览器 DevTools 的 Console 查看报错
- 确认前端是否加载了新代码：改 `app.js` 后如果行为没变，可能是缓存或没重跑 `build.ps1`
  （`go:embed` 会把 `web/` 打进 `joltgo.exe`，直接刷新浏览器只对开发期有效——实际上
  `go:embed` 在运行时从 exe 内读取，所以每次改前端必须重编 Go 才能生效）

## 已知限制 / 待办

- 玩家没有内部刚体（`mInnerBodyShape`），因此：
  - 刚体无法真正「推」玩家，只能靠角色穿透恢复把玩家挤开
  - 弹丸/敌人无法通过普通接触回调命中玩家（目前用距离判定扣血）
  - 后续可给角色加 `mInnerBodyShape`，让玩家在物理世界里有实体
- 构建脚本和路径硬编码为 Windows + `C:\msys64`，跨平台需要额外适配
- 端口 `8080`、重力、玩家参数、敌人参数目前散落在源码里，后续可抽到配置文件
- 音效为 Web Audio 实时合成，无素材管理；要替换为音频文件需自行引入

## 发布 / 版本管理

- 仓库根目录 git 仓库，默认分支 `main`
- `JoltPhysics/` 是 gitignored 的第三方依赖，升级它只需在对应目录 `git pull`
- 提交前运行一遍 `build.ps1` 确认能编译通过；二进制产物不应提交（已 gitignore）
- 许可证目前未指定，如需开源请补 `LICENSE` 文件

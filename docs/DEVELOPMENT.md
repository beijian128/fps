# 开发与维护

## 给新加入者的最短路径

1. 按 [BUILD.md](BUILD.md) 跑通构建与运行
2. 读 [ARCHITECTURE.md](ARCHITECTURE.md) 理解分层和数据流
3. 改客户端：只动 `godot_client/`，在 Godot 里直接 F5 运行即可
4. 改服务端逻辑：只动 `joltgo/game.go`，重跑 `build.ps1` 的 go build
5. 改物理接口：动 `joltgo/wrapper/`，必须重跑完整 `build.ps1`

## 如何扩展一个功能

新增物理能力时，按「从下到上」的顺序改，每一层都容易验证：

1. **C 包装层** `wrapper/`
   - 加一个 `extern "C"` 函数，参数/返回值只用 POD 类型
   - 头文件里加声明
2. **服务端** `game.go`
   - 在 `Game` 上新增 `xLocked` 玩法逻辑，并用带锁的公开方法暴露
3. **客户端** `godot_client/scripts/`
   - 传输层：`fps_client.gd`（需要新消息时加一个 `send_xxx` 方法）
   - 渲染层：`main.gd` / `body_entity.gd` 消费快照

示例：想加「玩家蹲下」——C 包装层加切换胶囊形状的函数；`Game` 加 `crouch`
输入状态并在 `stepLocked` 里应用；客户端在 `input` 消息里带上 `crouch` 字段。

## 代码约定

- C 包装层所有函数 `extern "C"`，头文件里统一用 `uint32_t`/`float`/`int` 等 C 类型
- Go 里 C 类型用 `C.float(...)`、`C.uint32_t(...)` 显式转换，避免 float32 与 `_Ctype_float` 的赋值错误
- `game.go`：所有对 `world` 和游戏状态的访问都在 `g.mu` 保护下；
  公开方法内部加锁，`xLocked` 方法要求调用方持锁（防止双重加锁）
- 客户端 GDScript：避免从 Variant 推断类型（默认告警会被当错误处理）；
  节点实例化用 `preload` 而非 `class_name`（纯命令行运行不依赖编辑器导入的全局类缓存）
- 新增 gitignored 的产物时，同步更新根目录 `.gitignore`

## 调试

### 服务端

- 看服务日志：启动时用重定向，或直接在终端前台运行 `.\joltgo.exe`
- Jolt 的断言/日志默认关闭；如需排查物理问题，可在 CMake 里开 `USE_ASSERTS=ON`（Debug）重编
- 抓包：用带 WebSocket 支持的工具连 `ws://localhost:8080/` 观察推送

### 客户端

- 无头冒烟测试（验证服务端 20 Hz 推送，需要服务端已启动）：

  ```bash
  Godot_v4.7.2-stable_win64_console.exe --headless \
    --path godot_client --script res://tests/ws_smoke.gd
  ```

  预期输出 `SMOKE unique_steps=81 span=80 elapsed_ms=4000` 左右
- 客户端运行期日志在 `%APPDATA%\Godot\app_userdata\Jolt FPS Client\logs\godot.log`

## 已知限制 / 待办

- 玩家不会被动态刚体推动/挤开（`CharacterImmovableListener` 对动态接触禁用
  `mCanPushCharacter`，撞上来的箱子/敌人被冲量弹开，角色纹丝不动）：
  - 代价是玩家也不会被移动的平台/箱子"带"着走（漂浮感被牺牲）
  - 敌人贴身伤害仍用距离判定（玩家无 `mInnerBodyShape`，普通接触回调不触发）
  - 后续若想要"玩家能被撞飞"，需给角色加 `mInnerBodyShape` 或其他方案
- 服务端 20 Hz tick 无条件运行：即使没有客户端连接世界也会演化
  （敌人会攻击闲置玩家，阵亡后自动复活）；如需「无客户端时暂停」可再扩展
- 构建脚本和路径硬编码为 Windows + `C:\msys64`，跨平台需要额外适配
- 端口 `8080`、重力、玩家参数、敌人参数目前散落在源码里，后续可抽到配置文件
- 音效为 Godot 程序化生成（`sfx.gd` 合成 16-bit WAV）；要替换为音频文件需自行引入

## 发布 / 版本管理

- 仓库根目录 git 仓库，默认分支 `main`
- `JoltPhysics/` 是 gitignored 的第三方依赖，升级它只需在对应目录 `git pull`
- 提交前运行一遍 `build.ps1` 确认能编译通过；二进制产物不应提交（已 gitignore）
- 许可证目前未指定，如需开源请补 `LICENSE` 文件

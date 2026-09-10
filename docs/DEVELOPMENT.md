# 开发与维护

## 给新加入者的最短路径

1. 按 [BUILD.md](BUILD.md) 跑通构建与运行
2. 读 [ARCHITECTURE.md](ARCHITECTURE.md) 理解分层和数据流
3. 改客户端：只动 `godot_client/`，在 Godot 里直接 F5 运行即可
4. 改服务端玩法逻辑：只动 `joltgo/sim/`（组件 + 系统），跑 `go test ./ecs ./sim` 后重跑 `build.ps1`
5. 改 ECS 核心：只动 `joltgo/ecs/`，注意它必须是零依赖、可单测的
6. 改物理接口：动 `joltgo/wrapper/` 或 `joltgo/physics/`，必须重跑完整 `build.ps1`
7. 改协议/pitaya 组件：动 `joltgo/game/protos/game.proto`（重跑 `protoc --go_out` 重新生成）+ `joltgo/game/` + `joltgo/match/` + 同步改 `godot_client/scripts/fps_client.gd`（protobuf 编解码）

## 如何扩展一个功能

新增物理能力时，按「从下到上」的顺序改，每一层都容易验证：

1. **C 包装层** `wrapper/`（需要新的物理能力时）
   - 加一个 `extern "C"` 函数，参数/返回值只用 POD 类型
   - **必须是纯物理桥**：只暴露 Jolt 原生能力，禁止把游戏业务写进 C++
     （敌人/弹丸/靶球/血量、角色移动策略、数值调参都属于 Go 侧 sim）
   - 头文件里加声明，`joltgo/physics/physics.go` 里补 `sim.Physics` 接口实现
2. **模拟层** `sim/`
   - 组件加在 `components.go`；逻辑按「组件 + 系统」拆分：
     系统加在 `systems.go`（或新文件），在 `Simulation.stepOne` 里按序接入
   - 系统只读组件和 `sim.Physics`，不要引入 cgo
   - **sim 无锁**：单线程所有，由 game 实例 goroutine 独占驱动；不要把并发加回 sim
   - 用 fake 物理（见 `sim/sim_test.go`）给新系统补单元测试
   - 新增快照字段时：先改 `game/protos/game.proto` 重生成 Go 码，再在
     `game/component.go` 的 `toSnapshot` 里填上映射
3. **客户端** `godot_client/scripts/`
   - 传输层：`fps_client.gd`（需要新消息时加一个 `send_xxx` 方法，注意 route 三段式
     `server.service.method` 与服务端 handler 方法名小写对应；新字段要在 protobuf
     编解码函数里读写）
   - 渲染层：`main.gd` / `body_entity.gd` 消费快照

### 改 protobuf 消息的完整流程

1. 编辑 `joltgo/game/protos/game.proto`（增字段/改字段号/加消息）
2. 在 `joltgo/` 下重生成 Go 码：
   ```bash
   protoc --go_out=. --go_opt=paths=source_relative -I . game/protos/game.proto
   ```
   （需要 `protoc` 与 `protoc-gen-go`；见 [BUILD.md](BUILD.md)）
3. 改 `joltgo/game/component.go`：handler 入参类型、`toSnapshot` 映射
4. 改 `godot_client/scripts/fps_client.gd`：`_encode_*` / `_decode_*` 手写 wire 编解码
5. 跑 `go build ./...` + 无头冒烟测试验证

示例：想加「玩家蹲下」——C 包装层加切换胶囊形状的函数（`jolt_character_set_shape`
之类，只透传形状参数）；`sim` 给玩家实体加 `Crouching` 组件、在输入系统里应用；
客户端在 `input` 消息里带上 `crouch` 字段。

## 代码约定

- C 包装层所有函数 `extern "C"`，头文件里统一用 `uint32_t`/`float`/`int` 等 C 类型
- **C 包装层只做纯物理桥**：不出现敌人/弹丸/靶球/血量等业务概念，不内置任何
  调参常量（重力、跳跃速度、胶囊尺寸、弹丸速度等一律由 Go 侧传入）
- Go 里 C 类型用 `C.float(...)`、`C.uint32_t(...)` 显式转换，避免 float32 与 `_Ctype_float` 的赋值错误
- `joltgo/physics/` 是唯一允许 cgo 的包；`sim/` 与 `ecs/` 必须是纯 Go；
  实体 id 由物理桥发放并维护与 Jolt BodyID 的映射；每个对局实例各建一个 Jolt 世界
- `joltgo/game/` 是对局实例层：`instance.go` 每局一个 goroutine 顺序执行；`component.go`
  的 handler 方法对应 route（三段式），只做协议 ↔ sim 翻译，不写玩法逻辑
- `joltgo/gate/` / `joltgo/match/` 是分布式路由与匹配层：gate 只 AddRoute，match 配对后
  RPC game.create
- `joltgo/third_party/pitaya/` 是内置第三方源码，不要在其上改业务代码；
  升级时重跑 `go mod tidy`
- `sim.Simulation` **无锁**：单线程所有，由实例 goroutine 独占访问；不要加锁、也不要
  在多个 goroutine 间共享同一实例
- ECS 组件是纯数据结构；`ecs.Each` 回调里不得增删实体（swap-remove 会打乱迭代），
  需要增删时先收集再处理；`ecs.Get` 返回的指针只在本 tick 内有效
- 快照协议（`sim/state.go`）是服务端内部契约；wire 契约在 `game/protos/game.proto`
  （protobuf），改动 proto 字段需重新生成 Go 码并同步改 Godot 客户端；
  上行消息的 route/字段契约见 [API.md](API.md)（改动 `joltgo/game/`、`joltgo/match/` 时同步更新）
- 场景地图只写 `sim/map.go` 的部件表：**只写半边**（带 `mirror: true` 的部件会自动
  补上绕 Y 轴旋转 180° 的孪生体），对称性由 `sim/map_test.go` 验证。舷梯参数
  （单级抬升 ≤ 0.4、进深 ≥ 0.5）是角色控制器决定的下限，改前先读 `map_test.go`
  里的说明并跑 `go test -tags joltdll ./physics` 确认真的走得上去。新增视觉材质号
  时同步改 `godot_client/scripts/body_entity.gd` 的 `MATS` 表（编号只能追加）
- 客户端 GDScript：避免从 Variant 推断类型（默认告警会被当错误处理）；
  节点实例化用 `preload` 而非 `class_name`（纯命令行运行不依赖编辑器导入的全局类缓存）
- 新增 gitignored 的产物时，同步更新根目录 `.gitignore`

## 测试

- 服务端单元测试（不依赖 cgo / Jolt DLL，直接跑）：

  ```bash
  cd joltgo && go test ./ecs ./sim
  ```

  `ecs` 覆盖组件存储语义；`sim` 用 fake 物理（只做运动学积分 + 地板钳制 + 接触生成）
  覆盖各系统行为：初始快照与角色/重力配置、射击校验（归一化 + LinearCast/摩擦配置）、
  命中结算（弹丸在接触对任意一侧）、无关接触忽略、传感器不挡弹丸、掉落、
  接触伤害与复活、非敌人接触不掉血、跳跃离地、金币接触拾取守恒、波次推进、
  弹丸过期、敌人静止、Reset 重建
- 全部包（含 cgo 与 `game` 组件编译检查）：

  ```bash
  go vet ./game && go test ./...
  ```

- 地图集成测试（跑真 Jolt；需 `libjolt_c.dll` 在 PATH 或与测试二进制同目录）：

  ```bash
  cd joltgo && go test -tags joltdll ./physics
  ```

  验证的是 fake 物理测不出来的东西：角色真的能从出生点沿舷梯走上高架走道
  （舷梯单级抬升/进深改坏了会失败），以及整张图搭出来后静态几何不漂移、
  位置无 NaN。默认构建不含这些文件，没装 DLL 的机器 `go test ./...` 依然全绿。

- 端到端冒烟测试（需要服务端已启动，见下文「客户端」小节）

## 调试

### 服务端

- 看服务日志：gate/match/game 三进程各自有日志（`deploy/start-all.ps1` 写 `deploy/*.log`），
  pitaya 默认 logrus 会打印 handler 注册、服务发现、RPC 等日志
- Jolt 的断言/日志默认关闭；如需排查物理问题，可在 CMake 里开 `USE_ASSERTS=ON`（Debug）重编
- 抓包：用带 WebSocket 支持的工具连 `ws://localhost:8080/`（gate）观察 pomelo 二进制帧；
  协议细节见 [API.md](API.md)

### 客户端

- 无头冒烟测试（验证匹配 + 20 Hz 推送，需要分布式服务端已启动）：

  ```bash
  Godot_v4.7.2-stable_win64_console.exe --headless \
    --path godot_client --script res://tests/ws_smoke.gd
  ```

  预期输出 `SMOKE matched` + `SMOKE unique_steps=80 span=79 elapsed_ms=4000` 左右。
  双客户端并发跑可验证 2 人匹配（match 日志出现 `with 2 players`）。
- 客户端运行期日志在 `%APPDATA%\Godot\app_userdata\Jolt FPS Client\logs\godot.log`

## 已知限制 / 待办

- 玩家不会被动态刚体推动/挤开（sim 通过 `SetCharacterDynamicPush(false)` 配置，
  包装层只暴露 `mCanPushCharacter` 这个物理级开关；撞上来的箱子/敌人被冲量弹开，
  角色纹丝不动）：
  - 代价是玩家也不会被移动的平台/箱子"带"着走（漂浮感被牺牲）
  - 后续若想要"玩家能被撞飞"，需给角色加 `mInnerBodyShape` 或其他方案
- 敌人接触伤害要求玩家**主动接触**怪物（朝它推/贴着它）：一旦分开（松开移动键、
  被弹开），接触事件停止、伤害停止——这是物理接触判定的固有语义，不是持续光环
- 服务端 20 Hz tick 无条件运行：即使没有客户端连接世界也在推进（怪物静止，
  闲置玩家不会受伤）；如需「无客户端时暂停」可再扩展
- 构建脚本和路径硬编码为 Windows + `C:\msys64`，跨平台需要额外适配
- 所有调参（端口、重力、玩家/敌人/弹丸参数）集中在 `joltgo/sim/` 常量区，
  目前为编译期常量，后续可抽到配置文件
- 音效为 Godot 程序化生成（`sfx.gd` 合成 16-bit WAV）；要替换为音频文件需自行引入

## 发布 / 版本管理

- 仓库根目录 git 仓库，默认分支 `main`
- `JoltPhysics/` 是 gitignored 的第三方依赖，升级它只需在对应目录 `git pull`
- 提交前运行一遍 `build.ps1` 确认能编译通过；二进制产物不应提交（已 gitignore）
- 许可证目前未指定，如需开源请补 `LICENSE` 文件

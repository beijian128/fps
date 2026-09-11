# 开发与维护

## 给新加入者的最短路径

1. 按 [BUILD.md](BUILD.md) 跑通构建与运行
2. 读 [ARCHITECTURE.md](ARCHITECTURE.md) 理解分层和数据流
3. 改客户端：只动 `godot_client/`，在 Godot 里直接 F5 运行即可
4. 改服务端玩法逻辑：只动 `joltgo/sim/`（组件 + 系统 + `replicate.go` 的属性映射），跑 `go test ./ecs ./sim ./replication` 后重跑 `build.ps1`
5. 改 ECS 核心：只动 `joltgo/ecs/`，注意它必须是零依赖、可单测的
6. 改物理接口：动 `joltgo/wrapper/` 或 `joltgo/physics/`，必须重跑完整 `build.ps1`
7. 加/改一个**同步属性**：只动 `joltgo/sim/replicate.go`（`declareAttributes` 加一行 + 变更点 `rep.Set`）+ `sim/replicate_test.go`，**不需要**改 proto、生成码或客户端解码（见下）
8. 改**协议结构**（增删消息 / route、改 Frame/Schema 字段号）：动 `joltgo/game/protos/game.proto`（重跑 `protoc --go_out` 重新生成）+ `joltgo/game/` + `joltgo/match/` + `joltgo/account/` + 同步改 `godot_client/scripts/fps_client.gd`（protobuf 编解码）
9. 改**账号/登录/凭证**：动 `joltgo/account/`（`token.go` 纯函数 / `store.go` Redis / `component.go` handler）+ 客户端 `fps_client.gd`（Request/Response）与 `main.gd`（登录面板），跑 `go test ./account`
10. 改**会话归属 / 多节点行为**：动 `joltgo/online/` + `joltgo/gate/session.go`，跑 `go test ./online ./gate ./account ./match`

## 如何扩展一个功能

新增物理能力时，按「从下到上」的顺序改，每一层都容易验证：

1. **C 包装层** `wrapper/`（需要新的物理能力时）
   - 加一个 `extern "C"` 函数，参数/返回值只用 POD 类型
   - **必须是纯物理桥**：只暴露 Jolt 原生能力，禁止把游戏业务写进 C++
     （玩家/弹丸/血量、角色移动策略、数值调参都属于 Go 侧 sim）
   - 头文件里加声明，`joltgo/physics/physics.go` 里补 `sim.Physics` 接口实现
2. **模拟层** `sim/`
   - 组件加在 `components.go`；逻辑按「组件 + 系统」拆分：
     系统加在 `systems.go`（或新文件），在 `Simulation.stepOne` 里按序接入
   - 系统只读组件和 `sim.Physics`，不要引入 cgo
   - **sim 无锁**：单线程所有，由 game 实例 goroutine 独占驱动；不要把并发加回 sim
   - 用 fake 物理（见 `sim/sim_test.go`）给新系统补单元测试
   - **同步属性映射**集中在 `sim/replicate.go`：加同步字段 = `declareAttributes` 加一行
     `Declare` + 在每个「值会变的地方」就近 `rep.Set`，再在 `replicate_test.go` 的
     `expectedAttrs` 里补一条断言。属性表必须在 `Simulation.New()` 里一次声明完整
     （`Set` 未声明属性会 panic），`replication.Store` 与 `sim` 一样非并发安全
     （由实例 goroutine 独占）
   - **漏写一处 `rep.Set` 不会在运行时暴露**（没有报错、没有日志），客户端只会静默
     停在旧值；唯一能抓住它的是 `sim/replicate_test.go` 的 oracle 测试（`expectedAttrs`
     从 ECS 世界独立推期望值，与 store 全量逐项比对）。所以**先补 `rep.Set`、再补断言**
3. **客户端** `godot_client/scripts/`
   - 传输层：`fps_client.gd`（上行有 Notify 三条：`match.join`（空消息）/ `game.cmd`
     （输入+射击+重置合并成一条）/ `game.resync`，以及 Request 三条：`account.register` /
     `account.login` / `account.resume`；需要新消息时加一个 `send_xxx` 方法，注意 route
     三段式 `server.service.method` 与服务端 handler 方法名小写对应；新消息要在 protobuf
     编解码函数里读写。**Request 要自己记 mid**，Response 帧里没有 route）
   - 世界状态：`world_store.gd` 按**属性名**累积；渲染层 `main.gd` / `body_entity.gd`
     通过 `_store.attr(id, "属性名")` 取值 —— 新增同步属性时客户端取新值只需这一句

### 改 protobuf 消息的完整流程

> **只加一个同步属性不必走这条路**——属性是数据不是字段，加一行 `Declare` + 变更点
> `rep.Set` 就够（见上文）。只有增删**消息**、改 **route**、或改 `Frame` / `Schema` 的
> 字段号时才需要下面的流程。

1. 编辑 `joltgo/game/protos/game.proto`（增字段/改字段号/加消息）
2. 在 `joltgo/` 下重生成 Go 码：
   ```bash
   protoc --go_out=. --go_opt=paths=source_relative -I . game/protos/game.proto
   ```
   （需要 `protoc` 与 `protoc-gen-go`；见 [BUILD.md](BUILD.md)）
3. 改 `joltgo/game/component.go`：handler 入参类型、`toFrame`（`replication.Frame` →
   `protos.Frame` 的转换）
4. 改 `godot_client/scripts/fps_client.gd`：`_encode_*` / `_decode_*` 手写 wire 编解码
5. 跑 `go build ./...` + 无头测试验证；改同步链路再跑 `tests/rejoin_smoke.gd`（需集群）

示例：想加「玩家蹲下」——C 包装层加切换胶囊形状的函数（`jolt_character_set_shape`
之类，只透传形状参数）；`sim` 给玩家实体加 `Crouching` 组件、在输入系统里应用；
`CommandMsg` 加一个 `crouch` 字段，客户端在 `game.cmd` 里带上。

## 代码约定

- C 包装层所有函数 `extern "C"`，头文件里统一用 `uint32_t`/`float`/`int` 等 C 类型
- **C 包装层只做纯物理桥**：不出现玩家/弹丸/血量等业务概念，不内置任何
  调参常量（重力、跳跃速度、胶囊尺寸、弹丸速度等一律由 Go 侧传入）
- Go 里 C 类型用 `C.float(...)`、`C.uint32_t(...)` 显式转换，避免 float32 与 `_Ctype_float` 的赋值错误
- `joltgo/physics/` 是唯一允许 cgo 的包；`sim/` 与 `ecs/` 必须是纯 Go；
  实体 id 由物理桥发放并维护与 Jolt BodyID 的映射；每个对局实例各建一个 Jolt 世界
- `joltgo/game/` 是对局实例层：`instance.go` 每局一个 goroutine 顺序执行；`component.go`
  的 handler 方法对应 route（三段式），只做协议 ↔ sim 翻译，不写玩法逻辑
- `joltgo/gate/` / `joltgo/match/` / `joltgo/account/` 是分布式接入、匹配与账号层：
  gate 只 AddRoute + 在会话绑定/断开时登记会话归属（`session.go`），match 从 Redis 队列
  配对后 RPC game.create，account 管注册/登录/凭证轮换。共享状态一律走 Redis
  （`joltgo/kv/` 连接、`joltgo/online/` 会话归属、`match/queue.go` 队列）
- **会话 UID = accountID**：身份由 account 服务签发，客户端不再自报。任何按 uid 索引的
  地方（game 实例注册表、push 目标、回局 fan-out）语义不变，只是那个字符串换了来源
- `joltgo/third_party/pitaya/` 是内置第三方源码，不要在其上改业务代码；
  升级时重跑 `go mod tidy`
- `sim.Simulation` **无锁**：单线程所有，由实例 goroutine 独占访问；不要加锁、也不要
  在多个 goroutine 间共享同一实例
- ECS 组件是纯数据结构；`ecs.Each` 回调里不得增删实体（swap-remove 会打乱迭代），
  需要增删时先收集再处理；`ecs.Get` 返回的指针只在本 tick 内有效
- **同步是通用的「实体-属性」帧**：`replication/` 是与 ECS 解耦的独立包（终值表 + 本帧
  脏集，不 import `ecs`），`sim/replicate.go` 是唯一知道「ECS 组件 ↔ 属性名」映射的地方。
  属性名是扁平字符串（约定 `组件.字段`），加同步字段只动这一个文件；**漏写 `rep.Set`
  是静默失败**，由 `sim/replicate_test.go` 的 oracle 测试兜底
- 帧的 wire 契约在 `game/protos/game.proto`（protobuf）；改 proto **字段定义**需重新生成
  Go 码并同步改 Godot 客户端，但**新增同步属性不需要**。上行 route/字段契约见
  [API.md](API.md)（改动 `joltgo/game/`、`joltgo/match/` 时同步更新）
- 场景地图只写 `sim/map.go` 的部件表：**只写半边**（带 `mirror: true` 的部件会自动
  补上绕 Y 轴旋转 180° 的孪生体），对称性由 `sim/map_test.go` 验证。舷梯参数
  （单级抬升 ≤ 0.4、进深 ≥ 0.5）是角色控制器决定的下限，改前先读 `map_test.go`
  里的说明并跑 `go test -tags joltdll ./physics` 确认真的走得上去。新增视觉材质号
  时同步改 `godot_client/scripts/body_entity.gd` 的 `MATS` 表（编号只能追加）
- 客户端 GDScript：避免从 Variant 推断类型（默认告警会被当错误处理）；
  节点实例化用 `preload` 而非 `class_name`（纯命令行运行不依赖编辑器导入的全局类缓存）
- 新增 gitignored 的产物时，同步更新根目录 `.gitignore`

## 测试

- 服务端单元测试（不依赖 cgo / Jolt DLL，直接跑；Redis 用例走 miniredis，不需要真 Redis）：

  ```bash
  cd joltgo && go test ./account ./online ./kv ./gate ./match ./ecs ./sim ./replication
  ```

  `account` 覆盖凭证生成/校验、bcrypt、Redis 存储与轮换、三个 handler 的分支；
  `online` / `kv` 覆盖会话归属读写与连接；`gate` 覆盖会话钩子与 bindgame remote；
  `match` 覆盖 Redis 队列（配对原子性、超时兜底）与开局链路；
  `ecs` 覆盖组件存储语义；`replication` 覆盖同步层（终值表 / 脏集去重 / full 帧不推进
  增量基线 / destroy 清基线 / 帧编码）；`sim` 用 fake 物理（只做运动学积分 + 地板钳制）
  覆盖各系统行为：初始同步属性与角色/重力配置、射击校验（归一化 + LinearCast/摩擦配置）、
  命中盒跟随、出生点落回自己命中盒外、命中结算（弹丸在接触对任意一侧）、无关接触忽略、
  命中场景几何只移除弹丸、命中自己的命中盒不算命中、扣血 / 击杀计分 / 立即复活、
  到线判胜与超时重开、跳跃离地、弹丸过期、输入限幅、Reset 重建；
  `sim/replicate_test.go` 是 oracle 测试——从 ECS 世界
  独立推期望属性再与 store 全量逐项比对，漏写的 `rep.Set` 在这里失败
- 全部包（含 cgo 与 `game` 组件编译检查；`./game ./physics` 需 `libjolt_c.dll` 在 PATH，
  `joltgo/` 下已构建）：

  ```bash
  cd joltgo && PATH="$PWD:$PATH" go vet ./gate ./account ./kv ./online ./match ./game ./physics ./sim ./replication ./ecs
  PATH="$PWD:$PATH" go test ./...
  ```

- 地图集成测试（跑真 Jolt；需 `libjolt_c.dll` 在 PATH 或与测试二进制同目录）：

  ```bash
  cd joltgo && go test -tags joltdll ./physics
  ```

  验证的是 fake 物理测不出来的东西：角色真的能从出生点沿舷梯走上高架走道
  （舷梯单级抬升/进深改坏了会失败），整张图搭出来后静态几何不漂移、
  位置无 NaN；以及 **PVP 命中链路的两个反直觉结论** —— CCD 弹丸真的能打中静态
  胶囊（命中盒的形状），`jolt_character_ignore_body` 的忽略表真的有阻挡差异
  （`pvp_hit_integration_test.go` 带对照组：不登记忽略时角色会被挡在刚体前）。
  默认构建不含这些文件，没装 DLL 的机器 `go test ./...` 依然全绿。

- 端到端冒烟测试（需要服务端已启动，见下文「客户端」小节）

## 调试

### 服务端

- 看服务日志：gate/account/match/game 四进程各自有日志（`deploy/start-all.ps1` 写 `deploy/*.log`），
  pitaya 默认 logrus 会打印 handler 注册、服务发现、RPC 等日志
- 看 Redis 数据：`deploy/redis-cli.exe` —— 账号 `acct:*`、凭证 `sess:*`、
  会话归属 `online:*`、匹配队列 `match:queue`（ZSET）
- Jolt 的断言/日志默认关闭；如需排查物理问题，可在 CMake 里开 `USE_ASSERTS=ON`（Debug）重编
- 抓包：用带 WebSocket 支持的工具连 `ws://localhost:8080/`（gate）观察 pomelo 二进制帧；
  协议细节见 [API.md](API.md)

### 客户端

- 无服务端也能跑的无头回归测试（`tests/`，用 `_console.exe` 变体）：

  ```bash
  Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/world_store_test.gd
  Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/frame_decode_test.gd
  Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/login_reply_decode_test.gd
  Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/game_frame_test.gd
  Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/reconnect_cleanup_test.gd
  ```

  分别覆盖：世界存储语义（full / removed / destroy / 未知属性）、`Frame`/`Schema` 解码、
  Response 帧（LEB128 mid、无 route、errorMask）与 `LoginReply` 解码、
  渲染路径（喂合成帧，不碰 WebSocket）、断线清理本地世界与插值状态。
- 冒烟测试（需要分布式服务端已启动：etcd + NATS + redis + gate/account/match/game 四进程）：

  ```bash
  Godot_v4.7.2-stable_win64_console.exe --headless \
    --path godot_client --script res://tests/login_smoke.gd
  Godot_v4.7.2-stable_win64_console.exe --headless \
    --path godot_client --script res://tests/ws_smoke.gd
  Godot_v4.7.2-stable_win64_console.exe --headless \
    --path godot_client --script res://tests/rejoin_smoke.gd
  ```

  `login_smoke` 验证「注册 → LoginReply(token) → 断线 → resume → onMatched」整条链路。
  `ws_smoke` 验证登录后匹配 + 20 Hz 增量帧推送，预期输出 `SMOKE matched` +
  `SMOKE unique_steps=80 span=79 elapsed_ms=4000` 左右。`rejoin_smoke` 验证断线重连回到
  **同一 match_id + 同一 player_idx** 并收到 full 帧——它是新协议下唯一端到端验证
  「重连回同一局」的测试，改匹配/回局/resync 链路后必跑。

  **验证 2 人匹配**（match 日志出现 `with 2 players`、slot 1、双人广播）时，必须让两个
  客户端用**两个不同的账号**，而且各自有独立的 `user://` 目录。两件事都要做：
  同一账号登两次是顶号（后连的把先连的踢掉，队列又按 UID 去重，只剩一条）；
  而 Godot 的 `user://` 是**每项目一个目录**（Windows 下
  `%APPDATA%\Godot\app_userdata\<项目名>`），同机所有实例共用，第二个客户端会读到第一个
  的 `auth_token.txt` 而自动登成同一个账号。Godot 4.7.2 **没有** `--user-data-dir` 参数
  （传了会被静默忽略），但 `user://` 落在 `%APPDATA%` 下，所以给额外实例换一个 `APPDATA`
  就能让它拥有独立的 `auth_token.txt`：

  ```bash
  # 第一个客户端：默认 user 目录，用账号 A 登录
  Godot_v4.7.2-stable_win64_console.exe --path godot_client
  # 第二个客户端：独立 APPDATA → 独立 user://auth_token.txt → 可以注册/登录账号 B
  APPDATA="$PWD/.client2" Godot_v4.7.2-stable_win64_console.exe --path godot_client
  ```

  PowerShell 里对应
  `$env:APPDATA="$PWD\.client2"; Godot_v4.7.2-stable_win64_console.exe --path godot_client`。
- 客户端运行期日志在 `%APPDATA%\Godot\app_userdata\Jolt FPS Client\logs\godot.log`

## 已知限制 / 待办

- 玩家不会被动态刚体推动/挤开（sim 通过 `SetCharacterDynamicPush(false)` 配置，
  包装层只暴露 `mCanPushCharacter` 这个物理级开关；撞上来的箱子被冲量弹开，
  角色纹丝不动）：
  - 代价是玩家也不会被移动的平台/箱子"带"着走（漂浮感被牺牲）
  - 后续若想要"玩家能被撞飞"，需给角色加 `mInnerBodyShape` 或其他方案
- 玩家之间也不会互相阻挡：两人的**命中盒**被双方角色忽略（命中盒只对弹丸有意义），
  所以贴到一起时不会互相卡住，但也不会被对方挡住——这是"不引入角色间碰撞"的选择
- 命中判定依赖**命中盒刚体**（见 `docs/ARCHITECTURE.md`「玩家命中盒」）：命中盒与角色
  共位、每 tick 跟随，因此死亡复活时它必须一并搬回出生点；漏搬会让旧位置继续挨枪
- 服务端 20 Hz tick 无条件运行：即使没有客户端连接世界也在推进（玩家不会自己掉血，
  但某一方已到 10 杀时到点仍会自动重开一局）；如需「无客户端时暂停」可再扩展
- 构建脚本和路径硬编码为 Windows + `C:\msys64`，跨平台需要额外适配
- 所有调参（端口、重力、玩家/弹丸参数、对局规则）集中在 `joltgo/sim/` 常量区，
  目前为编译期常量，后续可抽到配置文件
- 音效为 Godot 程序化生成（`sfx.gd` 合成 16-bit WAV）；要替换为音频文件需自行引入

## 发布 / 版本管理

- 仓库根目录 git 仓库，默认分支 `main`
- `JoltPhysics/` 是 gitignored 的第三方依赖，升级它只需在对应目录 `git pull`
- 提交前运行一遍 `build.ps1` 确认能编译通过；二进制产物不应提交（已 gitignore）
- 许可证目前未指定，如需开源请补 `LICENSE` 文件

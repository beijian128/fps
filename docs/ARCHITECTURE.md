# 架构说明

## 分层

```text
┌──────────────────────────────────────────────┐
│  Godot 客户端（godot_client/）                │
│  main.gd      输入/相机/插值渲染/HUD/音效     │
│  fps_client.gd  WebSocket 传输层（重连/收发） │
│  body_entity.gd 每个刚体一个渲染节点          │
└──────────────────────┬───────────────────────┘
                       │ WebSocket（JSON 文本帧）
┌──────────────────────▼───────────────────────┐
│  Go 服务（joltgo/）                           │
│  main.go     入口 + 20 Hz 模拟 tick           │
│  game.go     游戏状态/玩法逻辑（自带锁）      │
│  ws.go       WebSocket hub（广播/上行分发）   │
└──────────────────────┬───────────────────────┘
                       │ cgo
┌──────────────────────▼───────────────────────┐
│  C 包装层（wrapper/jolt_c.{h,cpp}）           │
│  extern "C"，只暴露 POD 类型                  │
└──────────────────────┬───────────────────────┘
                       │ C++ API
┌──────────────────────▼───────────────────────┐
│  Jolt Physics（libJolt.a → libjolt_c.dll）    │
└──────────────────────────────────────────────┘
```

## 为什么用 cgo + C ABI

cgo 只能直接调用 C ABI，不能调用 C++ 类/重载/模板。因此所有 Jolt C++ 调用都被
封装成 `extern "C"` 的普通函数，参数和返回值只用：

- 整数 / 浮点数
- 定长数组（`float pos[3]` 等）
- 不透明指针（`JoltWorld *`）

禁止跨边界传递 `std::string`、`std::vector`、C++ 对象、引用或异常。

## 数据流

游戏状态由服务端持有，客户端是「输入 + 展示」的瘦客户端，模拟节奏由服务端驱动：

1. 服务端以固定 **20 Hz** tick 自主推进：消费最新输入 → 更新角色控制器 → 更新
   敌人 AI → 步进物理 → 处理弹丸命中 → 结算伤害
2. 每次 tick 完毕，服务端把完整状态快照通过 WebSocket **主动推送**给所有客户端；
   射击、重置等即时操作立即补推
3. 客户端以 60 Hz 渲染，收到快照后做**影子跟随插值**：渲染时刻比真实时间滞后
   约一个 tick，在最近两帧快照之间线性插值（位置 lerp、旋转 slerp），
   让 20 Hz 的数据在 60 Hz 屏幕上保持平滑；同一 tick 的重复推送只保留首次
   到达时间，保证插值进度持续前进
4. 客户端每渲染帧（约 60 Hz）上报输入（世界空间水平速度 + 跳跃）；
   跳跃是边沿触发，服务端在下一个 tick 消费
5. 连接断开后客户端每秒自动重连；重连成功即收到当前快照，无需重新拉取

这种「服务端权威 + 固定 tick + 推送 + 客户端插值」让物理/游戏逻辑只存在于一处，
模拟快慢与客户端数量/帧率无关，客户端换引擎也不影响逻辑。

## 核心机制

### 玩家角色控制器

- 使用 Jolt `CharacterVirtual`，形状是「胶囊 + 向上平移」：
  - 胶囊半径 0.4 m，圆柱半高 0.5 m，总高 1.8 m
  - 用 `RotatedTranslatedShape` 上移 0.9 m，使形状底部位于脚底（`GetPosition()` 即脚底位置）
- 每 tick 由服务端调用 `jolt_update_character`：
  - 水平速度直接来自客户端输入（走 8 m/s，跑 14 m/s）
  - 垂直速度：着地时归零、跳跃设 8 m/s、再叠加重力积分
  - `ExtendedUpdate` 负责碰撞、贴地、上台阶
- 角色不可被推动/挤开：`CharacterImmovableListener`（包装层）对动态刚体接触
  禁用 `mCanPushCharacter`，约束速度清零后角色位置完全由输入决定；
  动态刚体通过接触冲量（`mCanReceiveImpulses`）被挡开，玩家照常能推箱子；
  静态几何保持默认，不影响贴地与上台阶
- 客户端只保留偏航/俯仰（鼠标视角），相机位置 = 服务端返回的脚底位置 + 1.6 m 眼高

### 弹丸

- 客户端下发 `shoot` 消息 → 服务端生成一枚小球：
  - 半径 0.08 m，初速 60 m/s
  - `EMotionQuality::LinearCast`，高速下不会穿透薄墙
  - `SetUserData` 标记为弹丸
- 命中判定依赖 `PhysicsSystem` 的 `ContactListener`：
  - `OnContactAdded` 在碰撞发生时（工作线程内）把「弹丸 BodyID + 目标 BodyID」写入受互斥锁保护的队列
  - 每 tick 结束后服务端 `jolt_poll_projectile_hits` 排空队列
- 命中后处理：命中靶球 → 销毁并加分；命中敌人 → 扣血，血尽销毁并重生；命中其他 → 移除弹丸
- 弹丸最多存活 60 tick（20 Hz 下 3 秒），超时自动移除

### 敌人 AI（PVE 波次）

- 敌人是动态胶囊刚体（半径 0.35 m，半高 0.5 m，初始 3 点血），视觉上是卡通圆滚滚怪物
- 每 tick `updateEnemies` 把敌人的水平速度设为「指向玩家的单位向量 × 2.2 m/s」，
  低难度：怪物贴身（1.4 m 内）每 tick 扣 0.4 点血（即 8/s），不会推动玩家
- 波次规则：第 1 波 3 只，之后每波 +1，最多 6 只；场上清空 2 秒后刷下一波
- 敌人死亡即被移除（不再立即重生），并在死亡位置掉落一枚金币

### 资源（金币）

- 金币是纯逻辑对象：不进物理世界、不参与碰撞，只在快照的 `resources` 中下发
- 初始 6 枚随机撒在地图（离出生点 4 m 外）；击杀掉落，场上上限 10 枚
- 每 tick 做距离判定（0.8 m），玩家走近即移除并 `gold++`
- 客户端渲染为金色双盘（自转 + 浮动动画），消失时播放拾取音效

## 关键设计决策与坑

### Jolt 必须和包装层用同一套编译参数

Jolt 的 Release 构建定义 `NDEBUG`、`JPH_DEBUG_RENDERER`、`JPH_PROFILE_ENABLED`
以及 CPU 指令集宏（`JPH_USE_AVX2` 等）。如果包装层自己手写编译命令、漏掉这些宏，
会出现两类问题：

- 缺 `NDEBUG` → `JPH_DEBUG` 被开启 → 引用 `JPH::AssertFailed` 但库内没定义 → 链接失败
- 指令集宏不一致 → 内联函数 ABI 不一致 → 链接或运行期错误

因此 `joltgo/CMakeLists.txt` 用 `add_subdirectory` 把 Jolt 作为子项目引入，
编译宏和指令集标志自动继承，避免手工维护。

### DLL 静态链接 C++ 运行库

`libjolt_c.dll` 编译时加了 `-static`，把 libstdc++ / libgcc / winpthread 静态链入，
所以运行时只需 `libjolt_c.dll` 一个文件，不需要 MSYS2 的 DLL 伴生。

### UCRT64 与 MINGW64 不能混用

项目统一使用 UCRT64 工具链（`C:\msys64\ucrt64\bin`）。`build.ps1` 会在编译包装层时
把 PATH 切到 `/ucrt64/bin`，并在 `go build` 时显式设置 `CC`/`CXX` 指向 UCRT64 编译器。
混用 MINGW64 会因 C 运行时不同（ucrtbase vs msvcrt）导致链接或运行异常。

### 服务端锁与广播

- `game.go`：全局 `sync.Mutex` 保护物理世界与游戏状态；公开方法
  （`ApplyInput` / `Shoot` / `Reset` / `Step` / `Snapshot`）内部加锁，
  内部 `xLocked` 方法要求调用方已持有锁，tick 循环与 WebSocket 读线程可安全并发
- `ws.go`：hub 用独立互斥锁保护客户端集合；广播发送不出去的慢客户端直接断开，
  不能拖慢 20 Hz 模拟
- WebSocket 实现是纯标准库手写的 RFC 6455：单帧读写、无掩码出站/掩码入站、
  不支持分片（双方消息都很小），满足本地 demo 需求而不引入依赖

### 客户端插值

- 快照双缓冲（`_prev_snap` / `_next_snap`）：tick 连续（step +1）时滚动，跳号时清空
- 渲染 alpha = 距新快照到达时间 / 0.05s，位置 lerp、四元数 slerp，
  渲染滞后一个 tick 平滑 20 Hz 数据
- 新生成/移除的刚体不参与插值：按最新快照直接创建或删除（弹丸消失有爆闪特效兜底）
- `FpsClient`（传输层）与渲染层通过信号解耦（`state_received` / `connection_changed`）

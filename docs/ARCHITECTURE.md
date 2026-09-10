# 架构说明

## 分层（分布式微服务）

```text
┌──────────────────────────────────────────────┐
│  Godot 客户端（godot_client/）                │
│  main.gd      输入/相机/双玩家渲染/HUD/音效   │
│  fps_client.gd  传输层：pomelo 握手/匹配/编解码│
│  body_entity.gd 每个刚体一个渲染节点          │
└──────────────────────┬───────────────────────┘
                       │ WebSocket（pomelo 帧 + protobuf payload）
┌──────────────────────▼───────────────────────┐
│  gate（frontend）                             │
│    pitaya acceptor / agent / session          │
│    gate.go：AddRoute 路由                     │
│      match.match.join → match 节点（轮询）    │
│      game.game.* → 定点 game 节点（会话数据） │
└──────────┬──────────────────┬────────────────┘
           │ NATS RPC         │ NATS RPC
┌──────────▼─────────┐  ┌─────▼────────────────────┐
│  match（backend）   │  │  game（backend）          │
│  配对队列          │  │  实例注册表（uid→Instance）│
│  分配 game 节点     │  │  instance.go：★每局一 goroutine│
│  RPC game.create   │  │   顺序执行、无锁          │
└────────────────────┘  │  sim/ ECS 模拟层（双玩家）│
                        │  ecs/ ECS 核心            │
                        │  physics/ cgo 物理桥      │
                        └──────────┬───────────────┘
                                   │ cgo
                        ┌──────────▼───────────────┐
                        │  C 包装层（wrapper/）      │
                        │  纯物理桥（双角色）        │
                        └──────────┬───────────────┘
                                   │ C++ API
                        ┌──────────▼───────────────┐
                        │  Jolt Physics（libjolt_c.dll）│
                        └──────────────────────────┘
```

三个服务由**单二进制** `joltgo.exe` 用 `-type gate|match|game` 区分角色，经
**etcd**（服务发现）+ **NATS**（RPC）互相通信，见 `joltgo/deploy/`。

## 对局实例模型（★ 无锁核心）

`game/instance.go` 是本次架构的核心：**每个对局一个 goroutine，内部顺序执行、不加锁**。

- 一个匹配（matchId）对应一个 `Instance`，构造时 `sim.New(physics.New())` 各建一个
  独立 Jolt 世界（互不共享）。
- `Instance.run()` 是唯一访问 sim 的 goroutine：一个 select 循环消费
  - **命令 channel**（`cmds`）：输入/射击/重置，由 RPC handler 经 `enqueue` 投递；
  - **20 Hz ticker**：`sim.Step()` + 广播同步帧（多数槽位发增量，待全量槽位发 full）；
  - **stop**：退出并释放物理世界。
- 因为只有这一条 goroutine 访问 sim，`sim.Simulation` **去掉了 `sync.Mutex`**——并发
  安全由「单线程所有」这一模型保证，输入命令在 channel 上自然串行化。
- `game.Component` 的实例注册表（uid → Instance）由 RPC handler 跨 goroutine 共享，
  用一把 `sync.Mutex` 保护（这只是注册表查找，不是游戏逻辑）。

## 服务端 ECS 架构

服务端游戏逻辑用「实体-组件-系统」（ECS）组织，核心在 `ecs/`（零依赖、可单测），
玩法层在 `sim/`：

- **实体（Entity）**：只是 `uint32` ID，由 Go 桥（`physics/physics.go`）从 1 递增发放，
  桥层维护实体 id ↔ Jolt BodyID 的双向映射（Jolt 原生 BodyID 会与 ECS 的
  InvalidEntity 约定冲突，不能直接透传）；纯逻辑实体（玩家）由 ECS 世界
  从 `1<<24` 起分配，两个 ID 空间不重叠。销毁的逻辑实体 id 进入回收池供
  `NewEntity` 复用，销毁行不在空 archetype 里累积
- **组件（Component）**：纯数据，按 archetype（原型）存储——组件集合（类型集）
  相同的实体归入同一 archetype，组件数据按列（SoA）连续存放、行号即实体下标；
  组件类型注册为整数 ComponentID，列按下标索引（热路径无 reflect.Type 哈希
  查找），archetype 按组件 ID 序列键 O(1) 查找。
  Add/Remove 组件时实体整体搬到目标 archetype（公共列复制、目标列补零、源行
  swap-remove，代价 O(组件数)）；`Add2/Add3/Add4` 批量挂载只搬一次家、不产生
  中间 archetype（spawn 路径用）。查询匹配在 archetype 粒度完成：`Without[U]` 追加排除
  条件，`QueryEach3` 三列行内直取 Body/Position/Rotation（每 archetype 只绑定一次
  列指针，无逐行查找），查询缓存匹配结果、新 archetype 出现时自动失效重建。
  规模模拟（`ecs/scale_test.go`）验证到 100+ archetype、10000 实体：
  `Position` / `Rotation` / `Body`（形状/尺寸/静态/活跃/材质，同步给客户端的渲染元数据）、
  `Player` / `Input` / `Health`、`Enemy` / `Target` / `Projectile` / `Resource`
- **系统（System）**：每 tick 按固定顺序运行，只通过组件和 `Physics` 接口交互：
  输入 → 物理步进 → 变换同步 → 弹丸命中 → 接触伤害 → 弹丸过期 →
  金币拾取 → 波次推进。碰撞判定全部走物理层（弹丸用刚体接触事件、
  伤害/拾取用角色接触事件），不做距离计算
- **物理抽象**：`sim.Physics` 接口隔离 Jolt cgo 调用；`physics/` 包
  是唯一允许 cgo 的包。因此 `sim` 层用 fake 物理即可单元测试，
  系统行为不依赖真实物理引擎。包装层本身不含任何业务（见下），
  所有游戏调参（重力/跳跃/胶囊尺寸/弹丸配置/敌人参数）都在 `sim/` 常量区

### 同步协议（实体-属性帧）

服务端不再手写快照，而是把「给客户端同步什么」从 ECS 里**完全拆出来**，做成独立包
`replication/`（不 import `ecs`）；`sim/replicate.go` 是唯一知道「组件 ↔ 属性」映射的地方。
玩法层在每个**变更点就近**调用 `rep.Set(实体, 属性, 终值)`，同步层只负责去重与打包：

```text
sim/ 各系统（变更点）──rep.Set(实体, 属性, 终值)──▶ replication.Store
                                                     ├ values : [实体][属性] = 终值
                                                     ├ dirty  : 本帧脏集
                                                     └ dead/gone : 销毁 / 移除
每 tick：Drain() ─增量帧─▶ 局内多数槽位        Full() ─全量帧(带 Schema)─▶ 重连 / resync 槽位
```

- **属性表（Schema）**：属性名是扁平字符串（约定 `组件.字段`，如 `Body.Mat`），在
  `sim.Simulation.New()` 里由 `declareAttributes` **一次性声明**（当前 17 个，清单见
  [API.md](API.md)）。Schema **只随 full 帧下发**（full 帧自带一份，避免「schema 与全量帧
  分两条消息、顺序可能颠倒」的竞态）；`version` 是属性表（名字 + Kind）的 FNV-1a 哈希，
  供客户端检测前后端属性表不一致（当前客户端只缓存、不强制比对——属性表有差异也不会
  崩：不认识的属性会被忽略）。**新增一个属性不需要改 proto / 重生成 Go 码 / 改
  客户端解码** —— 客户端只按属性名取值，不认识的属性照常存下、只是不渲染。
- **值类型**：`KindF32` / `KindI32` / `KindBool` / `KindStr` / `KindVec2` / `KindVec3` /
  `KindVec4`；wire 上按族塞进 `AttrValue` 的 `f` / `i` / `b` / `s`。以后加一个 int 属性，
  协议一个字都不用动。类型与 `Declare` 时声明的不符会 panic（防「写错属性名或值的
  构造器」导致客户端按错误 Kind 解码）。
- **增量 = 终值 + 脏集**：`Set` 与「上一次下发给客户端的值」比较，不同才标脏；同一帧内
  改多次只留终值（A→B→A 则撤销脏标记，不产生流量）。因此 `syncSystem` 每 tick 给全部
  刚体（含永不变化的静态船体）写 `Pos`/`Rot` 也不会产生任何流量。`Drain()` 每 tick
  **恰好调用一次**——它负责清脏并推进「已下发基线」。
- **全量 = 终值表整表**：`Full()` 与 `Drain()` 产出**同一个 `Frame` 类型、同一套编码**，
  区别只有 `full=true` + 携带 Schema。「把断线期间的所有帧补上」在这里的形态就是把净效果
  压缩成终值表一次性下发。`Full()` **刻意不修改增量基线**（全量只发给单个客户端）。
- **属性存在性**：缺省即不存在。标记组件（`Enemy` / `Target` / `Projectile`）就是值恒为
  `Bool(true)` 的属性 —— 存在即有该组件；`removed` / `destroy` 是属性存在性的终点。
- **实体 id 会被复用**：`Destroy` / `Reset` 必须把已下发基线（`sent`）一并清掉，否则重建后
  刚体 id 从头复用、新实体的 `Set` 会因「与旧实体值相同」被静默抑制 —— 客户端只收到
  destroy、再也收不到重建（`Body.*` 这类只在创建时 Set 一次的属性就永久丢了）。
- **就近 `Set` 的风险**：漏写一处 `rep.Set` 不会报错、没有日志，客户端只会静默停在旧值
  （store 不反查 ECS 世界）。这是「变更时显式 Set」换 O(变化量) 的固有代价，由
  `sim/replicate_test.go` 的 oracle 测试兜底（`expectedAttrs` 从 ECS 世界独立推期望值，
  与 store 全量逐项比对）——加同步字段时先补 `rep.Set`、再补断言。
- **已知安全取舍**：`JoinMsg.token` 是持有即可冒用的一次性身份，且被直接当作会话 UID；
  生产环境应换成服务端签发、可吊销、带过期的凭证。本 demo 不做。

### 每 tick 只同步一次变换

旧实现里每个系统各自枚举 C++ 刚体（构建同步帧、敌人 AI、伤害判定、计数、命中查找，
每 tick 多达 4~5 遍全量扫描）。重构后 `syncSystem` 每 tick 只枚举一次，
把位置/旋转/活跃状态写回组件并就近 `rep.Set`，其余系统全部读写 Go 侧组件。
顺序语义与旧实现完全一致：

- AI 读的是**上个 tick** 同步的位置（等价于旧实现「步进前枚举」拿到的值）；
  伤害与下发的同步帧读的是**本 tick 步进后**同步的位置（等价于旧实现「步进后枚举」）
- 两次 tick 之间创建的实体（如 RPC handler 投递的射击命令）在创建时就写入已知的
  初始位置，立即出现在同步帧里，不会闪现在原点

### 命中结算与物理桥的职责边界

- 包装层是**纯物理桥**：只暴露 Jolt 原生能力（世界/刚体/角色控制器/接触事件/
  射线），不携带任何游戏业务概念——没有敌人/弹丸/靶球/血量，没有角色移动
  策略与调参，接触事件原样上报所有刚体对
- 血量是 Go 侧 `Health` 组件，归零后直接 `jolt_remove_body` 并销毁实体；
  弹丸命中由弹丸系统从通用接触流中筛选（至少一侧带 `Projectile` 组件）
- Go 桥（`physics/physics.go`）负责 id 翻译：Jolt 原生 BodyID 透传会与 ECS 的
  InvalidEntity（0）约定冲突，桥层维护双向映射，向 sim 发放从 1 递增的实体 id
- **同步层与 ECS 解耦**：旧实现里 `bodyInfo` 的每个字段都要在这里由组件重建（`type` ←
  `Body.Kind`、`enemy/target/projectile` ← 标记组件、`health` ← `Health`）再整体下发；
  现在改成通用属性名，客户端**按名字**取值（`Body.Kind` / `Enemy` / `Projectile` / `Pos` /
  `Rot`…）。`replication/` 是独立包、不 import `ecs`，`sim/replicate.go` 是唯一知道
  「ECS 组件 ↔ 属性名」映射的地方 —— 详见上文「同步协议（实体-属性帧）」。

## 为什么用 cgo + C ABI

cgo 只能直接调用 C ABI，不能调用 C++ 类/重载/模板。因此所有 Jolt C++ 调用都被
封装成 `extern "C"` 的普通函数，参数和返回值只用：

- 整数 / 浮点数
- 定长数组（`float pos[3]` 等）
- 不透明指针（`JoltWorld *`）

禁止跨边界传递 `std::string`、`std::vector`、C++ 对象、引用或异常。

包装层是**纯物理桥**：函数只对应 Jolt 原生能力（`jolt_add_sphere` /
`jolt_set_body_velocity` / `jolt_poll_contacts` / `jolt_character_update` 等），
不带任何游戏语义。敌人/弹丸/靶球/血量、角色移动策略、所有数值调参都属于
Go 侧（sim）；包装层内部只保留两个物理层自身的状态：刚体 id 登记表（供枚举）
与接触事件队列（线程安全转发）。

## 数据流

游戏状态由服务端持有，客户端是「输入 + 展示」的瘦客户端，模拟节奏由服务端驱动：

1. 客户端连接 gate（WS）→ pomelo 握手 → 发 `match.match.join`（带持久化 `token`）进入匹配。
2. match 服务配对（2 人，或 10s 兜底单人）→ `GetServersByType("game")` 挑一个 game
   节点 → `RPCTo("game.game.create")` 让该节点创建对局实例。
3. game 节点的实例 goroutine 以固定 **20 Hz** tick 推进：消费最新输入 → 更新两个
   角色控制器 → 步进物理 → 处理弹丸命中/伤害/拾取/波次。
4. 每个 tick 结束，实例把本帧**增量**（route `onFrame`，只含变化的 (实体, 属性, 终值)）
   经 `SendPushToUsers` 通过 NATS 转发给 gate，gate 再推给局内客户端；被标记为待全量的
   槽位（重连 / resync）这一 tick 改推 **full 帧**（终值表整表 + Schema）。射击 / 重置
   不再单独补推，统一等下一 tick 的帧。
5. 客户端把增量累积进本地 `WorldStore`，60 Hz 渲染时对运动刚体和玩家位置做**影子跟随
   插值**（位置 lerp、四元数 slerp、玩家朝向 lerp_angle），让 20 Hz 数据在 60 Hz 屏幕上
   平滑。服务端不会重复推送同一 tick，客户端也不再靠快照 diff 推断「谁消失了」。
6. 客户端每渲染帧上报一条 `game.game.cmd`（输入 + 射击 + 重置**合并成一条**），
   由 gate 定点路由到托管该对局的 game 节点。
7. 连接断开后客户端每秒自动重连：重新握手 + 用**同一个 token** 发 `match.match.join`；
   match 先向各 game 节点 fan-out `game.rejoin`，命中存量实例则走与首次匹配相同的收尾
   （写会话数据 + 推 `onMatched`），客户端回到**同一对局、同一槽位**、不入匹配队列；
   客户端随后发 `game.resync` 请求 full 帧把本地世界整体重建（未命中则按新玩家重新匹配）。

这种「服务端权威 + 固定 tick + 推送 + 客户端插值」让物理/游戏逻辑只存在于一处，
模拟快慢与客户端数量/帧率无关，客户端换引擎也不影响逻辑。

## 核心机制

### 场景地图（运输船）

参考穿越火线「运输船」的一艘长甲板货船，坐标 x 为船宽（±13）、z 为船长
（±22）、甲板面 y = 0。布局全部写在 `sim/map.go` 的**部件表**里，`Simulation.init`
照表搭物理刚体：

- **船体**：甲板钢板 + 左右舷外板 + 艏艉横舱壁（围板的内侧面落在甲板范围内，
  不留可掉出去的缝；高度高过跳跃高度）
- **艏艉两个出生区**：玩家分别出生在 z = +18 / -18，开局面朝船中，
  出生区前方有掩体墙（中间留中央通道、两侧留舷侧绕后路线）
- **中部集装箱堆**：1.4 m → 2.4 m 的两级台阶，靠跳跃（跳跃高度约 1.8 m）就能爬上去
- **两舷高架走道**：面高 2.4 m、通长 26 m，各有一段舷梯上去，走道外侧有栏杆
  （内侧敞开便于跳下甲板）
- **甲板掩体**：舷侧长集装箱、木箱（动态）、烟囱、桅杆、系缆桩、悬浮靶球

两条不变量由代码与测试共同保证：

- **180° 旋转自映射**：部件表里带 `mirror` 的部件会自动补上 `(x,y,z) → (-x,y,-z)`
  的孪生体，改图只写半边；`sim/map_test.go` 逐件验证对称性，两个出生点因而完全等价
- **刷怪/刷资源不卡进掩体**：`mapBlocks(x,y,z,margin)` 是纯 Go 的几何判定
  （不依赖物理引擎，可单测），刷怪与刷金币都先查一次，避免怪物卡在集装箱里、
  金币刷进箱体拾不到

舷梯参数有硬约束：单级抬升 ≤ 0.4 m（Jolt `ExtendedUpdateSettings::mWalkStairsStepUp`）、
踏步进深 ≥ 0.5 m（必须大于角色半径 0.4 m，否则 `WalkStairs` 抬腿后会被再下一级绊住，
角色永远卡在最低一级）。这两条写进了 `map_test.go`，并有跑真引擎的
集成测试 `physics/map_integration_test.go`（`go test -tags joltdll ./physics`）
真的把角色从出生点走到走道上，防止改参数改坏手感。

同步属性 `Body.Mat` 给每个刚体一个材质号（`sim/map.go` 的 `Material`），客户端
（`body_entity.gd` 的 `MATS` 表）据此配色：甲板/船体/四色集装箱/走道格栅/栏杆/
木箱/钢构件/踏步。它只是配色提示，不参与任何物理或玩法判定；客户端遇到不认识的
材质号会退回默认配色，因此新增材质号向后兼容。

### 玩家角色控制器

- 每局两个玩家，各用一个 Jolt `CharacterVirtual`（形状是「胶囊 + 向上平移」，charIdx
  0/1 区分）：
  - 胶囊半径 0.4 m，圆柱半高 0.5 m，总高 1.8 m
  - 用 `RotatedTranslatedShape` 上移 0.9 m，使形状底部位于脚底（`GetPosition()` 即脚底位置）
  - 形状、出生点（两人分别出生在船的艏/艉）、重力、跳跃速度全部是 Go 侧（sim）的调参，
    通过 `jolt_character_create` 与 `jolt_set_gravity` 传入；包装层不携带任何角色参数
- 每 tick 由输入系统（`sim/inputSystem`）遍历两个玩家实现移动策略后交给物理层执行：
  - 水平速度直接来自各自客户端输入（走 8 m/s，跑 14 m/s）
  - 垂直速度：着地时归零、跳跃设 8.5 m/s、再叠加重力（-20 m/s²，比真实重力大，
    跳起/落地更快、手感更利落）
  - `ExtendedUpdate`（`jolt_character_update`）负责碰撞、贴地、上台阶
- 角色不可被推动/挤开：这是游戏规则，由 sim 通过 `SetCharacterDynamicPush(false)`
  配置——包装层只暴露「动态刚体能否推动角色」这个物理级开关
  （对应 Jolt `CharacterContactSettings::mCanPushCharacter`）。
  禁用后动态刚体通过接触冲量（`mCanReceiveImpulses`）被挡开，玩家照常能推箱子；
  静态几何保持默认，不影响贴地与上台阶
- 每个角色接触监听器把**该角色接触到的刚体 id**记录进队列（接触建立时 +
  每步接触求解时），Go 侧每 tick 按角色轮询——贴身伤害（敌人）与金币拾取（传感器球）
  都从这里判定，不写任何距离计算
- 客户端只保留偏航/俯仰（鼠标视角），相机位置 = 服务端返回的本地玩家脚底位置 + 1.6 m 眼高

### 弹丸

- 客户端下发 `shoot` 消息 → 服务端生成一枚小球（物理配置全部由 sim 设置）：
  - 半径 0.08 m，初速 60 m/s
  - `EMotionQuality::LinearCast`（`jolt_set_body_motion_quality`），高速下不会穿透薄墙
  - 摩擦/弹性置 0；「这是弹丸」是 Go 侧 `Projectile` 组件，物理层不区分
- 命中判定依赖 `PhysicsSystem` 的 `ContactListener`：
  - 包装层把**所有**刚体接触对原样写入受互斥锁保护的队列（纯物理事实，
    不区分弹丸/敌人/箱子）
  - 每 tick 结束后服务端 `jolt_poll_contacts` 排空队列，弹丸系统挑出至少一侧
    带 `Projectile` 组件的接触对进行结算，其余（箱子落地等）忽略
- 命中后处理：命中靶球 → 销毁并加分；命中敌人 → 扣血，血尽销毁并掉落金币；命中其他 → 移除弹丸
- 弹丸最多存活 60 tick（20 Hz 下 3 秒），超时自动移除

### 敌人（PVE 波次）

- 敌人是**静态**胶囊刚体（半径 0.35 m，半高 0.5 m，初始 3 点血），视觉上是卡通圆滚滚怪物
- **怪物不移动**：没有追击 AI，也不可被推动/击退——出生后原地待机，是固定「地雷」
- **伤害是物理接触判定**：角色的 CharacterContactListener 接触事件（接触求解每步触发）
  里出现敌人刚体即扣血，低难度每 tick 每只 0.4 点（即贴着怪物 8/s），
  不做任何距离计算
- 波次规则：第 1 波 3 只，之后每波 +1，最多 6 只；场上清空 2 秒后刷下一波
- 敌人血量是 Go 侧 `Health` 组件（3 点，被命中 3 次死亡）；死亡即被移除
  （不再立即重生），并在死亡位置掉落一枚金币

### 资源（金币）

- 金币是**Jolt 传感器球**（`mIsSensor = true`，半径 0.6 m，悬浮在 0.8 m 高）：
  不参与刚体碰撞响应（弹丸/箱子直接穿过），但角色控制器的碰撞查询会检测到它，
  角色接触到即拾取（拾取半径 = 角色半径 0.4 + 传感器半径 0.6 ≈ 水平 1 m，
  与旧距离判定手感一致）
- 初始 6 枚随机撒在地图（离出生点 4 m 外）；击杀掉落，场上上限 10 枚
- 金币在同步层里也是普通刚体（`Body.*` + `Pos` + `Resource.Kind`），客户端渲染时按
  `Resource.Kind` 把它从刚体渲染里排除、单独渲染金币节点；拾取即移除刚体并 `gold++`
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

### 服务端锁与广播（分布式模型）

- `sim.Simulation` **无锁**：由对局实例 goroutine（`game/instance.go`）独占驱动。
  输入/射击/重置经命令 channel 投递，同一 goroutine 顺序消费；20 Hz tick 也在同一
  goroutine 里，因此不需要任何互斥。并发安全由「单线程所有」模型保证。
- `ecs.World` 自身不加锁，由实例 goroutine 串行化。
- `game.Component` 的实例注册表（`uid → Instance`、`matchId → Instance`）跨 RPC
  handler 共享，用一把 `sync.Mutex` 保护——这只是注册表查找，不含游戏逻辑。
- 广播：实例每 tick 先 `sim.DrainFrame()`（**恰好一次**，它负责清脏并推进已下发基线），
  再按槽位分发 —— 多数槽位收增量帧，`pendingFull` 槽位（重连 / resync）收 `FullFrame()`。
  两路都调 `app.SendPushToUsers("onFrame", frame, uids, "gate")`，pitaya 经 NATS 用户
  频道转给 gate，gate 再推给对应会话。每个对局的 uids 在 `game.create` 时固定，实例
  退出即停止广播。
- 实例自退：所有槽位 60s 无上行消息即结束（`instanceIdleTimeout`，远大于客户端 1s 重连 +
  2.5s 看门狗）；`forget` 只在 `uidToInst[uid]` 仍指向该实例时才摘除映射 —— 玩家可能已经
  匹配进新对局，无脑删会把新对局的 uid 映射一起抹掉。
- 慢客户端：pitaya agent 每连接一个写者 + 有界发送缓冲，写不出去则断开连接，
  不会拖慢 20 Hz 模拟。
- 服务间通信：etcd（服务发现，60s 租约）+ NATS（RPC）。route 三段式
  `server.service.method`（`match.match.join` / `game.game.cmd` / `game.game.resync`）。

### 客户端插值

- 世界状态在 `WorldStore`（`world_store.gd`）里按需累积：实体 ID → { 属性名: 值 }，
  按**名字**取值，不认识的新属性照常存下、只是不渲染。full 帧先清空再整体覆盖，
  因此「首次进入 / 重连 / 乱序」在应用层没有区别。
- 插值状态由渲染层自己持有（`main.gd`）：保留每个刚体的**上一帧变换**作为插值起点
  （`_prev_body_xform`，`id → {pos, quat}`），收到新帧时把当前变换整份拷进去（`_body_xform`），
  渲染时在「上一帧 → 本帧」之间按 alpha = 距本帧到达时间 / 0.05s（`TICK`）做 lerp / slerp
  （渲染滞后一个 tick）。
- 玩家位置与远端朝向同样是 prev→target 插值（`_prev_player_pos` / `_player_pos_target`…），
  显示值与服务端下发值**分开存**：`_render_interpolated` 一帧内会被调两次（`_process`
  一次、`_on_frame` 收尾一次），显示值不能同时充当插值输入（否则第二次会拿结果再插一次）。
- 旧实现的双缓冲（`_prev_snap` / `_next_snap`）与 `_snap_sig` / `_frame_keeps_bodies`
  去重逻辑**整块删除**：增量协议下服务端不会重复推送，也不需要靠快照 diff 推断「谁消失了」
  —— 消失由 `destroy` / `removed` op 显式下发，销毁事件还带着消失前的属性快照，渲染层
  据此区分「弹丸爆闪」还是「金币拾取音」。
- 新生成 / 移除的刚体不参与插值：按最新帧直接创建或删除（弹丸消失有爆闪特效兜底）。
- 双玩家：按属性 `Player.Idx` 找槽位；本地玩家（player_idx）第一人称视角，
  远端玩家渲染 avatar。
- `FpsClient`（传输层）与渲染层通过信号解耦（`frame_received` / `matched_received` /
  `connection_changed`）。

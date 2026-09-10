# 实体-属性增量同步与断线回局

> 状态：设计已确认，待实施
> 日期：2026-09-10

## 1. 背景与目标

### 现状

服务端每 tick（20 Hz）向局内所有客户端推送**全量快照** `protos.Snapshot`：
`bodies`（150+ 个刚体，其中船体静态几何占绝大多数且永不变化）+ `resources` +
`players` + 顶层字段 `step/score/wave/gold`。

这些字段是**手写快照定义**的产物。代价有三层：

1. 客户端"重连恢复状态"这件事，本质上是靠下一帧全量快照重建的 —— 能work，但
   完全绑死在快照定义上。
2. 每新增一个 ECS 组件，就要动 **一整条链**：`game.proto` 加字段 → 重新生成 Go 码 →
   `sim/state.go` 加结构体字段 → `sim/snapshot()` 填值 → `game/component.go` 的
   `toSnapshot` 转换 → `fps_client.gd` 手写 protobuf 解码 → `main.gd` 渲染。
3. 每 tick 重发永不变化的静态几何，带宽被浪费在不变的数据上。

而且"重连"目前名不副实：`match.Join` 每次都发新的 `nuid` 当 uid，会话数据里的
`gameServerId` 随旧连接消失，客户端只能重新进匹配队列、开一个全新实例
（`wave`/`score`/场上实体全部从头开始）。

### 目标

1. **加 ECS 组件/属性不再需要新增"快照定义"**：没有 proto 改动、没有 Go 结构体改动、
   没有客户端解码改动。客户端不认识的新属性照常收到、只是不渲染。
2. **消息以帧为最小单位**，同一帧的消息合并成一条发送。
3. **只同步被修改的实体属性**；同一帧内同一属性被改多次，只同步最新终值一次。
4. **重连回到同一局**，断线期间服务端照常模拟；重连时把"过去所有帧"压缩成一次全量
   下发，与实时增量走**同一套机制、同一种编码**。

### 非目标

- 兴趣管理 / 区域裁剪（所有客户端收到同一份增量）。
- 断线期间的帧日志回放（已决定用"终值压缩"替代，见 §3.3）。
- 属性级"移除"的玩法路径（API 与协议都留好，当前无调用点）。
- 生产级身份安全（token 持有即可冒用，见 §7.5）。

## 2. 总体结构

核心是把"给客户端同步什么"从 ECS 里**完全拆出来**，做成一个独立结构体：
`replication.Store`。它不 import `ecs`，只持有

- `[实体ID][属性ID] = 属性终值` 的键值表，
- 本帧被改动过的 (实体, 属性) 脏集。

**标脏由开发者显式调用 `Set(实体, 属性, 终值)` 完成**，不依赖 ECS 的写入拦截、
不需要 `ecs` 加任何 API，也不需要反射遍历。

```
┌──────────────────────────────────────────────────────────┐
│  sim/  玩法层（每 tick 顺序执行）                          │
│    inputSystem / syncSystem / projectileSystem / …        │
│      │  在变更点显式调用                                  │
│      ▼                                                    │
│  replication.Store ── 终值表 + 本帧脏集（不 import ecs）   │
│      │                                                    │
│      ├── Drain() → Frame{full:false}   实时增量            │
│      └── Full()  → Frame{full:true}    重连全量（同一类型） │
└──────────────────────┬───────────────────────────────────┘
                       │ game/component.go 转 protobuf
                       ▼
        onFrame（增量广播） / onFrame（全量，单发）
```

`ecs/` **零改动**。

## 3. `replication.Store`

新包 `joltgo/replication`，只依赖标准库。

### 3.1 值类型

不做 `any` 装箱 —— 每 tick 会调用上千次 `Set`，接口装箱会造成持续堆分配。
用紧凑联合体：

```go
type Kind uint8

const (
    KindF32 Kind = iota // 1 个 float
    KindI32             // int32
    KindBool            // bool
    KindStr             // string
    KindVec2            // 2 个 float
    KindVec3            // 3 个 float
    KindVec4            // 4 个 float
)

type Value struct {
    kind Kind
    f    [4]float32
    i    int32
    s    string
}

func F32(v float32) Value
func I32(v int32) Value
func Bool(v bool) Value
func Str(v string) Value
func Vec2(x, y float32) Value
func Vec3(x, y, z float32) Value
func Vec4(x, y, z, w float32) Value
```

### 3.2 属性声明

属性名是**扁平字符串**，约定用 `组件.字段` 形式便于阅读（如 `"Body.Mat"`），
客户端只当它是不可解释的名字。

属性表在 `Simulation.New()` 时**一次性声明**（名字 + 类型）：

```go
// sim/replicate.go
func declareAttributes(rep *replication.Store) {
    rep.Declare("Pos", replication.KindVec3)
    rep.Declare("Rot", replication.KindVec4)
    rep.Declare("Health", replication.KindF32)
    rep.Declare("Facing", replication.KindF32)
    rep.Declare("Player.Idx", replication.KindI32)
    rep.Declare("Body.Kind", replication.KindI32)
    rep.Declare("Body.Size", replication.KindVec3)
    rep.Declare("Body.Static", replication.KindBool)
    rep.Declare("Body.Active", replication.KindBool)
    rep.Declare("Body.Mat", replication.KindI32)
    rep.Declare("Enemy", replication.KindBool)
    rep.Declare("Target", replication.KindBool)
    rep.Declare("Projectile", replication.KindBool)
    rep.Declare("Resource.Kind", replication.KindI32)
    rep.Declare("Game.Score", replication.KindI32)
    rep.Declare("Game.Wave", replication.KindI32)
    rep.Declare("Game.Gold", replication.KindI32)
}
```

**为什么需要声明（而不是首次 `Set` 时惰性登记）**：schema 必须完整、稳定。
惰性登记会让"schema 下发之后才第一次出现的属性"对客户端不可解码。声明只是
一份"名字 + 类型"表 —— 不改 proto、不改客户端、不重新生成任何东西，和老的
"快照定义"不是一个量级的东西。`Set` 一个未声明的属性是编程错误，在 dev/test
下直接 panic。

### 3.3 公开 API

```go
type Store struct {
    values map[uint32]map[uint32]Value // [实体ID][属性ID] = 属性终值
    dirty  map[uint32]map[uint32]bool  // 本帧被改过的 (实体, 属性)
    dead   map[uint32]bool             // 本帧销毁的实体
    // …属性名字/类型表（由 Declare 建立）
}

func (s *Store) Declare(name string, k Kind)         // 声明属性，分配属性 ID
func (s *Store) Set(id uint32, attr string, v Value) // 写终值；与旧值不同才标脏
func (s *Store) Remove(id uint32, attr string)       // 移除单个属性（当前无调用点）
func (s *Store) Destroy(id uint32)                   // 销毁实体（标销毁并清空其属性）
func (s *Store) Reset()                              // 清空终值/脏集（对局 Reset 用；保留属性声明）

func (s *Store) Schema() Schema                      // 属性表（名字/类型/版本哈希）
func (s *Store) Drain() Frame                        // 取本帧增量并清脏
func (s *Store) Full() Frame                         // 取全量（终值表整表），full=true
```

需求 2/3/4 在这个结构里都是自然结果：

- **同一帧改多次只同步最新** —— `values` 存终值、`Set` 覆盖式写入。一帧内 `Set`
  十次，`dirty` 里只有一条，`Drain` 出来的是终值。同一帧内先改 A 再改回 A（与
  帧初相同）则完全不产生条目。
- **只同步被修改的属性** —— `Set` 与旧值逐字段比较，相同则不标脏。这同时解决了
  `syncSystem` 每 tick 给**全部**刚体（含永不变化的船体静态几何）写 `Position`/
  `Rotation` 的问题：值没变就不脏。
- **重连 = 全量** —— `Full()` 就是 `values` 整表，与 `Drain()` 产出**同一个 `Frame`
  类型、同一套编码**。"把过去所有帧同步一次"在这里的形态是：所有帧的净效果被
  压缩成终值表，一次性下发。

**输出确定性**：`Drain()` / `Full()` 必须按实体 ID 升序、属性 ID 升序输出
（map 迭代无序，需显式排序），保证可测试、可对比、字节稳定。

### 3.4 属性存在性

缺省即"不存在"。客户端的本地 store 是 `{属性ID: 值}` 的映射，属性不在映射里
就代表该实体没有这个属性。因此：

- 标记组件（`Enemy` / `Target` / `Projectile`）表示为值恒为 `Bool(true)` 的属性，
  **存在即有该组件**。
- `destroy` / `removed` 是属性存在性的终点。

## 4. `sim/` 侧的属性映射与调用点

**所有 ECS ↔ 属性 的耦合都限制在 `sim/replicate.go` 一个文件里。**

### 4.1 实体与属性的对应

| 实体                          | 属性                                                                     |
| ----------------------------- | ------------------------------------------------------------------------ |
| 玩家（逻辑实体，id ≥ 1<<24）  | `Player.Idx` `Pos` `Health` `Facing`                                     |
| 刚体（物理实体，id ≥ 1）      | `Body.Kind` `Body.Size` `Body.Static` `Body.Active` `Body.Mat` `Pos` `Rot` |
| 怪物                          | 上述刚体属性 + `Enemy` + `Health`                                        |
| 靶球                          | 上述刚体属性 + `Target`                                                  |
| 弹丸                          | 上述刚体属性 + `Projectile`                                              |
| 金币（物理传感器球）          | 上述刚体属性 + `Resource.Kind`                                           |
| 单例实体（对局全局状态）      | `Game.Score` `Game.Wave` `Game.Gold`                                     |

### 4.2 `Set` 调用点（就近调用）

| 位置                        | 写入的属性                                     |
| --------------------------- | ---------------------------------------------- |
| `init()` 玩家创建           | `Player.Idx` `Pos` `Health` `Facing`           |
| `init()` 单例实体创建       | `Game.Score` `Game.Wave` `Game.Gold`           |
| `registerBody()`            | `Body.*` `Pos` `Rot`                           |
| `spawnEnemy()`              | `Enemy` `Health`                               |
| `spawnResource()`           | `Resource.Kind`                                |
| `syncSystem()`              | `Pos` `Rot` `Body.Active`（物理回写）           |
| `inputSystem()`             | `Facing`（玩家朝向）                           |
| `projectileSystem()`        | `Health`（敌人扣血）、`Game.Score`             |
| `enemyDamageSystem()`       | `Health`（玩家扣血）、复活时的 `Pos` `Health`  |
| `resourceSystem()`          | `Game.Gold`                                    |
| `waveSystem()`              | `Game.Wave`                                    |
| `destroyBody()`             | `rep.Destroy(id)`                              |

### 4.3 就近调用带来的风险与兜底

**漏写一处 `Set` 就是静默不同步** —— store 与世界就此分叉，而且 store 自己发现
不了（`Full()` 拿到的也是同一份错误数据）。这是选择"就近调用"换 O(变化量) 时
必须接受的代价，用两条测试兜底：

1. **oracle 一致性测试**（§10.1）：以 ECS 世界为 oracle，逐项比对 store 内容。
   任何漏写的 `Set` 都会在这里炸。
2. **新建实体的属性集由 `registerBody()` 单点负责**，刷怪/刷金币/发射弹丸全部
   走它，避免"每个创建路径各写一遍"。

## 5. 组件边界调整

为了让"实体 + 属性"成为框架里的唯一概念，消掉所有特例：

1. **`Player{}` → `Player{Idx int}`** —— 客户端据此认出"哪个实体是我"
   （现在靠 `players` 数组下标 + `MatchResult.player_idx`）。
2. **新增 `Facing{Yaw float32}`，从 `Input` 拆出** —— `Input` 是客户端上行数据，
   不该回灌给客户端；朝向才是需要下发的。`inputSystem` 负责把 `Input.Yaw` 写进
   `Facing`。
3. **新增 `GameState{Score, Wave, Gold int32}`，挂在一个单例实体上** ——
   计分/波次/金币本来就不是实体属性，现在是快照的顶层字段。做成组件后，框架里
   不再有"顶层字段"这个概念，以后加全局状态也自动同步。实体用 `ecs.NewEntity()`
   分配，id 不固定。

`step` **保留在 frame 头**：它是传输元数据（帧号），不是世界状态。

## 6. Wire 协议

`game/protos/game.proto` 中 `Snapshot` / `BodyInfo` / `ResourceInfo` /
`PlayerState` **全部删除**，替换为通用消息：

```proto
// 属性表。服务端启动时声明，客户端在 full 帧里收到一份。
message SchemaField { uint32 id = 1; string name = 2; int32 kind = 3; }
message Schema     { repeated SchemaField fields = 1; uint32 version = 2; }

// 单个属性的值。按 schema 声明的 kind 取值。
message AttrValue {
  uint32 id = 1;         // 属性 ID（对应 SchemaField.id）
  repeated float f = 2;  // KindF32(1 项) / KindVec2(2) / KindVec3(3) / KindVec4(4)，packed
  int32  i = 3;          // KindI32
  bool   b = 4;          // KindBool
  string s = 5;          // KindStr
}

message EntityDelta {
  uint32 id = 1;
  bool   destroy = 2;            // 实体销毁
  repeated uint32 removed = 3;   // 不再存在的属性 ID
  repeated AttrValue set = 4;    // 新增或变更的属性（新值）
}

message Frame {
  int32  step = 1;               // 帧号（= sim 的 tick 计数）
  bool   full = 2;               // true = 全量帧：客户端先清空本地 store 再整体覆盖
  repeated EntityDelta entities = 3;
  Schema schema = 4;             // 仅 full 帧携带
}
```

设计要点：

- **值的编码是泛型的**：浮点塞 `f`、整数塞 `i`、bool 塞 `b`、字符串塞 `s`。
  以后加一个 `int32` 属性，协议一个字都不用动。
- **`full` 帧自带 schema**，避免"schema 和全量帧分两条消息发出、顺序可能颠倒"的
  竞态。
- **没有 `added` 与 `set` 之分**：客户端把 `set` 当作"写值（不存在则创建）"，
  配合 `removed` / `destroy` 已足够表达一切。少一个字段、少一条分支。
- 客户端**只按属性名访问**（`"Enemy"` 存在即怪物、`"Body.Mat"` 决定配色），
  不认识的属性照常存进 store、只是不渲染。

## 7. 重连链路

### 7.1 稳定身份

客户端首次运行生成 UUID，存 `user://client_id.txt`，每次 `match.join` 带上
（`JoinMsg` 加 `token` 字段）。`match.Join` 把 token **直接当作 pitaya 会话 UID**
（现在是每次新建的 `nuid.New().Next()`）：`s.Bind(ctx, token)`。

已验证链路：match 是 backend → `bindInFront` → gate 的 `Sys.BindSession` →
**前端会话** `Bind(token)`；前端 `Bind` 遇到同 UID 会主动关掉旧会话、把新连接
顶上去（`third_party/pitaya/pkg/session/session.go:460-464`）。而
`SendPushToUsers` 正是按 UID 从 `sessionsByUID` 取会话，所以实例里的 `uids`
数组（= token）在重连后依然指向正确的连接 —— **广播代码一行不用改**。

### 7.2 回局查询

`match.Join` 拿到 token 后先**不回匹配队列**，fan-out 问一遍所有 game 节点：

```
RPCTo(gameNode, "game.game.rejoin", &RejoinMsg{Token: token}) → &RejoinReply{Found, MatchId, PlayerIdx}
```

命中则走与匹配成功完全相同的收尾（写会话数据 `gameServerId` + `PushToFront` +
推 `onMatched`，`player_idx` 沿用原槽位）；未命中则正常入队，等同新玩家。
demo 规模下通常只有一个 game 节点，fan-out 就是一次 RPC。

### 7.3 全量补齐（客户端驱动，无竞态）

1. 客户端收到 `onMatched` 后主动发 `game.game.resync`，并把本地置为"等全量"：
   期间**丢弃所有增量帧**。
2. 服务端把该槽位标记为待全量；下一 tick 用 `Full()` 单独推给这一个 uid
   （增量帧照常广播给其他玩家）。
3. 客户端收到 `full=true` 的帧：清空本地 store，整体覆盖应用。

即使全量帧与增量帧在途上乱序，也不会出现"半新半旧"：收到 full 之前一概丢弃
增量，收到 full 之后一概按增量应用。

首次进新对局也走同一条 `resync` 路径 —— **客户端只有一条入口**。

### 7.4 断线期间

实例本来就 20 Hz 无条件推进（`game/instance.go` 的 ticker 不看客户端是否在线），
断线期间模拟照常、推不出去的消息 pitaya 静默丢弃。**无需额外保活**。

### 7.5 已知安全取舍

token 是持有即可冒用的一次性身份。生产环境要换成服务端签发的凭证（登录时下发、
可吊销、带过期）。本 demo 不做。

## 8. 实例回收（顺带修）

现有问题：对局实例**永不回收** —— `game.Component` 只在 `Shutdown()` 时停实例，
两个玩家都走了实例还占着一个 Jolt 世界继续跑。加了回局之后这个泄漏更显眼（实例
要跨断线存活，就更需要明确"什么时候该死"）。

规则：实例记录每个槽位的最后在线时间（客户端每次发 `game.game.*` 消息时刷新），
**连续 `instanceIdleTimeout`（建议 60 s）无任何在线玩家则自行 Stop 并从注册表摘除**。
60 s 远大于客户端 1 s 重连 + 看门狗 2.5 s，正常重连不会误杀。

判定在线用"最近一次收到该玩家上行消息的时间"而不是 pitaya 的会话状态 ——
后者跨服务不可见，前者是实例本就持有的信息。

## 9. 客户端改造

### 9.1 传输层 `fps_client.gd`

- 生成/持久化 token（`user://client_id.txt`），`send_match_join()` 带上。
- 新增 `_decode_frame` / `_decode_schema` / `_decode_attr_value`；删除
  `_decode_snapshot` / `_decode_body` / `_decode_resource` / `_decode_player`。
- 新增上行 `game.game.resync`。
- 新增信号 `frame_received(frame: Dictionary)`，替代 `state_received`。
- 新增 `send_command(move, yaw, jump, shoot, reset)`：把同一渲染帧内的输入、射击、
  重置**合并成一条消息**（需求 1 的上行侧）。服务端 `game.game.input` /
  `game.game.shoot` / `game.game.reset` 三个 handler 合并为一个 `game.game.cmd`
  （`shoot` / `reset` 作为边沿字段）。

### 9.2 新增 `world_store.gd`

持有 `实体ID → {属性名: 值}` 的本地状态与 `属性ID → 属性名/类型` 的 schema。

- `apply(frame)`：`full` 帧先清空；随后按序应用 `destroy` / `removed` / `set`。
- `names(id)` / `has(id, name)` / `get(id, name)` / `entities_with(name)`。
- `apply` 后发一个"本帧有变化的实体 ID 集合"，供渲染层做场景同步。

### 9.3 `main.gd`

- `bodies` / `players` / `resources` 三个概念换成按属性名查询：
  "有 `Enemy` 属性就是怪物"、"有 `Player.Idx` 属性就是玩家"、
  自己的实体 = `Player.Idx == player_idx`。
- 每帧流程改为：**先整体应用到 store，再做一次场景同步**
  （新 id 建节点、消失的 id 删节点、其余更新）。顺序问题因此自然消失。
- 删除 `_snap_sig` / `_frame_keeps_bodies` 两个去重 hack —— 增量协议下不再需要
  "同一 tick 重复推送"的判定。
- `_detect_impacts` 改为消费 `destroy` op（弹丸消失不再靠快照 diff 推断）。
- 插值改为在节点上保留上一帧变换（`prev_pos` / `prev_quat`），不再维护
  `_prev_snap` / `_next_snap` 两份完整快照。

`body_entity.gd` 的 `sync_from(body: Dictionary)` 接口保持不变 —— 由 `main.gd` 从
组件属性组装出等价的 Dictionary，程序化建模代码零改动。

## 10. 测试

### 10.1 oracle 一致性（针对 §4.3 的风险）

每 tick 结束后，从 ECS 世界直接读出一份"应有属性集"，与 `store.Full()` 逐项比对。
任何漏写的 `Set` 都会在这里失败。这是"就近调用"方案的**主要防线**。

### 10.2 增量重建 == 全量（覆盖需求 2、3）

跑 N tick，把每 tick 的 `Drain()` 流喂给测试侧的客户端 store，最后再取一份
`Full()`，两者必须逐项相等。

### 10.3 同帧多次只同步最新（覆盖需求 2）

- 同一 tick 内对同一 (实体, 属性) `Set` 三次不同值 → `Drain()` 里只有一条，且是终值。
- `Set` 成与当前相同的值 → 不产生条目。

### 10.4 新增属性零协议改动（覆盖需求 1）

加一个 string 属性 → `Schema()` 自动多一项、帧里自动带值、不认识它的客户端忽略。
这条测试直接编码"自适应新增组件"这个目标。

### 10.5 重连

- 单测：`Simulation` 跑 N tick 后 `Drain()` 多次，再 `Full()`，验 §10.2。
- 集成：客户端 store 应用若干增量后强制清空并接收 full 帧，结果与"一直在线的
  客户端"一致。

### 10.6 客户端

Schema / Frame 解码（含 `full` 覆盖语义、`removed` / `destroy`）进
`godot_client/tests/` 现有 headless 测试。

### 10.7 保留的既有测试

`sim_test.go` 里对 `Snapshot()` 的断言改为使用**测试侧的 oracle 构造器**
（从 ECS 世界直接读出一份等价的 `State`），断言主体不变 —— 这样 800+ 行现有
行为测试基本原地保留，只换取值来源。

## 11. 影响面

| 位置                | 改动                                                                                  |
| ------------------- | ------------------------------------------------------------------------------------- |
| `joltgo/ecs/`       | **零改动**                                                                            |
| `joltgo/replication/` | **新增**：`Store` + `Frame`/`Schema` 类型 + 单测                                     |
| `joltgo/sim/`       | 新增 `replicate.go`；删 `state.go` 与 `snapshot()`；`systems.go` 各变更点加 `Set`；`Player{Idx}`、`Facing`、`GameState` 单例 |
| `joltgo/game/`      | `toSnapshot` → `toFrame`；`broadcast` → `Drain`/`Full`；`input`/`shoot`/`reset` 三 handler 合并为 `cmd`；新增 `rejoin`/`resync` handler；实例回收 |
| `joltgo/match/`     | `JoinMsg` 加 `token`；回局 fan-out RPC                                                 |
| `joltgo/gate/`      | 无改动                                                                                 |
| `joltgo/game/protos/` | 删 `Snapshot`/`BodyInfo`/`ResourceInfo`/`PlayerState`，加 `Schema`/`AttrValue`/`EntityDelta`/`Frame`；重新生成 Go 码 |
| `godot_client/`     | 新增 `world_store.gd`；`fps_client.gd` 协议层重写；`main.gd` 改为按属性名查询          |
| 文档                | `AGENTS.md` §3/§4、`docs/ARCHITECTURE.md`、`docs/API.md` 同步（wire 契约变了）         |

## 12. 风险

| 风险                                    | 处理                                                                        |
| --------------------------------------- | --------------------------------------------------------------------------- |
| 就近 `Set` 漏写导致静默不同步           | §10.1 oracle 测试；新建实体属性统一走 `registerBody()`                      |
| 全量帧体积（150+ 实体）撑爆下行缓冲     | 实测一帧大小；必要时调大 Godot `WebSocketPeer` 的 `inbound_buffer_size`，或把全量拆成多帧（协议已支持，`full` 只在第一帧置位） |
| 属性表声明与 `Set` 调用点不同步         | 未声明属性 `Set` 直接 panic（dev/test），CI 必跑                            |
| 协议不兼容（前后端必须同版本发布）      | 服务端与客户端同一次提交内升级；`Schema.version` 哈希用于落地时打印不匹配告警 |
| 重连 fan-out 随 game 节点数线性增长     | demo 规模可接受；节点多了再换成 etcd 上的 token→节点注册表                   |

## 13. 交付顺序

1. `replication` 包 + 单测（§10.3、§10.4）。
2. `sim/replicate.go` + 组件边界调整 + oracle 测试（§10.1、§10.2），同时把
   `sim_test.go` 切到 oracle 构造器（§10.7）。
3. `game/` 的 `toFrame` / `Drain` / `Full`，proto 重生成。
4. 重连链路（token / `rejoin` / `resync`）+ 实例回收。
5. 客户端 `world_store.gd` + `fps_client.gd` + `main.gd`。
6. 文档同步。

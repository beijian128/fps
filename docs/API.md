# 网络协议

客户端只连一个 WebSocket 端点：`ws://localhost:8080/`（gate 服务）。服务端是
**分布式三服务**（gate / match / game），客户端只感知 route，不感知后端节点分布。
传输走 **pitaya 的 pomelo 帧格式**（二进制），payload 用 **protobuf** 序列化
（schema 见 `joltgo/game/protos/game.proto`）。

> 改动任何 route 或改 proto 的**字段定义**，必须同步改 `joltgo/game/protos/game.proto`、
> `joltgo/game/`、`joltgo/match/` 与 `godot_client/scripts/fps_client.gd`（protobuf 编解码）。
> **但新增一个同步属性不走这条路**——属性是数据不是字段：加一行 `Declare` + 变更点
> `rep.Set` + 客户端按名字取值即可（见下文「属性表」与 `AGENTS.md` §3）。
> route 一律**三段式** `server.service.method`。

## 帧格式（pomelo）

每个 WebSocket 二进制 message 恰好封装一帧：

```text
┌──────────┬───────────────────────────┬──────────────────────┐
│ type (1B)│  data 长度 (3B 大端)       │  data                │
└──────────┴───────────────────────────┴──────────────────────┘
```

`type` 取值：

| 值 | 名称 | 方向 | 说明 |
|---|---|---|---|
| 0x01 | Handshake | 双向 | 握手请求 / 响应（data 是 JSON） |
| 0x02 | HandshakeAck | 客户端 → 服务端 | 握手确认（data 是 JSON） |
| 0x03 | Heartbeat | 双向 | 心跳保活（data 为空） |
| 0x04 | Data | 双向 | 业务消息（data 是 message 编码） |

## message 编码（Data 帧的 data）

```text
flag (1B) ─ route 长度 (1B) ─ route 字符串 ─ protobuf payload
```

- `flag = 消息类型 << 1`：Notify=0x02、Request=0x00、Response=0x04、Push=0x06
- route 采用**未压缩**形式：1 字节长度 + 字符串（服务端不设 route 字典）
- 本 demo 客户端只发 Notify、只收 Push；消息无 id
- payload 是 protobuf wire 编码（proto3），非 JSON

## 握手流程

1. 客户端连接后发 `Handshake`，data 为：
   ```jsonc
   { "sys": { "platform": "godot", "libVersion": "0.1.0", "clientVersion": "0.1.0" } }
   ```
2. 服务端回 `Handshake` 响应，data 为：
   ```jsonc
   { "code": 200, "sys": { "heartbeat": 30, "dict": {}, "serializer": "protobuf" } }
   ```
3. 客户端发 `HandshakeAck`（data `{"sys":{},"user":{}}`），会话进入 Working 状态
4. 客户端发 `match.match.join`（Notify，`JoinMsg`，带持久化 `token`）进入匹配队列
5. 之后客户端按固定间隔发 `Heartbeat` 空帧保活

## 匹配结果（Push，route `onMatched`）

match 服务配对成功后（2 人，或 10s 无第二人则单人兜底）推给客户端，payload 是
`MatchResult`：

```proto
message MatchResult {
  string match_id = 1;       // 对局 id
  string game_server_id = 2; // 分配到的 game 节点 id
  int32 player_idx = 3;      // 本客户端在局内的玩家槽位（0/1）
}
```

**重连也走这条 push**：断线后客户端用同一 `token` 重新 `match.join`，match 服务向各
game 节点 fan-out `game.rejoin`，命中存量实例时用同一条收尾路径（写会话数据 +
推 `onMatched`），客户端回到**同一对局、同一 `player_idx`**、不再进匹配队列；收到
`onMatched` 后客户端主动发 `game.resync` 请求全量帧（见下）。

## 服务端 → 客户端：同步帧（Push，route `onFrame`）

匹配成功后，game 节点以 **20 Hz** 固定节奏推进对局，每个 tick 结束把**一帧**经 gate
推给局内玩家。payload 是 `Frame`：

```proto
message Frame {
  int32 step = 1;                  // 帧号（= 服务端 tick 计数）
  bool full = 2;                   // 全量帧（重连 / resync），客户端先清空再整体覆盖
  repeated EntityDelta entities = 3; // 帧内实体按 id 升序
  Schema schema = 4;               // 仅 full 帧携带
}
```

```proto
message EntityDelta {
  uint32 id = 1;
  bool destroy = 2;          // 整个实体消失（客户端删除该实体全部属性）
  repeated uint32 removed = 3; // 移除的属性 ID（属性存在性的终点）
  repeated AttrValue set = 4;  // 本帧变化的 (属性, 终值)
}
```

**应用顺序固定为 `destroy` → `removed` → `set`。**

```proto
message AttrValue {
  uint32 id = 1;         // 属性 ID，由 Schema 还原成名字
  repeated float f = 2;  // KindF32(1 项) / KindVec2(2) / KindVec3(3) / KindVec4(4)
  int32 i = 3;           // KindI32
  bool b = 4;            // KindBool
  string s = 5;          // KindStr
}
```

```proto
message SchemaField {
  uint32 id = 1;   // 属性 ID（Frame 里的 AttrValue.id）
  string name = 2; // 属性名，客户端按名字取值
  int32 kind = 3;  // 值类型，与 replication.Kind 取值一致
}

message Schema {
  repeated SchemaField fields = 1;
  uint32 version = 2; // 属性表哈希（名字 + 类型），客户端据此检测前后端不一致
}
```

要点：

- **增量帧 ≠ 快照**。它只含本帧变化的 `(实体, 属性, 终值)`，是**终值**不是相对量：
  同一帧内改多次只发最后一个值；值没变（与「上一次下发的值」相同）就不产生流量——
  所以连静态几何每 tick 被写一遍也不会出现在帧里。
- **full 帧 = 终值表整表**（`full=true`，携带 Schema）。用于重连 / 首次进入 / 客户端
  主动 `resync`。客户端收到后**先清空本地世界再整体覆盖**，因此即使中间先到了几帧
  增量也会被整帧盖掉——不存在「onMatched 与 full 帧谁先到」的竞态，也不会残留
  半新半旧的状态。
- **Schema 只随 full 帧下发**（不单发），因此不存在「属性表与全量帧两条消息顺序颠倒」
  的问题。属性 ID → 名字/类型的映射在客户端缓存，之后解码增量只靠它；`version`
  供客户端检测前后端属性表是否一致（当前客户端只缓存、不强制比对）。
- 客户端**按属性名取值**，不认识的属性照常存下、只是不渲染——这就是前后端可独立
  演进的原因。

## 属性表（客户端唯一需要知道的语义清单）

属性名是扁平字符串（约定 `组件.字段`），在 `sim.Simulation.New()` 里由
`declareAttributes` **一次性声明**，随 full 帧下发。当前共 17 个：

| 属性名 | Kind | 含义 |
|---|---|---|
| `Pos` | Vec3 | 位置（刚体为质心，玩家为脚底） |
| `Rot` | Vec4 | 旋转四元数 `[x,y,z,w]` |
| `Health` | F32 | 血量（玩家 / 敌人） |
| `Facing` | F32 | 水平朝向（弧度，绕 Y 轴） |
| `Player.Idx` | I32 | 玩家槽位（0/1） |
| `Body.Kind` | I32 | 刚体形状：0 box / 1 sphere / 2 capsule |
| `Body.Size` | Vec3 | box 半边长；sphere 半径在 `[0]`；capsule 半径在 `[0]`、半高在 `[1]` |
| `Body.Static` | Bool | 是否静态刚体 |
| `Body.Active` | Bool | 是否仍在模拟（未休眠） |
| `Body.Mat` | I32 | 视觉材质号（配色提示，编号见 `sim/map.go` `Material`）；不参与物理/玩法判定 |
| `Enemy` | Bool | 存在即为敌人（标记组件） |
| `Target` | Bool | 存在即为靶球（标记组件） |
| `Projectile` | Bool | 存在即为弹丸（标记组件） |
| `Resource.Kind` | I32 | 资源类型：0 = 金币 |
| `Game.Score` | I32 | 团队共享分数 |
| `Game.Wave` | I32 | 当前波次 |
| `Game.Gold` | I32 | 团队共享金币 |

- **属性存在性**：缺省即不存在。`Enemy` / `Target` / `Projectile` 三个标记属性值恒为
  `true`，存在与否即代表该组件有无；`removed` / `destroy` 是属性存在性的终点。
- **实体 ID 空间**：物理刚体 id（1 起递增）与逻辑实体 id（玩家，从 `1<<24` 起）不重叠，
  都直接作为 `EntityDelta.id`。
- **金币也是普通刚体**：同步为 `Body.*` + `Pos` + `Resource.Kind`，客户端按
  `Resource.Kind` 把它从刚体渲染里排除、单独渲染金币节点。
- **`Body.Mat` 向后兼容**：客户端不认识某个材质号时按未标材质的默认配色渲染
  （静态钢灰 / 动态木色），因此新增材质号只需追加编号。

## 客户端 → 服务端：上行消息（Notify）

route 三段式 `server.service.method`，payload 是 protobuf。

### match.match.join —— 加入匹配队列 / 重连回局

```proto
message JoinMsg {
  string token = 1;
}
```

`token` 是客户端持久化的身份（首次运行生成 UUID 并写入 `user://client_id.txt`）。
服务端把它当作**会话 UID**：重连时同一个 token 能找回原来的对局实例（见上文
`onMatched` 的重连说明）。

### game.game.cmd —— 一帧上行命令（约 60 Hz）

输入、射击、重置**合并成一条消息**（帧是最小发送单位）。`shoot` / `reset` 是边沿
触发，未触发时为 `false`：

```proto
message CommandMsg {
  repeated float move = 1;   // [wx, wz]，世界空间水平期望速度（m/s）
  float yaw = 2;             // 水平朝向（弧度，绕 Y 轴）
  bool jump = 3;             // 跳跃边沿触发（服务端下一 tick 消费）
  bool shoot = 4;            // 射击边沿触发
  repeated float origin = 5; // [x, y, z] 枪口位置（shoot 为 true 时有效）
  repeated float dir = 6;    // [x, y, z] 射击方向（服务端会归一化，零向量忽略）
  bool reset = 7;            // 重建场景边沿触发
}
```

- `move`：世界空间水平期望速度（m/s），由客户端按相机朝向算出
- `jump` / `shoot` / `reset`：**边沿触发**——服务端仅在收到后的下一个 tick 消费，
  客户端只需在按下瞬间置 `true` 一次
- `reset`：销毁并重建整个场景（运输船地图的甲板/船体/集装箱/走道/舷梯/桅杆、木箱、
  靶球、敌人、金币），重置分数、血量、步数、波次、输入状态
- 射击 / 重置**不再单独补推**，统一等下一 tick 的同步帧

### game.game.resync —— 请求全量帧

payload 空（Notify）。客户端在重连拿到 `onMatched` 后发一次：服务端把该槽位的
**下一帧**标为全量，用 `onFrame` 单独下发一份 full 帧（含 Schema），客户端据此整体
重建本地世界。

## 服务端内部 RPC（客户端不可见）

match 与 game 节点之间走 pitaya `RPCTo`（三段式 route），payload 也在同一 proto 里：

```proto
// match → game，创建对局（route "game.game.create"）
message CreateGameMsg {
  string match_id = 1;
  repeated string uids = 2; // 按槽位顺序（下标即 player_idx），1 个 = 单人兜底局
}
message CreateGameReply { int32 code = 1; } // 0 = 成功

// match → game，回局查询（route "game.game.rejoin"）：该节点是否托管此 token 的存量实例
message RejoinMsg  { string token = 1; }
message RejoinReply {
  bool found = 1;
  string match_id = 2;
  int32 player_idx = 3; // 原本的玩家槽位（0/1）
}
```

## 波次规则（PVE）

- 第 1 波 3 只怪物，之后每波 +1，最多 6 只；清空当前波 2 秒后刷下一波
- 低难度：怪物静止不动（无追击 AI，不可推动），**接触伤害** 8/s（角色接触到怪物
  即扣血，物理接触判定，非距离判定），3 发子弹击杀一只
- 怪物不可推动玩家（玩家不可被挤开，这是服务端游戏规则：sim 通过
  `SetCharacterDynamicPush(false)` 配置，包装层只暴露物理开关）

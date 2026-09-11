# 网络协议

客户端只连一个 WebSocket 端点：`ws://localhost:8080/`（gate 服务）。服务端是
**分布式四服务**（gate / account / match / game），客户端只感知 route，不感知后端节点分布。
传输走 **pitaya 的 pomelo 帧格式**（二进制），payload 用 **protobuf** 序列化
（schema 见 `joltgo/game/protos/game.proto`）。

> 改动任何 route 或改 proto 的**字段定义**，必须同步改 `joltgo/game/protos/game.proto`、
> `joltgo/game/`、`joltgo/match/`、`joltgo/account/` 与 `godot_client/scripts/fps_client.gd`
> （protobuf 编解码）。
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

Notify（上行）与 Push（下行）**带 route**：

```text
flag (1B) ─ route 长度 (1B) ─ route 字符串 ─ protobuf payload
```

Request（上行）与 Response（下行）**带 message id（mid）**，Response **没有 route**：

```text
Request ： flag (1B) ─ mid (LEB128 变长) ─ route 长度 (1B) ─ route 字符串 ─ protobuf payload
Response： flag (1B) ─ mid (LEB128 变长) ─ protobuf payload
```

- `flag = 消息类型 << 1`：Request=0x00、Notify=0x02、Response=0x04、Push=0x06
- route 采用**未压缩**形式：1 字节长度 + 字符串（服务端不设 route 字典）
- **Response 帧里没有 route** —— 客户端靠自己发 Request 时分配的 mid 认领响应，
  所以必须自己维护 `mid → route` 表（见 `fps_client.gd` 的 `_pending`）
- `mid` 是 LEB128 变长无符号整数（每字节低 7 位有效，最高位为「还有后续字节」）
- **`flag & 0x20`（errorMask）置位时**，Response 的 payload **不是**业务消息，而是 pitaya
  的错误内容（客户端按字符串打印）。当作 `LoginReply` 解会得到垃圾字段，必须先判这一位
- payload 是 protobuf wire 编码（proto3），非 JSON
- 本项目里：帧同步与匹配结果走 Notify/Push（无 mid），`account.*` 三条路走 Request/Response

## 路由一览

| route | 类型 | 方向 | payload → 应答 |
|---|---|---|---|
| `account.account.register` | Request | 客户端 → account | `RegisterMsg` → `LoginReply` |
| `account.account.login` | Request | 客户端 → account | `LoginMsg` → `LoginReply` |
| `account.account.resume` | Request | 客户端 → account | `ResumeMsg` → `LoginReply` |
| `match.match.join` | Notify | 客户端 → match | `JoinMsg`（空） |
| `game.game.cmd` | Notify | 客户端 → game | `CommandMsg` |
| `game.game.resync` | Notify | 客户端 → game | 空 |
| `onMatched` | Push | game/match → 客户端 | `MatchResult` |
| `onFrame` | Push | game → 客户端 | `Frame` |
| `game.game.create` | RPC | match → game | `CreateGameMsg` → `CreateGameReply` |
| `game.game.rejoin` | RPC | match → game | `RejoinMsg` → `RejoinReply` |
| `gate.gate.bindgame` | RPC | match → gate | `BindGameMsg` → `BindGameReply` |
| `gate.sys.kick` | RPC | account → gate | pitaya 内置 `KickMsg` → `KickAnswer` |

`gate.gate.bindgame` 注册用的是 `RegisterRemote` 而不是 `Register`，所以它只在 remote 表里、
只能被 `RPCTo` 命中；客户端发同名 route 会被路由到 handler 池、找不到而报错。

## 握手与登录流程

1. 客户端连接后发 `Handshake`，data 为：
   ```jsonc
   { "sys": { "platform": "godot", "libVersion": "0.1.0", "clientVersion": "0.1.0" } }
   ```
2. 服务端回 `Handshake` 响应，data 为：
   ```jsonc
   { "code": 200, "sys": { "heartbeat": 30, "dict": {}, "serializer": "protobuf" } }
   ```
3. 客户端发 `HandshakeAck`（data `{"sys":{},"user":{}}`），会话进入 Working 状态
4. **登录**（Request/Response）：本地有凭证就发 `account.account.resume`，否则等玩家在登录面板上
   选择 `account.account.register` 或 `.login`。服务端回 `LoginReply`；`ok=true` 时会话已被
   `Bind` 到账号 ID，客户端保存 `token` 备下次免登录
5. 拿到 `LoginReply.ok=true` **之后**才发 `match.match.join` 进入匹配队列。
   **未登录不得发 join** —— 会话没有 uid，`match.join` 会被服务端直接忽略（没有任何应答），
   玩家会永远卡在「正在匹配…」
6. 之后客户端按固定间隔发 `Heartbeat` 空帧保活

## 账号（Request/Response，route `account.account.*`）

```proto
message RegisterMsg { string username = 1; string password = 2; }
message LoginMsg    { string username = 1; string password = 2; }
message ResumeMsg   { string token = 1; }

message LoginReply {
  bool   ok = 1;
  string token = 2;      // ok=true 时签发的凭证（base64url，43 字符）
  string username = 3;   // 服务端记录的原始大小写
  string account_id = 4; // 会话 UID
  string reason = 5;     // ok=false 时的原因码
}
```

约束与语义：

- 用户名 `^[a-zA-Z0-9_]{3,16}$`，**大小写不敏感**（占名与查名都按小写归一，`username`
  字段回的是注册时的原始大小写）；密码 6–64 字节，服务端用 bcrypt 存哈希
- `token` 是 32 字节随机数的 base64url（无 padding，43 字符），TTL **7 天**，
  每次 `resume` 成功都会续期；一次 `register` / `login` 会**轮换** token 并作废旧的
  —— 同一账号只有一个活凭证，第二个客户端登录会把第一个顶下线
- **原因码**（`reason`，服务端不给自由文本）：`bad_credentials`（用户名不存在与密码错误
  共用，不泄露账号是否存在）/ `name_taken` / `bad_username` / `bad_password` /
  `rate_limited` / `token_invalid` / `internal`
- **限流按用户名**（1 分钟 10 次），不是按 IP：account 是 backend 服务，它拿到的 pitaya
  agent 是 `Remote`，`RemoteAddr()` 返回 nil，看不见客户端 IP
- 传输层错误（会话已绑定别的账号等）走 Response 的 errorMask 或 `reason=internal`，
  客户端两条都要处理

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

**重连也走这条 push**：断线后客户端先用本地凭证 `account.account.resume` 登回同一个账号，
再发 `match.join`；match 服务向各 game 节点 fan-out `game.rejoin`，命中存量实例时用同一条
收尾路径（写会话数据 + 推 `onMatched`），客户端回到**同一对局、同一 `player_idx`**、
不再进匹配队列；收到 `onMatched` 后客户端主动发 `game.resync` 请求全量帧（见下）。

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
  uint32 version = 2; // 属性表哈希（名字 + 类型）；随 Schema 携带供诊断，客户端目前不比对
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
  随 Schema 一起携带**仅供诊断**，客户端**不比对**它——客户端容忍未知属性（见下），
  属性表不一致也不会出错，比对没有意义。
- 客户端**按属性名取值**，不认识的属性照常存下、只是不渲染——这就是前后端可独立
  演进的原因。

## 属性表（客户端唯一需要知道的语义清单）

属性名是扁平字符串（约定 `组件.字段`），在 `sim.Simulation.New()` 里由
`declareAttributes` **一次性声明**，随 full 帧下发。当前共 14 个：

| 属性名 | Kind | 含义 |
|---|---|---|
| `Pos` | Vec3 | 位置（刚体为质心，玩家为脚底） |
| `Rot` | Vec4 | 旋转四元数 `[x,y,z,w]` |
| `Health` | F32 | 血量（玩家，满血 100） |
| `Facing` | F32 | 水平朝向（弧度，绕 Y 轴） |
| `Player.Idx` | I32 | 玩家槽位（0/1） |
| `Player.Kills` | I32 | 该玩家的击杀数 |
| `Player.Deaths` | I32 | 该玩家的死亡数 |
| `Body.Kind` | I32 | 刚体形状：0 box / 1 sphere / 2 capsule |
| `Body.Size` | Vec3 | box 半边长；sphere 半径在 `[0]`；capsule 半径在 `[0]`、半高在 `[1]` |
| `Body.Static` | Bool | 是否静态刚体 |
| `Body.Active` | Bool | 是否仍在模拟（未休眠） |
| `Body.Mat` | I32 | 视觉材质号（配色提示，编号见 `sim/map.go` `Material`）；不参与物理/玩法判定 |
| `Projectile` | Bool | 存在即为弹丸（标记属性） |
| `Game.Winner` | I32 | 对局结果：-1 = 进行中，否则为获胜玩家的槽位（0/1） |

- **属性存在性**：缺省即不存在。`Projectile` 是标记属性，值恒为 `true`，存在与否即
  代表该组件有无；`removed` / `destroy` 是属性存在性的终点。
- **实体 ID 空间**：物理刚体 id（1 起递增）与逻辑实体 id（玩家，从 `1<<24` 起）不重叠，
  都直接作为 `EntityDelta.id`。
- **命中盒不下发**：每个玩家有一个跟随角色的**命中盒刚体**（弹丸打中它才算命中，
  见 `docs/ARCHITECTURE.md`），但它是纯物理装置——没有 `Body` 组件、没有任何同步属性，
  客户端看不见也不需要知道它。同步属性表是「下发给客户端的东西」的清单，
  不是 ECS 组件表。
- **`Body.Mat` 向后兼容**：客户端不认识某个材质号时按未标材质的默认配色渲染
  （静态钢灰 / 动态木色），因此新增材质号只需追加编号。

## 客户端 → 服务端：上行消息（Notify）

route 三段式 `server.service.method`，payload 是 protobuf。

### match.match.join —— 加入匹配队列 / 重连回局

```proto
message JoinMsg {}
```

**空消息** —— 身份不再由客户端携带，而是取自会话绑定（`account.*` 登录成功时
`session.Bind(accountID)` 写入的会话 UID）。未绑定的会话发 join 会被**静默忽略**
（只在服务端日志里留一行），所以客户端必须先拿到 `LoginReply.ok=true`。
重连时用同一个账号登录/resume 就能找回原来的对局实例（见上文 `onMatched` 的重连说明）。

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

match / account / gate / game 节点之间走 pitaya `RPCTo`（三段式 route），payload 也在同一 proto 里：

```proto
// match → game，创建对局（route "game.game.create"）
message CreateGameMsg {
  string match_id = 1;
  repeated string uids = 2; // 按槽位顺序（下标即 player_idx），1 个 = 单人兜底局
}
message CreateGameReply { int32 code = 1; } // 0 = 成功

// match → game，回局查询（route "game.game.rejoin"）：该节点是否托管此会话 UID 的存量实例
message RejoinMsg  { string token = 1; }    // 字段名是历史遗留，装的是会话 UID（账号 ID）
message RejoinReply {
  bool found = 1;
  string match_id = 2;
  int32 player_idx = 3; // 原本的玩家槽位（0/1）
}

// match → gate，请玩家所属的 gate 把对局信息写进会话数据（route "gate.gate.bindgame"）
message BindGameMsg {
  string uid = 1;            // 会话 UID（账号 ID）
  string game_server_id = 2; // 托管该对局的 game 节点 id
  string match_id = 3;
  int32  player_idx = 4;
}
message BindGameReply { bool found = 1; } // false = 这个 gate 上已经没有该会话（顺带探活）
```

`gate.gate.bindgame` 之所以必须由 gate 来做：会话数据（`gameServerId`）是 gate 的 AddRoute
用来给 `game.game.*` 做定点路由的依据，只有持有那条连接的 gate 能写。match 通过
`online:{accountID}` 找到是哪个 gate，`found=false` 就说明人已经不在（掉线或被顶号），
这是 online 登记允许陈旧的兜底。

account → gate 的踢人用的是 pitaya 内置的 `gate.sys.kick`（`KickMsg` / `KickAnswer`），
本项目的 proto 里没有、也不该自己造一套。

## 对局规则（PVP）

- **纯 1v1 对枪**：场上只有两名玩家，没有怪物/波次/金币/靶球
- **击杀数先到 10 的一方获胜**（`sim.killTarget`）。分出胜负后写入全局单例的
  `Game.Winner`（-1 = 进行中，否则为获胜玩家的槽位），5 秒后服务端自动重开一局
- 每发弹丸命中扣 34 点血（满血 100，3 发击杀）
- 被击杀者**立即**在己方出生点满血复活，击杀方 +1 杀、被杀方 +1 死亡；
  两人的出生点是地图两端（绕 Y 轴 180° 对称，完全等价）
- 玩家不可被动态刚体推动（不可被挤开；服务端游戏规则：sim 通过
  `SetCharacterDynamicPush(false)` 配置，包装层只暴露物理开关）

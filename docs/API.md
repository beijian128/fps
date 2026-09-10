# 网络协议

客户端只连一个 WebSocket 端点：`ws://localhost:8080/`（gate 服务）。服务端是
**分布式三服务**（gate / match / game），客户端只感知 route，不感知后端节点分布。
传输走 **pitaya 的 pomelo 帧格式**（二进制），payload 用 **protobuf** 序列化
（schema 见 `joltgo/game/protos/game.proto`）。

> 改动任何 route 或 proto 字段，必须同步改 `joltgo/game/protos/game.proto`、
> `joltgo/game/`、`joltgo/match/` 与 `godot_client/scripts/fps_client.gd`（protobuf 编解码）。
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
4. 客户端发 `match.match.join`（Notify，空 payload）进入匹配队列
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

## 服务端 → 客户端：状态快照（Push，route `onSnapshot`）

匹配成功后，game 节点以 **20 Hz** 固定节奏推进对局，每个 tick 结束把完整快照经
gate 推给局内玩家；射击、重置等即时操作也会立即补推。payload 是 `Snapshot`：

```proto
message Snapshot {
  repeated BodyInfo bodies = 1;      // 按 id 升序
  repeated ResourceInfo resources = 2;
  repeated PlayerState players = 3;  // 按玩家槽位 0/1 顺序
  int32 step = 4;                    // 模拟 tick 计数（20 Hz）
  int32 score = 5;                   // 团队共享分数
  int32 wave = 6;
  int32 gold = 7;                    // 团队共享金币
}
```

```proto
message PlayerState {
  repeated float pos = 1; // [x, y, z] 脚底位置（客户端眼高 = y + 1.6）
  float health = 2;
}
```

### BodyInfo

```proto
message BodyInfo {
  uint32 id = 1;
  int32 type = 2;          // 0 = box, 1 = sphere, 2 = enemy capsule
  bool static = 3;
  bool target = 4;
  bool enemy = 5;
  bool projectile = 6;
  repeated float pos = 7;  // [x, y, z] 质心位置（packed float）
  repeated float quat = 8; // [x, y, z, w] 旋转四元数
  repeated float size = 9; // box 半边长；sphere 半径在 [0]；capsule 半径在 [0]、半高在 [1]
  float health = 10;       // 敌人血量（其他刚体为 0）
  bool active = 11;        // 是否仍在模拟（未休眠）
  int32 mat = 12;          // 视觉材质（场景配色，编号见 sim/map.go Material）：
                           //   0 默认 1 甲板 2 船体 3–6 集装箱 7 高架走道
                           //   8 栏杆 9 木箱 10 桅杆/烟囱/系缆桩 11 舷梯踏步
                           // 靶球/敌人/弹丸由 target/enemy/projectile 标志位优先决定外观
}
```

`mat` 只是**配色提示**，不影响任何物理或玩法判定；客户端不认识某个材质号时
按未标材质的默认配色渲染（静态钢灰 / 动态木色），因此新增材质号是向后兼容的。

### ResourceInfo

```proto
message ResourceInfo {
  int32 id = 1;
  repeated float pos = 2; // [x, y, z] 悬浮位置
  int32 kind = 3;         // 0 = 金币
}
```

金币在物理上是 **Jolt 传感器球**（半径 0.6 m、悬浮 0.8 m）：不参与刚体碰撞
（弹丸/箱子穿过），但角色控制器能接触到——**接触即拾取**（等效水平拾取半径
= 角色半径 0.4 + 传感器半径 0.6 ≈ 1.0 m）；击杀怪物会在死亡位置掉落金币
（场上上限 10 枚）。传感器球不出现在快照 `bodies` 里，客户端只渲染
`resources` 列表。

## 客户端 → 服务端：上行消息（Notify）

route 三段式 `server.service.method`，payload 是 protobuf。

### match.match.join —— 加入匹配队列

payload：`JoinMsg`（空消息，无字段）。握手后发的第一条业务消息；match 服务据此绑定
会话 UID 并入队，配对后推 `onMatched`。

### game.game.input —— 上报输入（约 60 Hz）

```proto
message InputMsg {
  repeated float move = 1; // [wx, wz]，世界空间水平期望速度（m/s）
  bool jump = 2;           // 跳跃边沿触发
}
```

- `move`：世界空间水平期望速度（m/s），由客户端按相机朝向算出
- `jump`：**边沿触发**——服务端仅在收到后的下一个 tick 消费，客户端只需在按键
  按下瞬间置 true 一次

### game.game.shoot —— 发射弹丸

```proto
message ShootMsg {
  repeated float origin = 1; // [x, y, z] 枪口起点
  repeated float dir = 2;    // [x, y, z] 朝向
}
```

`dir` 会被服务端归一化（零向量忽略）。服务端立即补推一帧快照。

### game.game.reset —— 重建场景

payload：空（handler 无入参）。销毁并重建整个场景（运输船地图的甲板/船体/集装箱/
走道/舷梯/桅杆、木箱、靶球、敌人、金币），重置分数、血量、步数、波次、输入状态，
并立即补推一帧。

## 波次规则（PVE）

- 第 1 波 3 只怪物，之后每波 +1，最多 6 只；清空当前波 2 秒后刷下一波
- 低难度：怪物静止不动（无追击 AI，不可推动），**接触伤害** 8/s（角色接触到怪物
  即扣血，物理接触判定，非距离判定），3 发子弹击杀一只
- 怪物不可推动玩家（玩家不可被挤开，这是服务端游戏规则：sim 通过
  `SetCharacterDynamicPush(false)` 配置，包装层只暴露物理开关）

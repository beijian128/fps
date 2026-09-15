# 对局生命周期与玩家战绩设计

- 日期：2026-09-15
- 状态：设计已确认，待实现
- 范围：客户端界面改造的 **A 子项目**（服务端能力与数据契约）。B 子项目（登录页 / 个人信息页 / 商城页 / 背包页 / 匹配页五个界面与设计系统）另开 spec。

## 1. 背景与目标

改造前的现状：

- 局外玩家数据只有钱包与背包，**没有任何对局战绩落库**。`game` 服务既不碰 Redis、也不对外发 RPC，胜负只作为 `Game.Winner` 同步属性推给在线客户端。
- 一场分出胜负后 5 秒 `matchSystem` 直接 `Reset()` 重开下一局，玩家永远困在同一个实例里，不存在「打完一局回大厅」这条路。
- 匹配只有 `match.join` 一条路：10 秒等不到第二人就单人开局兜底，客户端除了一行文字之外对队列状态一无所知。
- 场景重置（`CommandMsg.reset` + 客户端 HUD 的 Reset 按钮）是调试入口，没有产品语义。

本子项目的目标是把「匹配 → 对局 → 结算 → 回大厅 → 再匹配」这条生命周期补全，并为后续界面提供个人档案数据。完成时**服务端能力与数据契约齐备、端到端可验证**；客户端只做协议编解码与状态机的最小接线，五个界面的视觉与信息架构留给 B。

## 2. 非目标

- 五个界面（登录 / 个人信息 / 商城 / 背包 / 匹配）的视觉、布局与信息架构 —— 属于 B。
- 排行榜、段位、赛季、改名卡、装备影响数值、等级解锁玩法。等级纯展示。
- 战绩上报的可靠投递：不重试、不去重、不保证「至少一次」。
- 单人练习模式（删除兜底后，本地单人验证需要第二个客户端）。
- 对局内换装、loadout 注入、局内经济。
- 结算界面的具体呈现（B 负责），本子项目只保证客户端能正确进入「已结束」状态。

## 3. 已确认决策

1. **档案粒度**：等级 / 经验 / 累计击杀 / 累计死亡 / 场次 / 胜 / 负 + 最近 20 场历史。等级纯展示，不影响玩法。
2. **记账规则**：每判出一次胜负上报一次；两个槽位都是真实玩家才入账；掉线照记（服务端权威，掉线不取消这一场也不额外惩罚）；没分出胜负就被回收的实例不记。
3. **对局生命周期**：判出胜负即终结实例，**删除 5 秒自动重开**。只有玩家主动匹配并成功匹配才开新局。
4. **无上行回收超时**：60 秒 → **30 分钟**。它同时是「掉线回局窗口」。
5. **上报路径**：`game` 主动 RPC 推给 `logic`（remote route），**不重试、不去重**，失败只记日志。
6. **匹配**：删除 10 秒单人兜底；取消匹配立即生效、可立刻重排、无冷却；匹配状态由服务端**主动推送**（不做客户端轮询）。
7. **入队语义**：`Enqueue` 改 `ZADD NX`，重复入队变成幂等空操作。
8. **reset 彻底删除**：协议字段、服务端处理、`sim` 的世界重建能力、客户端按钮与相关测试全部移除，不留死代码。
9. **不做测试钩子**：不加「可配置击杀目标」之类的开关；「打满 10 杀」这条路径不做端到端脚本验证（见 §9）。

## 4. 数据模型

### 4.1 玩家档案 Hash（新增）

`persist/protos/player/player.proto` 新增 `DBUserProfile`，`REDBKey` 增加 `UserProfileDB = 3`；键沿用现有命名：**`REDB#3:<accountID>:0`**。

字段：`xp`、`kills`、`deaths`、`matches`、`wins`、`losses`、`schema_version`。

改完跑 `joltgo/gen-redis.ps1` 重新生成 `player.redis.go`（生成物必须提交，不手改）。

**等级不落库**：由 `xp` 经 `logic/level.go` 的纯函数派生。这样等级曲线以后随便调，不需要迁移存量数据，也不可能出现「等级字段与经验不同步」的脏数据。回复里同时下发 `level`、`xp_into_level`、`xp_for_next_level`，客户端画进度条不必自己实现曲线。

**累加不占锁**：新增 `persist.PlayerStore.AddMatchStats(ctx, id, delta)`，一条 `MULTI/EXEC` 管道做 6 次 `HINCRBY`。与钱包扣款不同，这里没有跨命令的读-改-写不变量，纯计数累加不需要 `distlock`；加锁反而是把原子操作降级。`Service.EnsureProfile` 顺带建这份档案（登录即建档）。

### 4.2 最近 20 场（新增）

- 结构：Redis List **`playerhist:<accountID>`**，`LPUSH` + `LTRIM 0 19`，新记录在前，超出自动截断，无额外清理任务。
- 元素：序列化后的单场记录，结构定义在 `player.proto`（`DBMatchRecord`）。
- 为什么用 List 而不是塞进 profile Hash 的 repeated 字段：历史是「只追加 + 截断」，List 天然原子；Hash 里的 repeated 字段必须把整行读出来改再写回，那就必须上锁。
- **持久化结构与下发结构分开**：`persist/protos/player` 里的 `DBMatchRecord` 负责落库，`game/protos` 里的 `MatchRecord` 负责下发，由 `logic` 做转换 —— 与现有 `PlayerBag → LogicShopItem` 的做法一致，不破坏分层。
- 记录字段：`match_id`、`won`、`kills`、`deaths`、`opponent_kills`、`duration_seconds`、`opponent_name`、`ended_at`。
- **对手名字来源**：`game` 只有 uid，拿不到用户名，所以由 `logic` 在记账时经 `persist.UsernameByID` **只读** account Hash（`acct:1:<id>:0` 的 `username` 字段）解析成快照写进记录。这是有意的跨服务只读：同库、不写别人的键、不加新 route。边界写进本 spec 与 `AGENTS.md`。

### 4.3 等级与经验

常量放在 `logic/level.go`（可调，不影响结构）：

- 每场：胜 **+50 XP**、负 **+10 XP**；每击杀 **+5 XP**（按各自击杀数）。
- 等级：`level = 1 + xp / 200`；`xp_into_level = xp - (level-1) * 200`；因为曲线是线性的，`xp_for_next_level` 在当前公式下恒为 200（保留这个字段是为了将来换成非线性曲线时客户端不用改）。

## 5. Wire 协议

全部加在 `joltgo/game/protos/game.proto`，改完重跑 `protoc --go_out`。

### 5.1 结算上报（服务间）

```proto
message SlotResult {
  string uid = 1;     // 空串表示该槽位没有真实玩家
  int32 kills = 2;
  int32 deaths = 3;
}
message RecordMatchMsg {
  string match_id = 1;
  repeated SlotResult slots = 2;   // 下标即槽位 0/1
  int32 winner_slot = 3;           // 0/1
  int32 duration_seconds = 4;
}
message RecordMatchReply {
  bool ok = 1;
  bool applied = 2;   // false = 收到但按规则未入账（如槽位不齐）
  string reason = 3;
}
```

一个实例一场对局，`match_id` 单独就够当将来的幂等键，因此**不引入场序字段**。

### 5.2 个人档案（客户端请求）

```proto
message MatchRecord {
  string match_id = 1;
  bool won = 2;
  int32 kills = 3;
  int32 deaths = 4;
  int32 opponent_kills = 5;
  int32 duration_seconds = 6;
  string opponent_name = 7;
  int64 ended_at = 8;
}
message PlayerProfileMsg {}
message PlayerProfileReply {
  bool ok = 1;
  string reason = 2;
  int32 level = 3;
  int64 xp = 4;
  int64 xp_into_level = 5;
  int64 xp_for_next_level = 6;
  int32 kills = 7;
  int32 deaths = 8;
  int32 matches = 9;
  int32 wins = 10;
  int32 losses = 11;
  repeated MatchRecord recent_matches = 12;
}
```

### 5.3 匹配（客户端请求 + 服务端推送）

```proto
message MatchCancelMsg {}
message MatchCancelReply {
  bool ok = 1;
  string reason = 2;   // cancelled | already_matched | not_queued | unauthenticated
}
```

`ok = true` **当且仅当**取消真的发生（`reason = "cancelled"`）；`already_matched` 与 `not_queued` 都是「你已经不在队列里了」，`ok = false` + 对应 `reason`。客户端三种情况的动作都是「留在匹配前的位置」：`cancelled` 停掉状态展示，`already_matched` 什么都不做（`onMatched` 马上到），`not_queued` 同理。

```proto
// onMatchStatus 推送载荷
message MatchStatus {
  int32 queued_players = 1;
  int32 waited_seconds = 2;
}
// onMatchEnded 推送载荷
message MatchEnded {
  string match_id = 1;
  int32 winner_slot = 2;
  repeated SlotResult slots = 3;
  int32 duration_seconds = 4;
}
```

### 5.4 上行命令的协议变更

`CommandMsg` **删除 `bool reset = 7;`**，并写 `reserved 7;`（防止字段号被后续复用后新旧客户端互相错解）。其余字段号不动。

### 5.5 Route 总表

| 方向 | route | 类型 | 说明 |
|------|-------|------|------|
| C→S | `game.game.cmd` | Notify | 输入 + 射击（`reset` 字段删除） |
| C→S | `match.match.join` | Notify | 不变 |
| C→S | `match.match.cancel` | Request/Response | 新增 |
| C→S | `logic.logic.state` / `.purchase` / `.equip` | Request/Response | 不变 |
| C→S | `logic.logic.profile` | Request/Response | 新增 |
| S→S | `logic.logic.recordmatch` | Remote（`RPC`） | 新增，`game` → `logic` |
| S→C | `onMatched` / `onFrame` | Push | 不变 |
| S→C | `onMatchStatus` | Push | 新增 |
| S→C | `onMatchEnded` | Push | 新增 |

**gate 一行都不用改**：新 route 的 svType 仍是 `logic` 与 `match`，现有 `routeRandom("logic")` / `routeAny("match")` 自动覆盖。

## 6. 服务端流程

### 6.1 sim：把结算结果交出来

- `matchSystem` 判出胜负时（`systems.go`）：照旧写 `winner`、`overAt`、`syncGameState()`、`rep.Set(attrGameWinner)`，**额外**填充一份一次性的 `MatchOutcome`：胜者槽位、双方 kills/deaths、本场 tick 数。**时长定义为「从实例开始 tick 到判出胜负的 tick 数 × 0.05 秒」**（一个实例只有一场，所以就是胜者判定时的 `step`）。
- **不再 `Reset()`**。随之一并删除（彻底删除，不留死代码）：
  - `Simulation.Reset()` 与内部 `reset()`；
  - `replication.Store.Reset()`（世界重建路径消失后它没有任何生产调用者，只剩自测；「Reset 必须清已下发基线」那条文档化不变量也随之失效）；
  - `game.Instance.Reset()`、`game/component.go` 里的 `Reset_` 分支；
  - `CommandMsg.reset`；
  - 客户端 HUD 的 Reset 按钮与相关状态（见 §8）。
- `Init()` 退化为「只调用一次 `init()`」；世界生命周期 = 实例生命周期，因此**实体 id 在单个实例内不再复用**（新建实例即新物理世界，id 从 1 重新发放）。
- 新增 `DrainOutcome() (MatchOutcome, bool)`：**取走即清**，与 `DrainFrame` 同一风格，保证一次结算恰好上报一次，且不会跨实例串味。

### 6.2 game：上报并终结实例

每 tick 的顺序固定为：

```
Step() → broadcast()（先把含 Game.Winner 的增量帧发出去）
      → if out, ok := DrainOutcome(); ok {
            push onMatchEnded 给两个槽位
            go reportMatch(out)      // 不阻塞 tick
            defer i.Stop()
            i.onExit()               // 摘注册表
            return                   // 与空闲回收走同一条退出路径
        }
```

退出必须**照抄现有空闲回收分支的形状**（`defer i.Stop()` + `i.onExit()` + `return`，见 `game/instance.go` 的 `run()`）：只调 `Stop()` 不够 —— 那只关掉 stop channel，让 goroutine 从 `case <-i.stop` 返回，**而那条路不会调用 `onExit`**，uid→实例表里会永久留着这条死实例，「玩家被塞回死对局」的坑原封不动。

- `reportMatch` 在**独立 goroutine** 里跑，只带一份纯数据快照（不碰 sim）。**不能在实例 goroutine 里同步发**：pitaya 的 RPC 默认 5 秒超时，卡住就是 100 个 tick 停摆、客户端 2.5 秒接收看门狗立刻判定掉线。
- 用 `app.RPC(ctx, "logic.logic.recordmatch", reply, msg)`（**不是 `RPCTo`**）：`RPC` 在 `RPCType_User` 下走 router 的 default route，从 game 节点可直接选到一个 logic 节点，不需要额外 `AddRoute`。ctx 超时 2 秒，失败只 `log.Printf`。
- `onExit` → `forget(inst, matchID, uids)` 从 uid→实例表摘除。**摘除是必须的**：否则玩家点「再来一局」时 `match.join` 的 `tryRejoin` 会命中已停止的实例，被 `pushMatched` 塞进一个永远不会有帧的死对局。
- `instanceIdleTimeout`：**60 秒 → 30 分钟**。没有分出胜负就回收的实例不产战绩。

### 6.3 logic：记账与查询

- `Remote.RecordMatch`：**必须用 `RegisterRemote`**（game 是用 RPC 打过来的，走 remotes 表；串成 handler 会拿 `ErrNotFoundCode`，同 `gate.gate.bindgame` 那个坑）。
- `Service.RecordMatch(ctx, req)`：
  1. 槽位校验：两个槽位 uid 都非空才入账，否则 `applied=false` + `reason`（兜底删除后正常路径不该出现，纯防御，避免将来加练习局时污染战绩）。
  2. 每个玩家：`persist.AddMatchStats` 累加；胜者 `wins+1`、另一方 `losses+1`；双方 `matches+1`；`xp += 50|10 + 5×kills`。
  3. 每个玩家 `LPUSH playerhist:<id>` + `LTRIM 0 19`，对手名先经 `persist.UsernameByID` 解析成快照（解析不到就存空串，不阻塞入账）。
  4. 任一步失败只记日志。**没有重试就没有补偿机会**，这是明确取舍：偶发丢一条战绩，好过阻塞对局或引入重放风险。
- `Service.Profile(ctx, accountID)`：读档案 Hash + `LRANGE playerhist:<id> 0 19`；**缺行就地补建零值档案再返回**（存量账号没有这一行，否则个人页直接 `profile_missing`）；等级由纯函数从 xp 派生。

### 6.4 match：取消、状态推送、入队语义

- **删除单人兜底**：`AfterInit` 的 tick 只保留 `PopPair`（tick 频率不变）；`queue.go` 的 `staleScript` / `PopStale` 与 `match.go` 的 `timeout` 常量删除，相关单测删除。
- **`Enqueue` 改 `ZADD NX`**：客户端在等待期每 15 秒静默重发一次 `match.join`，原来的覆盖式 `ZADD` 会把「入队时间」一次次刷新，导致服务端算出来的等待时长永远是 0–15 秒。改成 NX 后重发是幂等空操作，等待时长在断线重连后也连续。
- **`match.match.cancel`**（Request/Response）：
  - `ZREM match:queue uid` 返回 1 → `cancelled`；
  - 返回 0 → 调现有的 `tryRejoin(uid)`：命中说明**已经进局**，返回 `already_matched`，并照常 `bindGameOn` + `pushMatched`（把玩家带进对局，而不是从局里拽出来）；未命中 → `not_queued`；
  - 未绑定会话 → 忽略 + 记日志（沿用 `Join` 的做法）；
  - **窄竞态**：tick 刚把 uid 从队列弹出、实例尚未建立时，cancel 会看到「既不在队列、也查不到实例」→ 返回 `not_queued`；紧接着 `onMatched` 正常到达，客户端切进对局。**不会出现玩家被丢在半空**的状态。
- **`onMatchStatus` 推送**：在现有 1 秒 tick 里，对队列中的每个 uid 逐个推 `{queued_players, waited_seconds}`（`ZCARD` 与 `now - score`，时间戳取 Redis 服务端时钟，多节点一致）。逐个推是因为等待时长因人而异；队列规模小时成本可忽略，将来队列上千再改成批量推人数 + 客户端本地计时。
- 推送可行性的前提是会话已绑定 uid，而能进队列的人必然已登录，天然满足（与 `pushMatched` 同一条路径）。

## 7. 边界与错误处理

- **上报失败即丢**：RPC 超时 / logic 不可达只记日志，战绩少一条，不重试、不阻塞对局。
- **帧序**：先广播含 `Game.Winner` 的增量帧，再推 `onMatchEnded`。客户端以 `onMatchEnded` 作为「本局结束」的权威信号，收到后不再等帧。
- **为什么必须有 `onMatchEnded`**：对局一结束实例就没了、帧流随之中断，而客户端的接收看门狗是「已匹配 + 2.5 秒没有帧 = 判定断线」。没有这条显式事件，每局打完客户端都会把它当成掉线，自动重连、resume、重新入队。`Game.Winner` 是持续状态而不是一次性生命周期事件，也不带时长，不能替代它。
- **对局结束后的重连**：实例已摘除 → `tryRejoin` 未命中 → 正常入队（主路径，不是错误）。
- **结算期间掉线**：`onMatchEnded` 推送失败无所谓，resume 后重新匹配。
- **档案缺行 / 历史为空 / 历史不足 20 条**：都返回成功 + 零值或短列表，不报错。
- **未登录就发 join/cancel**：忽略 + 记日志。
- **双方挂机不打架**：30 分钟后实例回收，不产战绩。

## 8. 客户端最小接线（A 范围内）

只做「协议 + 状态机正确性」，任何新界面都留给 B。

- `fps_client.gd`：新增 `send_profile()`（Request/Response）、`send_cancel_match()`（Request/Response）、`onMatchStatus` / `onMatchEnded` 的推送解码与对应信号、`_is_known_request_route` 增加 `match.` 前缀；删除 `send_command` 的 `reset` 参数与字段 7 编码。
- `main.gd`：删除 reset 相关的一切（`_pending_reset`、`_reset_pending`、`_is_reset_frame`、`_on_reset_pressed`、HUD 的 Reset 按钮）；收到 `onMatchEnded` 时**停看门狗**（`_matched = false`）、清空本地世界与插值状态、回到未匹配状态并给出临时文字提示（不实现结算界面）。
- 这条接线是 A 的验收前提：没有它，每局打完都会触发 §7 里描述的「打完 → 重连 → 又排上队」循环。

## 9. 测试策略

**Go 单测**（`cd joltgo; PATH="$PWD:$PATH" go test -count=1 ./...`）：

- `persist`：`AddMatchStats` 六项累加；历史 `LPUSH + LTRIM` 截断（写 25 条只剩 20、新在前）；`UsernameByID` 命中与缺失。
- `logic`：胜负两条入账路径（wins / losses / matches / xp）；等级派生边界（0 XP、恰好 200 XP、跨级、`xp_into_level`）；单人局不入账；profile 缺行补建；历史序列化往返。
- `sim`：`DrainOutcome` 取走即清；判出胜负后不再重开；删除 `Reset` 后改造或删除既有用例（`sim_test.go`、`replicate_test.go`）。
- `match`：cancel 三态；`Enqueue` 重发不刷新 score；兜底删除后不再单人开局（原 `PopStale` 用例删除）；status 推送载荷。
- `game`：用 fake app 断言「上报不阻塞 tick」（发出记录后 tick 继续推进）与「实例结束后注册表摘除」（`forget` 被调用）。
- 真实 Jolt 集成：`go test -count=1 -tags joltdll ./physics` 仍需全绿。

**客户端无头测试**：

- `frame_decode_test.gd`：删除字段 7 的断言；新增 `onMatchStatus` / `onMatchEnded` / `MatchCancelReply` / `PlayerProfileReply` 解码用例。
- 新增用例（沿用 `game_frame_test.gd` 的合成推送风格）：喂 `onMatchEnded` → 断言看门狗停用、本地世界清空、状态回到大厅，且**不触发重连**。
- 三个冒烟测试（`login_smoke` / `ws_smoke` / `rejoin_smoke`）改为**两个客户端真配对**：各自随机注册账号，一方先入队、另一方入队后配成同一 `match_id` 且 `player_idx` 不同（兜底删除后这是唯一的配对方式）。`rejoin_smoke` 的回局语义不变。
- **明确不做**：脚本里真打满 10 杀再验证结算的端到端用例（用户决定不加「可配置击杀目标」这类测试钩子）。结算链路由 `sim` / `game` / `logic` 单测覆盖，客户端结算态由合成推送的无头用例覆盖。

## 10. 文档更新

- `AGENTS.md`：§3.4（tick 顺序与结算）、§3.9/§5（reset 移除后命中盒不再重建）、§4 数据流（匹配 → 对局 → 结算 → 回大厅、兜底删除、两个新推送）、§5（入队 NX、上报不去重、`CommandMsg` 字段保留号）、§6 测试命令（冒烟需要两个客户端）。
- `docs/API.md`：新 route、两个新推送、`CommandMsg` 删除 `reset` 与保留字段号、新增原因码。
- `docs/ARCHITECTURE.md`：实例生命周期（创建 → 结算 → 终结）、`sim` 与 `replication` 的边界变化、匹配队列语义。
- `README.md` / `godot_client/README.md`：按键表去掉 Reset、HUD 说明、匹配流程。

## 11. 验收标准

1. `go test -count=1 ./...` 与 `go test -count=1 -tags joltdll ./physics` 全绿。
2. 两个客户端（两个账号）能配成同一局；一方掉线后 30 分钟内 resume 能回到同一对局。
3. 一局分出胜负后：两名玩家都收到 `onMatchEnded`；服务端实例停止并从 uid→实例表摘除；`logic` 里两名玩家的 stats / xp / 历史各 +1 条；`logic.logic.profile` 能读到完整档案。
4. 对局结束后客户端**不会**自动重连、不会自动重新入队；点「开始匹配」重新进队列。
5. 匹配期的队列人数与等待时长来自 `onMatchStatus` 推送；取消匹配返回 `cancelled`；重复入队不刷新等待时长。
6. 全仓搜不到 reset 残留（按钮、状态闩锁、处理分支、世界重建 API），只保留 `game.proto` 里 `reserved 7;` 的字段号保留。

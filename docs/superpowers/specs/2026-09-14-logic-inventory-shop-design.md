# Logic 微服务：玩家背包与商城设计

- 日期：2026-09-14
- 状态：设计已确认，待实现

## 1. 背景与目标

当前服务端由 `gate / account / match / game` 四种角色组成，局外玩家数据尚无独立归属。本次新增第五种角色 `logic`，承载商城、钱包、背包与装备状态等局外业务。

首版目标是形成完整闭环：

`登录成功 → logic 用户上线事件 → 钱包/背包初始化 → 查看商城 → 购买物品 → 背包数量更新 → 装备/卸下主武器 → 客户端刷新`

`logic` 必须无本地业务状态，可部署多节点；客户端请求随机路由到任一节点；同一账号的并发写操作使用 Redis 分布式锁串行化。

## 2. 非目标

首版明确不包含：

- 枪械数值、战斗属性、对局内换枪或 loadout 注入。
- `game` 读取或感知背包、商城与装备数据。
- 充值、真实支付、对局奖励、任务产出或其他加币路径。
- 动态商品后台、运营改价、上下架和商品审核。
- 购买订单历史、审计流水和请求幂等。
- 消耗品使用、库存容量、物品实例化、掉落、交易或赠送。
- 关系型数据库、异步落库、对账系统。
- 跨 Redis key 的原子事务或崩溃自动恢复。

## 3. 已确认决策

- 首版只闭合局外数据链路，不影响对局战斗。
- 新账号或已有账号首次上线时获得 1000 金币，背包为空。
- 商品目录是 `logic` 内版本化静态配置，改商品需要发版。
- 所有物品均可叠加，包括枪械；重复购买只累加数量。
- 每次购买允许 `quantity = 1..99`。
- 不使用客户端幂等键，不保存订单。
- 同一账号使用分布式锁；锁等待上限 2 秒，超时返回 `busy`。
- 购买采用“多步写入 + 失败补偿”，不承诺崩溃原子性。
- 首版只有一个 `primary_weapon` 装备槽，只写持久化状态，不影响对局。
- 客户端界面纳入首版。
- 缺失档案不在一般读取路径懒创建，只由同步的 `logic.logic.online` 事件创建或修复。

## 4. 静态商品目录

商品定义位于 `joltgo/logic/catalog.go`，不写入 Redis。

| item_id | 名称 | 价格 | 装备槽 |
|---|---|---:|---|
| `rifle` | 步枪 | 300 | `primary_weapon` |
| `pistol` | 手枪 | 150 | `primary_weapon` |
| `shotgun` | 霰弹枪 | 500 | `primary_weapon` |
| `medkit` | 医疗包 | 50 | 空 |

目录在进程启动时校验：

- `item_id` 非空且唯一。
- 价格大于等于 0。
- `equip_slot` 只允许空串或首版已知槽位。
- 目录只允许追加或修改后随版本发布，不在运行时修改。

## 5. 服务架构

新增角色：

```text
客户端
  │ WebSocket
  ▼
gate ── logic.logic.* 随机路由 ──▶ logic 任一节点 ──▶ Redis
  │
  └── account / match / game 保持现有路由

account ── logic.logic.online（同步随机 RPC）──▶ logic 任一节点
```

- `main.go` 支持 `joltgo.exe -type logic`。
- `logic` 节点连接 Redis，不保存玩家业务状态。
- `gate` 增加 `logic` 路由函数，在多个 `logic` 节点之间随机选择。
- `account` 通过服务发现获取 `logic` 节点，并随机选择一个执行上线事件。
- `logic` 的客户端 handler 与后端 remote 分开注册，避免客户端直接调用上线事件。

## 6. 登录上线事件

`account.finishLogin` 在凭证校验通过后、返回任何成功 `LoginReply` 之前执行：

```text
校验账号凭证
  → 随机选择 logic 节点
  → 同步调用 logic.logic.online
  → logic 在账号锁内 EnsureProfile
  → 成功后轮换 token
  → Bind 会话
  → 踢旧连接
  → LoginReply.ok=true
```

规则：

- `logic.logic.online` 携带 `account_id`，由后端 RPC 调用，不携带客户端 token。
- 事件失败、无可用 `logic` 节点或 RPC 超时，当前登录返回 `internal`，不能返回成功。
- 事件必须在 token 轮换和 `Bind` 之前完成，避免 `logic` 故障时先作废玩家当前有效凭证。
- 重复登录或同一连接上的幂等登录也必须保证上线事件成功；`EnsureProfile` 本身可重复执行。
- 注册流程先创建账号，再复用同一套 `finishLogin` 收尾。

## 7. 数据模型

新增 `joltgo/persist/protos/player/player.proto`（独立 Go 包 `playerpb`），由固定版本 `protoc-gen-redis` 生成。使用默认 key 格式：

```text
REDB#%d:%d:%d
```

模块枚举定义在 proto 中：

```proto
enum REDBKey {
  REDB_KEY_UNSPECIFIED = 0;
  UserBagDB = 1;
  UserWalletDB = 2;
}

enum DBSchemaVersion {
  DB_SCHEMA_VERSION_UNSPECIFIED = 0;
  DB_SCHEMA_VERSION_CURRENT = 1;
}
```

Redis key：

- 背包：`REDB#1:<accountID>:0`
- 钱包：`REDB#2:<accountID>:0`

生成代码调用形式：

```go
uint32(REDBKey_UserBagDB)
uint32(REDBKey_UserWalletDB)
```

### 7.1 DBUserWallet

- `coins int64`
- `schema_version DBSchemaVersion`

### 7.2 DBUserBag

- 嵌套 `DBItems`
- `equipped_primary_weapon string`
- `schema_version DBSchemaVersion`

`DBItems`：

- `repeated DBItem items`

`DBItem`：

- `item_id string`
- `quantity int64`
- `acquired_at int64`

语义：

- 同一 `item_id` 只存在一个条目。
- 重复购买累加 `quantity`，保留第一次获得时的 `acquired_at`。
- 没有数量为零的条目。
- `equipped_primary_weapon` 为空表示未装备。
- 装备枪械不消耗数量；只要 `quantity > 0` 即可装备。
- 钱包和背包互相独立，可以分别检查存在性并通过上线事件修复缺失项。

## 8. 领域与仓储边界

建议结构：

```text
joltgo/logic/
├── catalog.go       # 静态商品目录与启动校验
├── component.go     # State / Purchase / Equip handler
├── remote.go        # Online 后端 remote
├── service.go       # 双层检查、锁、购买补偿、装备规则
├── store.go         # Service 依赖的窄接口
└── *_test.go
```

`persist` 层新增：

- `persist/protos/player/player.proto`
- `persist/protos/player/player.redis.go`
- `persist/player.go`

`persist/player.go` 只暴露领域记录与仓储接口，不把生成类型泄漏给 `logic` 业务层。业务规则、错误码映射和锁协调都留在 `logic`。`gen-redis.ps1` 分两次调用生成器：`account.proto` 保持 `key_format=acct:%d:%d:%d`，`player/player.proto` 使用 `key_format=REDB#%d:%d:%d`。账号生成码继续留在 `persistpb`，玩家数据生成码进入独立 `playerpb` 包，避免生成类型重名。

## 9. Wire 协议

保持现有约定，在 `joltgo/game/protos/game.proto` 中新增逻辑业务消息，并重新生成 Go 码。

### 9.1 客户端请求

```proto
message LogicStateMsg {}

message PurchaseMsg {
  string item_id = 1;
  int32 quantity = 2;
}

message EquipMsg {
  string item_id = 1; // 空串表示卸下主武器
}
```

### 9.2 客户端响应

```proto
message LogicShopItem {
  string item_id = 1;
  string display_name = 2;
  int64 price = 3;
  string equip_slot = 4;
  int64 owned_quantity = 5;
}

message LogicStateReply {
  bool ok = 1;
  string reason = 2;
  int64 coins = 3;
  repeated LogicShopItem items = 4;
  string equipped_primary_weapon = 5;
}
```

`State`、`Purchase`、`Equip` 均使用 Request/Response，并统一返回 `LogicStateReply`。购买或装备成功时返回最新状态；失败时返回 `ok=false` 与原因码，状态字段不用于覆盖客户端当前状态。

### 9.3 后端上线事件

```proto
message UserOnlineMsg {
  string account_id = 1;
}

message UserOnlineReply {
  bool ok = 1;
  string reason = 2;
}
```

### 9.4 Route

| route | 类型 | 方向 | payload → reply |
|---|---|---|---|
| `logic.logic.state` | Request | 客户端 → logic | `LogicStateMsg` → `LogicStateReply` |
| `logic.logic.purchase` | Request | 客户端 → logic | `PurchaseMsg` → `LogicStateReply` |
| `logic.logic.equip` | Request | 客户端 → logic | `EquipMsg` → `LogicStateReply` |
| `logic.logic.online` | RPC | account → logic | `UserOnlineMsg` → `UserOnlineReply` |

客户端身份只取会话 UID。客户端传入的账号字段一律不信任；未绑定会话返回 `unauthenticated`。

## 10. 错误码

失败原因码：

- `bad_quantity`：数量不在 `1..99`。
- `item_not_found`：静态目录中不存在该商品。
- `insufficient_funds`：金币不足。
- `not_owned`：未拥有该物品。
- `not_equippable`：物品没有装备槽。
- `busy`：2 秒内未获得账号锁。
- `unauthenticated`：会话没有绑定账号。
- `profile_missing`：一般读取或写操作发现钱包或背包缺失。
- `internal`：Redis、RPC、数据解析或补偿失败等其他错误。

上线事件只返回 `ok/reason`，`busy` 和内部错误对登录统一表现为 `internal`。

## 11. 双层检查与写入流程

所有“先读再写，并根据读取结果决定是否继续写”的操作采用：

1. 锁外读取并判断。
2. 无需写入时直接返回。
3. 需要写入时获取 `user:lock:<accountID>`。
4. 锁内重新读取并重新判断。
5. 锁内仍需要写入时才执行写操作。
6. 锁外结果不得作为最终写入依据。

### 11.1 EnsureProfile

1. 锁外分别检查钱包和背包是否存在。
2. 两者都存在则直接成功。
3. 任一缺失则获取账号锁。
4. 锁内重新检查两者。
5. 钱包缺失时创建 `coins=1000`。
6. 背包缺失时创建空背包。
7. 已存在的部分不能重置，不能重复发放金币。

如果钱包创建成功而背包创建失败，补偿删除本次新建的钱包；补偿失败则记录高优先级日志。下一次上线事件可以继续修复缺失项。

### 11.2 State

- 不获取锁。
- 读取会话 UID、钱包和背包。
- 将静态目录与背包数量合并为 `LogicShopItem`。
- 钱包或背包缺失时返回 `profile_missing`，不在读取路径创建。

### 11.3 Purchase

1. 校验会话 UID、`item_id` 和 `quantity`。
2. 在静态目录中查商品；不存在则返回 `item_not_found`。
3. 锁外读取钱包；钱包缺失返回 `profile_missing`，余额不足则返回 `insufficient_funds`。
4. 获取账号锁；2 秒超时返回 `busy`。
5. 锁内重读钱包和背包；任一缺失返回 `profile_missing`，不代替上线事件初始化。
6. 重新检查商品、余额和数量。
7. 计算 `newCoins = oldCoins - price * quantity`。
8. 写钱包。
9. 在内存中查找或追加背包条目，累加数量。
10. 写背包。
11. 背包写失败时，用旧余额补偿回写钱包。
12. 返回锁内计算出的最新 `LogicStateReply`。

首版不存在加币路径，所以“锁外余额不足直接返回”是安全的；未来增加充值、奖励或后台加币后，必须保留锁内重读，不能仅依赖锁外判断。

### 11.4 Equip

1. 读取背包；缺失返回 `profile_missing`。
2. 空 `item_id` 表示卸下；已经是未装备状态则直接返回。
3. 非空 `item_id` 时检查物品存在、可装备且数量大于 0。
4. 已经装备同一物品则直接返回。
5. 确实需要改变装备状态时才获取账号锁。
6. 锁内重读背包并重新检查拥有关系与装备槽。
7. 写入新的 `equipped_primary_weapon`。

装备不会修改物品数量。装备与 `medkit` 等不可装备物品返回 `not_equippable`。

## 12. 锁与一致性语义

- 锁 key：`user:lock:<accountID>`。
- TTL 为 10 秒，启用 watchdog。
- 重试间隔为 10 毫秒，业务等待最多 2 秒。
- 锁只在确实需要写入时获取，快速失败和无操作路径不加锁。
- 锁用于串行化同一账号的业务检查和写入，不替代事务。
- 钱包与背包是两个 Redis Hash，写入不是单个原子提交。
- 写前检查 `lock.IsLost()`；锁已丢失时中止当前写并返回 `internal`。
- 补偿只进行一次立即尝试，失败后记录日志并返回 `internal`。

首版明确不保证：

- 进程崩溃时钱包与背包同时提交。
- 补偿失败后的自动恢复。
- 响应丢失后的 exactly-once 购买。
- 无锁 `State` 读取与并发购买之间的快照一致性。

客户端必须在购买请求未完成时禁用购买按钮，并且响应超时后不得自动重试。若购买实际已提交但响应丢失，客户端只能在重新拉取 `State` 后看到最新余额与数量。

## 13. 部署与配置

- `start-all.ps1` 增加 `logic` 进程。
- 日志增加 `deploy/logic.log` 与 `deploy/logic.out.log`。
- `stop-infra.ps1` 已按进程名停止全部 `joltgo`，无需新增独立停止分支。
- `logic` 必须连接 Redis；Redis 不可用时启动失败。
- `gate`、`account`、`match`、`game` 的现有部署方式不变。
- 多 `logic` 节点共享同一 Redis，因此没有粘性会话要求。

## 14. Godot 客户端

`godot_client/scripts/fps_client.gd`：

- 新增 `state`、`purchase`、`equip` 三类 Request 编码。
- 复用现有 mid → route 归因处理 Response。
- 为 Logic 响应增加信号或统一响应分发。
- 购买失败原因映射为可展示文本。

`godot_client/scripts/main.gd`：

- 增加商城/背包面板。
- 展示金币、商品名称、价格、持有数量和当前主武器。
- 支持购买数量、购买、装备和卸下。
- 请求成功后整体替换 UI 状态；失败时不本地预扣或预加。
- 登录成功后即可请求状态，不依赖匹配或进入对局。

## 15. 测试策略

### 15.1 纯业务测试

- 静态目录合法、唯一、价格和槽位正确。
- `EnsureProfile` 幂等。
- 两个并发上线事件只初始化一次。
- 钱包或背包单项缺失时只补缺失项。
- 购买成功、余额不足、非法数量、商品不存在。
- 重复购买累加数量并保留首次获得时间。
- 同账号并发购买被串行化，不发生丢更新。
- 不同账号可以并行处理。
- 装备、卸下、未拥有、不可装备、重复状态。
- 锁超时返回 `busy`。
- 钱包写后背包写失败的补偿路径。
- 补偿失败时返回 `internal` 并记录错误。

### 15.2 仓储测试

- 钱包与背包 Hash 的 key 使用 `REDBKey` 枚举值。
- 读写往返保持字段、数量和装备状态。
- `schema_version DBSchemaVersion` 正确写入。
- 集合字段整体读改写行为符合 `protoc-gen-redis` 约定。

### 15.3 服务集成测试

- `account.finishLogin` 成功调用上线事件。
- 上线事件失败时登录返回 `internal`。
- 重复登录仍保证档案存在。
- `gate` 在有多个 `logic` 节点时能随机选择可用节点。
- 客户端到 `logic.logic.online` 的调用被拒绝。
- 客户端 `state / purchase / equip` 使用会话 UID。

### 15.4 Godot 测试

- Logic Response 帧解码。
- `ok=false` 原因码不会污染本地 UI 状态。
- 购买后使用服务端返回状态刷新金币和数量。
- 装备与卸下状态刷新。
- 有集群时增加 smoke：注册/登录 → 初始 1000 金币 → 购买 → 背包数量增加。

## 16. 验收标准

- `joltgo -type logic` 可独立启动并连接 Redis。
- 至少两个 `logic` 节点时，客户端请求可以随机落入任一节点。
- 新账号和已有账号首次成功登录后都存在 1000 金币的钱包与空背包。
- `logic` 不可用时登录失败，且不会先作废当前有效凭证。
- 购买会正确扣款并累加数量；余额不足不会产生任何写入。
- 同账号并发购买由锁串行化，不会出现同一旧余额被重复消费。
- 装备状态可持久化、查询、切换和卸下。
- `game` 与对局协议未因本功能发生行为变化。
- 所有新增 Go 测试、协议解码测试和可运行的 Godot 回归测试通过。
- `AGENTS.md`、`README.md` 与 `docs/` 中对应的架构、API、构建和开发说明同步更新。

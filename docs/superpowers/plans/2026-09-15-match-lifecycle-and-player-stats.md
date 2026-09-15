# 对局生命周期与玩家战绩 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让「匹配 → 对局 → 结算 → 回大厅 → 再匹配」这条生命周期在服务端真正闭环：战绩落库、判出胜负即终结实例、匹配可取消且状态由服务端推送，并彻底移除单人兜底与场景重置。

**Architecture:** 新增一份玩家档案（Hash 计数 + List 历史）由 `logic` 服务持有；`sim` 在判出胜负时交出一份一次性的结算快照，`game` 在实例 goroutine 之外用一条 `RPC` 推给 `logic` 的 remote，然后走与空闲回收相同的退出路径终结实例；`match` 失去兜底、队列改 `ZADD NX`、新增取消与状态推送。客户端本阶段只做协议编解码与状态机正确性。

**Tech Stack:** Go 1.26（cgo 只出现在 `joltgo/physics`）、pitaya v3（内置源码，Cluster 模式：etcd + NATS + Redis）、protobuf（`protoc` + `protoc-gen-go`，持久化另用 `protoc-gen-redis`）、Godot 4.7.2（GDScript，headless 测试）。

**Spec:** `docs/superpowers/specs/2026-09-15-match-lifecycle-and-player-stats-design.md`

## Global Constraints

- **提交规则**：直接提交到 `main`，不开 feature branch、不走 PR，历史保持线性。提交信息用 `<type>: <subject>`（`feat` / `fix` / `docs` / `refactor` / `test`），AI 提交结尾加 `Co-Authored-By: Codex <noreply@openai.com>` 尾注。
- **cgo 边界**：`import "C"` 只能出现在 `joltgo/physics/`。`sim/` 与 `ecs/` 必须保持纯 Go，只依赖 `sim.Physics` 接口。
- **pitaya 边界**：`joltgo/third_party/pitaya/` 只允许保留既有的日志脱敏补丁，不放业务代码。
- **同步协议**：新增同步**属性**不需要改 proto；本计划改的是**消息与 route 结构**，必须 `protoc --go_out` 重生成，且**必须提交生成物**。
- **持久化协议**：改 `persist/protos/**` 后跑 `joltgo/gen-redis.ps1` 重生成 `.redis.go`，不手改生成文件，生成物必须提交。
- **Redis 数据目录不清空**：`deploy/redis-data` 里是账号与凭证，任何脚本都不要删它（`etcd-data` 相反，每次启动清空）。
- **字段号永不重用**：`game.proto` 删除字段后写 `reserved <N>;`。
- **Go 测试命令**（Git Bash，`PATH` 必须含 `joltgo/` 才能加载 `libjolt_c.dll`）：
  - 纯 Go 包：`cd joltgo; PATH="$PWD:$PATH" go test -count=1 ./persist ./logic ./sim ./match ./game ./replication ./ecs ./account ./online ./gate ./kv`
  - 全量：`cd joltgo; PATH="$PWD:$PATH" go test -count=1 ./...`
  - 真 Jolt 集成：`cd joltgo; PATH="$PWD:$PATH" go test -count=1 -tags joltdll ./physics`
- **Godot 无头测试**（`_console.exe` 变体）：
  - `Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/<name>.gd`
  - 服务端构建（需要时）：`cd joltgo; .\build.ps1`；起集群：`cd joltgo\deploy; .\start-all.ps1`
- **不要**为测试给 `sim` 加「可配置击杀目标」之类的开关（spec §9 明确不做）。

---

## File Structure

| 文件 | 职责 | 变化 |
|------|------|------|
| `joltgo/persist/protos/player/player.proto` | 玩家持久化模型（生成 `.redis.go`） | 加 `DBUserProfile` / `DBMatchRecord` / `UserProfileDB` |
| `joltgo/persist/player.go` | 玩家 Hash/List 仓储（redigo） | 加档案计数、历史 List、只读用户名 |
| `joltgo/persist/player_test.go` | 仓储测试（miniredis） | 加新用例 |
| `joltgo/logic/level.go` | 等级/经验纯函数 | 新建 |
| `joltgo/logic/level_test.go` | 等级边界测试 | 新建 |
| `joltgo/logic/service.go` | logic 领域逻辑 | `Store` 接口扩展；加 `Profile` / `RecordMatch` |
| `joltgo/logic/component.go` | logic handler（客户端请求） | 加 `Profile` handler |
| `joltgo/logic/remote.go` | logic remote（服务间 RPC） | 加 `RecordMatch` |
| `joltgo/logic/service_test.go` | logic 领域测试 | 加新用例 |
| `joltgo/game/protos/game.proto` | wire 契约 | 新增若干消息；删 `CommandMsg.reset` 并 `reserved 7` |
| `joltgo/sim/simulation.go` | ECS 玩法层 | `MatchOutcome` + `DrainOutcome`；删 `Reset`/`reset` |
| `joltgo/sim/systems.go` | 每 tick 系统 | `matchSystem` 不再重开，产出结算快照 |
| `joltgo/game/instance.go` | 对局实例 goroutine | 结算上报、`onMatchEnded` 推送、终结实例；删 `Reset` |
| `joltgo/game/component.go` | game handler / remote | 删 `Reset_` 分支 |
| `joltgo/match/queue.go` | 匹配队列（Redis ZSET） | `ZADD NX`、加 `Snapshot`、删 `PopStale` |
| `joltgo/match/match.go` | 匹配服务 | 删兜底、加 `Cancel`、加状态推送 |
| `joltgo/replication/store.go` | 同步层终值表 | 删 `Reset` |
| `godot_client/scripts/fps_client.gd` | 传输层 + 编解码 | 新请求/新推送解码；删 reset 编码 |
| `godot_client/scripts/main.gd` | 输入/渲染/HUD/面板 | 删 reset；接 `onMatchEnded` |
| `godot_client/tests/*.gd` | 无头测试与冒烟 | 更新 + 双客户端冒烟 |
| `AGENTS.md` / `docs/*.md` / `README.md` / `godot_client/README.md` | 文档 | 同步 |

---

### Task 1: 玩家档案与历史的持久化模型

**Files:**
- Modify: `joltgo/persist/protos/player/player.proto`
- Regenerate: `joltgo/persist/protos/player/player.redis.go`（跑 `gen-redis.ps1`）
- Modify: `joltgo/persist/player.go`
- Test: `joltgo/persist/player_test.go`

**Interfaces:**
- Consumes: 无（本任务是底层）
- Produces:
  - `persist.PlayerStats{XP, Kills, Deaths, Matches, Wins, Losses int64}`
  - `persist.PlayerStatsDelta{XP, Kills, Deaths, Matches, Wins, Losses int64}`
  - `persist.PlayerMatchRecord{MatchID string; Won bool; Kills, Deaths, OpponentKills, DurationSeconds int32; OpponentName string; EndedAt int64}`
  - `func PlayerProfileKey(id uint64) string` → `REDB#3:<id>:0`
  - `func PlayerHistoryKey(id uint64) string` → `playerhist:<id>`
  - `(*PlayerStore) GetStats(ctx, id) (PlayerStats, bool, error)`
  - `(*PlayerStore) SaveStats(ctx, id, PlayerStats) error`（建档）
  - `(*PlayerStore) AddStats(ctx, id, PlayerStatsDelta) error`（`MULTI/EXEC` + 6×`HINCRBY`）
  - `(*PlayerStore) AppendMatchRecord(ctx, id, PlayerMatchRecord) error`（`LPUSH` + `LTRIM 0 19`）
  - `(*PlayerStore) ListMatchRecords(ctx, id, limit int) ([]PlayerMatchRecord, error)`
  - `(*PlayerStore) UsernameByID(ctx, id) (string, bool, error)`（只读 `acct:1:<id>:0`）

- [ ] **Step 1: 扩展 `player.proto`**

在 `joltgo/persist/protos/player/player.proto` 里，给 `REDBKey` 加一个值，并追加两条消息：

```proto
enum REDBKey {
  REDB_KEY_UNSPECIFIED = 0;
  UserBagDB = 1;
  UserWalletDB = 2;
  UserProfileDB = 3;
}
```

```proto
message DBUserProfile {
  int64 xp = 1;
  int32 kills = 2;
  int32 deaths = 3;
  int32 matches = 4;
  int32 wins = 5;
  int32 losses = 6;
  DBSchemaVersion schema_version = 7;
}

message DBMatchRecord {
  string match_id = 1;
  bool won = 2;
  int32 kills = 3;
  int32 deaths = 4;
  int32 opponent_kills = 5;
  int32 duration_seconds = 6;
  string opponent_name = 7;
  int64 ended_at = 8;
}
```

- [ ] **Step 2: 重新生成持久化代码**

Run: `cd joltgo; .\gen-redis.ps1`
Expected: 打印 `Generated ...account.redis.go,...player.redis.go`，`git diff` 里出现 `DBUserProfile` / `DBMatchRecord` 与 `FieldDBUserProfile_*` 常量。

- [ ] **Step 3: 写失败的仓储测试**

追加到 `joltgo/persist/player_test.go`：

```go
func TestPlayerStatsAndHistory(t *testing.T) {
	store, mr := newTestPlayerStore(t)
	ctx := context.Background()

	if _, ok, err := store.GetStats(ctx, 7); err != nil || ok {
		t.Fatalf("未建档时 GetStats 应返回 ok=false，得到 ok=%v err=%v", ok, err)
	}
	if err := store.SaveStats(ctx, 7, PlayerStats{}); err != nil {
		t.Fatalf("SaveStats: %v", err)
	}
	if got := PlayerProfileKey(7); got != "REDB#3:7:0" {
		t.Fatalf("PlayerProfileKey = %q", got)
	}
	if got := PlayerHistoryKey(7); got != "playerhist:7" {
		t.Fatalf("PlayerHistoryKey = %q", got)
	}

	delta := PlayerStatsDelta{XP: 55, Kills: 10, Deaths: 3, Matches: 1, Wins: 1}
	if err := store.AddStats(ctx, 7, delta); err != nil {
		t.Fatalf("AddStats: %v", err)
	}
	if err := store.AddStats(ctx, 7, PlayerStatsDelta{XP: 15, Deaths: 2, Matches: 1, Losses: 1}); err != nil {
		t.Fatalf("AddStats 第二次: %v", err)
	}
	stats, ok, err := store.GetStats(ctx, 7)
	want := PlayerStats{XP: 70, Kills: 10, Deaths: 5, Matches: 2, Wins: 1, Losses: 1}
	if err != nil || !ok || stats != want {
		t.Fatalf("GetStats: got=%+v ok=%v err=%v want=%+v", stats, ok, err, want)
	}

	// 历史：写 25 条，只应留下最新 20 条，且新在前。
	for i := 0; i < 25; i++ {
		rec := PlayerMatchRecord{
			MatchID:         fmt.Sprintf("m%02d", i),
			Won:             i%2 == 0,
			Kills:           int32(i),
			Deaths:          int32(i + 1),
			OpponentKills:   int32(i + 2),
			DurationSeconds: 60 + int32(i),
			OpponentName:    fmt.Sprintf("opp%02d", i),
			EndedAt:         int64(1000 + i),
		}
		if err := store.AppendMatchRecord(ctx, 7, rec); err != nil {
			t.Fatalf("AppendMatchRecord(%d): %v", i, err)
		}
	}
	records, err := store.ListMatchRecords(ctx, 7, 20)
	if err != nil {
		t.Fatalf("ListMatchRecords: %v", err)
	}
	if len(records) != 20 {
		t.Fatalf("历史应截断到 20 条，得到 %d", len(records))
	}
	if records[0].MatchID != "m24" || records[19].MatchID != "m05" {
		t.Fatalf("历史应按新在前排列且只保留最近 20 条，得到首=%s 末=%s",
			records[0].MatchID, records[19].MatchID)
	}
	if !reflect.DeepEqual(records[0], PlayerMatchRecord{
		MatchID: "m24", Won: true, Kills: 24, Deaths: 25, OpponentKills: 26,
		DurationSeconds: 84, OpponentName: "opp24", EndedAt: 1024,
	}) {
		t.Fatalf("历史记录往返不一致: %+v", records[0])
	}

	// 空历史不是错误。
	empty, err := store.ListMatchRecords(ctx, 8, 20)
	if err != nil || len(empty) != 0 {
		t.Fatalf("无历史时应返回空列表与 nil，得到 %v / %v", empty, err)
	}
}

func TestUsernameByID(t *testing.T) {
	store, mr := newTestPlayerStore(t)
	ctx := context.Background()

	if _, ok, err := store.UsernameByID(ctx, 9); err != nil || ok {
		t.Fatalf("账号不存在时应 ok=false，得到 ok=%v err=%v", ok, err)
	}

	// 手工塞一条账号 Hash：logic 只读 username 字段，字段号取自 account 生成码。
	if err := mr.HSet("acct:1:9:0", "1", "alice"); err != nil {
		t.Fatalf("HSet: %v", err)
	}
	name, ok, err := store.UsernameByID(ctx, 9)
	if err != nil || !ok || name != "alice" {
		t.Fatalf("UsernameByID: got=%q ok=%v err=%v", name, ok, err)
	}
}
```

同时把 `player_test.go` 的 import 改成：

```go
import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/alicebob/miniredis/v2"
	redigo "github.com/gomodule/redigo/redis"
	playerpb "joltgo/persist/protos/player"
)
```

> `mr.HSet` 的第一个参数必须是 `acct:1:9:0`（`AccountKey` 的格式），第二个参数是 account 生成码里 `FieldDBAccount_Username` 的实际编号 —— 打开 `joltgo/persist/protos/account.redis.go` 确认这个常量，并把它写进测试里（**不要硬编码猜测**）。

- [ ] **Step 4: 跑测试确认失败**

Run: `cd joltgo; PATH="$PWD:$PATH" go test -count=1 ./persist -run 'TestPlayerStatsAndHistory|TestUsernameByID'`
Expected: 编译失败，`undefined: PlayerStats`、`store.GetStats undefined` 等。

- [ ] **Step 5: 实现仓储**

在 `joltgo/persist/player.go` 的常量区加：

```go
const (
	playerProfileNamespace uint32 = uint32(playerpb.REDBKey_UserProfileDB)
	historyLimit                  = 20
)
```

在类型区加：

```go
// PlayerStats 是玩家档案里的累计统计（等级由 XP 派生，不落库）。
type PlayerStats struct {
	XP      int64
	Kills   int64
	Deaths  int64
	Matches int64
	Wins    int64
	Losses  int64
}

// PlayerStatsDelta 是一次对局结算要累加的增量。
type PlayerStatsDelta struct {
	XP      int64
	Kills   int64
	Deaths  int64
	Matches int64
	Wins    int64
	Losses  int64
}

// PlayerMatchRecord 是历史里的单场战绩（对手名在记账时解析成快照）。
type PlayerMatchRecord struct {
	MatchID         string
	Won             bool
	Kills           int32
	Deaths          int32
	OpponentKills   int32
	DurationSeconds int32
	OpponentName    string
	EndedAt         int64
}

func PlayerProfileKey(id uint64) string {
	return fmt.Sprintf("REDB#%d:%d:%d", playerProfileNamespace, id, 0)
}

// PlayerHistoryKey 是历史 List 的键：它不归 protoc-gen-redis 管（List 不是 Hash 行），
// 所以不套 REDB# 命名。
func PlayerHistoryKey(id uint64) string {
	return fmt.Sprintf("playerhist:%d", id)
}
```

把这三个方法加到 `PlayerPersistence` 接口里（`GetStats` / `SaveStats` / `AddStats` / `AppendMatchRecord` / `ListMatchRecords` / `UsernameByID`），并实现：

```go
func (s *PlayerStore) SaveStats(ctx context.Context, id uint64, stats PlayerStats) error {
	conn, err := s.pool.GetContext(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	row := &playerpb.DBUserProfile{
		Xp:            stats.XP,
		Kills:         int32(stats.Kills),
		Deaths:        int32(stats.Deaths),
		Matches:       int32(stats.Matches),
		Wins:          int32(stats.Wins),
		Losses:        int32(stats.Losses),
		SchemaVersion: playerpb.DBSchemaVersion_DB_SCHEMA_VERSION_CURRENT,
	}
	return row.SetFields(conn, playerProfileNamespace, id, 0)
}

// AddStats 用一条 MULTI/EXEC 累加六项计数：这里没有跨命令的读-改-写不变量，
// 纯计数累加不需要分布式锁（对比钱包扣款）。
func (s *PlayerStore) AddStats(ctx context.Context, id uint64, delta PlayerStatsDelta) error {
	conn, err := s.pool.GetContext(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	key := PlayerProfileKey(id)
	increments := []struct {
		field playerpb.FieldDBUserProfile
		delta int64
	}{
		{playerpb.FieldDBUserProfile_Xp, delta.XP},
		{playerpb.FieldDBUserProfile_Kills, delta.Kills},
		{playerpb.FieldDBUserProfile_Deaths, delta.Deaths},
		{playerpb.FieldDBUserProfile_Matches, delta.Matches},
		{playerpb.FieldDBUserProfile_Wins, delta.Wins},
		{playerpb.FieldDBUserProfile_Losses, delta.Losses},
	}
	if err := conn.Send("MULTI"); err != nil {
		return err
	}
	for _, inc := range increments {
		if err := conn.Send("HINCRBY", key, inc.field, inc.delta); err != nil {
			return err
		}
	}
	_, err = conn.Do("EXEC")
	return err
}

func (s *PlayerStore) GetStats(ctx context.Context, id uint64) (PlayerStats, bool, error) {
	conn, err := s.pool.GetContext(ctx)
	if err != nil {
		return PlayerStats{}, false, err
	}
	defer conn.Close()
	exists, err := redis.Bool(conn.Do("EXISTS", PlayerProfileKey(id)))
	if err != nil || !exists {
		return PlayerStats{}, false, err
	}
	var row playerpb.DBUserProfile
	if err := row.GetFields(conn, playerProfileNamespace, id, 0); err != nil {
		return PlayerStats{}, false, err
	}
	return PlayerStats{
		XP:      row.Xp,
		Kills:   int64(row.Kills),
		Deaths:  int64(row.Deaths),
		Matches: int64(row.Matches),
		Wins:    int64(row.Wins),
		Losses:  int64(row.Losses),
	}, true, nil
}

func (s *PlayerStore) AppendMatchRecord(ctx context.Context, id uint64, rec PlayerMatchRecord) error {
	conn, err := s.pool.GetContext(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	row := &playerpb.DBMatchRecord{
		MatchId:         rec.MatchID,
		Won:             rec.Won,
		Kills:           rec.Kills,
		Deaths:          rec.Deaths,
		OpponentKills:   rec.OpponentKills,
		DurationSeconds: rec.DurationSeconds,
		OpponentName:    rec.OpponentName,
		EndedAt:         rec.EndedAt,
	}
	blob, err := proto.Marshal(row)
	if err != nil {
		return err
	}
	key := PlayerHistoryKey(id)
	if err := conn.Send("MULTI"); err != nil {
		return err
	}
	if err := conn.Send("LPUSH", key, blob); err != nil {
		return err
	}
	if err := conn.Send("LTRIM", key, 0, historyLimit-1); err != nil {
		return err
	}
	_, err = conn.Do("EXEC")
	return err
}

func (s *PlayerStore) ListMatchRecords(ctx context.Context, id uint64, limit int) ([]PlayerMatchRecord, error) {
	conn, err := s.pool.GetContext(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if limit <= 0 || limit > historyLimit {
		limit = historyLimit
	}
	blobs, err := redis.ByteSlices(conn.Do("LRANGE", PlayerHistoryKey(id), 0, limit-1))
	if err != nil {
		return nil, err
	}
	out := make([]PlayerMatchRecord, 0, len(blobs))
	for _, blob := range blobs {
		var row playerpb.DBMatchRecord
		if err := proto.Unmarshal(blob, &row); err != nil {
			return nil, err
		}
		out = append(out, PlayerMatchRecord{
			MatchID:         row.MatchId,
			Won:             row.Won,
			Kills:           row.Kills,
			Deaths:          row.Deaths,
			OpponentKills:   row.OpponentKills,
			DurationSeconds: row.DurationSeconds,
			OpponentName:    row.OpponentName,
			EndedAt:         row.EndedAt,
		})
	}
	return out, nil
}

// UsernameByID 只读 account 服务的账号 Hash 取用户名：logic 从不写这个键，
// 只借用一次显示名（见 spec §4.2）。读不到就返回 ok=false，不当作错误。
func (s *PlayerStore) UsernameByID(ctx context.Context, id uint64) (string, bool, error) {
	conn, err := s.pool.GetContext(ctx)
	if err != nil {
		return "", false, err
	}
	defer conn.Close()
	name, err := redis.String(conn.Do("HGET", AccountKey(id), accountUsernameField))
	if err == redis.ErrNil {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return name, true, nil
}
```

`joltgo/persist/player.go` 的 import 需要加上 `"github.com/golang/protobuf/proto"`；`accountUsernameField` 用 `joltgo/persist/store.go` 里已有的账号字段常量（若还没有具名常量，就在 `store.go` 里加一行 `const accountUsernameField = accountpb.FieldDBAccount_Username`，**字段号不要手写数字**）。

- [ ] **Step 6: 跑测试确认通过**

Run: `cd joltgo; PATH="$PWD:$PATH" go test -count=1 ./persist`
Expected: `ok  joltgo/persist`

- [ ] **Step 7: 提交**

```bash
git add joltgo/persist/protos/player/player.proto joltgo/persist/protos/player/player.redis.go joltgo/persist/player.go joltgo/persist/player_test.go joltgo/persist/store.go
git commit -m "feat: 增加玩家档案与最近战绩的持久化模型" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 2: 等级与经验派生

**Files:**
- Create: `joltgo/logic/level.go`
- Test: `joltgo/logic/level_test.go`

**Interfaces:**
- Consumes: 无
- Produces:
  - `logic.Level(xp int64) int32`
  - `logic.XPIntoLevel(xp int64) int64`
  - `logic.XPForNextLevel() int64`
  - `logic.MatchXP(won bool, kills int32) int64`
  - 常量 `logic.XPPerWin = 50`、`logic.XPPerLoss = 10`、`logic.XPPerKill = 5`、`logic.XPPerLevel = 200`

- [ ] **Step 1: 写失败的测试**

新建 `joltgo/logic/level_test.go`：

```go
package logic

import "testing"

func TestLevelDerivation(t *testing.T) {
	cases := []struct {
		xp    int64
		level int32
		into  int64
	}{
		{xp: 0, level: 1, into: 0},
		{xp: 199, level: 1, into: 199},
		{xp: 200, level: 2, into: 0},
		{xp: 399, level: 2, into: 199},
		{xp: 400, level: 3, into: 0},
		{xp: 1000, level: 6, into: 0},
	}
	for _, tc := range cases {
		if got := Level(tc.xp); got != tc.level {
			t.Fatalf("Level(%d) = %d，期望 %d", tc.xp, got, tc.level)
		}
		if got := XPIntoLevel(tc.xp); got != tc.into {
			t.Fatalf("XPIntoLevel(%d) = %d，期望 %d", tc.xp, got, tc.into)
		}
	}
	// 负经验按 0 处理，不能出现负数等级。
	if got := Level(-50); got != 1 {
		t.Fatalf("Level(-50) = %d，期望 1", got)
	}
	if got := XPIntoLevel(-50); got != 0 {
		t.Fatalf("XPIntoLevel(-50) = %d，期望 0", got)
	}
	if got := XPForNextLevel(); got != XPPerLevel {
		t.Fatalf("XPForNextLevel() = %d，期望 %d", got, XPPerLevel)
	}
}

func TestMatchXP(t *testing.T) {
	if got := MatchXP(true, 10); got != 100 {
		t.Fatalf("胜利 + 10 杀 = %d，期望 100", got)
	}
	if got := MatchXP(false, 3); got != 25 {
		t.Fatalf("失败 + 3 杀 = %d，期望 25", got)
	}
	if got := MatchXP(false, 0); got != XPPerLoss {
		t.Fatalf("失败 0 杀 = %d，期望 %d", got, XPPerLoss)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd joltgo; PATH="$PWD:$PATH" go test -count=1 ./logic -run 'TestLevelDerivation|TestMatchXP'`
Expected: 编译失败，`undefined: Level`。

- [ ] **Step 3: 实现**

新建 `joltgo/logic/level.go`：

```go
package logic

// 等级与经验的数值全部集中在这里：等级**不落库**，由 XP 现算，所以曲线随时可调，
// 既不需要迁移存量数据，也不可能出现「等级字段与经验不同步」的脏数据。
const (
	XPPerWin  int64 = 50
	XPPerLoss int64 = 10
	XPPerKill int64 = 5

	// XPPerLevel 是升一级所需经验。当前曲线是线性的，
	// 换成非线性曲线时只要改这三个函数，客户端不用动（它只消费下发的三个字段）。
	XPPerLevel int64 = 200
)

// Level 从累计经验派生等级：0 XP = 1 级，200 XP = 2 级。
func Level(xp int64) int32 {
	if xp < 0 {
		xp = 0
	}
	return int32(1 + xp/XPPerLevel)
}

// XPIntoLevel 是当前等级内已积累的经验。
func XPIntoLevel(xp int64) int64 {
	if xp < 0 {
		xp = 0
	}
	return xp - int64(Level(xp)-1)*XPPerLevel
}

// XPForNextLevel 是升到下一级需要的经验总量（线性曲线下恒等于 XPPerLevel）。
func XPForNextLevel() int64 { return XPPerLevel }

// MatchXP 是一局结束时获得的经验：胜/负基础值 + 各自击杀数。
func MatchXP(won bool, kills int32) int64 {
	base := XPPerLoss
	if won {
		base = XPPerWin
	}
	return base + int64(kills)*XPPerKill
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd joltgo; PATH="$PWD:$PATH" go test -count=1 ./logic`
Expected: `ok  joltgo/logic`

- [ ] **Step 5: 提交**

```bash
git add joltgo/logic/level.go joltgo/logic/level_test.go
git commit -m "feat: 增加等级与经验的派生规则" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 3: wire 协议新增（不改现有字段）

**Files:**
- Modify: `joltgo/game/protos/game.proto`
- Regenerate: `joltgo/game/protos/game.pb.go`

**Interfaces:**
- Consumes: 无
- Produces（Go 类型名，后续任务直接引用）：
  - `protos.SlotResult{Uid string; Kills, Deaths int32}`
  - `protos.RecordMatchMsg{MatchId string; Slots []*SlotResult; WinnerSlot, DurationSeconds int32}`
  - `protos.RecordMatchReply{Ok, Applied bool; Reason string}`
  - `protos.MatchRecord{MatchId string; Won bool; Kills, Deaths, OpponentKills, DurationSeconds int32; OpponentName string; EndedAt int64}`
  - `protos.PlayerProfileMsg{}` / `protos.PlayerProfileReply{Ok bool; Reason string; Level int32; XP, XPIntoLevel, XPForNextLevel int64; Kills, Deaths, Matches, Wins, Losses int32; RecentMatches []*MatchRecord}`
  - `protos.MatchCancelMsg{}` / `protos.MatchCancelReply{Ok bool; Reason string}`
  - `protos.MatchStatus{QueuedPlayers, WaitedSeconds int32}`
  - `protos.MatchEnded{MatchId string; WinnerSlot int32; Slots []*SlotResult; DurationSeconds int32}`

- [ ] **Step 1: 追加消息**

在 `joltgo/game/protos/game.proto` 末尾追加（**不要动 `CommandMsg`**，删字段是 Task 9 的事）：

```proto
// 单个槽位的结算数据。下标即 player_idx；uid 为空串表示该槽位没有真实玩家。
message SlotResult {
  string uid = 1;
  int32 kills = 2;
  int32 deaths = 3;
}

// game → logic：一局分出胜负时的结算上报（remote route logic.logic.recordmatch）。
message RecordMatchMsg {
  string match_id = 1;
  repeated SlotResult slots = 2;
  int32 winner_slot = 3;
  int32 duration_seconds = 4;
}

message RecordMatchReply {
  bool ok = 1;
  bool applied = 2; // false = 收到但按规则未入账
  string reason = 3;
}

// 历史里的单场战绩（下发结构，与持久化结构分开）。
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

message MatchCancelMsg {}

message MatchCancelReply {
  bool ok = 1;
  string reason = 2; // cancelled | already_matched | not_queued | unauthenticated
}

// onMatchStatus 推送载荷：匹配期每秒一次。
message MatchStatus {
  int32 queued_players = 1;
  int32 waited_seconds = 2;
}

// onMatchEnded 推送载荷：一局结束的权威信号。
message MatchEnded {
  string match_id = 1;
  int32 winner_slot = 2;
  repeated SlotResult slots = 3;
  int32 duration_seconds = 4;
}
```

- [ ] **Step 2: 重新生成**

Run（在 `joltgo/game/protos` 下按仓库既有方式生成；若不确定，见 `docs/BUILD.md` 的 protoc 段落）：

```bash
cd joltgo/game/protos
protoc --go_out=. --go_opt=paths=source_relative game.proto
```

Expected: `game.pb.go` 里出现 `type SlotResult struct`、`type MatchEnded struct` 等；`git diff` 只新增、不修改既有消息的字段号。

- [ ] **Step 3: 确认全仓库仍能编译**

Run: `cd joltgo; go build ./...`
Expected: 无输出（编译通过）。此时还没有任何逻辑用到新消息。

- [ ] **Step 4: 提交**

```bash
git add joltgo/game/protos/game.proto joltgo/game/protos/game.pb.go
git commit -m "feat: 增加战绩、档案与匹配交互的 wire 消息" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 4: logic 记账与档案查询

**Files:**
- Modify: `joltgo/logic/service.go`
- Modify: `joltgo/logic/component.go`
- Modify: `joltgo/logic/remote.go`
- Test: `joltgo/logic/service_test.go`

**Interfaces:**
- Consumes: `persist.PlayerStats` / `PlayerStatsDelta` / `PlayerMatchRecord` / `GetStats` / `SaveStats` / `AddStats` / `AppendMatchRecord` / `ListMatchRecords` / `UsernameByID`（Task 1）；`Level` / `XPIntoLevel` / `XPForNextLevel` / `MatchXP`（Task 2）；`protos.*`（Task 3）
- Produces:
  - `logic.Profile{XP int64; Kills, Deaths, Matches, Wins, Losses int32; Recent []persist.PlayerMatchRecord}`
  - `(*Service) Profile(ctx, accountID string) (Profile, error)`
  - `logic.MatchSlot{UID string; Kills, Deaths int32}`
  - `logic.MatchResult{MatchID string; Slots []MatchSlot; WinnerSlot, DurationSeconds int32}`
  - `(*Service) RecordMatch(ctx, MatchResult) (bool, error)`（返回 applied）
  - `(*Remote) RecordMatch(ctx, *protos.RecordMatchMsg) (*protos.RecordMatchReply, error)`
  - `(*Component) Profile(ctx, *protos.PlayerProfileMsg) (*protos.PlayerProfileReply, error)`

- [ ] **Step 1: 写失败的测试**

追加到 `joltgo/logic/service_test.go`：

```go
func TestRecordMatchUpdatesBothPlayers(t *testing.T) {
	env := newServiceTestEnv(t)
	ctx := context.Background()
	if err := env.svc.EnsureProfile(ctx, "7"); err != nil {
		t.Fatalf("EnsureProfile(7): %v", err)
	}
	if err := env.svc.EnsureProfile(ctx, "8"); err != nil {
		t.Fatalf("EnsureProfile(8): %v", err)
	}
	if err := env.store.SaveStats(ctx, 8, persist.PlayerStats{}); err != nil {
		t.Fatalf("SaveStats(8): %v", err)
	}
	// 对手名从 account Hash 只读解析；这里手工塞一条，验证快照落库。
	if err := env.mr.HSet("acct:1:8:0", "1", "bob"); err != nil {
		t.Fatalf("HSet: %v", err)
	}

	applied, err := env.svc.RecordMatch(ctx, MatchResult{
		MatchID: "m1",
		Slots:   []MatchSlot{{UID: "7", Kills: 10, Deaths: 7}, {UID: "8", Kills: 7, Deaths: 10}},
		WinnerSlot: 0, DurationSeconds: 84,
	})
	if err != nil || !applied {
		t.Fatalf("RecordMatch: applied=%v err=%v", applied, err)
	}

	winner, err := env.svc.Profile(ctx, "7")
	if err != nil {
		t.Fatalf("Profile(7): %v", err)
	}
	if winner.Matches != 1 || winner.Wins != 1 || winner.Losses != 0 || winner.Kills != 10 || winner.Deaths != 7 {
		t.Fatalf("胜者统计不对: %+v", winner)
	}
	if winner.XP != 100 { // 50 + 10*5
		t.Fatalf("胜者 XP = %d，期望 100", winner.XP)
	}
	loser, err := env.svc.Profile(ctx, "8")
	if err != nil {
		t.Fatalf("Profile(8): %v", err)
	}
	if loser.Matches != 1 || loser.Wins != 0 || loser.Losses != 1 || loser.XP != 45 { // 10 + 7*5
		t.Fatalf("败者统计不对: %+v", loser)
	}

	if len(winner.Recent) != 1 {
		t.Fatalf("胜者应有一条历史，得到 %d", len(winner.Recent))
	}
	rec := winner.Recent[0]
	if rec.MatchID != "m1" || !rec.Won || rec.Kills != 10 || rec.OpponentKills != 7 ||
		rec.DurationSeconds != 84 || rec.OpponentName != "bob" || rec.EndedAt == 0 {
		t.Fatalf("历史记录不对: %+v", rec)
	}
	if loser.Recent[0].OpponentName != "" {
		t.Fatalf("账号 7 没有 account Hash，对手名应为空串，得到 %q", loser.Recent[0].OpponentName)
	}
}

func TestRecordMatchSkipsIncompleteSlots(t *testing.T) {
	env := newServiceTestEnv(t)
	ctx := context.Background()
	if err := env.svc.EnsureProfile(ctx, "7"); err != nil {
		t.Fatalf("EnsureProfile: %v", err)
	}

	applied, err := env.svc.RecordMatch(ctx, MatchResult{
		MatchID: "solo",
		Slots:   []MatchSlot{{UID: "7", Kills: 10}, {}},
		WinnerSlot: 0, DurationSeconds: 30,
	})
	if err != nil {
		t.Fatalf("RecordMatch 不应返回错误: %v", err)
	}
	if applied {
		t.Fatal("槽位不齐时不应入账")
	}
	profile, err := env.svc.Profile(ctx, "7")
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if profile.Matches != 0 || profile.XP != 0 || len(profile.Recent) != 0 {
		t.Fatalf("未入账时档案不应变化: %+v", profile)
	}
}

func TestProfileCreatesMissingStatsRow(t *testing.T) {
	env := newServiceTestEnv(t)
	ctx := context.Background()
	if err := env.svc.EnsureProfile(ctx, "7"); err != nil {
		t.Fatalf("EnsureProfile: %v", err)
	}
	// 模拟存量账号：钱包/背包有，档案行被手工删掉。
	env.mr.Del(persist.PlayerProfileKey(7))

	profile, err := env.svc.Profile(ctx, "7")
	if err != nil {
		t.Fatalf("缺档案行时 Profile 应就地补建而不是报错: %v", err)
	}
	if profile.XP != 0 || profile.Level != 1 || profile.Matches != 0 {
		t.Fatalf("补建后应是零值档案: %+v", profile)
	}
	if _, ok, err := env.store.GetStats(ctx, 7); err != nil || !ok {
		t.Fatalf("补建后档案行应存在: ok=%v err=%v", ok, err)
	}
}

func TestProfileRejectsUnknownAccount(t *testing.T) {
	env := newServiceTestEnv(t)
	if _, err := env.svc.Profile(context.Background(), "not-a-number"); ReasonOf(err) != ReasonUnauthenticated {
		t.Fatalf("非法 accountID 应返回 unauthenticated，得到 %v", err)
	}
}
```

`serviceTestEnv` 需要暴露 `mr *miniredis.Miniredis`；如果它现在没有这个字段，就在 `newServiceTestEnv` 里补上：

```go
type serviceTestEnv struct {
	svc   *Service
	store *persist.PlayerStore
	mr    *miniredis.Miniredis
}

// newServiceTestEnv 里：return &serviceTestEnv{svc: ..., store: store, mr: mr}
```

`acct:1:8:0` 的 `"1"` 必须是 `account` 生成码里 `username` 字段的真实编号（打开 `joltgo/persist/protos/account.redis.go` 确认后写死到这个测试里）。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd joltgo; PATH="$PWD:$PATH" go test -count=1 ./logic -run 'TestRecordMatch|TestProfile'`
Expected: 编译失败，`undefined: MatchResult`、`env.svc.Profile undefined`。

- [ ] **Step 3: 实现领域逻辑**

在 `joltgo/logic/service.go` 里加错误码：

```go
const (
	ReasonNotEnoughPlayers = "not_enough_players"
)
```

扩展 `Store` 接口（**必须与 `persist.PlayerPersistence` 的新方法一一对应**）：

```go
type Store interface {
	GetWallet(context.Context, uint64) (persist.PlayerWallet, bool, error)
	SaveWallet(context.Context, uint64, persist.PlayerWallet) error
	AddCoins(context.Context, uint64, int64) error
	DeleteWallet(context.Context, uint64) error
	GetBag(context.Context, uint64) (persist.PlayerBag, bool, error)
	SaveBag(context.Context, uint64, persist.PlayerBag) error
	DeleteBag(context.Context, uint64) error
	// 玩家档案（战绩）与历史
	GetStats(context.Context, uint64) (persist.PlayerStats, bool, error)
	SaveStats(context.Context, uint64, persist.PlayerStats) error
	AddStats(context.Context, uint64, persist.PlayerStatsDelta) error
	AppendMatchRecord(context.Context, uint64, persist.PlayerMatchRecord) error
	ListMatchRecords(context.Context, uint64, int) ([]persist.PlayerMatchRecord, error)
	UsernameByID(context.Context, uint64) (string, bool, error)
}
```

加类型与两个方法：

```go
// Profile 是个人信息页要的一份档案快照。
type Profile struct {
	XP      int64
	Kills   int32
	Deaths  int32
	Matches int32
	Wins    int32
	Losses  int32
	Recent  []persist.PlayerMatchRecord
}

// MatchSlot 是一局里单个槽位的战绩。
type MatchSlot struct {
	UID    string
	Kills  int32
	Deaths int32
}

// MatchResult 是一局的结算结果（由 game 上报）。
type MatchResult struct {
	MatchID         string
	Slots           []MatchSlot
	WinnerSlot      int32
	DurationSeconds int32
}

// Profile 读取玩家档案。存量账号没有档案行（redis-data 永不清空，老账号一定缺），
// 所以这里读到缺行要**就地补建零值档案**再返回，而不是报 profile_missing。
func (s *Service) Profile(ctx context.Context, accountID string) (Profile, error) {
	id, err := parseAccountID(accountID)
	if err != nil {
		return Profile{}, err
	}
	stats, ok, err := s.store.GetStats(ctx, id)
	if err != nil {
		return Profile{}, err
	}
	if !ok {
		stats = persist.PlayerStats{}
		if err := s.store.SaveStats(ctx, id, stats); err != nil {
			return Profile{}, err
		}
	}
	recent, err := s.store.ListMatchRecords(ctx, id, 20)
	if err != nil {
		return Profile{}, err
	}
	return Profile{
		XP:      stats.XP,
		Kills:   int32(stats.Kills),
		Deaths:  int32(stats.Deaths),
		Matches: int32(stats.Matches),
		Wins:    int32(stats.Wins),
		Losses:  int32(stats.Losses),
		Recent:  recent,
	}, nil
}

// RecordMatch 把一局结果记进两名玩家的档案与历史，返回是否真的入账。
//
// 两个槽位都必须有真实 uid 才入账：兜底单人局已经删除，这条是防御 —— 将来若有
// 练习局/调试入口，不至于把「10 杀木桩」刷成胜率。
func (s *Service) RecordMatch(ctx context.Context, res MatchResult) (bool, error) {
	if len(res.Slots) < 2 || res.Slots[0].UID == "" || res.Slots[1].UID == "" {
		return false, nil
	}
	if res.WinnerSlot != 0 && res.WinnerSlot != 1 {
		return false, reason(ReasonInternal)
	}

	for slot := range res.Slots {
		me := res.Slots[slot]
		opp := res.Slots[1-slot]
		id, err := parseAccountID(me.UID)
		if err != nil {
			return false, err
		}
		won := int32(slot) == res.WinnerSlot
		delta := persist.PlayerStatsDelta{
			XP:      MatchXP(won, me.Kills),
			Kills:   int64(me.Kills),
			Deaths:  int64(me.Deaths),
			Matches: 1,
		}
		if won {
			delta.Wins = 1
		} else {
			delta.Losses = 1
		}
		if err := s.store.AddStats(ctx, id, delta); err != nil {
			// 没有重试就没有补偿机会：只记日志，不返回错误给 game（它不会重发）。
			log.Printf("logic: record match %s stats for %s failed: %v", res.MatchID, me.UID, err)
			continue
		}
		opponentName := ""
		if oppID, err := parseAccountID(opp.UID); err == nil {
			if name, found, err := s.store.UsernameByID(ctx, oppID); err != nil {
				log.Printf("logic: resolve username for %s failed: %v", opp.UID, err)
			} else if found {
				opponentName = name
			}
		}
		rec := persist.PlayerMatchRecord{
			MatchID:         res.MatchID,
			Won:             won,
			Kills:           me.Kills,
			Deaths:          me.Deaths,
			OpponentKills:   opp.Kills,
			DurationSeconds: res.DurationSeconds,
			OpponentName:    opponentName,
			EndedAt:         s.now().Unix(),
		}
		if err := s.store.AppendMatchRecord(ctx, id, rec); err != nil {
			log.Printf("logic: append match history for %s failed: %v", me.UID, err)
		}
	}
	return true, nil
}
```

`EnsureProfile` 里补建档案行：在它已有的「wallet/bag 都存在就直接返回」判断附近，改成同时检查 stats —— 最简单可靠的做法是让它的 `hasWallet && hasBag` 分支里先确保档案行存在：

```go
if hasWallet && hasBag {
	if _, hasStats, err := s.store.GetStats(ctx, id); err != nil {
		return err
	} else if !hasStats {
		if err := s.store.SaveStats(ctx, id, persist.PlayerStats{}); err != nil {
			return err
		}
	}
	return nil
}
```

并在锁内建号分支里同样补一句 `SaveStats(ctx, id, persist.PlayerStats{})`（**放在建 bag 之后**，与既有 wallet→bag 的顺序一致）。

- [ ] **Step 4: 接线 handler 与 remote**

`joltgo/logic/component.go` 加：

```go
// Profile 是客户端请求 handler（route "logic.profile"）：身份只来自会话绑定。
func (c *Component) Profile(ctx context.Context, _ *protos.PlayerProfileMsg) (*protos.PlayerProfileReply, error) {
	profile, err := c.service.Profile(ctx, c.boundAccount(ctx))
	if err != nil {
		return &protos.PlayerProfileReply{Ok: false, Reason: ReasonOf(err)}, nil
	}
	reply := &protos.PlayerProfileReply{
		Ok:              true,
		Level:           Level(profile.XP),
		XP:              profile.XP,
		XPIntoLevel:     XPIntoLevel(profile.XP),
		XPForNextLevel:  XPForNextLevel(),
		Kills:           profile.Kills,
		Deaths:          profile.Deaths,
		Matches:         profile.Matches,
		Wins:            profile.Wins,
		Losses:          profile.Losses,
		RecentMatches:   make([]*protos.MatchRecord, 0, len(profile.Recent)),
	}
	for _, rec := range profile.Recent {
		reply.RecentMatches = append(reply.RecentMatches, &protos.MatchRecord{
			MatchId:         rec.MatchID,
			Won:             rec.Won,
			Kills:           rec.Kills,
			Deaths:          rec.Deaths,
			OpponentKills:   rec.OpponentKills,
			DurationSeconds: rec.DurationSeconds,
			OpponentName:    rec.OpponentName,
			EndedAt:         rec.EndedAt,
		})
	}
	return reply, nil
}
```

`joltgo/logic/remote.go` 加（**remote 而非 handler**：`game` 是用 `RPC` 打过来的）：

```go
// RecordMatch 是服务间 remote（route "logic.recordmatch"）：game 在对局结束时上报。
func (r *Remote) RecordMatch(ctx context.Context, msg *protos.RecordMatchMsg) (*protos.RecordMatchReply, error) {
	slots := make([]MatchSlot, 0, len(msg.Slots))
	for _, slot := range msg.Slots {
		if slot == nil {
			slots = append(slots, MatchSlot{})
			continue
		}
		slots = append(slots, MatchSlot{UID: slot.Uid, Kills: slot.Kills, Deaths: slot.Deaths})
	}
	applied, err := r.service.RecordMatch(ctx, MatchResult{
		MatchID:         msg.MatchId,
		Slots:           slots,
		WinnerSlot:      msg.WinnerSlot,
		DurationSeconds: msg.DurationSeconds,
	})
	if err != nil {
		return &protos.RecordMatchReply{Ok: false, Reason: ReasonOf(err)}, nil
	}
	if !applied {
		return &protos.RecordMatchReply{Ok: true, Applied: false, Reason: ReasonNotEnoughPlayers}, nil
	}
	return &protos.RecordMatchReply{Ok: true, Applied: true}, nil
}
```

`logic/remote.go` 的 import 需要加 `"log"` 吗？不需要 —— 日志在 `service.go` 里（那里已经 import 了 `log`）。

- [ ] **Step 5: 跑测试确认通过**

Run: `cd joltgo; PATH="$PWD:$PATH" go test -count=1 ./logic ./persist`
Expected: 两个包都 `ok`

- [ ] **Step 6: 提交**

```bash
git add joltgo/logic
git commit -m "feat: logic 支持战绩记账与个人档案查询" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 5: sim 产出结算快照

**Files:**
- Modify: `joltgo/sim/simulation.go`
- Modify: `joltgo/sim/systems.go`
- Test: `joltgo/sim/sim_test.go`

**Interfaces:**
- Consumes: `MaxPlayers`（同包常量）
- Produces:
  - `sim.MatchOutcome{W inner? }` → 精确定义：`type MatchOutcome struct { WinnerSlot int; Kills [MaxPlayers]int32; Deaths [MaxPlayers]int32; DurationSeconds int32 }`
  - `(*Simulation) DrainOutcome() (MatchOutcome, bool)`（取走即清）

> 本任务**不动** `Reset`：删除它是 Task 9 的事，这样每个任务都能独立编译并保持测试绿。

- [ ] **Step 1: 写失败的测试**

追加到 `joltgo/sim/sim_test.go`：

```go
// 结算快照必须「取走即清」，否则 game 侧会重复上报同一场。
func TestDrainOutcomeIsTakeOnce(t *testing.T) {
	s := New(newFakePhysics())
	s.Init()
	defer s.Shutdown()

	if _, ok := s.DrainOutcome(); ok {
		t.Fatal("还没分出胜负时不应有结算快照")
	}

	// 直接写分数（同包测试可以直接访问 score），省掉打 10 次真实命中。
	s.score[0] = PlayerScore{Kills: killTarget, Deaths: 4}
	s.score[1] = PlayerScore{Kills: 3, Deaths: killTarget}
	for i := 0; i < 3; i++ {
		s.Step()
	}

	out, ok := s.DrainOutcome()
	if !ok {
		t.Fatal("判出胜负后应有结算快照")
	}
	if out.WinnerSlot != 0 {
		t.Fatalf("WinnerSlot = %d，期望 0", out.WinnerSlot)
	}
	if out.Kills[0] != killTarget || out.Kills[1] != 3 || out.Deaths[0] != 4 || out.Deaths[1] != killTarget {
		t.Fatalf("结算快照的 k/d 不对: %+v", out)
	}
	if out.DurationSeconds != int32(s.step/20) {
		t.Fatalf("DurationSeconds = %d，期望 %d", out.DurationSeconds, s.step/20)
	}
	if _, ok := s.DrainOutcome(); ok {
		t.Fatal("第二次 DrainOutcome 必须返回 ok=false（取走即清）")
	}
}

// 判出胜负后不得再重开一局：分数与 winner 都要停在那里等 game 侧终结实例。
func TestWinnerStopsTheRound(t *testing.T) {
	s := New(newFakePhysics())
	s.Init()
	defer s.Shutdown()

	s.score[1] = PlayerScore{Kills: killTarget}
	for i := 0; i < 3; i++ {
		s.Step()
	}
	winnerStep := s.step
	winner := s.winner
	if winner != 1 {
		t.Fatalf("winner = %d，期望 1", winner)
	}

	// 旧实现会在 matchOverTicks（5 秒 = 100 tick）后自动 Reset；这里推 200 tick。
	for i := 0; i < 200; i++ {
		s.Step()
	}
	if s.winner != winner {
		t.Fatalf("胜负判定后不应被重开，winner 变成了 %d", s.winner)
	}
	if s.score[1].Kills != killTarget {
		t.Fatalf("胜负判定后分数不应被清零，得到 %d", s.score[1].Kills)
	}
	if s.step <= winnerStep {
		t.Fatalf("tick 应继续推进（实例由 game 侧停止），step=%d", s.step)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd joltgo; PATH="$PWD:$PATH" go test -count=1 ./sim -run 'TestDrainOutcomeIsTakeOnce|TestWinnerStopsTheRound'`
Expected: `TestDrainOutcomeIsTakeOnce` 编译失败（`s.DrainOutcome undefined`）；`TestWinnerStopsTheRound` 会因自动重开而失败。

- [ ] **Step 3: 实现结算快照**

在 `joltgo/sim/simulation.go` 的 `Simulation` 结构体里加两个字段（紧挨着 `winner` / `overAt`）：

```go
	outcome    MatchOutcome // 本局结算快照（判出胜负时填充，由 game 侧取走）
	hasOutcome bool         // outcome 是否待取（取走即清，保证只上报一次）
```

在同文件加类型与方法：

```go
// MatchOutcome 是一局分出胜负时的结算快照。一个实例只打一局，所以它只会被填充一次。
type MatchOutcome struct {
	WinnerSlot      int
	Kills           [MaxPlayers]int32
	Deaths          [MaxPlayers]int32
	DurationSeconds int32
}

// DrainOutcome 取走结算快照（取走即清）。与 DrainFrame 同一风格：调用方每 tick
// 调用一次，拿到 ok=false 就什么都不做。
func (s *Simulation) DrainOutcome() (MatchOutcome, bool) {
	if !s.hasOutcome {
		return MatchOutcome{}, false
	}
	s.hasOutcome = false
	return s.outcome, true
}
```

`reset()` 里把 `hasOutcome` 一并清掉 —— 即使 Task 9 之后 `reset()` 会被删掉，此刻它还在，必须保持自洽。

- [ ] **Step 4: 让 matchSystem 产出快照并停止重开**

把 `joltgo/sim/systems.go` 的 `matchSystem` 改成（删掉 `matchOverTicks` 的重开分支）：

```go
// matchSystem 对局结算：某一方击杀数先到 killTarget 即分出胜负（写 Game.Winner）。
// 胜负判定后**不重开**：填充一份一次性的结算快照，由 game 侧取走、上报并终结实例。
func (s *Simulation) matchSystem() {
	if s.winner >= 0 {
		return
	}
	for i := 0; i < MaxPlayers; i++ {
		if s.score[i].Kills < killTarget {
			continue
		}
		s.winner = int32(i)
		s.syncGameState()
		s.rep.Set(uint32(s.game), attrGameWinner, replication.I32(s.winner))
		out := MatchOutcome{WinnerSlot: i, DurationSeconds: int32(s.step / 20)}
		for slot := 0; slot < MaxPlayers; slot++ {
			out.Kills[slot] = s.score[slot].Kills
			out.Deaths[slot] = s.score[slot].Deaths
		}
		s.outcome = out
		s.hasOutcome = true
		return
	}
}
```

同时删掉不再使用的 `matchOverTicks` 常量与 `overAt` 字段的所有引用（`reset()` 里的赋值、`stepOne` 里的清理）——`gofmt` 与 `go build` 会指出漏掉的地方。

- [ ] **Step 5: 跑测试确认通过**

Run: `cd joltgo; PATH="$PWD:$PATH" go test -count=1 ./sim ./replication`
Expected: 两个包都 `ok`（若原先有断言「5 秒后自动重开」的用例，按 Task 9 的清单一并删掉，不要留着自相矛盾的期望）。

- [ ] **Step 6: 提交**

```bash
git add joltgo/sim
git commit -m "feat: 对局分出胜负时产出结算快照且不再重开" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 6: game 上报战绩并终结实例

**Files:**
- Modify: `joltgo/game/instance.go`
- Test: `joltgo/game/component_test.go`

**Interfaces:**
- Consumes: `sim.MatchOutcome` / `DrainOutcome`（Task 5）；`protos.RecordMatchMsg` / `protos.SlotResult` / `protos.MatchEnded`（Task 3）
- Produces:
  - `(i *Instance) finishMatch(out sim.MatchOutcome)`（推 `onMatchEnded`、异步上报、`onExit`、关 `stop`）
  - `(i *Instance) reportMatch(out sim.MatchOutcome)`（一条 `RPC` 发往 `logic.logic.recordmatch`）
  - `(i *Instance) matchEnded(out sim.MatchOutcome) *protos.MatchEnded`
  - `(i *Instance) presentUIDs() []string`

- [ ] **Step 1: 写失败的测试**

追加到 `joltgo/game/component_test.go`：

```go
// recordingApp 只实现本任务用到的两个方法：推送与 RPC。
type recordingApp struct {
	pitaya.Pitaya
	mu       sync.Mutex
	pushed   []string
	pushArg  []interface{}
	pushUIDs [][]string
	rpcRoute []string
	rpcArg   []interface{}
}

func (a *recordingApp) SendPushToUsers(route string, v interface{}, uids []string, frontendType string) ([]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pushed = append(a.pushed, route)
	a.pushArg = append(a.pushArg, v)
	a.pushUIDs = append(a.pushUIDs, uids)
	return nil, nil
}

func (a *recordingApp) RPC(ctx context.Context, routeStr string, reply proto.Message, arg proto.Message) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rpcRoute = append(a.rpcRoute, routeStr)
	a.rpcArg = append(a.rpcArg, arg)
	return nil
}

func (a *recordingApp) waitRPCs(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		got := len(a.rpcRoute)
		a.mu.Unlock()
		if got >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等 %d 次 RPC 超时", want)
}

func TestFinishMatchPushesThenReportsThenExits(t *testing.T) {
	app := &recordingApp{}
	exited := 0
	inst := &Instance{
		app:     app,
		matchID: "m1",
		uids:    []string{"7", "8"},
		stop:    make(chan struct{}),
		onExit:  func() { exited++ },
	}

	inst.finishMatch(sim.MatchOutcome{
		WinnerSlot:      1,
		Kills:           [sim.MaxPlayers]int32{3, 10},
		Deaths:          [sim.MaxPlayers]int32{10, 3},
		DurationSeconds: 84,
	})

	if len(app.pushed) != 1 || app.pushed[0] != endedRoute {
		t.Fatalf("应先推一条 %s，得到 %v", endedRoute, app.pushed)
	}
	ended, ok := app.pushArg[0].(*protos.MatchEnded)
	if !ok {
		t.Fatalf("onMatchEnded 载荷类型不对: %T", app.pushArg[0])
	}
	if ended.MatchId != "m1" || ended.WinnerSlot != 1 || ended.DurationSeconds != 84 {
		t.Fatalf("onMatchEnded 载荷不对: %+v", ended)
	}
	if len(ended.Slots) != 2 || ended.Slots[0].Uid != "7" || ended.Slots[1].Kills != 10 {
		t.Fatalf("onMatchEnded 的槽位数据不对: %+v", ended.Slots)
	}
	if len(app.pushUIDs[0]) != 2 || app.pushUIDs[0][0] != "7" {
		t.Fatalf("onMatchEnded 应推给两个玩家，得到 %v", app.pushUIDs[0])
	}

	app.waitRPCs(t, 1)
	if app.rpcRoute[0] != recordMatchRoute {
		t.Fatalf("上报 route = %s，期望 %s", app.rpcRoute[0], recordMatchRoute)
	}
	record, ok := app.rpcArg[0].(*protos.RecordMatchMsg)
	if !ok {
		t.Fatalf("上报载荷类型不对: %T", app.rpcArg[0])
	}
	if record.MatchId != "m1" || record.WinnerSlot != 1 || record.DurationSeconds != 84 {
		t.Fatalf("上报载荷不对: %+v", record)
	}
	if len(record.Slots) != 2 || record.Slots[0].Uid != "7" || record.Slots[0].Deaths != 10 || record.Slots[1].Uid != "8" {
		t.Fatalf("上报槽位数据不对: %+v", record.Slots)
	}

	select {
	case <-inst.stop:
	default:
		t.Fatal("实例必须被停止（否则 goroutine 不会退出）")
	}
	if exited != 1 {
		t.Fatalf("onExit 应恰好调用一次，得到 %d", exited)
	}
}

// 空槽位（单人局）也要照发：是否入账由 logic 判定。
func TestFinishMatchToleratesMissingSlot(t *testing.T) {
	app := &recordingApp{}
	inst := &Instance{
		app:     app,
		matchID: "solo",
		uids:    []string{"7"},
		stop:    make(chan struct{}),
	}
	inst.finishMatch(sim.MatchOutcome{WinnerSlot: 0, DurationSeconds: 10})
	app.waitRPCs(t, 1)

	record := app.rpcArg[0].(*protos.RecordMatchMsg)
	if len(record.Slots) != 2 {
		t.Fatalf("槽位应补齐到 %d 个，得到 %d", sim.MaxPlayers, len(record.Slots))
	}
	if record.Slots[0].Uid != "7" || record.Slots[1].Uid != "" {
		t.Fatalf("空槽位必须是空串 uid，得到 %+v", record.Slots)
	}
	if len(app.pushUIDs[0]) != 1 {
		t.Fatalf("推送目标应只含在座玩家，得到 %v", app.pushUIDs[0])
	}
}
```

`component_test.go` 的 import 需要补齐 `"sync"`、`"time"`、`"github.com/golang/protobuf/proto"`、`"joltgo/sim"`（其余已有）。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd joltgo; PATH="$PWD:$PATH" go test -count=1 ./game -run 'TestFinishMatch'`
Expected: 编译失败，`undefined: endedRoute`、`inst.finishMatch undefined`。

- [ ] **Step 3: 实现上报与终结**

在 `joltgo/game/instance.go` 的常量区加（`frontendType` 已存在，可复用）：

```go
const (
	// endedRoute 是「本局结束」的权威信号：实例随即终结、帧流随之中断，
	// 客户端必须靠它停下接收看门狗，否则每局打完都会被误判成掉线。
	endedRoute = "onMatchEnded"
	// recordMatchRoute 是 game → logic 的战绩上报（logic 侧以 remote 注册）。
	recordMatchRoute   = "logic.logic.recordmatch"
	recordMatchTimeout = 2 * time.Second
)
```

加方法：

```go
// finishMatch 是一局结束后的收尾，必须由实例 goroutine 调用：先推送（此时本 tick 的
// 增量帧已经 broadcast 过），再异步上报，最后走与空闲回收**相同**的退出路径
// （onExit 摘注册表 + Stop）。只调 Stop 是不够的 —— 那样 goroutine 会从
// case <-i.stop 返回，而那条路不调用 onExit，uid→实例表会永久留下死实例。
func (i *Instance) finishMatch(out sim.MatchOutcome) {
	if uids := i.presentUIDs(); len(uids) > 0 {
		if _, err := i.app.SendPushToUsers(endedRoute, i.matchEnded(out), uids, frontendType); err != nil {
			log.Printf("instance %s: push %s failed: %v", i.matchID, endedRoute, err)
		}
	}
	go i.reportMatch(out)
	i.Stop()
	if i.onExit != nil {
		i.onExit()
	}
}

// presentUIDs 返回在座玩家的 uid（跳过空槽位）。
func (i *Instance) presentUIDs() []string {
	out := make([]string, 0, len(i.uids))
	for _, uid := range i.uids {
		if uid != "" {
			out = append(out, uid)
		}
	}
	return out
}

func (i *Instance) uidAt(slot int) string {
	if slot < 0 || slot >= len(i.uids) {
		return ""
	}
	return i.uids[slot]
}

func (i *Instance) matchEnded(out sim.MatchOutcome) *protos.MatchEnded {
	msg := &protos.MatchEnded{
		MatchId:         i.matchID,
		WinnerSlot:      int32(out.WinnerSlot),
		DurationSeconds: out.DurationSeconds,
		Slots:           make([]*protos.SlotResult, 0, sim.MaxPlayers),
	}
	for slot := 0; slot < sim.MaxPlayers; slot++ {
		msg.Slots = append(msg.Slots, &protos.SlotResult{
			Uid:    i.uidAt(slot),
			Kills:  out.Kills[slot],
			Deaths: out.Deaths[slot],
		})
	}
	return msg
}

// reportMatch 在独立 goroutine 里上报战绩：**绝不能在实例 goroutine 里同步发** ——
// pitaya 的 RPC 默认 5 秒超时，卡住就是 100 个 tick 停摆、客户端看门狗立刻判定掉线。
// 用 RPC（不是 RPCTo）：RPCType_User 走 router 的 default route，从 game 节点可直接
// 选到一个 logic 节点。失败只记日志：不重试、不去重。
func (i *Instance) reportMatch(out sim.MatchOutcome) {
	ctx, cancel := context.WithTimeout(context.Background(), recordMatchTimeout)
	defer cancel()
	msg := &protos.RecordMatchMsg{
		MatchId:         i.matchID,
		WinnerSlot:      int32(out.WinnerSlot),
		DurationSeconds: out.DurationSeconds,
		Slots:           make([]*protos.SlotResult, 0, sim.MaxPlayers),
	}
	for slot := 0; slot < sim.MaxPlayers; slot++ {
		msg.Slots = append(msg.Slots, &protos.SlotResult{
			Uid:    i.uidAt(slot),
			Kills:  out.Kills[slot],
			Deaths: out.Deaths[slot],
		})
	}
	reply := &protos.RecordMatchReply{}
	if err := i.app.RPC(ctx, recordMatchRoute, reply, msg); err != nil {
		log.Printf("instance %s: report match failed: %v", i.matchID, err)
	}
}
```

把 `run()` 的 tick 分支改成（顺序不能变：先广播本 tick 的增量帧，再收尾）：

```go
case <-ticker.C:
	i.sim.Step()
	i.broadcast()
	if out, ok := i.sim.DrainOutcome(); ok {
		// 上面那次 broadcast 很关键：含 Game.Winner 的帧必须在 onMatchEnded 之前出去。
		i.finishMatch(out)
		return
	}
	if i.idleExpired() {
		// ... 保持原有空闲回收分支不变
	}
```

`finishMatch` 把「停止实例」与「摘注册表」这两步收进一个方法，正是为了让它在**不建物理世界**的前提下可测（`sim` 需要 cgo，`game` 的既有测试都是手工拼 `Instance`）。行为上与空闲回收分支等价：那条路是 `defer i.Stop()` + `i.onExit()` + `return`，这里是 `finishMatch` 内部 `Stop()` + `onExit()`，回到 `run()` 后 `return`，两条路都会让 `defer i.sim.Shutdown()` 生效。

导入需要加上 `"context"` 与 `"joltgo/game/protos"`（`log`、`time`、`sync` 已有）。

- [ ] **Step 4: 把空闲回收超时改成 30 分钟**

在 `joltgo/game/instance.go` 改常量与注释：

```go
// instanceIdleTimeout 是「所有槽位都无上行消息」多久之后结束实例。
//
// 30 分钟同时是掉线回局窗口：服务端权威、对局不因掉线暂停，玩家在这段时间内
// resume 回来仍能通过 tryRejoin 接回原局。远大于客户端 1s 重连 + 2.5s 看门狗，
// 正常重连绝不会误杀。这条超时只对「谁都没打死谁」的局生效 —— 判出胜负的局会
// 在结算时立刻终结（见 finishMatch）。
const instanceIdleTimeout = 30 * time.Minute
```

- [ ] **Step 5: 跑测试确认通过**

Run: `cd joltgo; PATH="$PWD:$PATH" go test -count=1 ./game ./sim`
Expected: 两个包都 `ok`（`./game` 需要 `joltgo/libjolt_c.dll` 在 `PATH` 上）。

- [ ] **Step 6: 提交**

```bash
git add joltgo/game
git commit -m "feat: 对局结束推送结果并上报战绩后终结实例" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 7: 匹配队列去掉兜底并改为 NX 入队

**Files:**
- Modify: `joltgo/match/queue.go`
- Modify: `joltgo/match/match.go`
- Test: `joltgo/match/queue_test.go`、`joltgo/match/match_test.go`

**Interfaces:**
- Consumes: 无（同包）
- Produces:
  - `match.QueueEntry{UID string; WaitedSeconds int32}`
  - `(*Queue) Snapshot(ctx) (int, []QueueEntry, error)`（队列总人数 + 逐人等待时长）
  - `(*Queue) Remove(ctx, uid string) (bool, error)`（Task 8 用）

- [ ] **Step 1: 写失败的测试**

追加到 `joltgo/match/queue_test.go`：

```go
// 客户端在匹配期每 15 秒静默重发一次 match.join：入队必须是 NX，重发不能刷新
// 「入队时间」，否则服务端算出来的等待时长永远在 0–15 秒之间跳。
func TestEnqueueKeepsOriginalWaitTime(t *testing.T) {
	q, mr := newTestQueue(t)
	ctx := context.Background()
	if err := q.Enqueue(ctx, "u1"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	mr.FastForward(20 * time.Second)
	if err := q.Enqueue(ctx, "u1"); err != nil {
		t.Fatalf("重复 Enqueue: %v", err)
	}

	total, entries, err := q.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if total != 1 || len(entries) != 1 {
		t.Fatalf("重复入队不应产生第二条，得到 total=%d entries=%+v", total, entries)
	}
	if entries[0].UID != "u1" {
		t.Fatalf("队列成员不对: %+v", entries[0])
	}
	if entries[0].WaitedSeconds < 20 {
		t.Fatalf("重复入队刷新了等待时长：WaitedSeconds=%d", entries[0].WaitedSeconds)
	}
}

func TestSnapshotReportsQueueSize(t *testing.T) {
	q, mr := newTestQueue(t)
	ctx := context.Background()
	if err := q.Enqueue(ctx, "u1"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	mr.FastForward(5 * time.Second)
	if err := q.Enqueue(ctx, "u2"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	mr.FastForward(3 * time.Second)

	total, entries, err := q.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if total != 2 {
		t.Fatalf("队列人数 = %d，期望 2", total)
	}
	// ZRANGE 按 score 升序：先入队的在前。
	if entries[0].UID != "u1" || entries[1].UID != "u2" {
		t.Fatalf("顺序不对: %+v", entries)
	}
	if entries[0].WaitedSeconds != 8 || entries[1].WaitedSeconds != 3 {
		t.Fatalf("等待时长不对: %+v", entries)
	}
}

func TestRemoveTakesUIDOutOfQueue(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()
	if err := q.Enqueue(ctx, "u1"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	removed, err := q.Remove(ctx, "u1")
	if err != nil || !removed {
		t.Fatalf("Remove: removed=%v err=%v", removed, err)
	}
	if removed, err := q.Remove(ctx, "u1"); err != nil || removed {
		t.Fatalf("重复 Remove 应返回 false: removed=%v err=%v", removed, err)
	}
	if total, _, _ := q.Snapshot(ctx); total != 0 {
		t.Fatalf("移除后队列应清空，得到 %d", total)
	}
}
```

如果 `queue_test.go` 还没有 `newTestQueue(t) (*Queue, *miniredis.Miniredis)` helper，就照 `match_test.go` 里 `newTestComponent` 的写法补一个（`miniredis.RunT(t)` + `redis.NewClient`）。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd joltgo; PATH="$PWD:$PATH" go test -count=1 ./match -run 'TestEnqueueKeepsOriginalWaitTime|TestSnapshotReportsQueueSize|TestRemoveTakesUIDOutOfQueue'`
Expected: 编译失败（`q.Snapshot undefined`、`q.Remove undefined`）；`TestEnqueueKeepsOriginalWaitTime` 在改 NX 前也会因 `WaitedSeconds=0` 失败。

- [ ] **Step 3: 改队列实现**

`joltgo/match/queue.go` 的 `enqueueScript` 改成 NX（旧注释里「重复入队即重新计时」的语义已作废，一并改写）：

```lua
local t = redis.call('TIME')
local ms = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
redis.call('ZADD', KEYS[1], 'NX', ms, ARGV[1])
return 1
```

删掉整个 `staleScript` 与 `PopStale`。加：

```go
// QueueEntry 是队列里一员的等待状态。
type QueueEntry struct {
	UID           string
	WaitedSeconds int32
}

// Snapshot 返回队列总人数与逐人等待时长（按入队时间升序）。
// 时间基准用 Redis 服务端时钟，与入队保持一致 —— 多节点共享同一个时钟。
func (q *Queue) Snapshot(ctx context.Context) (int, []QueueEntry, error) {
	members, err := q.rdb.ZRangeWithScores(ctx, queueKey, 0, -1).Result()
	if err != nil {
		return 0, nil, err
	}
	nowMillis, err := q.serverMillis(ctx)
	if err != nil {
		return 0, nil, err
	}
	entries := make([]QueueEntry, 0, len(members))
	for _, z := range members {
		uid, ok := z.Member.(string)
		if !ok {
			continue
		}
		waited := nowMillis - int64(z.Score)
		if waited < 0 {
			waited = 0
		}
		entries = append(entries, QueueEntry{UID: uid, WaitedSeconds: int32(waited / 1000)})
	}
	return len(entries), entries, nil
}

// serverMillis 读 Redis 服务端时钟（毫秒）：等待时长的定义必须与入队用同一个时钟，
// 否则一个快 15 秒的节点算出来的等待时长就是错的。
func (q *Queue) serverMillis(ctx context.Context) (int64, error) {
	t, err := q.rdb.Time(ctx).Result()
	if err != nil {
		return 0, err
	}
	return t.UnixMilli(), nil
}

// Remove 把 uid 移出队列，返回是否真的移除了（false = 本来就不在队列里，
// 可能是刚被别人配对走，调用方应据此去查回局）。
func (q *Queue) Remove(ctx context.Context, uid string) (bool, error) {
	n, err := q.rdb.ZRem(ctx, queueKey, uid).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
```

在 `joltgo/match/match.go`：

- `AfterInit` 的 goroutine 里删掉 `c.tryMatchTimeout(context.Background())`，函数注释改成「配对循环：凑满两人即开局（没有单人兜底）」。
- 删掉 `tryMatchTimeout` 函数与 `timeout = 10 * time.Second` 常量（`tickInterval` 仍在用，别删）。

同时删掉 `queue_test.go` / `match_test.go` 里针对 `PopStale`、`tryMatchTimeout`、「兜底单人开局」的用例；既有那条「重连再 ZADD 会更新 score」的用例改成断言**不更新**（即 Step 1 的 `TestEnqueueKeepsOriginalWaitTime`）。

- [ ] **Step 4: 跑测试确认通过**

Run: `cd joltgo; PATH="$PWD:$PATH" go test -count=1 ./match`
Expected: `ok  joltgo/match`

- [ ] **Step 5: 提交**

```bash
git add joltgo/match
git commit -m "refactor: 匹配队列改为 NX 入队并删除单人兜底" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 8: 取消匹配与匹配状态推送

**Files:**
- Modify: `joltgo/match/match.go`
- Test: `joltgo/match/match_test.go`

**Interfaces:**
- Consumes: `Queue.Remove` / `Queue.Snapshot` / `QueueEntry`（Task 7）；`protos.MatchCancelReply` / `protos.MatchStatus`（Task 3）；既有 `tryRejoin` / `pushMatched`
- Produces:
  - `(*Component) Cancel(ctx, *protos.MatchCancelMsg) (*protos.MatchCancelReply, error)`（route `match.match.cancel`）
  - `(*Component) pushMatchStatus(ctx)`

- [ ] **Step 1: 写失败的测试**

追加到 `joltgo/match/match_test.go`：

```go
// pushRecordingApp 记录推送；Join/Cancel 用不到的 pitaya 能力不实现。
type pushRecordingApp struct {
	pitaya.Pitaya
	sess   session.Session
	mu     sync.Mutex
	routes []string
	args   []interface{}
	uids   [][]string
}

func (a *pushRecordingApp) GetSessionFromCtx(context.Context) session.Session { return a.sess }

func (a *pushRecordingApp) GetServersByType(string) (map[string]*cluster.Server, error) {
	return nil, errors.New("no game server") // 回局查询必然 miss
}

func (a *pushRecordingApp) SendPushToUsers(route string, v interface{}, uids []string, frontendType string) ([]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.routes = append(a.routes, route)
	a.args = append(a.args, v)
	a.uids = append(a.uids, uids)
	return nil, nil
}

func newPushTestComponent(t *testing.T, sess session.Session) (*Component, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return New(&pushRecordingApp{sess: sess}, NewQueue(rdb), online.NewStore(rdb)), mr
}

func TestCancelRemovesFromQueue(t *testing.T) {
	c, _ := newPushTestComponent(t, &joinTestSession{uid: "7"})
	ctx := context.Background()
	if err := c.queue.Enqueue(ctx, "7"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	reply, err := c.Cancel(ctx, &protos.MatchCancelMsg{})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if !reply.Ok || reply.Reason != "cancelled" {
		t.Fatalf("取消应答不对: %+v", reply)
	}
	if total, _, _ := c.queue.Snapshot(ctx); total != 0 {
		t.Fatalf("取消后队列应清空，得到 %d", total)
	}
}

func TestCancelReportsNotQueued(t *testing.T) {
	c, _ := newPushTestComponent(t, &joinTestSession{uid: "7"})

	reply, err := c.Cancel(context.Background(), &protos.MatchCancelMsg{})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if reply.Ok || reply.Reason != "not_queued" {
		t.Fatalf("不在队列时应返回 not_queued（ok=false），得到 %+v", reply)
	}
}

func TestCancelRejectsUnboundSession(t *testing.T) {
	c, _ := newPushTestComponent(t, &joinTestSession{uid: ""})

	reply, err := c.Cancel(context.Background(), &protos.MatchCancelMsg{})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if reply.Ok || reply.Reason != "unauthenticated" {
		t.Fatalf("未绑定会话应返回 unauthenticated，得到 %+v", reply)
	}
}

// 匹配状态推送：队列里每人一条，载荷是总人数与自己已等待的秒数。
func TestPushMatchStatusCoversQueue(t *testing.T) {
	c, mr := newPushTestComponent(t, nil)
	app := c.app.(*pushRecordingApp)
	ctx := context.Background()
	if err := c.queue.Enqueue(ctx, "7"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	mr.FastForward(4 * time.Second)
	if err := c.queue.Enqueue(ctx, "8"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	mr.FastForward(2 * time.Second)

	c.pushMatchStatus(ctx)

	if len(app.routes) != 2 {
		t.Fatalf("队列里 2 人应推 2 条，得到 %d", len(app.routes))
	}
	for i, route := range app.routes {
		if route != statusRoute {
			t.Fatalf("第 %d 条 route = %s，期望 %s", i, route, statusRoute)
		}
		status, ok := app.args[i].(*protos.MatchStatus)
		if !ok {
			t.Fatalf("第 %d 条载荷类型不对: %T", i, app.args[i])
		}
		if status.QueuedPlayers != 2 {
			t.Fatalf("第 %d 条队列人数 = %d，期望 2", i, status.QueuedPlayers)
		}
	}
	first := app.args[0].(*protos.MatchStatus)
	second := app.args[1].(*protos.MatchStatus)
	if first.WaitedSeconds != 6 || second.WaitedSeconds != 2 {
		t.Fatalf("等待时长不对: 第一条=%d 第二条=%d", first.WaitedSeconds, second.WaitedSeconds)
	}
	if app.uids[0][0] != "7" || app.uids[1][0] != "8" {
		t.Fatalf("推送目标不对: %v / %v", app.uids[0], app.uids[1])
	}
}

// 空队列不该产生任何推送。
func TestPushMatchStatusSkipsEmptyQueue(t *testing.T) {
	c, _ := newPushTestComponent(t, nil)
	app := c.app.(*pushRecordingApp)

	c.pushMatchStatus(context.Background())

	if len(app.routes) != 0 {
		t.Fatalf("空队列不应推送，得到 %v", app.routes)
	}
}
```

注意 `Component` 需要能取回 `app`：如果 `Component` 结构体里字段名是 `app`（小写，同包可见），上面的 `c.app.(*pushRecordingApp)` 直接可用；否则给它加一个同包可见的 getter。

`match_test.go` 的 import 需要补 `"sync"` 与 `"time"`。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd joltgo; PATH="$PWD:$PATH" go test -count=1 ./match -run 'TestCancel|TestPushMatchStatus'`
Expected: 编译失败（`c.Cancel undefined`、`c.pushMatchStatus undefined`、`statusRoute undefined`）。

- [ ] **Step 3: 实现**

在 `joltgo/match/match.go` 的常量区加：

```go
	statusRoute = "onMatchStatus" // match → 客户端 push 的 route（匹配期队列状态）
```

`AfterInit` 的 tick 改成同时推状态：

```go
func (c *Component) AfterInit() {
	go func() {
		ticker := time.NewTicker(tickInterval)
		defer ticker.Stop()
		for range ticker.C {
			ctx := context.Background()
			c.tryMatch(ctx)
			c.pushMatchStatus(ctx)
		}
	}()
}
```

加两个函数：

```go
// pushMatchStatus 把队列状态推给每个正在等待的玩家。
//
// 逐个推而不是一批推同一个载荷：等待时长因人而异。队列规模小时成本可忽略；
// 将来队列上千就改成「批量推人数 + 客户端本地计时」（见 spec §6.4）。
func (c *Component) pushMatchStatus(ctx context.Context) {
	total, entries, err := c.queue.Snapshot(ctx)
	if err != nil {
		log.Printf("match: queue snapshot failed: %v", err)
		return
	}
	if total == 0 {
		return
	}
	for _, entry := range entries {
		status := &protos.MatchStatus{
			QueuedPlayers: int32(total),
			WaitedSeconds: entry.WaitedSeconds,
		}
		if _, err := c.app.SendPushToUsers(statusRoute, status, []string{entry.UID}, "gate"); err != nil {
			log.Printf("match: push %s to %s failed: %v", statusRoute, entry.UID, err)
		}
	}
}

// Cancel 是客户端请求 handler（route "match.cancel"）：把已登录会话移出匹配队列。
//
// 三态语义（见 spec §5.3）：真的移出了才 ok=true；ZREM 返回 0 说明人已经不在这条
// 队列里 —— 可能是 tick 刚把他配对走（那就顺手走一次回局查询，把他带进已经开好的
// 对局，而不是从局里拽出来），也可能只是从来没排过队。
func (c *Component) Cancel(ctx context.Context, _ *protos.MatchCancelMsg) (*protos.MatchCancelReply, error) {
	s := c.app.GetSessionFromCtx(ctx)
	if s == nil || s.UID() == "" {
		log.Printf("match: cancel rejected: session not bound")
		return &protos.MatchCancelReply{Ok: false, Reason: "unauthenticated"}, nil
	}
	uid := s.UID()
	removed, err := c.queue.Remove(ctx, uid)
	if err != nil {
		log.Printf("match: cancel %s failed: %v", uid, err)
		return &protos.MatchCancelReply{Ok: false, Reason: "internal"}, nil
	}
	if removed {
		log.Printf("match: uid %s cancelled matchmaking", uid)
		return &protos.MatchCancelReply{Ok: true, Reason: "cancelled"}, nil
	}
	// 竞态窗口：tick 可能刚把他弹出去、实例还没建好。这里查不到就返回 not_queued，
	// 紧接着 onMatched 会照常到达，客户端切进对局 —— 不会出现「悬空玩家」。
	if c.tryRejoin(ctx, uid) {
		return &protos.MatchCancelReply{Ok: false, Reason: "already_matched"}, nil
	}
	return &protos.MatchCancelReply{Ok: false, Reason: "not_queued"}, nil
}
```

既有 `Join` 直接解引用会话，`Cancel` 必须自带 nil 判断（未绑定会话时 `GetSessionFromCtx` 可能返回 nil）。

- [ ] **Step 4: 跑测试确认通过**

Run: `cd joltgo; PATH="$PWD:$PATH" go test -count=1 ./match`
Expected: `ok  joltgo/match`

- [ ] **Step 5: 提交**

```bash
git add joltgo/match
git commit -m "feat: 支持取消匹配并推送队列状态" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 9: 彻底删除场景重置

**Files:**
- Modify: `joltgo/game/protos/game.proto`（删 `reset`，`reserved 7`）
- Regenerate: `joltgo/game/protos/game.pb.go`
- Modify: `joltgo/game/component.go`、`joltgo/game/instance.go`
- Modify: `joltgo/sim/simulation.go`
- Modify: `joltgo/replication/store.go`
- Test: `joltgo/sim/sim_test.go`、`joltgo/sim/replicate_test.go`、`joltgo/replication/store_test.go`

**Interfaces:**
- Consumes: 无
- Produces: 无新增；**移除** `sim.Simulation.Reset`、`game.Instance.Reset`、`replication.Store.Reset`、`CommandMsg.Reset_`

> 顺序要紧：proto 一改，`component.go` 里的 `msg.Reset_` 就不存在了，必须在同一个提交里删干净。

- [ ] **Step 1: 改 proto 并确认编译报错**

`CommandMsg` 改为（删掉 `bool reset = 7;`）：

```proto
message CommandMsg {
  repeated float move = 1;   // [wx, wz]，世界空间水平期望速度（m/s）
  float yaw = 2;
  bool jump = 3;
  bool shoot = 4;
  repeated float origin = 5;
  repeated float dir = 6;
  reserved 7;                // 曾是 bool reset（场景重置），功能已删除，字段号不再复用
  reserved "reset";
}
```

Run: `cd joltgo; go build ./...`
Expected: 编译失败，指出 `joltgo/game/component.go` 里的 `msg.Reset_ undefined`（在 `game.pb.go` 重新生成之前，报错也可能指向生成码与 proto 不一致 —— 下一步重生成）。

- [ ] **Step 2: 重新生成并删掉服务端引用**

```bash
cd joltgo/game/protos
protoc --go_out=. --go_opt=paths=source_relative game.proto
```

删除 `joltgo/game/component.go` 里这整段：

```go
	// 字段名 reset 与 protoc-gen-go 生成的 Reset() 方法冲突，被重命名为 Reset_
	// （wire 字段号仍是 7，语义不变）。
	if msg.Reset_ {
		inst.Reset()
	}
```

删除 `joltgo/game/instance.go` 的：

```go
// Reset 重建对局场景。
func (i *Instance) Reset() {
	i.enqueue(func() { i.sim.Reset() })
}
```

删除 `joltgo/replication/store.go` 的 `Reset` 方法（世界重建路径消失后它没有任何生产调用者，留着就是死代码 + 一条不再成立的文档化不变量）。

- [ ] **Step 3: sim 删掉世界重建能力**

`joltgo/sim/simulation.go`：

```go
// Init 初始化世界。一个 Simulation 只初始化一次（实例创建时调用），
// 世界生命周期 = 实例生命周期，因此实体 id 在单个实例内不会复用。
func (s *Simulation) Init() {
	s.init()
}
```

删除公开的 `func (s *Simulation) Reset() { s.reset() }`、删除内部 `func (s *Simulation) reset()`（它的调用者只剩 `Init` 的重复初始化分支，而重复初始化已不再受支持），并删掉 `reset()` 里那些现在无处可去的赋值（`s.rep.Reset()`、`overAt`、`hasOutcome`）——`go build` 会逐个报出来。

- [ ] **Step 4: 清理自测**

- `joltgo/replication/store_test.go`：删掉 `TestResetKeepsDeclarations` 与 `TestSetAfterResetIsDirtyAgain`（只覆盖已删除 API）。
- `joltgo/sim/sim_test.go`：删掉 `TestResetRebuildsScene` 与「重复 Init 应等同 Reset」那条用例；删掉所有 `s.Reset()` 调用点。
- `joltgo/sim/replicate_test.go:306` 附近的 `s.Reset()`：随 API 删除一并删掉该用例。

Run: `cd joltgo; PATH="$PWD:$PATH" go test -count=1 ./...`
Expected: 全绿。

- [ ] **Step 5: 全仓确认没有 reset 残留**

Run: `cd C:/Users/zhubeijian/Desktop/Projects/fps; git grep -n "Reset_\|\.Reset()" -- joltgo`
Expected: 只剩 protobuf 生成码里各消息的 `func (x *Foo) Reset()`（protoc-gen-go 的固定方法名，与业务无关）；**不应**再出现 `sim.Reset`、`Instance.Reset`、`Store.Reset`、`msg.Reset_`。

- [ ] **Step 6: 提交**

```bash
git add joltgo
git commit -m "refactor: 彻底删除场景重置功能" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 10: 客户端协议层

**Files:**
- Modify: `godot_client/scripts/fps_client.gd`
- Test: `godot_client/tests/frame_decode_test.gd`

**Interfaces:**
- Consumes: 服务端 route `logic.logic.profile`（Request/Response）、`match.match.cancel`（Request/Response）、推送 `onMatchStatus` / `onMatchEnded`（Task 3/4/8/6）
- Produces（后续任务与 B 都依赖这些名字）：
  - 信号 `profile_received(result: Dictionary)`、`match_cancel_received(result: Dictionary)`、`match_status_received(result: Dictionary)`、`match_ended_received(result: Dictionary)`
  - `send_profile()`、`send_cancel_match()`
  - `_decode_profile(buf) -> Dictionary`、`_decode_match_cancel_reply(buf) -> Dictionary`、`_decode_match_status(buf) -> Dictionary`、`_decode_match_ended(buf) -> Dictionary`
  - `send_command(move, yaw, jump, shoot, origin, dir)`（**去掉第 7 个 `reset` 参数**）

- [ ] **Step 1: 改测试（先红）**

`godot_client/tests/frame_decode_test.gd`：

1. `_test_command_encoding` 里删掉 `_check(seen.get(7, -1) == _c.WIRE_VARINT, "reset 应是字段 7")`，换成反向断言，并把注释改掉：

```gdscript
# 上行字段号必须与 CommandMsg 一致。字段 7（曾是 reset）已随功能删除并 reserved，
# 客户端不应再发它。
func _test_command_encoding() -> void:
	var buf: PackedByteArray = _c._encode_command(
		Vector2(1.0, 2.0), 0.5, true, true, Vector3(3, 4, 5), Vector3(0, 0, 1))
	# ...（中间解析循环保持原样）
	_check(seen.get(1, -1) == _c.WIRE_LEN, "move 应是字段 1（packed float）")
	_check(seen.get(2, -1) == _c.WIRE_FIXED32, "yaw 应是字段 2（fixed32）")
	_check(seen.get(3, -1) == _c.WIRE_VARINT, "jump 应是字段 3")
	_check(seen.get(4, -1) == _c.WIRE_VARINT, "shoot 应是字段 4")
	_check(seen.get(5, -1) == _c.WIRE_LEN, "origin 应是字段 5")
	_check(seen.get(6, -1) == _c.WIRE_LEN, "dir 应是字段 6")
	_check(not seen.has(7), "字段 7 已 reserved，客户端不得再发")
	_done["command"] = true
```

2. 在 `_init` 的用例列表里加四个新用例，并在文件末尾追加它们：

```gdscript
func _test_profile_decode() -> void:
	var record := PackedByteArray()
	record.append_array(_f_bytes(1, "m1".to_utf8_buffer()))
	record.append_array(_f_varint(2, 1))          # won
	record.append_array(_f_varint(3, 10))         # kills
	record.append_array(_f_varint(4, 7))          # deaths
	record.append_array(_f_varint(5, 7))          # opponent_kills
	record.append_array(_f_varint(6, 84))         # duration_seconds
	record.append_array(_f_bytes(7, "bob".to_utf8_buffer()))
	record.append_array(_f_varint(8, 1737000000)) # ended_at

	var buf := PackedByteArray()
	buf.append_array(_f_varint(1, 1))    # ok
	buf.append_array(_f_varint(3, 3))    # level
	buf.append_array(_f_varint(4, 420))  # xp
	buf.append_array(_f_varint(5, 20))   # xp_into_level
	buf.append_array(_f_varint(6, 200))  # xp_for_next_level
	buf.append_array(_f_varint(7, 30))   # kills
	buf.append_array(_f_varint(8, 12))   # deaths
	buf.append_array(_f_varint(9, 4))    # matches
	buf.append_array(_f_varint(10, 3))   # wins
	buf.append_array(_f_varint(11, 1))   # losses
	buf.append_array(_f_bytes(12, record))

	var d: Dictionary = _c._decode_profile(buf)
	_check(bool(d.get("ok", false)), "profile 应解码为 ok=true")
	_check(int(d.get("level", 0)) == 3, "level 应是 3")
	_check(int(d.get("xp", 0)) == 420, "xp 应是 420")
	_check(int(d.get("xp_for_next_level", 0)) == 200, "xp_for_next_level 应是 200")
	_check(int(d.get("wins", 0)) == 3 && int(d.get("losses", 0)) == 1, "胜负场次应解出")
	var recent: Array = d.get("recent_matches", [])
	_check(recent.size() == 1, "应解出 1 条历史")
	if recent.size() == 1:
		_check(String(recent[0]["match_id"]) == "m1", "历史 match_id 应是 m1")
		_check(bool(recent[0]["won"]), "历史 won 应是 true")
		_check(int(recent[0]["opponent_kills"]) == 7, "历史 opponent_kills 应是 7")
		_check(String(recent[0]["opponent_name"]) == "bob", "历史 opponent_name 应是 bob")
		_check(int(recent[0]["ended_at"]) == 1737000000, "历史 ended_at 应是 1737000000")
	# order=-1 表示服务端按时间倒序下发，客户端按原样保留。
	_check(bool(d.get("_malformed", false)) == false, "合法载荷不应判为畸形")
	_done["profile"] = true

func _test_match_cancel_reply() -> void:
	var buf := PackedByteArray()
	buf.append_array(_f_bytes(2, "not_queued".to_utf8_buffer()))
	var d: Dictionary = _c._decode_match_cancel_reply(buf)
	_check(bool(d.get("ok", true)) == false, "缺省 ok 应是 false")
	_check(String(d.get("reason", "")) == "not_queued", "reason 应解出")
	_done["cancel"] = true

func _test_match_status_decode() -> void:
	var buf := PackedByteArray()
	buf.append_array(_f_varint(1, 3))   # queued_players
	buf.append_array(_f_varint(2, 12))  # waited_seconds
	var d: Dictionary = _c._decode_match_status(buf)
	_check(int(d.get("queued_players", 0)) == 3, "queued_players 应是 3")
	_check(int(d.get("waited_seconds", 0)) == 12, "waited_seconds 应是 12")
	_done["status"] = true

func _test_match_ended_decode() -> void:
	var slot0 := PackedByteArray()
	slot0.append_array(_f_bytes(1, "7".to_utf8_buffer()))
	slot0.append_array(_f_varint(2, 3))
	slot0.append_array(_f_varint(3, 10))
	var slot1 := PackedByteArray()
	slot1.append_array(_f_bytes(1, "8".to_utf8_buffer()))
	slot1.append_array(_f_varint(2, 10))
	slot1.append_array(_f_varint(3, 3))

	var buf := PackedByteArray()
	buf.append_array(_f_bytes(1, "m1".to_utf8_buffer()))
	buf.append_array(_f_varint(2, 1))
	buf.append_array(_f_bytes(3, slot0))
	buf.append_array(_f_bytes(3, slot1))
	buf.append_array(_f_varint(4, 84))

	var d: Dictionary = _c._decode_match_ended(buf)
	_check(String(d.get("match_id", "")) == "m1", "match_id 应是 m1")
	_check(int(d.get("winner_slot", -1)) == 1, "winner_slot 应是 1")
	_check(int(d.get("duration_seconds", 0)) == 84, "duration_seconds 应是 84")
	var slots: Array = d.get("slots", [])
	_check(slots.size() == 2, "应解出 2 个槽位")
	if slots.size() == 2:
		_check(String(slots[0]["uid"]) == "7" && int(slots[0]["kills"]) == 3, "槽位 0 数据不对")
		_check(String(slots[1]["uid"]) == "8" && int(slots[1]["deaths"]) == 3, "槽位 1 数据不对")
	_done["ended"] = true
```

3. `_init` 的 `for name: String in [...]` 里补上 `"profile"`、`"cancel"`、`"status"`、`"ended"`。

Run: `Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/frame_decode_test.gd`
Expected: FAIL —— `_decode_profile` 等函数不存在，且 `_encode_command` 参数个数不匹配。

- [ ] **Step 2: 删掉 reset 编码**

`fps_client.gd` 里：`send_command` 与 `_encode_command` 去掉最后一个 `reset: bool` 参数，删掉 `if reset:` 那一段与相关注释（第 272–293 行附近），并把函数头注释改成新字段表 `move=1 yaw=2 jump=3 shoot=4 origin=5 dir=6`。

- [ ] **Step 3: 加信号、请求与解码**

信号区加：

```gdscript
signal profile_received(result: Dictionary)        # PlayerProfileReply
signal match_cancel_received(result: Dictionary)   # MatchCancelReply
signal match_status_received(result: Dictionary)   # onMatchStatus 推送
signal match_ended_received(result: Dictionary)    # onMatchEnded 推送
```

`send_match_join()` 旁边加：

```gdscript
## send_profile 拉取个人档案（Request/Response；身份来自会话，客户端不自报 uid）。
func send_profile() -> void:
	_send_tracked("logic.logic.profile", PackedByteArray())

## send_cancel_match 取消匹配（Request/Response）。
## 响应里 ok=false + reason=already_matched 表示已经进局，此时 onMatched 马上就到。
func send_cancel_match() -> void:
	_send_tracked("match.match.cancel", PackedByteArray())
```

`_is_known_request_route` 改为：

```gdscript
func _is_known_request_route(route: String) -> bool:
	return route.begins_with("account.") or route.begins_with("logic.") or route.begins_with("match.")
```

`_emit_request_failure` 改为（新增两个分支，注意 profile 失败的形状与 logic.state 不同）：

```gdscript
func _emit_request_failure(route: String, reason: String) -> void:
	if route == "logic.logic.profile":
		profile_received.emit({"_failed": true, "reason": reason})
	elif route.begins_with("logic."):
		logic_state_received.emit(_logic_failure(reason))
	elif route == "match.match.cancel":
		match_cancel_received.emit({"ok": false, "reason": reason, "_failed": true})
	else:
		login_result.emit({"ok": false, "reason": reason})
```

`_on_push` 的 `match route:` 补两条：

```gdscript
		"onMatchStatus":
			match_status_received.emit(_decode_match_status(payload))
		"onMatchEnded":
			# 本局结束是权威信号：实例已经终结、帧流不会再来，必须停掉接收看门狗
			# （否则 2.5 秒后会被误判成掉线，触发重连 + 重新入队）。
			_matched = false
			_match_retry_at = 0.0
			match_ended_received.emit(_decode_match_ended(payload))
```

`_on_response` 的分发补两条（在 `if route.begins_with("logic.")` 之前加 profile 的精确匹配）：

```gdscript
	if route == "logic.logic.profile":
		var profile := _decode_profile(payload)
		if bool(profile.get("_malformed", false)):
			_emit_request_failure(route, "internal")
		else:
			profile_received.emit(profile)
		return
	if route == "match.match.cancel":
		var cancel := _decode_match_cancel_reply(payload)
		if bool(cancel.get("_malformed", false)):
			_emit_request_failure(route, "internal")
		else:
			match_cancel_received.emit(cancel)
		return
	if route.begins_with("logic."):
		# ... 保持原有逻辑（现在只会是 state/purchase/equip）
```

`_decode_logic_state` 下面加四个解码器（沿用同一套 `_read_varint_checked` + `_malformed` 风格）：

```gdscript
## PlayerProfileReply：ok=1 reason=2 level=3 xp=4 xp_into_level=5 xp_for_next_level=6
## kills=7 deaths=8 matches=9 wins=10 losses=11 recent_matches=12(MatchRecord)。
func _decode_profile(buf: PackedByteArray) -> Dictionary:
	var d := {
		"ok": false,
		"reason": "",
		"level": 0,
		"xp": 0,
		"xp_into_level": 0,
		"xp_for_next_level": 0,
		"kills": 0,
		"deaths": 0,
		"matches": 0,
		"wins": 0,
		"losses": 0,
		"recent_matches": [],
	}
	var i := 0
	while i < buf.size():
		var tag: Array = _read_varint_checked(buf, i)
		if not bool(tag[2]):
			return {"_malformed": true}
		i = int(tag[1])
		var field := int(tag[0]) >> 3
		var wire := int(tag[0]) & 7
		if wire == WIRE_VARINT:
			var r: Array = _read_varint_checked(buf, i)
			if not bool(r[2]):
				return {"_malformed": true}
			i = int(r[1])
			match field:
				1: d["ok"] = int(r[0]) != 0
				3: d["level"] = int(r[0])
				4: d["xp"] = int(r[0])
				5: d["xp_into_level"] = int(r[0])
				6: d["xp_for_next_level"] = int(r[0])
				7: d["kills"] = int(r[0])
				8: d["deaths"] = int(r[0])
				9: d["matches"] = int(r[0])
				10: d["wins"] = int(r[0])
				11: d["losses"] = int(r[0])
		elif wire == WIRE_LEN:
			var rl: Array = _read_varint_checked(buf, i)
			if not bool(rl[2]):
				return {"_malformed": true}
			i = int(rl[1])
			var size := int(rl[0])
			if size < 0 or i + size > buf.size():
				return {"_malformed": true}
			var sub: PackedByteArray = buf.slice(i, i + size)
			i += size
			match field:
				2: d["reason"] = sub.get_string_from_utf8()
				12:
					var rec: Dictionary = _decode_match_record(sub)
					if bool(rec.get("_malformed", false)):
						return {"_malformed": true}
					d["recent_matches"].append(rec)
		else:
			break
	return d

## MatchRecord：match_id=1 won=2 kills=3 deaths=4 opponent_kills=5
## duration_seconds=6 opponent_name=7 ended_at=8。
func _decode_match_record(buf: PackedByteArray) -> Dictionary:
	var d := {
		"match_id": "",
		"won": false,
		"kills": 0,
		"deaths": 0,
		"opponent_kills": 0,
		"duration_seconds": 0,
		"opponent_name": "",
		"ended_at": 0,
	}
	var i := 0
	while i < buf.size():
		var tag: Array = _read_varint_checked(buf, i)
		if not bool(tag[2]):
			return {"_malformed": true}
		i = int(tag[1])
		var field := int(tag[0]) >> 3
		var wire := int(tag[0]) & 7
		if wire == WIRE_VARINT:
			var r: Array = _read_varint_checked(buf, i)
			if not bool(r[2]):
				return {"_malformed": true}
			i = int(r[1])
			match field:
				2: d["won"] = int(r[0]) != 0
				3: d["kills"] = int(r[0])
				4: d["deaths"] = int(r[0])
				5: d["opponent_kills"] = int(r[0])
				6: d["duration_seconds"] = int(r[0])
				8: d["ended_at"] = int(r[0])
		elif wire == WIRE_LEN:
			var rl: Array = _read_varint_checked(buf, i)
			if not bool(rl[2]):
				return {"_malformed": true}
			i = int(rl[1])
			var size := int(rl[0])
			if size < 0 or i + size > buf.size():
				return {"_malformed": true}
			var sub: PackedByteArray = buf.slice(i, i + size)
			i += size
			match field:
				1: d["match_id"] = sub.get_string_from_utf8()
				7: d["opponent_name"] = sub.get_string_from_utf8()
		else:
			break
	return d

## MatchCancelReply：ok=1 reason=2。
func _decode_match_cancel_reply(buf: PackedByteArray) -> Dictionary:
	var d := {"ok": false, "reason": ""}
	var i := 0
	while i < buf.size():
		var tag: Array = _read_varint_checked(buf, i)
		if not bool(tag[2]):
			return {"_malformed": true}
		i = int(tag[1])
		var field := int(tag[0]) >> 3
		var wire := int(tag[0]) & 7
		if wire == WIRE_VARINT:
			var r: Array = _read_varint_checked(buf, i)
			if not bool(r[2]):
				return {"_malformed": true}
			i = int(r[1])
			if field == 1:
				d["ok"] = int(r[0]) != 0
		elif wire == WIRE_LEN:
			var rl: Array = _read_varint_checked(buf, i)
			if not bool(rl[2]):
				return {"_malformed": true}
			i = int(rl[1])
			var size := int(rl[0])
			if size < 0 or i + size > buf.size():
				return {"_malformed": true}
			var sub: PackedByteArray = buf.slice(i, i + size)
			i += size
			if field == 2:
				d["reason"] = sub.get_string_from_utf8()
		else:
			break
	return d

## MatchStatus：queued_players=1 waited_seconds=2。
func _decode_match_status(buf: PackedByteArray) -> Dictionary:
	var d := {"queued_players": 0, "waited_seconds": 0}
	var i := 0
	while i < buf.size():
		var tag: Array = _read_varint_checked(buf, i)
		if not bool(tag[2]):
			return {"_malformed": true}
		i = int(tag[1])
		var field := int(tag[0]) >> 3
		var wire := int(tag[0]) & 7
		if wire != WIRE_VARINT:
			break
		var r: Array = _read_varint_checked(buf, i)
		if not bool(r[2]):
			return {"_malformed": true}
		i = int(r[1])
		match field:
			1: d["queued_players"] = int(r[0])
			2: d["waited_seconds"] = int(r[0])
	return d

## MatchEnded：match_id=1 winner_slot=2 slots=3(SlotResult) duration_seconds=4。
func _decode_match_ended(buf: PackedByteArray) -> Dictionary:
	var d := {"match_id": "", "winner_slot": -1, "duration_seconds": 0, "slots": []}
	var i := 0
	while i < buf.size():
		var tag: Array = _read_varint_checked(buf, i)
		if not bool(tag[2]):
			return {"_malformed": true}
		i = int(tag[1])
		var field := int(tag[0]) >> 3
		var wire := int(tag[0]) & 7
		if wire == WIRE_VARINT:
			var r: Array = _read_varint_checked(buf, i)
			if not bool(r[2]):
				return {"_malformed": true}
			i = int(r[1])
			match field:
				2: d["winner_slot"] = int(r[0])
				4: d["duration_seconds"] = int(r[0])
		elif wire == WIRE_LEN:
			var rl: Array = _read_varint_checked(buf, i)
			if not bool(rl[2]):
				return {"_malformed": true}
			i = int(rl[1])
			var size := int(rl[0])
			if size < 0 or i + size > buf.size():
				return {"_malformed": true}
			var sub: PackedByteArray = buf.slice(i, i + size)
			i += size
			if field == 1:
				d["match_id"] = sub.get_string_from_utf8()
			elif field == 3:
				var slot := _decode_slot_result(sub)
				if bool(slot.get("_malformed", false)):
					return {"_malformed": true}
				d["slots"].append(slot)
		else:
			break
	return d

## SlotResult：uid=1 kills=2 deaths=3。
func _decode_slot_result(buf: PackedByteArray) -> Dictionary:
	var d := {"uid": "", "kills": 0, "deaths": 0}
	var i := 0
	while i < buf.size():
		var tag: Array = _read_varint_checked(buf, i)
		if not bool(tag[2]):
			return {"_malformed": true}
		i = int(tag[1])
		var field := int(tag[0]) >> 3
		var wire := int(tag[0]) & 7
		if wire == WIRE_VARINT:
			var r: Array = _read_varint_checked(buf, i)
			if not bool(r[2]):
				return {"_malformed": true}
			i = int(r[1])
			match field:
				2: d["kills"] = int(r[0])
				3: d["deaths"] = int(r[0])
		elif wire == WIRE_LEN:
			var rl: Array = _read_varint_checked(buf, i)
			if not bool(rl[2]):
				return {"_malformed": true}
			i = int(rl[1])
			var size := int(rl[0])
			if size < 0 or i + size > buf.size():
				return {"_malformed": true}
			var sub: PackedByteArray = buf.slice(i, i + size)
			i += size
			if field == 1:
				d["uid"] = sub.get_string_from_utf8()
		else:
			break
	return d
```

- [ ] **Step 4: 跑测试确认通过**

Run: `Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/frame_decode_test.gd`
Expected: `frame_decode_test: OK`

Run: `Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/logic_state_decode_test.gd`
Expected: `OK`（回归：profile 的分发没把 state 抢走）

- [ ] **Step 5: 提交**

```bash
git add godot_client/scripts/fps_client.gd godot_client/tests/frame_decode_test.gd
git commit -m "feat: 客户端支持档案、取消匹配与匹配推送的解码" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 11: 客户端状态机与 reset 清理

**Files:**
- Modify: `godot_client/scripts/main.gd`
- Test: `godot_client/tests/match_ended_test.gd`（新建）、`godot_client/tests/reconnect_cleanup_test.gd`（回归）

**Interfaces:**
- Consumes: `fps_client.match_ended_received` / `send_match_join()`（Task 10）
- Produces: `(main.gd) _on_match_ended(result: Dictionary)`；`_lobby_hint` 状态的临时实现（B 会替换成真正的结算/大厅界面）

- [ ] **Step 1: 写失败的测试**

新建 `godot_client/tests/match_ended_test.gd`：

```gdscript
extends SceneTree
## 对局结束（onMatchEnded）必须让客户端进入「已结束」状态：
## 停掉接收看门狗、清空本地世界，且**不触发重连、不自动重新入队**。
##
## 这是去掉自动重开之后最容易被忽略的一条：实例一终结帧流就断，
## 若客户端仍认为自己在对局里，2.5 秒看门狗会把「本局结束」误判成掉线。

const MainScene := preload("res://scenes/main.tscn")

var _failures := 0
var _done := {}

class FakeClient:
	extends Node
	signal match_ended_received(result: Dictionary)
	signal matched_received(result: Dictionary)
	signal connection_changed(connected: bool)
	signal login_result(result: Dictionary)
	signal logic_state_received(result: Dictionary)
	signal frame_received(frame: Dictionary)

	var client_token := "token"
	var join_calls := 0
	var resync_calls := 0

	func send_match_join() -> void:
		join_calls += 1

	func send_resync() -> void:
		resync_calls += 1

	func send_command(_move: Vector2, _yaw: float, _jump: bool, _shoot: bool,
			_origin: Vector3, _dir: Vector3) -> void:
		pass

func _init() -> void:
	var main: Node = MainScene.instantiate()
	var fake := FakeClient.new()
	main.fps_client = fake
	main.add_child(fake)
	root.add_child(main)
	fake.connection_changed.emit(true)

	# 进入对局：匹配成功 → 客户端会请求 full 帧。
	fake.matched_received.emit({"match_id": "m1", "game_server_id": "g1", "player_idx": 0})
	_check(main._matched, "onMatched 之后应处于对局态")

	# 造一点本地世界状态，验证结算时会清掉。直接塞渲染节点即可 ——
	# 合成整帧属于 game_frame_test.gd 的职责（它驱动 _on_frame）。
	main._entities[100] = Node3D.new()
	main.add_child(main._entities[100])
	fake.join_calls = 0

	fake.match_ended_received.emit({
		"match_id": "m1",
		"winner_slot": 0,
		"duration_seconds": 84,
		"slots": [{"uid": "7", "kills": 10, "deaths": 3}, {"uid": "8", "kills": 3, "deaths": 10}],
	})
	_done["ended"] = true

	_check(not main._matched, "收到 onMatchEnded 后必须离开对局态（否则看门狗会误判断线）")
	_check(fake.join_calls == 0, "结算不得自动重新入队：重新匹配必须由玩家主动发起")
	_check(main._entities.is_empty(), "结算应清空本地实体缓存")
	_check(main.conn_label.visible, "结算应给出可见的临时提示（正式界面在 B 子项目）")

	if not _done.has("ended"):
		_failures += 1
		printerr("FAIL: 用例没跑完（中途抛错了？）")
	if _failures > 0:
		printerr("match_ended_test: %d 项失败" % _failures)
		quit(1)
	else:
		print("match_ended_test: OK")
		quit(0)

func _check(cond: bool, msg: String) -> void:
	if not cond:
		_failures += 1
		printerr("FAIL: " + msg)
```

Run: `Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/match_ended_test.gd`
Expected: 失败 —— 信号 `match_ended_received` 无人连接（`_matched` 仍为 true）。

- [ ] **Step 2: 删掉 reset**

`godot_client/scripts/main.gd`：

1. 删掉变量 `_pending_reset`、`_reset_pending` 及其注释块。
2. `_process` 里删掉 `var reset := _pending_reset` / `_pending_reset = false`，并把 `fps_client.send_command(...)` 改成 6 参调用（去掉 `reset`）。
3. `_on_frame` 里删掉 `if _reset_pending and _is_reset_frame(destroyed):` 那一段（以及配套的 `_reset_pending = false`）。
4. 删掉 `_is_reset_frame` 函数与 `_on_reset_pressed` 函数。
5. `_build_hud` 里删掉 `reset_btn` 的创建与 `pressed.connect(_on_reset_pressed)`。

- [ ] **Step 3: 接上结算状态机**

`_ready` 里连接信号（放在 `fps_client.matched_received.connect(_on_matched)` 旁边）：

```gdscript
	fps_client.match_ended_received.connect(_on_match_ended)
```

加处理函数（放在 `_on_matched` 之后）：

```gdscript
## _on_match_ended 本局结束：服务端实例已经终结，帧流不会再来。
##
## 三件事缺一不可：离开对局态（停掉 2.5 秒接收看门狗）、清空本地世界（下一局的实体
## id 从头发，残留的渲染节点会串台）、给出可见提示。**不自动重新入队** —— 重新匹配
## 必须由玩家主动发起（正式的大厅/结算界面属于 B 子项目，这里只留临时提示）。
func _on_match_ended(result: Dictionary) -> void:
	_matched = false
	_store.clear()
	_reset_interp()
	for id in _entities:
		_entities[id].queue_free()
	_entities = {}
	conn_label.text = _match_ended_text(result)
	conn_label.visible = true

## 注意：这里**不要**写 `_reset_pending`。Step 2 已经把这个变量连同它的闩锁语义一起删了
## （场景重置功能已移除），写回去会直接编译不过。
##
## _match_ended_text 结算提示的临时文案（B 子项目会用真正的结算界面替换它）。
## 注意读的是**自己槽位**的战绩，不是胜者槽位的 —— 两边的 k/d 是一对镜像数字，
## 读错了会把自己的战绩显示成对手的。
func _match_ended_text(result: Dictionary) -> String:
	var winner := int(result.get("winner_slot", -1))
	var slots: Array = result.get("slots", [])
	var duration := int(result.get("duration_seconds", 0))
	if winner < 0 or slots.size() < 2 or _my_player_idx >= slots.size():
		return "本局结束，按 Enter 重新匹配"
	var result_text := "胜利" if winner == _my_player_idx else "失败"
	var mine: Dictionary = slots[_my_player_idx]
	return "本局结束：%s（我方 %d 杀 %d 死，用时 %d 秒），按 Enter 重新匹配" % [
		result_text, int(mine["kills"]), int(mine["deaths"]), duration]
```

再加一个临时入口，让 A 阶段能手工开下一局（B 会用真界面替换）：在 `_unhandled_input` 的 `KEY_B` 分支后面加

```gdscript
	if event is InputEventKey and event.pressed and event.keycode == KEY_ENTER:
		if _logic_authenticated and not _matched:
			conn_label.text = "正在匹配…"
			fps_client.send_match_join()
		return
```

- [ ] **Step 4: 跑测试确认通过**

Run: `Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/match_ended_test.gd`
Expected: `match_ended_test: OK`

Run（回归，确认删 reset 与改状态机没有破坏既有行为）：

```bash
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/game_frame_test.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/reconnect_cleanup_test.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/logic_panel_test.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/world_store_test.gd
```
Expected: 四个都 OK。

- [ ] **Step 5: 提交**

```bash
git add godot_client/scripts/main.gd godot_client/tests/match_ended_test.gd
git commit -m "feat: 客户端接住对局结束并移除场景重置入口" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 12: 冒烟测试改为两个客户端真配对

**Files:**
- Create: `godot_client/tests/pair_helper.gd`
- Modify: `godot_client/tests/login_smoke.gd`、`godot_client/tests/ws_smoke.gd`、`godot_client/tests/rejoin_smoke.gd`

**Interfaces:**
- Consumes: 活集群（`joltgo\deploy\start-all.ps1`）、`fps_client.gd` 的 `send_register` / `send_match_join` / `matched_received` / `frame_received`
- Produces: `PairHelper`（`RefCounted` 子类）—— `register_and_join(username) -> Node`、`wait_for_pair(timeout_ms) -> Dictionary`

> 为什么必须改：单人兜底删除后，一个客户端**永远匹配不上**。三个冒烟测试原本都靠「10 秒兜底单人开局」拿到 `onMatched`，不改就会稳定挂死。

- [ ] **Step 1: 写辅助类**

新建 `godot_client/tests/pair_helper.gd`：

```gdscript
extends RefCounted
## 冒烟测试用的「两个客户端真配对」辅助。
##
## 单人兜底删除后，匹配必须凑满两个真实玩家才会开局，所以每个冒烟测试都要起两个
## FpsClient：各自随机注册一个账号（同一账号登两次是顶号），双方都进队后才可能配对。
## 注意：Godot 的 user:// 是每项目一个目录，多进程会共用凭证文件 —— 但本辅助在同一
## 进程里创建两个独立的 FpsClient 实例，凭证只在 _save_token 时落盘，互不干扰。

const FpsClient := preload("res://scripts/fps_client.gd")
const PASSWORD := "smokepass"

var _clients: Array[Node] = []
var _matched: Array[Dictionary] = [null, null]

## add_client 在给定父节点下新建一个客户端并连接匹配回调。
func add_client(parent: Node) -> Node:
	var client: Node = FpsClient.new()
	parent.add_child(client)
	var index := _clients.size()
	_clients.append(client)
	client.matched_received.connect(func(result: Dictionary) -> void:
		_matched[index] = result)
	return client

## register_username 生成一个不会与上一轮撞名的用户名。
func unique_username(prefix: String) -> String:
	return "%s_%d" % [prefix, Time.get_ticks_usec()]

## wait_for_clients 等到两个客户端都连上（handshake 完成）。返回 false 表示超时。
func wait_connected(timeout_ms: int) -> bool:
	var deadline := Time.get_ticks_msec() + timeout_ms
	while Time.get_ticks_msec() < deadline:
		var all := true
		for client in _clients:
			if not bool(client.connected):
				all = false
				break
		if all:
			return true
		await Engine.get_main_loop().process_frame
	return false

## register_both 让两个客户端各注册一个账号（等 LoginReply）。
func register_both(prefix: String, timeout_ms: int) -> Array[String]:
	var names: Array[String] = []
	for i in _clients.size():
		var name := unique_username("%s%d" % [prefix, i])
		names.append(name)
		var done := [false]
		var cb := func(result: Dictionary) -> void:
			if bool(result.get("ok", false)):
				done[0] = true
		_clients[i].login_result.connect(cb)
		_clients[i].send_register(name, PASSWORD)
		var deadline := Time.get_ticks_msec() + timeout_ms
		while not done[0] and Time.get_ticks_msec() < deadline:
			await Engine.get_main_loop().process_frame
		if not done[0]:
			return []
	return names

## join_both 让两个客户端都进匹配队列（先让 B 入队，再让 A 入队，尽早凑满两人）。
func join_both() -> void:
	if _clients.size() >= 2:
		_clients[1].send_match_join()
		await Engine.get_main_loop().process_frame
	_clients[0].send_match_join()

## wait_for_pair 等到两个客户端都被配对进同一局。返回 {"A": result, "B": result}。
func wait_for_pair(timeout_ms: int) -> Dictionary:
	var deadline := Time.get_ticks_msec() + timeout_ms
	while Time.get_ticks_msec() < deadline:
		if _matched[0] != null and _matched[1] != null:
			return {"A": _matched[0], "B": _matched[1]}
		await Engine.get_main_loop().process_frame
	return {}
```

- [ ] **Step 2: 改三个冒烟测试**

三个文件共同的改法：**删掉「等单人兜底」那段**，换成本辅助的配对流程。具体到每个文件：

`login_smoke.gd`：它原本的流程是「注册 → 断线 → resume → 等 onMatched」。改动是**在 resume 成功之后、等 onMatched 之前**插入一个对等客户端，并把收尾断言从「我收到了 onMatched」升级为「两个人进了同一局」：

1. `_phase == 1`（resume 成功）分支里，保持原有的 `send_match_join()`，另外新建 `var _pair := PairHelper.new()` 与 `var _peer := _pair.add_client(root)`；用一个 `_peer_username := _pair.unique_username("smokepeer")` 注册第二个账号（**同一个账号登两次是顶号**，必须换名）。
2. 新增成员变量：`var _pair := null`、`var _peer := null`、`var _peer_username := ""`、`var _peer_match := {}`，并把 `peer.matched_received` 接到一个只记 `_peer_match = result` 的回调上。
3. 等 `onMatched` 的那段改成「等彼此都匹配上」：把现有的 `while not _matched and ...` 循环条件改成 `while (not _matched or _peer_match.is_empty()) and Time.get_ticks_msec() < deadline`。
4. 收尾断言补两条：

```gdscript
	_check(not _peer_match.is_empty(), "对等客户端也必须收到 onMatched（单人兜底已删除）")
	_check(String(_match_result.get("match_id", "")) == String(_peer_match.get("match_id", "")),
		"两个客户端必须配进同一局")
	_check(int(_match_result.get("player_idx", 0)) != int(_peer_match.get("player_idx", 0)),
		"两个客户端的 player_idx 必须不同（0/1）")
```

（`_match_result` 是你在 `_on_matched` 里记下自己那份 `result` 的新变量；`_on_matched` 原本只置 `_matched = true`，加一行赋值即可。）

`ws_smoke.gd`：把「等匹配完成（单人兜底 10s 内会 onMatched）」那段换成「先起第二个客户端注册并 join，再等两边都 onMatched」，帧计数仍然只统计**本客户端**的 `frame_received`；原有「4 秒内至少 N 帧」与 `step` 递增断言保持不变。

`rejoin_smoke.gd`：配对成功后，断开 A、等 B 仍在局内（继续收帧），A 用 `_force_reconnect()` 走 resume → `match.join` → `tryRejoin`，断言回到**同一个 `match_id` 且同一 `player_idx`**。B 的存在是必要的：只有 A 一个客户端时，回局查询会因为实例已被摘除（或从未建立）而失败。

每个文件顶部的注释与「运行」说明也要同步改：从「需要活集群」补一句「**需要两个客户端的配对**，单人无法开局」。

三个文件都要在顶部加 `const PairHelper := preload("res://tests/pair_helper.gd")`。用法区分清楚：

- `add_client(parent)` 与 `unique_username(prefix)` 是同步的，随时可调。
- `wait_connected(timeout_ms)`、`register_both(prefix, timeout_ms)`、`join_both()`、`wait_for_pair(timeout_ms)` 内部有 `await`，**调用处必须写 `await`**（GDScript 里不写就只会拿到一个协程对象，循环根本没跑，测试会静默卡到超时）。例如：`var names: Array[String] = await pair.register_both("smoke", 10000)`。
- `login_smoke.gd` / `rejoin_smoke.gd` 需要更细的控制（一个客户端要单独断线重连），所以用 `add_client` + 各自的回调手动驱动；`ws_smoke.gd` 直接用 `register_both` + `join_both` + `wait_for_pair` 三步走最省事。

- [ ] **Step 3: 起集群并跑三个冒烟**

先重新构建服务端 —— 冒烟测试跑的是 `joltgo/joltgo.exe`，不重建就还是在测旧代码（旧代码有单人兜底、没有 `match.cancel`），测试会以完全误导的方式失败：

```bash
cd joltgo; .\build.ps1
```

然后重启本地集群（`start-infra.ps1` 每次启动都会清空 `etcd-data`，但**不会**动 `redis-data`，账号会保留）：

```bash
cd joltgo/deploy && powershell -File start-all.ps1    # 或在 PowerShell 里 .\start-all.ps1
```

```bash
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/login_smoke.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/ws_smoke.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/rejoin_smoke.gd
```

Expected: 三个都打印 `... OK`。若卡在「等匹配」：先查 `deploy/match.log` 有没有 `pop pair failed`，再确认两个客户端确实都发过 `match.join`（`deploy/gate.log` 里能看到两条 `match.match.join`）。

- [ ] **Step 4: 跑一次真实收尾（人工验证 §11 的第 3、4 条）**

用两个客户端（第二个用独立 `APPDATA`，见 `docs/DEVELOPMENT.md` 的多客户端段落）打满一局（10 杀），确认：

- 两边都出现结算提示，且**没有**自动重连（`deploy/gate.log` 里不应出现新的 handshake 之后立刻 resume 的循环）；
- `deploy/game.log` 里出现 `report match` 相关日志无错误；
- 用 `redis-cli -p 6379 HGETALL REDB#3:<accountID>:0` 与 `LRANGE playerhist:<accountID> 0 19` 看到档案与历史各 +1。

- [ ] **Step 5: 提交**

```bash
git add godot_client/tests
git commit -m "test: 冒烟测试改为两个客户端真配对" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 13: 文档同步

**Files:**
- Modify: `AGENTS.md`
- Modify: `docs/API.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `README.md`、`godot_client/README.md`

**Interfaces:**
- Consumes: 前面全部任务的实际行为
- Produces: 无（文档）

- [ ] **Step 1: `AGENTS.md`**

逐条改：

1. §3.4 tick 顺序保持不变，但把「命中全部靠刚体接触事件」那段之后的结算描述改成「某一方先到 10 杀即判出胜负 → 产出结算快照 → `game` 推 `onMatchEnded` + 上报 logic + **终结实例**（不再 5 秒自动重开）」。
2. §3.9 玩家命中盒那段里，「`reset()` 走 `physics.Destroy()` + 重建」改为「世界生命周期 = 实例生命周期，实例创建时 `Init()` 建一次；场景重置功能已删除」。
3. §4 数据流的 match 那一行：`匹配 2 人（或 10s 兜底单人）` → `匹配 2 人（没有兜底：凑不满就一直等，玩家可取消）`；在 game 那一行后面补「一局结束：`game` 推 `onMatchEnded` → 上报 `logic.logic.recordmatch` → 终结实例 → 玩家回大厅主动重新匹配」；补 `onMatchStatus` 推送。
4. §5 坑位列表：新增三条 —— 「入队是 `ZADD NX`：重发 `match.join` 不会刷新等待时长」「战绩上报不重试不去重，失败只记日志」「`CommandMsg` 字段 7 已 `reserved`」「`logic` 只读 `acct:1:<id>:0` 的 username 取对手名（只读、不写）」。
5. §6 测试命令：三个冒烟测试的注释补「需要两个客户端真配对」；`frame_decode_test` 说明里去掉 reset 字段号那条。

- [ ] **Step 2: `docs/API.md`**

- `CommandMsg` 表：删 `reset` 行，加一行「字段 7 `reserved`（曾是场景重置）」。
- route 表补 `match.match.cancel`（Request/Response）、`logic.logic.profile`（Request/Response）、`logic.logic.recordmatch`（remote，game → logic）。
- 推送表补 `onMatchStatus`、`onMatchEnded`（含字段表）。
- 错误/原因码补 `cancelled` / `already_matched` / `not_queued` / `not_enough_players`。

- [ ] **Step 3: `docs/ARCHITECTURE.md`**

- 实例生命周期一节：从「创建 → 每 tick → 空闲回收」改成「创建 → 每 tick → **判出胜负即终结**（推 `onMatchEnded` + 上报 + 摘注册表）→ 空闲回收只用于『谁都没打死谁』的局（30 分钟）」。
- 命令 channel 描述里删掉 reset；`matchSystem 直接调 Reset() 重开一局`（约 417 行）整段改写成新语义。
- 匹配一节：删兜底、写 `ZADD NX`、写取消三态与状态推送。

- [ ] **Step 4: 两个 README**

- `README.md` 按键表：删掉 `ESC | 释放鼠标（可点 Reset 重开）` 里的 Reset 提示，改成 `释放鼠标`。
- `godot_client/README.md`：第 16 行「输入 + 射击 + 重置」→「输入 + 射击」；第 22 行 HUD 说明删「右上角 Reset 重开」；第 36 行删「可点 Reset」；补一句「一局结束后回到大厅，按 Enter 重新匹配（正式界面见后续 UI 改造）」。

- [ ] **Step 5: 提交**

```bash
git add AGENTS.md docs/API.md docs/ARCHITECTURE.md README.md godot_client/README.md
git commit -m "docs: 同步对局生命周期、战绩与匹配语义" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

## 完成标准

按 spec §11 验收，逐条可判定：

1. `cd joltgo; PATH="$PWD:$PATH" go test -count=1 ./...` 全绿；`go test -count=1 -tags joltdll ./physics` 全绿。
2. 两个客户端（两个账号）能配成同一局。
3. 一局结束后：双方收到 `onMatchEnded`；`deploy/game.log` 无上报错误；`REDB#3:<id>:0` 与 `playerhist:<id>` 各 +1；`logic.logic.profile` 能读出档案与历史。
4. 结束后客户端不自动重连、不自动入队；按 Enter（临时入口）或后续 B 的界面按钮重新匹配。
5. 匹配期数据来自 `onMatchStatus` 推送；`match.match.cancel` 返回 `cancelled`；重复 `match.join` 不刷新等待时长。
6. `git grep -n "Reset_\|\.Reset()" -- joltgo` 只剩 protobuf 生成码的 `Reset()`；`command + reset` 在客户端与文档中不再出现。

## 已知取舍（来自 spec，不要"顺手修"）

- 战绩上报不重试、不去重：logic 不可达时那一场战绩丢失，只留日志。
- 不做「打满 10 杀」的端到端脚本验证（不加可配置击杀目标这类测试钩子）：结算链路由单测覆盖，客户端结算态由合成推送的无头用例覆盖。
- 双方挂机不打架的实例会空转 30 分钟才回收，不产战绩。

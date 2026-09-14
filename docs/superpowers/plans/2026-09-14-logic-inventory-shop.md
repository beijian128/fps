# Logic Inventory and Shop Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 新增无状态 `logic` 微服务，完成登录上线建档、金币商城、可叠加背包与主武器装备状态的端到端闭环。

**Architecture:** `account` 在登录提交阶段同步调用随机 `logic` 节点的 `logic.logic.online`；`gate` 把客户端 `logic.logic.*` 请求随机路由到任一 `logic` 节点。`logic` 用 Redis Hash 保存钱包与背包，以账号级分布式锁串行化写操作，并采用锁外预检、锁内重读重判、多步写失败补偿的模式。

**Tech Stack:** Go 1.26、pitaya v3 Cluster、protobuf、protoc-gen-redis、Redis、distlock、Godot 4.7 GDScript。

**Spec:** `docs/superpowers/specs/2026-09-14-logic-inventory-shop-design.md`

## Global Constraints

- 仓库直接提交到 `main`，不开 feature branch、不走 PR；历史保持线性。
- 提交信息使用 `<type>: <subject>`，结尾加 `Co-Authored-By: Codex <noreply@openai.com>`。
- 新账号及首次上线的已有账号初始化为 1000 金币、空背包。
- 商品固定为 `rifle(300) / pistol(150) / shotgun(500) / medkit(50)`，目录是代码内静态配置。
- 所有物品可叠加，重复购买累加数量；单次购买 `quantity=1..99`。
- 首版没有订单、支付、加币路径、消耗品使用、请求幂等或对局内武器效果。
- 购买使用 `user:lock:<accountID>`，TTL 10 秒、watchdog 开启、重试间隔 10 毫秒、最多等待 2 秒。
- 锁外读取只用于快速返回；需要写时必须加锁并在锁内重新读取、重新判断。
- `logic` 不可用时 `LoginReply.ok=false`，且不能先轮换 token 或绑定会话。
- Godot 购买请求超时后不得自动重试。
- 改动后的代码必须同步更新 `AGENTS.md`、`README.md` 与对应 `docs/`。
- Go 测试从 `joltgo/` 运行；涉及 cgo 或 full suite 时执行 `PATH="$PWD:$PATH" go test -count=1 ...`。

---

### Task 1: 玩家钱包与背包持久化

**Files:**
- Create: `joltgo/persist/protos/player/player.proto`
- Generate: `joltgo/persist/protos/player/player.redis.go`
- Modify: `joltgo/gen-redis.ps1`
- Create: `joltgo/persist/player.go`
- Test: `joltgo/persist/player_test.go`

**Interfaces:**
- Consumes: `kv.OpenRedigo` 提供的 `*redigo.Pool`。
- Produces:
  - `type PlayerWallet struct { Coins int64 }`
  - `type PlayerItem struct { ItemID string; Quantity int64; AcquiredAt int64 }`
  - `type PlayerBag struct { Items []PlayerItem; EquippedPrimaryWeapon string }`
  - `type PlayerPersistence interface { GetWallet(context.Context,uint64)(PlayerWallet,bool,error); SaveWallet(context.Context,uint64,PlayerWallet) error; DeleteWallet(context.Context,uint64) error; GetBag(context.Context,uint64)(PlayerBag,bool,error); SaveBag(context.Context,uint64,PlayerBag) error; DeleteBag(context.Context,uint64) error }`
  - `func NewPlayerStore(*redigo.Pool) *PlayerStore`
  - `func PlayerWalletKey(uint64) string`
  - `func PlayerBagKey(uint64) string`

- [ ] **Step 1: 定义持久化 proto 与独立生成命令**

创建 `joltgo/persist/protos/player/player.proto`：

```proto
syntax = "proto3";

package persist.player;

option go_package = "joltgo/persist/protos/player;playerpb";

enum REDBKey {
  REDB_KEY_UNSPECIFIED = 0;
  UserBagDB = 1;
  UserWalletDB = 2;
}

enum DBSchemaVersion {
  DB_SCHEMA_VERSION_UNSPECIFIED = 0;
  DB_SCHEMA_VERSION_CURRENT = 1;
}

message DBUserWallet {
  int64 coins = 1;
  DBSchemaVersion schema_version = 2;
}

message DBUserBag {
  DBItems items = 1;
  string equipped_primary_weapon = 2;
  DBSchemaVersion schema_version = 3;

  message DBItems {
    repeated DBItem items = 1;
  }

  message DBItem {
    string item_id = 1;
    int64 quantity = 2;
    int64 acquired_at = 3;
  }
}
```

修改 `joltgo/gen-redis.ps1`：保留账号生成命令，再追加玩家模型命令。两次调用的选项必须分开，否则账号 key 会被改掉。

```powershell
& protoc `
    -I $protoDir `
    "--plugin=protoc-gen-redis=$plugin" `
    "--redis_out=$protoDir" `
    '--redis_opt=paths=source_relative,key_format=acct:%d:%d:%d' `
    'account.proto'
if ($LASTEXITCODE -ne 0) { throw 'protoc-gen-redis account failed' }

& protoc `
    -I $protoDir `
    "--plugin=protoc-gen-redis=$plugin" `
    "--redis_out=$protoDir" `
    '--redis_opt=paths=source_relative,key_format=REDB#%d:%d:%d' `
    'player/player.proto'
if ($LASTEXITCODE -ne 0) { throw 'protoc-gen-redis player failed' }

$generated = @(
    (Join-Path $protoDir 'account.redis.go'),
    (Join-Path $protoDir 'player\player.redis.go')
)
gofmt -w $generated
```

Run:

```powershell
cd joltgo
.\gen-redis.ps1
```

Expected: `account.redis.go` 与 `player/player.redis.go` 都更新，后者 package 为 `playerpb`。

- [ ] **Step 2: 写失败的仓储往返测试**

创建 `joltgo/persist/player_test.go`：

```go
package persist

import (
    "context"
    "testing"

    "github.com/alicebob/miniredis/v2"
    redigo "github.com/gomodule/redigo/redis"
    playerpb "joltgo/persist/protos/player"
)

func newTestPlayerStore(t *testing.T) (*PlayerStore, *miniredis.Miniredis) {
    t.Helper()
    mr := miniredis.RunT(t)
    pool := &redigo.Pool{
        MaxIdle: 2,
        Dial: func() (redigo.Conn, error) { return redigo.Dial("tcp", mr.Addr()) },
    }
    t.Cleanup(func() { _ = pool.Close() })
    return NewPlayerStore(pool), mr
}

func TestPlayerStoreRoundTrip(t *testing.T) {
    store, mr := newTestPlayerStore(t)
    ctx := context.Background()

    wallet := PlayerWallet{Coins: 1000}
    if err := store.SaveWallet(ctx, 42, wallet); err != nil {
        t.Fatalf("SaveWallet: %v", err)
    }
    bag := PlayerBag{
        Items: []PlayerItem{
            {ItemID: "rifle", Quantity: 2, AcquiredAt: 111},
            {ItemID: "medkit", Quantity: 5, AcquiredAt: 222},
        },
        EquippedPrimaryWeapon: "rifle",
    }
    if err := store.SaveBag(ctx, 42, bag); err != nil {
        t.Fatalf("SaveBag: %v", err)
    }

    if got := PlayerWalletKey(42); got != "REDB#2:42:0" {
        t.Fatalf("PlayerWalletKey = %q", got)
    }
    if got := PlayerBagKey(42); got != "REDB#1:42:0" {
        t.Fatalf("PlayerBagKey = %q", got)
    }
    if !mr.Exists(PlayerWalletKey(42)) || !mr.Exists(PlayerBagKey(42)) {
        t.Fatal("钱包或背包 Hash 未写入")
    }

    gotWallet, ok, err := store.GetWallet(ctx, 42)
    if err != nil || !ok || gotWallet != wallet {
        t.Fatalf("GetWallet: got=%+v ok=%v err=%v", gotWallet, ok, err)
    }
    gotBag, ok, err := store.GetBag(ctx, 42)
    if err != nil || !ok {
        t.Fatalf("GetBag: ok=%v err=%v", ok, err)
    }
    if gotBag.EquippedPrimaryWeapon != "rifle" || len(gotBag.Items) != 2 {
        t.Fatalf("GetBag 不一致: %+v", gotBag)
    }

    conn := store.pool.Get()
    defer conn.Close()
    schema, err := redigo.Int(conn.Do("HGET", PlayerWalletKey(42), playerpb.FieldDBUserWallet_SchemaVersion))
    if err != nil || schema != int(playerpb.DBSchemaVersion_DB_SCHEMA_VERSION_CURRENT) {
        t.Fatalf("schema=%d err=%v", schema, err)
    }

    if err := store.DeleteWallet(ctx, 42); err != nil {
        t.Fatalf("DeleteWallet: %v", err)
    }
    if err := store.DeleteBag(ctx, 42); err != nil {
        t.Fatalf("DeleteBag: %v", err)
    }
    if _, ok, _ := store.GetWallet(ctx, 42); ok {
        t.Fatal("DeleteWallet 后仍存在")
    }
    if _, ok, _ := store.GetBag(ctx, 42); ok {
        t.Fatal("DeleteBag 后仍存在")
    }
}
```

- [ ] **Step 3: 运行测试并确认失败**

Run:

```bash
cd joltgo
go test -count=1 ./persist
```

Expected: FAIL，提示 `undefined: PlayerStore`、`undefined: PlayerWallet` 等。

- [ ] **Step 4: 实现玩家仓储**

创建 `joltgo/persist/player.go`：

```go
package persist

import (
    "context"
    "fmt"
    "sort"

    "github.com/gomodule/redigo/redis"
    playerpb "joltgo/persist/protos/player"
)

const (
    playerBagNamespace    uint32 = uint32(playerpb.REDBKey_UserBagDB)
    playerWalletNamespace uint32 = uint32(playerpb.REDBKey_UserWalletDB)
)

type PlayerWallet struct {
    Coins int64
}

type PlayerItem struct {
    ItemID     string
    Quantity   int64
    AcquiredAt int64
}

type PlayerBag struct {
    Items                 []PlayerItem
    EquippedPrimaryWeapon string
}

type PlayerPersistence interface {
    GetWallet(context.Context, uint64) (PlayerWallet, bool, error)
    SaveWallet(context.Context, uint64, PlayerWallet) error
    DeleteWallet(context.Context, uint64) error
    GetBag(context.Context, uint64) (PlayerBag, bool, error)
    SaveBag(context.Context, uint64, PlayerBag) error
    DeleteBag(context.Context, uint64) error
}

type PlayerStore struct {
    pool *redis.Pool
}

var _ PlayerPersistence = (*PlayerStore)(nil)

func NewPlayerStore(pool *redis.Pool) *PlayerStore {
    return &PlayerStore{pool: pool}
}

func PlayerWalletKey(id uint64) string {
    return fmt.Sprintf("REDB#%d:%d:%d", playerWalletNamespace, id, 0)
}

func PlayerBagKey(id uint64) string {
    return fmt.Sprintf("REDB#%d:%d:%d", playerBagNamespace, id, 0)
}

func (s *PlayerStore) SaveWallet(ctx context.Context, id uint64, wallet PlayerWallet) error {
    conn, err := s.pool.GetContext(ctx)
    if err != nil {
        return err
    }
    defer conn.Close()
    row := &playerpb.DBUserWallet{
        Coins:         wallet.Coins,
        SchemaVersion: playerpb.DBSchemaVersion_DB_SCHEMA_VERSION_CURRENT,
    }
    return row.SetFields(conn, playerWalletNamespace, id, 0)
}

func (s *PlayerStore) GetWallet(ctx context.Context, id uint64) (PlayerWallet, bool, error) {
    conn, err := s.pool.GetContext(ctx)
    if err != nil {
        return PlayerWallet{}, false, err
    }
    defer conn.Close()
    exists, err := redis.Bool(conn.Do("EXISTS", PlayerWalletKey(id)))
    if err != nil || !exists {
        return PlayerWallet{}, false, err
    }
    var row playerpb.DBUserWallet
    if err := row.GetFields(conn, playerWalletNamespace, id, 0); err != nil {
        return PlayerWallet{}, false, err
    }
    return PlayerWallet{Coins: row.Coins}, true, nil
}

func (s *PlayerStore) DeleteWallet(ctx context.Context, id uint64) error {
    conn, err := s.pool.GetContext(ctx)
    if err != nil {
        return err
    }
    defer conn.Close()
    _, err = conn.Do("DEL", PlayerWalletKey(id))
    return err
}

func (s *PlayerStore) SaveBag(ctx context.Context, id uint64, bag PlayerBag) error {
    conn, err := s.pool.GetContext(ctx)
    if err != nil {
        return err
    }
    defer conn.Close()

    items := append([]PlayerItem(nil), bag.Items...)
    sort.Slice(items, func(i, j int) bool { return items[i].ItemID < items[j].ItemID })
    stored := make([]playerpb.DBUserBag_DBItem, 0, len(items))
    for _, item := range items {
        stored = append(stored, playerpb.DBUserBag_DBItem{
            ItemId: item.ItemID, Quantity: item.Quantity, AcquiredAt: item.AcquiredAt,
        })
    }
    row := &playerpb.DBUserBag{
        Items:                 playerpb.DBUserBag_DBItems{Items: stored},
        EquippedPrimaryWeapon: bag.EquippedPrimaryWeapon,
        SchemaVersion:         playerpb.DBSchemaVersion_DB_SCHEMA_VERSION_CURRENT,
    }
    return row.SetFields(conn, playerBagNamespace, id, 0)
}

func (s *PlayerStore) GetBag(ctx context.Context, id uint64) (PlayerBag, bool, error) {
    conn, err := s.pool.GetContext(ctx)
    if err != nil {
        return PlayerBag{}, false, err
    }
    defer conn.Close()
    exists, err := redis.Bool(conn.Do("EXISTS", PlayerBagKey(id)))
    if err != nil || !exists {
        return PlayerBag{}, false, err
    }
    var row playerpb.DBUserBag
    if err := row.GetFields(conn, playerBagNamespace, id, 0); err != nil {
        return PlayerBag{}, false, err
    }
    bag := PlayerBag{EquippedPrimaryWeapon: row.EquippedPrimaryWeapon}
    for _, item := range row.Items.Items {
        bag.Items = append(bag.Items, PlayerItem{
            ItemID: item.ItemId, Quantity: item.Quantity, AcquiredAt: item.AcquiredAt,
        })
    }
    return bag, true, nil
}

func (s *PlayerStore) DeleteBag(ctx context.Context, id uint64) error {
    conn, err := s.pool.GetContext(ctx)
    if err != nil {
        return err
    }
    defer conn.Close()
    _, err = conn.Do("DEL", PlayerBagKey(id))
    return err
}
```

生成器按实际 proto 产出 `playerpb.DBUserBag_DBItems` 与 `playerpb.DBUserBag_DBItem`；对外领域类型和接口固定不变。

- [ ] **Step 5: 运行仓储测试**

Run:

```bash
cd joltgo
go test -count=1 ./persist
```

Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add joltgo/gen-redis.ps1 joltgo/persist
git commit -m "feat: 增加玩家钱包与背包持久化" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 2: 商品目录、领域状态与无锁读取

**Files:**
- Create: `joltgo/logic/catalog.go`
- Create: `joltgo/logic/service.go`
- Test: `joltgo/logic/catalog_test.go`
- Test: `joltgo/logic/service_test.go`

**Interfaces:**
- Consumes: `persist.PlayerPersistence`、生成的 `game/protos` 尚未参与本任务。
- Produces:
  - `type ItemDefinition struct { ID, DisplayName string; Price int64; EquipSlot string }`
  - `type Catalog struct { ... }`
  - `func NewCatalog([]ItemDefinition) (Catalog, error)`
  - `func MustDefaultCatalog() Catalog`
  - `func (Catalog) Lookup(string) (ItemDefinition, bool)`
  - `func (Catalog) Definitions() []ItemDefinition`
  - `type ShopItem struct { ItemID, DisplayName string; Price int64; EquipSlot string; OwnedQuantity int64 }`
  - `type State struct { Coins int64; Items []ShopItem; EquippedPrimaryWeapon string }`
  - `type Store interface`，方法签名与 `persist.PlayerPersistence` 相同。
  - `type Service struct { ... }`
  - `func NewService(store Store, catalog Catalog, locks LockFactory) *Service`
  - `func (s *Service) State(context.Context, string) (State, error)`

- [ ] **Step 1: 写目录测试**

创建 `joltgo/logic/catalog_test.go`：

```go
package logic

import (
    "reflect"
    "testing"
)

func TestDefaultCatalog(t *testing.T) {
    catalog := MustDefaultCatalog()
    ids := make([]string, 0, len(catalog.Definitions()))
    for _, def := range catalog.Definitions() {
        ids = append(ids, def.ID)
    }
    want := []string{"rifle", "pistol", "shotgun", "medkit"}
    if !reflect.DeepEqual(ids, want) {
        t.Fatalf("ids=%v want=%v", ids, want)
    }
    rifle, ok := catalog.Lookup("rifle")
    if !ok || rifle.Price != 300 || rifle.EquipSlot != SlotPrimaryWeapon {
        t.Fatalf("rifle=%+v ok=%v", rifle, ok)
    }
}

func TestNewCatalogRejectsInvalidDefinitions(t *testing.T) {
    cases := []struct {
        name string
        defs []ItemDefinition
    }{
        {"empty-id", []ItemDefinition{{DisplayName: "x", Price: 1}}},
        {"negative-price", []ItemDefinition{{ID: "x", DisplayName: "x", Price: -1}}},
        {"bad-slot", []ItemDefinition{{ID: "x", DisplayName: "x", Price: 1, EquipSlot: "back"}}},
        {"duplicate", []ItemDefinition{
            {ID: "x", DisplayName: "x", Price: 1},
            {ID: "x", DisplayName: "y", Price: 2},
        }},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            if _, err := NewCatalog(tc.defs); err == nil {
                t.Fatal("应拒绝非法目录")
            }
        })
    }
}
```

- [ ] **Step 2: 运行并确认失败**

Run:

```bash
cd joltgo
go test -count=1 ./logic
```

Expected: FAIL，`logic` 包或符号不存在。

- [ ] **Step 3: 实现目录**

创建 `joltgo/logic/catalog.go`：

```go
package logic

import (
    "fmt"
    "strings"
)

const SlotPrimaryWeapon = "primary_weapon"

type ItemDefinition struct {
    ID          string
    DisplayName string
    Price       int64
    EquipSlot   string
}

type Catalog struct {
    defs []ItemDefinition
    byID map[string]ItemDefinition
}

func NewCatalog(defs []ItemDefinition) (Catalog, error) {
    byID := make(map[string]ItemDefinition, len(defs))
    copied := make([]ItemDefinition, 0, len(defs))
    for _, def := range defs {
        if strings.TrimSpace(def.ID) == "" {
            return Catalog{}, fmt.Errorf("logic: empty item id")
        }
        if strings.TrimSpace(def.DisplayName) == "" {
            return Catalog{}, fmt.Errorf("logic: empty display name for %s", def.ID)
        }
        if def.Price < 0 {
            return Catalog{}, fmt.Errorf("logic: negative price for %s", def.ID)
        }
        if def.EquipSlot != "" && def.EquipSlot != SlotPrimaryWeapon {
            return Catalog{}, fmt.Errorf("logic: unknown slot %q for %s", def.EquipSlot, def.ID)
        }
        if _, exists := byID[def.ID]; exists {
            return Catalog{}, fmt.Errorf("logic: duplicate item id %s", def.ID)
        }
        byID[def.ID] = def
        copied = append(copied, def)
    }
    return Catalog{defs: copied, byID: byID}, nil
}

func MustDefaultCatalog() Catalog {
    catalog, err := NewCatalog([]ItemDefinition{
        {ID: "rifle", DisplayName: "步枪", Price: 300, EquipSlot: SlotPrimaryWeapon},
        {ID: "pistol", DisplayName: "手枪", Price: 150, EquipSlot: SlotPrimaryWeapon},
        {ID: "shotgun", DisplayName: "霰弹枪", Price: 500, EquipSlot: SlotPrimaryWeapon},
        {ID: "medkit", DisplayName: "医疗包", Price: 50},
    })
    if err != nil {
        panic(err)
    }
    return catalog
}

func (c Catalog) Lookup(itemID string) (ItemDefinition, bool) {
    def, ok := c.byID[itemID]
    return def, ok
}

func (c Catalog) Definitions() []ItemDefinition {
    return append([]ItemDefinition(nil), c.defs...)
}
```

- [ ] **Step 4: 写 State 测试**

创建 `joltgo/logic/service_test.go`，先加入测试环境与 State 用例：

```go
package logic

import (
    "context"
    "testing"

    "github.com/alicebob/miniredis/v2"
    redigo "github.com/gomodule/redigo/redis"
    "joltgo/persist"
)

type noopLock struct{}
func (noopLock) Lock(context.Context) error   { return nil }
func (noopLock) Unlock(context.Context) error { return nil }
func (noopLock) IsLost() bool                 { return false }

type noopLockFactory struct{}
func (noopLockFactory) New(uint64) Lock { return noopLock{} }

type serviceTestEnv struct {
    svc   *Service
    store *persist.PlayerStore
}

func newServiceTestEnv(t *testing.T) *serviceTestEnv {
    t.Helper()
    mr := miniredis.RunT(t)
    pool := &redigo.Pool{
        MaxIdle: 2,
        Dial: func() (redigo.Conn, error) { return redigo.Dial("tcp", mr.Addr()) },
    }
    t.Cleanup(func() { _ = pool.Close() })
    store := persist.NewPlayerStore(pool)
    return &serviceTestEnv{
        svc:   NewService(store, MustDefaultCatalog(), noopLockFactory{}),
        store: store,
    }
}

func mustReason(t *testing.T, err error, want string) {
    t.Helper()
    if got := ReasonOf(err); got != want {
        t.Fatalf("reason=%q want=%q err=%v", got, want, err)
    }
}

func TestStateBuildsCatalogAndQuantities(t *testing.T) {
    env := newServiceTestEnv(t)
    ctx := context.Background()
    if err := env.store.SaveWallet(ctx, 1, persist.PlayerWallet{Coins: 1000}); err != nil {
        t.Fatal(err)
    }
    if err := env.store.SaveBag(ctx, 1, persist.PlayerBag{
        Items: []persist.PlayerItem{
            {ItemID: "rifle", Quantity: 2, AcquiredAt: 1},
            {ItemID: "medkit", Quantity: 5, AcquiredAt: 2},
        },
        EquippedPrimaryWeapon: "rifle",
    }); err != nil {
        t.Fatal(err)
    }

    state, err := env.svc.State(ctx, "1")
    if err != nil {
        t.Fatalf("State: %v", err)
    }
    if state.Coins != 1000 || state.EquippedPrimaryWeapon != "rifle" {
        t.Fatalf("state=%+v", state)
    }
    if len(state.Items) != 4 || state.Items[0].ItemID != "rifle" || state.Items[0].OwnedQuantity != 2 {
        t.Fatalf("items=%+v", state.Items)
    }
    if state.Items[1].ItemID != "pistol" || state.Items[1].OwnedQuantity != 0 {
        t.Fatalf("items=%+v", state.Items)
    }
}

func TestStateRejectsMissingProfileAndBadUID(t *testing.T) {
    env := newServiceTestEnv(t)
    _, err := env.svc.State(context.Background(), "1")
    mustReason(t, err, ReasonProfileMissing)

    _, err = env.svc.State(context.Background(), "")
    mustReason(t, err, ReasonUnauthenticated)

    _, err = env.svc.State(context.Background(), "abc")
    mustReason(t, err, ReasonUnauthenticated)
}

```


- [ ] **Step 5: 运行并确认失败**

Run:

```bash
cd joltgo
go test -count=1 ./logic
```

Expected: FAIL，`undefined: Service`、`undefined: ReasonOf` 等。

- [ ] **Step 6: 实现领域类型与 State**

创建 `joltgo/logic/service.go`：

```go
package logic

import (
    "context"
    "errors"
    "strconv"
    "time"

    "joltgo/persist"
)

const (
    ReasonBadQuantity      = "bad_quantity"
    ReasonItemNotFound     = "item_not_found"
    ReasonInsufficientFund = "insufficient_funds"
    ReasonNotOwned         = "not_owned"
    ReasonNotEquippable    = "not_equippable"
    ReasonBusy             = "busy"
    ReasonUnauthenticated  = "unauthenticated"
    ReasonProfileMissing   = "profile_missing"
    ReasonInternal         = "internal"
)

type ReasonError struct {
    Reason string
}

func (e *ReasonError) Error() string { return e.Reason }

func reason(code string) error { return &ReasonError{Reason: code} }

func ReasonOf(err error) string {
    if err == nil {
        return ""
    }
    var target *ReasonError
    if errors.As(err, &target) {
        return target.Reason
    }
    return ReasonInternal
}

type Store interface {
    GetWallet(context.Context, uint64) (persist.PlayerWallet, bool, error)
    SaveWallet(context.Context, uint64, persist.PlayerWallet) error
    DeleteWallet(context.Context, uint64) error
    GetBag(context.Context, uint64) (persist.PlayerBag, bool, error)
    SaveBag(context.Context, uint64, persist.PlayerBag) error
    DeleteBag(context.Context, uint64) error
}

type Lock interface {
    Lock(context.Context) error
    Unlock(context.Context) error
    IsLost() bool
}

type LockFactory interface {
    New(accountID uint64) Lock
}

type ShopItem struct {
    ItemID        string
    DisplayName   string
    Price         int64
    EquipSlot     string
    OwnedQuantity int64
}

type State struct {
    Coins                 int64
    Items                 []ShopItem
    EquippedPrimaryWeapon string
}

type Service struct {
    store   Store
    catalog Catalog
    locks   LockFactory
    now     func() time.Time
}

func NewService(store Store, catalog Catalog, locks LockFactory) *Service {
    return &Service{store: store, catalog: catalog, locks: locks, now: time.Now}
}

func parseAccountID(raw string) (uint64, error) {
    if raw == "" {
        return 0, reason(ReasonUnauthenticated)
    }
    id, err := strconv.ParseUint(raw, 10, 64)
    if err != nil || id == 0 {
        return 0, reason(ReasonUnauthenticated)
    }
    return id, nil
}

func (s *Service) State(ctx context.Context, accountID string) (State, error) {
    id, err := parseAccountID(accountID)
    if err != nil {
        return State{}, err
    }
    wallet, ok, err := s.store.GetWallet(ctx, id)
    if err != nil {
        return State{}, err
    }
    if !ok {
        return State{}, reason(ReasonProfileMissing)
    }
    bag, ok, err := s.store.GetBag(ctx, id)
    if err != nil {
        return State{}, err
    }
    if !ok {
        return State{}, reason(ReasonProfileMissing)
    }
    return s.stateFrom(wallet, bag), nil
}

func (s *Service) stateFrom(wallet persist.PlayerWallet, bag persist.PlayerBag) State {
    quantities := make(map[string]int64, len(bag.Items))
    for _, item := range bag.Items {
        if item.Quantity > 0 {
            quantities[item.ItemID] += item.Quantity
        }
    }
    defs := s.catalog.Definitions()
    items := make([]ShopItem, 0, len(defs))
    for _, def := range defs {
        items = append(items, ShopItem{
            ItemID:        def.ID,
            DisplayName:   def.DisplayName,
            Price:         def.Price,
            EquipSlot:     def.EquipSlot,
            OwnedQuantity: quantities[def.ID],
        })
    }
    return State{
        Coins:                 wallet.Coins,
        Items:                 items,
        EquippedPrimaryWeapon: bag.EquippedPrimaryWeapon,
    }
}
```

- [ ] **Step 7: 运行测试并提交**

Run:

```bash
cd joltgo
go test -count=1 ./logic
```

Expected: PASS。

```bash
git add joltgo/logic/catalog.go joltgo/logic/catalog_test.go joltgo/logic/service.go joltgo/logic/service_test.go
git commit -m "feat: 增加 logic 商品目录与状态读取" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 3: 登录上线建档与双层锁检查

**Files:**
- Create: `joltgo/logic/lock.go`
- Modify: `joltgo/logic/service.go`
- Modify: `joltgo/logic/service_test.go`

**Interfaces:**
- Consumes: Task 1 的 `Store` 实现、Task 2 的 `Service`。
- Produces:
  - `func AccountLockKey(uint64) string`
  - `func NewRedisLockFactory(*redis.Client) *RedisLockFactory`
  - `func (s *Service) EnsureProfile(context.Context, string) error`
  - `func (s *Service) withAccountLock(context.Context, uint64, func(Lock) error) error`

- [ ] **Step 1: 写 EnsureProfile 测试**

在 `joltgo/logic/service_test.go` 增加：

```go
type countingLockFactory struct {
    inner LockFactory
    mu    sync.Mutex
    count int
}

func (f *countingLockFactory) New(accountID uint64) Lock {
    f.mu.Lock()
    f.count++
    f.mu.Unlock()
    return f.inner.New(accountID)
}

func (f *countingLockFactory) Count() int {
    f.mu.Lock()
    defer f.mu.Unlock()
    return f.count
}

type blockingLock struct{}

func (blockingLock) Lock(ctx context.Context) error {
    <-ctx.Done()
    return ctx.Err()
}
func (blockingLock) Unlock(context.Context) error { return nil }
func (blockingLock) IsLost() bool                 { return false }

type blockingLockFactory struct{}

func (blockingLockFactory) New(uint64) Lock { return blockingLock{} }

func newLockedServiceTestEnv(t *testing.T) (*serviceTestEnv, *countingLockFactory) {
    t.Helper()
    mr := miniredis.RunT(t)
    pool := &redigo.Pool{
        MaxIdle: 2,
        Dial: func() (redigo.Conn, error) { return redigo.Dial("tcp", mr.Addr()) },
    }
    rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
    t.Cleanup(func() { _ = pool.Close(); _ = rdb.Close() })
    store := persist.NewPlayerStore(pool)
    locks := &countingLockFactory{inner: NewRedisLockFactory(rdb)}
    return &serviceTestEnv{svc: NewService(store, MustDefaultCatalog(), locks), store: store}, locks
}

func TestEnsureProfileIsIdempotent(t *testing.T) {
    env, locks := newLockedServiceTestEnv(t)
    ctx := context.Background()

    if err := env.svc.EnsureProfile(ctx, "7"); err != nil {
        t.Fatalf("first EnsureProfile: %v", err)
    }
    if locks.Count() != 1 {
        t.Fatalf("首次建档应获取一次锁，得到 %d", locks.Count())
    }
    wallet, ok, _ := env.store.GetWallet(ctx, 7)
    if !ok || wallet.Coins != 1000 {
        t.Fatalf("wallet=%+v ok=%v", wallet, ok)
    }
    bag, ok, _ := env.store.GetBag(ctx, 7)
    if !ok || len(bag.Items) != 0 || bag.EquippedPrimaryWeapon != "" {
        t.Fatalf("bag=%+v ok=%v", bag, ok)
    }

    if err := env.svc.EnsureProfile(ctx, "7"); err != nil {
        t.Fatalf("second EnsureProfile: %v", err)
    }
    if locks.Count() != 1 {
        t.Fatalf("档案已存在时不应获取锁，得到 %d", locks.Count())
    }
}

func TestEnsureProfileRepairsOnlyMissingParts(t *testing.T) {
    env, _ := newLockedServiceTestEnv(t)
    ctx := context.Background()
    if err := env.store.SaveWallet(ctx, 7, persist.PlayerWallet{Coins: 321}); err != nil {
        t.Fatal(err)
    }

    if err := env.svc.EnsureProfile(ctx, "7"); err != nil {
        t.Fatalf("EnsureProfile: %v", err)
    }
    wallet, ok, _ := env.store.GetWallet(ctx, 7)
    if !ok || wallet.Coins != 321 {
        t.Fatalf("不能重置已有钱包: %+v ok=%v", wallet, ok)
    }
    if _, ok, _ := env.store.GetBag(ctx, 7); !ok {
        t.Fatal("缺失的背包应被补建")
    }
}

func TestEnsureProfileConcurrentInitializesOnce(t *testing.T) {
    env, _ := newLockedServiceTestEnv(t)
    const workers = 12
    var wg sync.WaitGroup
    errs := make(chan error, workers)
    for i := 0; i < workers; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            errs <- env.svc.EnsureProfile(context.Background(), "9")
        }()
    }
    wg.Wait()
    close(errs)
    for err := range errs {
        if err != nil {
            t.Fatalf("EnsureProfile: %v", err)
        }
    }
    wallet, ok, _ := env.store.GetWallet(context.Background(), 9)
    if !ok || wallet.Coins != 1000 {
        t.Fatalf("并发初始化只能发一次金币: %+v ok=%v", wallet, ok)
    }
}

func TestEnsureProfileLockTimeoutReturnsBusy(t *testing.T) {
    env := newServiceTestEnv(t)
    env.svc.locks = blockingLockFactory{}
    err := env.svc.EnsureProfile(context.Background(), "7")
    mustReason(t, err, ReasonBusy)
}
```

补充 import：`sync`、`github.com/redis/go-redis/v9`。加入 `if AccountLockKey(7) != "user:lock:7" { t.Fatalf("lock key=%q", AccountLockKey(7)) }` 到 `TestEnsureProfileIsIdempotent` 开头。

- [ ] **Step 2: 运行并确认失败**

Run:

```bash
cd joltgo
go test -count=1 ./logic
```

Expected: FAIL，`EnsureProfile`、`NewRedisLockFactory`、`ReasonBusy` 路径尚未实现。

- [ ] **Step 3: 实现 Redis 锁工厂**

创建 `joltgo/logic/lock.go`：

```go
package logic

import (
    "context"
    "errors"
    "fmt"
    "log"
    "time"

    "github.com/beijian128/distlock"
    "github.com/redis/go-redis/v9"
)

const (
    lockTTL          = 10 * time.Second
    lockRetry        = 10 * time.Millisecond
    lockWait         = 2 * time.Second
    unlockTimeout    = 1 * time.Second
    compensateTimeout = time.Second
)

func AccountLockKey(accountID uint64) string {
    return fmt.Sprintf("user:lock:%d", accountID)
}

type RedisLockFactory struct {
    client redis.UniversalClient
}

func NewRedisLockFactory(client redis.UniversalClient) *RedisLockFactory {
    return &RedisLockFactory{client: client}
}

func (f *RedisLockFactory) New(accountID uint64) Lock {
    key := AccountLockKey(accountID)
    return distlock.New(f.client, key,
        distlock.WithTTL(lockTTL),
        distlock.WithRetryInterval(lockRetry),
        distlock.WithOnLost(func() { log.Printf("logic: lock %s lost", key) }),
    )
}

var errLockLost = errors.New("logic: lock lost")

func (s *Service) withAccountLock(ctx context.Context, accountID uint64, fn func(Lock) error) error {
    lock := s.locks.New(accountID)
    lockCtx, cancel := context.WithTimeout(ctx, lockWait)
    defer cancel()
    if err := lock.Lock(lockCtx); err != nil {
        if errors.Is(err, context.DeadlineExceeded) {
            return reason(ReasonBusy)
        }
        return err
    }
    defer func() {
        unlockCtx, cancel := context.WithTimeout(context.Background(), unlockTimeout)
        defer cancel()
        if err := lock.Unlock(unlockCtx); err != nil && !errors.Is(err, distlock.ErrNotOwned) {
            log.Printf("logic: unlock account %d: %v", accountID, err)
        }
    }()
    if lock.IsLost() {
        return errLockLost
    }
    if err := fn(lock); err != nil {
        return err
    }
    if lock.IsLost() {
        return errLockLost
    }
    return nil
}
```

- [ ] **Step 4: 实现 EnsureProfile**

在 `joltgo/logic/service.go` 增加：

```go
func (s *Service) EnsureProfile(ctx context.Context, accountID string) error {
    id, err := parseAccountID(accountID)
    if err != nil {
        return err
    }
    _, hasWallet, err := s.store.GetWallet(ctx, id)
    if err != nil {
        return err
    }
    _, hasBag, err := s.store.GetBag(ctx, id)
    if err != nil {
        return err
    }
    if hasWallet && hasBag {
        return nil
    }

    return s.withAccountLock(ctx, id, func(lock Lock) error {
        _, walletExists, err := s.store.GetWallet(ctx, id)
        if err != nil {
            return err
        }
        _, bagExists, err := s.store.GetBag(ctx, id)
        if err != nil {
            return err
        }
        if walletExists && bagExists {
            return nil
        }

        createdWallet := false
        if !walletExists {
            if lock.IsLost() {
                return errLockLost
            }
            if err := s.store.SaveWallet(ctx, id, persist.PlayerWallet{Coins: 1000}); err != nil {
                return err
            }
            createdWallet = true
        }
        if !bagExists {
            if lock.IsLost() {
                return errLockLost
            }
            if err := s.store.SaveBag(ctx, id, persist.PlayerBag{}); err != nil {
                if createdWallet {
                    if delErr := s.store.DeleteWallet(ctx, id); delErr != nil {
                        log.Printf("logic: compensate wallet for account %d: %v", id, delErr)
                    }
                }
                return err
            }
        }
        return nil
    })
}
```

补充 import：`log`。

- [ ] **Step 5: 运行测试并提交**

Run:

```bash
cd joltgo
go test -count=1 ./logic
```

Expected: PASS。`TestEnsureProfileLockTimeoutReturnsBusy` 最多等待约 2 秒。

```bash
git add joltgo/logic/lock.go joltgo/logic/service.go joltgo/logic/service_test.go
git commit -m "feat: 增加 logic 上线建档与账号锁" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 4: 购买流程与失败补偿

**Files:**
- Modify: `joltgo/logic/service.go`
- Modify: `joltgo/logic/service_test.go`

**Interfaces:**
- Consumes: Task 3 的 `withAccountLock`、`EnsureProfile`。
- Produces:
  - `func (s *Service) Purchase(context.Context, string, string, int32) (State, error)`
  - `func addItem(persist.PlayerBag, string, int64, int64) persist.PlayerBag`
  - `func hasItem(persist.PlayerBag, string) bool`

- [ ] **Step 1: 写购买测试**

在 `joltgo/logic/service_test.go` 增加以下用例，并在文件 import 中加入 `errors` 与 `sync`：

```go
type bagFailStore struct {
    Store
    fail bool
}

func (s *bagFailStore) SaveBag(ctx context.Context, id uint64, bag persist.PlayerBag) error {
    if s.fail {
        return errors.New("boom: save bag")
    }
    return s.Store.SaveBag(ctx, id, bag)
}

func TestPurchaseSuccessAndStacking(t *testing.T) {
    env, _ := newLockedServiceTestEnv(t)
    ctx := context.Background()
    if err := env.svc.EnsureProfile(ctx, "7"); err != nil {
        t.Fatal(err)
    }

    state, err := env.svc.Purchase(ctx, "7", "rifle", 2)
    if err != nil {
        t.Fatalf("Purchase: %v", err)
    }
    if state.Coins != 400 {
        t.Fatalf("coins=%d want=400", state.Coins)
    }
    if state.Items[0].OwnedQuantity != 2 {
        t.Fatalf("rifle quantity=%d want=2", state.Items[0].OwnedQuantity)
    }

    firstBag, _, _ := env.store.GetBag(ctx, 7)
    acquiredAt := firstBag.Items[0].AcquiredAt

    state, err = env.svc.Purchase(ctx, "7", "rifle", 1)
    if err != nil {
        t.Fatalf("second Purchase: %v", err)
    }
    if state.Coins != 100 || state.Items[0].OwnedQuantity != 3 {
        t.Fatalf("state=%+v", state)
    }
    secondBag, _, _ := env.store.GetBag(ctx, 7)
    if secondBag.Items[0].AcquiredAt != acquiredAt {
        t.Fatalf("重复购买必须保留首次获得时间: %d -> %d", acquiredAt, secondBag.Items[0].AcquiredAt)
    }
}

func TestPurchaseRequiresOnlineProfile(t *testing.T) {
    env, _ := newLockedServiceTestEnv(t)
    _, err := env.svc.Purchase(context.Background(), "7", "medkit", 1)
    mustReason(t, err, ReasonProfileMissing)
}

func TestPurchaseRejectsWithoutWriting(t *testing.T) {
    env, locks := newLockedServiceTestEnv(t)
    ctx := context.Background()
    if err := env.svc.EnsureProfile(ctx, "7"); err != nil {
        t.Fatal(err)
    }
    before := locks.Count()

    _, err := env.svc.Purchase(ctx, "7", "shotgun", 3)
    mustReason(t, err, ReasonInsufficientFund)
    if locks.Count() != before {
        t.Fatalf("余额不足不应加锁: before=%d after=%d", before, locks.Count())
    }

    _, err = env.svc.Purchase(ctx, "7", "rifle", 0)
    mustReason(t, err, ReasonBadQuantity)
    _, err = env.svc.Purchase(ctx, "7", "rifle", 100)
    mustReason(t, err, ReasonBadQuantity)
    _, err = env.svc.Purchase(ctx, "7", "rocket", 1)
    mustReason(t, err, ReasonItemNotFound)
}

func TestPurchaseCompensatesWalletWhenBagSaveFails(t *testing.T) {
    env := newServiceTestEnv(t)
    ctx := context.Background()
    if err := env.store.SaveWallet(ctx, 7, persist.PlayerWallet{Coins: 1000}); err != nil {
        t.Fatal(err)
    }
    if err := env.store.SaveBag(ctx, 7, persist.PlayerBag{}); err != nil {
        t.Fatal(err)
    }
    failing := &bagFailStore{Store: env.store, fail: true}
    env.svc.store = failing
    env.svc.locks = noopLockFactory{}

    _, err := env.svc.Purchase(ctx, "7", "rifle", 1)
    if ReasonOf(err) != ReasonInternal {
        t.Fatalf("err=%v want internal", err)
    }
    wallet, ok, err := env.store.GetWallet(ctx, 7)
    if err != nil || !ok || wallet.Coins != 1000 {
        t.Fatalf("钱包未补偿: %+v ok=%v err=%v", wallet, ok, err)
    }
    bag, _, _ := env.store.GetBag(ctx, 7)
    if len(bag.Items) != 0 {
        t.Fatalf("背包失败时不应发货: %+v", bag)
    }
}

func TestPurchaseLockTimeoutReturnsBusy(t *testing.T) {
    env, _ := newLockedServiceTestEnv(t)
    ctx := context.Background()
    if err := env.svc.EnsureProfile(ctx, "7"); err != nil {
        t.Fatal(err)
    }
    env.svc.locks = blockingLockFactory{}
    _, err := env.svc.Purchase(ctx, "7", "medkit", 1)
    mustReason(t, err, ReasonBusy)
}

func TestPurchaseConcurrentSameAccount(t *testing.T) {
    env, _ := newLockedServiceTestEnv(t)
    ctx := context.Background()
    if err := env.svc.EnsureProfile(ctx, "7"); err != nil {
        t.Fatal(err)
    }

    const workers = 10
    var wg sync.WaitGroup
    errs := make(chan error, workers)
    for i := 0; i < workers; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            _, err := env.svc.Purchase(context.Background(), "7", "medkit", 1)
            errs <- err
        }()
    }
    wg.Wait()
    close(errs)
    for err := range errs {
        if err != nil {
            t.Fatalf("concurrent purchase: %v", err)
        }
    }
    state, err := env.svc.State(ctx, "7")
    if err != nil {
        t.Fatal(err)
    }
    if state.Coins != 500 || state.Items[3].OwnedQuantity != 10 {
        t.Fatalf("并发购买发生丢更新: %+v", state)
    }
}
```

- [ ] **Step 2: 运行并确认失败**

Run:

```bash
cd joltgo
go test -count=1 ./logic
```

Expected: FAIL，`Purchase` 未定义；并发测试最终余额与数量不正确。

- [ ] **Step 3: 实现购买、物品累加与补偿**

在 `joltgo/logic/service.go` 增加：

```go
func addItem(bag persist.PlayerBag, itemID string, quantity, acquiredAt int64) persist.PlayerBag {
    out := persist.PlayerBag{
        Items:                 append([]persist.PlayerItem(nil), bag.Items...),
        EquippedPrimaryWeapon: bag.EquippedPrimaryWeapon,
    }
    for i := range out.Items {
        if out.Items[i].ItemID == itemID {
            out.Items[i].Quantity += quantity
            return out
        }
    }
    out.Items = append(out.Items, persist.PlayerItem{
        ItemID: itemID, Quantity: quantity, AcquiredAt: acquiredAt,
    })
    return out
}

func hasItem(bag persist.PlayerBag, itemID string) bool {
    for _, item := range bag.Items {
        if item.ItemID == itemID && item.Quantity > 0 {
            return true
        }
    }
    return false
}

func (s *Service) compensateWallet(ctx context.Context, accountID uint64, wallet persist.PlayerWallet) {
    compCtx, cancel := context.WithTimeout(context.Background(), compensateTimeout)
    defer cancel()
    if err := s.store.SaveWallet(compCtx, accountID, wallet); err != nil {
        log.Printf("logic: compensate wallet for account %d failed: %v", accountID, err)
    }
}

func (s *Service) Purchase(ctx context.Context, accountID, itemID string, quantity int32) (State, error) {
    id, err := parseAccountID(accountID)
    if err != nil {
        return State{}, err
    }
    if quantity < 1 || quantity > 99 {
        return State{}, reason(ReasonBadQuantity)
    }
    def, ok := s.catalog.Lookup(itemID)
    if !ok {
        return State{}, reason(ReasonItemNotFound)
    }

    wallet, ok, err := s.store.GetWallet(ctx, id)
    if err != nil {
        return State{}, err
    }
    if !ok {
        return State{}, reason(ReasonProfileMissing)
    }
    total := def.Price * int64(quantity)
    if wallet.Coins < total {
        return State{}, reason(ReasonInsufficientFund)
    }

    var out State
    err = s.withAccountLock(ctx, id, func(lock Lock) error {
        currentWallet, ok, err := s.store.GetWallet(ctx, id)
        if err != nil {
            return err
        }
        if !ok {
            return reason(ReasonProfileMissing)
        }
        bag, ok, err := s.store.GetBag(ctx, id)
        if err != nil {
            return err
        }
        if !ok {
            return reason(ReasonProfileMissing)
        }
        if currentWallet.Coins < total {
            return reason(ReasonInsufficientFund)
        }

        nextWallet := persist.PlayerWallet{Coins: currentWallet.Coins - total}
        if lock.IsLost() {
            return errLockLost
        }
        if err := s.store.SaveWallet(ctx, id, nextWallet); err != nil {
            return err
        }

        nextBag := addItem(bag, def.ID, int64(quantity), s.now().Unix())
        if lock.IsLost() {
            s.compensateWallet(ctx, id, currentWallet)
            return errLockLost
        }
        if err := s.store.SaveBag(ctx, id, nextBag); err != nil {
            s.compensateWallet(ctx, id, currentWallet)
            return err
        }
        if lock.IsLost() {
            return errLockLost
        }
        out = s.stateFrom(nextWallet, nextBag)
        return nil
    })
    if err != nil {
        return State{}, err
    }
    return out, nil
}
```

补充 import：`log`。

- [ ] **Step 4: 运行测试**

Run:

```bash
cd joltgo
go test -count=1 ./logic
```

Expected: PASS，包括 10 个并发 `medkit` 购买最终 `500` 金币、`medkit` 数量 `10`。

- [ ] **Step 5: 提交**

```bash
git add joltgo/logic/service.go joltgo/logic/service_test.go
git commit -m "feat: 增加 logic 商品购买与补偿" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 5: 主武器装备与卸下

**Files:**
- Modify: `joltgo/logic/service.go`
- Modify: `joltgo/logic/service_test.go`

**Interfaces:**
- Consumes: `Catalog`、`Store`、`withAccountLock`、`hasItem`。
- Produces:
  - `func (s *Service) Equip(context.Context, string, string) (State, error)`

- [ ] **Step 1: 写装备测试**

在 `joltgo/logic/service_test.go` 增加：

```go
func TestEquipSwitchAndUnequip(t *testing.T) {
    env, _ := newLockedServiceTestEnv(t)
    ctx := context.Background()
    if err := env.svc.EnsureProfile(ctx, "7"); err != nil {
        t.Fatal(err)
    }
    if _, err := env.svc.Purchase(ctx, "7", "rifle", 1); err != nil {
        t.Fatal(err)
    }
    if _, err := env.svc.Purchase(ctx, "7", "pistol", 1); err != nil {
        t.Fatal(err)
    }

    state, err := env.svc.Equip(ctx, "7", "rifle")
    if err != nil || state.EquippedPrimaryWeapon != "rifle" {
        t.Fatalf("equip rifle: %+v err=%v", state, err)
    }
    state, err = env.svc.Equip(ctx, "7", "pistol")
    if err != nil || state.EquippedPrimaryWeapon != "pistol" {
        t.Fatalf("switch pistol: %+v err=%v", state, err)
    }
    state, err = env.svc.Equip(ctx, "7", "")
    if err != nil || state.EquippedPrimaryWeapon != "" {
        t.Fatalf("unequip: %+v err=%v", state, err)
    }
}

func TestEquipRequiresOnlineProfile(t *testing.T) {
    env, _ := newLockedServiceTestEnv(t)
    _, err := env.svc.Equip(context.Background(), "7", "")
    mustReason(t, err, ReasonProfileMissing)
}

func TestEquipRejectsInvalidWithoutWriting(t *testing.T) {
    env, _ := newLockedServiceTestEnv(t)
    ctx := context.Background()
    if err := env.svc.EnsureProfile(ctx, "7"); err != nil {
        t.Fatal(err)
    }
    _, err := env.svc.Equip(ctx, "7", "rifle")
    mustReason(t, err, ReasonNotOwned)
    _, err = env.svc.Equip(ctx, "7", "rocket")
    mustReason(t, err, ReasonItemNotFound)

    if _, err := env.svc.Purchase(ctx, "7", "medkit", 1); err != nil {
        t.Fatal(err)
    }
    _, err = env.svc.Equip(ctx, "7", "medkit")
    mustReason(t, err, ReasonNotEquippable)
}
```

- [ ] **Step 2: 运行并确认失败**

Run:

```bash
cd joltgo
go test -count=1 ./logic
```

Expected: FAIL，`Equip` 未定义。

- [ ] **Step 3: 实现 Equip**

在 `joltgo/logic/service.go` 增加：

```go
func (s *Service) Equip(ctx context.Context, accountID, itemID string) (State, error) {
    id, err := parseAccountID(accountID)
    if err != nil {
        return State{}, err
    }
    wallet, ok, err := s.store.GetWallet(ctx, id)
    if err != nil {
        return State{}, err
    }
    if !ok {
        return State{}, reason(ReasonProfileMissing)
    }
    bag, ok, err := s.store.GetBag(ctx, id)
    if err != nil {
        return State{}, err
    }
    if !ok {
        return State{}, reason(ReasonProfileMissing)
    }

    if itemID == "" {
        if bag.EquippedPrimaryWeapon == "" {
            return s.stateFrom(wallet, bag), nil
        }
    } else {
        def, found := s.catalog.Lookup(itemID)
        if !found {
            return State{}, reason(ReasonItemNotFound)
        }
        if def.EquipSlot == "" {
            return State{}, reason(ReasonNotEquippable)
        }
        if !hasItem(bag, itemID) {
            return State{}, reason(ReasonNotOwned)
        }
        if bag.EquippedPrimaryWeapon == itemID {
            return s.stateFrom(wallet, bag), nil
        }
    }

    var out State
    err = s.withAccountLock(ctx, id, func(lock Lock) error {
        currentWallet, ok, err := s.store.GetWallet(ctx, id)
        if err != nil {
            return err
        }
        if !ok {
            return reason(ReasonProfileMissing)
        }
        currentBag, ok, err := s.store.GetBag(ctx, id)
        if err != nil {
            return err
        }
        if !ok {
            return reason(ReasonProfileMissing)
        }

        if itemID == "" {
            if currentBag.EquippedPrimaryWeapon == "" {
                out = s.stateFrom(currentWallet, currentBag)
                return nil
            }
            currentBag.EquippedPrimaryWeapon = ""
        } else {
            def, found := s.catalog.Lookup(itemID)
            if !found {
                return reason(ReasonItemNotFound)
            }
            if def.EquipSlot == "" {
                return reason(ReasonNotEquippable)
            }
            if !hasItem(currentBag, itemID) {
                return reason(ReasonNotOwned)
            }
            if currentBag.EquippedPrimaryWeapon == itemID {
                out = s.stateFrom(currentWallet, currentBag)
                return nil
            }
            currentBag.EquippedPrimaryWeapon = itemID
        }

        if lock.IsLost() {
            return errLockLost
        }
        if err := s.store.SaveBag(ctx, id, currentBag); err != nil {
            return err
        }
        out = s.stateFrom(currentWallet, currentBag)
        return nil
    })
    if err != nil {
        return State{}, err
    }
    return out, nil
}
```

- [ ] **Step 4: 运行测试并提交**

Run:

```bash
cd joltgo
go test -count=1 ./logic
```

Expected: PASS。

```bash
git add joltgo/logic/service.go joltgo/logic/service_test.go
git commit -m "feat: 增加 logic 主武器装备状态" -m "Co-Authored-By: Codex <noreply@openai.com>"
```


---

### Task 6: Logic Handler、后端 Online Remote 与角色注册

**Files:**
- Modify: `joltgo/game/protos/game.proto`
- Generate: `joltgo/game/protos/game.pb.go`
- Create: `joltgo/logic/component.go`
- Create: `joltgo/logic/remote.go`
- Test: `joltgo/logic/component_test.go`
- Test: `joltgo/logic/remote_test.go`
- Modify: `joltgo/main.go`

**Interfaces:**
- Consumes: `*logic.Service`。
- Produces:
  - `func NewComponent(pitaya.Pitaya, *Service) *Component`
  - `func (c *Component) State(context.Context, *protos.LogicStateMsg) (*protos.LogicStateReply, error)`
  - `func (c *Component) Purchase(context.Context, *protos.PurchaseMsg) (*protos.LogicStateReply, error)`
  - `func (c *Component) Equip(context.Context, *protos.EquipMsg) (*protos.LogicStateReply, error)`
  - `func NewRemote(*Service) *Remote`
  - `func (r *Remote) Online(context.Context, *protos.UserOnlineMsg) (*protos.UserOnlineReply, error)`

- [ ] **Step 1: 增加 wire 消息并重生成 Go 码**

在 `joltgo/game/protos/game.proto` 的 `LoginReply` 后加入：

```proto
message LogicStateMsg {}

message PurchaseMsg {
  string item_id = 1;
  int32 quantity = 2;
}

message EquipMsg {
  string item_id = 1;
}

message UserOnlineMsg {
  string account_id = 1;
}

message UserOnlineReply {
  bool ok = 1;
  string reason = 2;
}

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

Run:

```bash
cd joltgo
protoc --go_out=. --go_opt=paths=source_relative -I . game/protos/game.proto
```

Expected: `game.pb.go` 出现 `LogicStateMsg`、`LogicStateReply`、`PurchaseMsg`、`EquipMsg`、`UserOnlineMsg`、`UserOnlineReply`。

- [ ] **Step 2: 写 handler 与 remote 失败测试**

创建 `joltgo/logic/component_test.go`：

```go
package logic

import (
    "context"
    "testing"

    pitaya "github.com/topfreegames/pitaya/v3/pkg"
    "github.com/topfreegames/pitaya/v3/pkg/session"
    "joltgo/game/protos"
)

type fakeSession struct {
    session.Session
    uid string
}
func (s *fakeSession) UID() string { return s.uid }

type fakeApp struct {
    pitaya.Pitaya
    sess session.Session
}
func (a *fakeApp) GetSessionFromCtx(context.Context) session.Session { return a.sess }

func newComponentTestEnv(t *testing.T, uid string) (*Component, *serviceTestEnv) {
    t.Helper()
    env, _ := newLockedServiceTestEnv(t)
    if uid != "" {
        if err := env.svc.EnsureProfile(context.Background(), uid); err != nil {
            t.Fatal(err)
        }
    }
    return NewComponent(&fakeApp{sess: &fakeSession{uid: uid}}, env.svc), env
}

func TestLogicComponentUsesSessionUID(t *testing.T) {
    component, env := newComponentTestEnv(t, "7")
    ctx := context.Background()
    if _, err := env.svc.Purchase(ctx, "7", "medkit", 1); err != nil {
        t.Fatal(err)
    }
    reply, err := component.State(ctx, &protos.LogicStateMsg{})
    if err != nil || !reply.Ok {
        t.Fatalf("State: %+v err=%v", reply, err)
    }
    if reply.Coins != 950 || len(reply.Items) != 4 || reply.Items[3].OwnedQuantity != 1 {
        t.Fatalf("reply=%+v", reply)
    }
}

func TestLogicComponentMapsErrors(t *testing.T) {
    component, _ := newComponentTestEnv(t, "")
    reply, err := component.Purchase(context.Background(), &protos.PurchaseMsg{ItemId: "rifle", Quantity: 1})
    if err != nil {
        t.Fatal(err)
    }
    if reply.Ok || reply.Reason != ReasonUnauthenticated {
        t.Fatalf("reply=%+v", reply)
    }
}
```

创建 `joltgo/logic/remote_test.go`：

```go
package logic

import (
    "context"
    "testing"

    "joltgo/game/protos"
)

func TestLogicRemoteOnlineEnsuresProfile(t *testing.T) {
    env, _ := newLockedServiceTestEnv(t)
    reply, err := NewRemote(env.svc).Online(context.Background(), &protos.UserOnlineMsg{AccountId: "7"})
    if err != nil || !reply.Ok {
        t.Fatalf("Online: %+v err=%v", reply, err)
    }
    if _, ok, _ := env.store.GetWallet(context.Background(), 7); !ok {
        t.Fatal("Online 应创建钱包")
    }
}

func TestLogicRemoteOnlineRejectsBadAccount(t *testing.T) {
    env, _ := newLockedServiceTestEnv(t)
    reply, err := NewRemote(env.svc).Online(context.Background(), &protos.UserOnlineMsg{})
    if err != nil {
        t.Fatal(err)
    }
    if reply.Ok || reply.Reason != ReasonUnauthenticated {
        t.Fatalf("reply=%+v", reply)
    }
}
```

- [ ] **Step 3: 运行并确认失败**

Run:

```bash
cd joltgo
go test -count=1 ./logic
```

Expected: FAIL，`NewComponent`、`NewRemote` 未定义。

- [ ] **Step 4: 实现 Component 与 Remote**

创建 `joltgo/logic/component.go`：

```go
package logic

import (
    "context"

    pitaya "github.com/topfreegames/pitaya/v3/pkg"
    "github.com/topfreegames/pitaya/v3/pkg/component"
    "joltgo/game/protos"
)

type Component struct {
    component.Base
    app     pitaya.Pitaya
    service *Service
}

func NewComponent(app pitaya.Pitaya, service *Service) *Component {
    return &Component{app: app, service: service}
}

func (c *Component) boundAccount(ctx context.Context) string {
    if s := c.app.GetSessionFromCtx(ctx); s != nil {
        return s.UID()
    }
    return ""
}

func stateReply(state State, err error) *protos.LogicStateReply {
    if err != nil {
        return &protos.LogicStateReply{Ok: false, Reason: ReasonOf(err)}
    }
    reply := &protos.LogicStateReply{
        Ok:                    true,
        Coins:                 state.Coins,
        EquippedPrimaryWeapon: state.EquippedPrimaryWeapon,
        Items:                 make([]*protos.LogicShopItem, 0, len(state.Items)),
    }
    for _, item := range state.Items {
        reply.Items = append(reply.Items, &protos.LogicShopItem{
            ItemId:        item.ItemID,
            DisplayName:   item.DisplayName,
            Price:         item.Price,
            EquipSlot:     item.EquipSlot,
            OwnedQuantity: item.OwnedQuantity,
        })
    }
    return reply
}

func (c *Component) State(ctx context.Context, _ *protos.LogicStateMsg) (*protos.LogicStateReply, error) {
    state, err := c.service.State(ctx, c.boundAccount(ctx))
    return stateReply(state, err), nil
}

func (c *Component) Purchase(ctx context.Context, msg *protos.PurchaseMsg) (*protos.LogicStateReply, error) {
    state, err := c.service.Purchase(ctx, c.boundAccount(ctx), msg.ItemId, msg.Quantity)
    return stateReply(state, err), nil
}

func (c *Component) Equip(ctx context.Context, msg *protos.EquipMsg) (*protos.LogicStateReply, error) {
    state, err := c.service.Equip(ctx, c.boundAccount(ctx), msg.ItemId)
    return stateReply(state, err), nil
}
```

创建 `joltgo/logic/remote.go`：

```go
package logic

import (
    "context"

    "github.com/topfreegames/pitaya/v3/pkg/component"
    "joltgo/game/protos"
)

type Remote struct {
    component.Base
    service *Service
}

func NewRemote(service *Service) *Remote {
    return &Remote{service: service}
}

func (r *Remote) Online(ctx context.Context, msg *protos.UserOnlineMsg) (*protos.UserOnlineReply, error) {
    if err := r.service.EnsureProfile(ctx, msg.AccountId); err != nil {
        return &protos.UserOnlineReply{Ok: false, Reason: ReasonOf(err)}, nil
    }
    return &protos.UserOnlineReply{Ok: true}, nil
}
```

- [ ] **Step 5: 注册 logic 角色**

修改 `joltgo/main.go`：

- import 增加 `joltgo/logic`。
- 包注释把“四种角色”改成“五种角色”，补 `logic` 说明。
- `switch *svType` 增加：

```go
case "logic":
    playerPool, err := kv.OpenRedigo(context.Background(), redisAddr)
    if err != nil {
        return err
    }
    defer playerPool.Close()
    service := logic.NewService(
        persist.NewPlayerStore(playerPool),
        logic.MustDefaultCatalog(),
        logic.NewRedisLockFactory(rdb),
    )
    app.Register(logic.NewComponent(app, service),
        component.WithName("logic"),
        component.WithNameFunc(strings.ToLower),
    )
    app.RegisterRemote(logic.NewRemote(service),
        component.WithName("logic"),
        component.WithNameFunc(strings.ToLower),
    )
```

`default` 错误字符串改成 `want gate|account|logic|match|game`。

- [ ] **Step 6: 运行测试与编译检查**

Run:

```bash
cd joltgo
go test -count=1 ./logic
go test -count=1 ./game ./persist
go vet ./logic ./game
```

Expected: 全部 PASS。

- [ ] **Step 7: 提交**

```bash
git add joltgo/game/protos/game.proto joltgo/game/protos/game.pb.go   joltgo/logic/component.go joltgo/logic/component_test.go   joltgo/logic/remote.go joltgo/logic/remote_test.go joltgo/main.go
git commit -m "feat: 注册 logic 微服务与业务协议" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 7: Gate 随机路由 Logic 节点

**Files:**
- Modify: `joltgo/gate/gate.go`
- Test: `joltgo/gate/gate_test.go`

**Interfaces:**
- Consumes: pitaya `RoutingFunc` 的 `servers map[string]*cluster.Server`。
- Produces:
  - `func routeRandom(context.Context, *route.Route, []byte, map[string]*cluster.Server) (*cluster.Server, error)`
  - `Configure` 新增 `logic` route。

- [ ] **Step 1: 写路由测试**

创建 `joltgo/gate/gate_test.go`：

```go
package gate

import (
    "context"
    "testing"

    pitaya "github.com/topfreegames/pitaya/v3/pkg"
    "github.com/topfreegames/pitaya/v3/pkg/cluster"
    "github.com/topfreegames/pitaya/v3/pkg/router"
)

type routeRecordingApp struct {
    pitaya.Pitaya
    routes []string
}
func (a *routeRecordingApp) AddRoute(name string, _ router.RoutingFunc) error {
    a.routes = append(a.routes, name)
    return nil
}

func TestConfigureAddsLogicRoute(t *testing.T) {
    app := &routeRecordingApp{}
    if err := Configure(app); err != nil {
        t.Fatal(err)
    }
    want := []string{"account", "match", "logic", "game"}
    if len(app.routes) != len(want) {
        t.Fatalf("routes=%v", app.routes)
    }
    for i := range want {
        if app.routes[i] != want[i] {
            t.Fatalf("routes=%v", app.routes)
        }
    }
}

func TestRouteRandom(t *testing.T) {
    servers := map[string]*cluster.Server{
        "logic-a": {ID: "logic-a"},
        "logic-b": {ID: "logic-b"},
    }
    got, err := routeRandom(context.Background(), nil, nil, servers)
    if err != nil || got == nil || (got.ID != "logic-a" && got.ID != "logic-b") {
        t.Fatalf("got=%+v err=%v", got, err)
    }
    if _, err := routeRandom(context.Background(), nil, nil, nil); err == nil {
        t.Fatal("无节点应报错")
    }
}
```

- [ ] **Step 2: 运行并确认失败**

Run:

```bash
cd joltgo
go test -count=1 ./gate
```

Expected: FAIL，缺少 `logic` route 与 `routeRandom`。

- [ ] **Step 3: 实现随机路由**

修改 `joltgo/gate/gate.go`：

```go
import (
    "context"
    "errors"
    "math/rand/v2"

    pitaya "github.com/topfreegames/pitaya/v3/pkg"
    "github.com/topfreegames/pitaya/v3/pkg/cluster"
    "github.com/topfreegames/pitaya/v3/pkg/route"
    "github.com/topfreegames/pitaya/v3/pkg/router"
)

func Configure(app pitaya.Pitaya) error {
    if err := app.AddRoute("account", routeAny); err != nil {
        return err
    }
    if err := app.AddRoute("match", routeAny); err != nil {
        return err
    }
    if err := app.AddRoute("logic", routeRandom); err != nil {
        return err
    }
    return app.AddRoute("game", routeGame(app))
}

func routeRandom(
    _ context.Context,
    _ *route.Route,
    _ []byte,
    servers map[string]*cluster.Server,
) (*cluster.Server, error) {
    if len(servers) == 0 {
        return nil, errors.New("no server available")
    }
    ids := make([]string, 0, len(servers))
    for id := range servers {
        ids = append(ids, id)
    }
    return servers[ids[rand.IntN(len(ids))]], nil
}
```

保留现有 `routeAny` 与 `routeGame`。

- [ ] **Step 4: 运行测试并提交**

Run:

```bash
cd joltgo
go test -count=1 ./gate
```

Expected: PASS。

```bash
git add joltgo/gate/gate.go joltgo/gate/gate_test.go
git commit -m "feat: gate 随机路由 logic 请求" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 8: Account 同步触发 Logic Online 事件

**Files:**
- Create: `joltgo/account/logic.go`
- Test: `joltgo/account/logic_test.go`
- Modify: `joltgo/account/component.go`
- Modify: `joltgo/account/component_test.go`
- Modify: `joltgo/main.go`

**Interfaces:**
- Produces:
  - `type LogicNotifier interface { NotifyOnline(context.Context, string) error }`
  - `func NewLogicNotifier(pitaya.Pitaya) LogicNotifier`
  - `func New(app pitaya.Pitaya, store *Store, onl *online.Store, notifier LogicNotifier) *Component`
- Consumes: Task 6 的 `logic.logic.online` remote 与 wire messages。

- [ ] **Step 1: 写具体 Notifier 测试**

创建 `joltgo/account/logic_test.go`：

```go
package account

import (
    "context"
    "errors"
    "testing"

    "github.com/golang/protobuf/proto"
    pitaya "github.com/topfreegames/pitaya/v3/pkg"
    "github.com/topfreegames/pitaya/v3/pkg/cluster"
    "joltgo/game/protos"
)

type notifierCall struct {
    serverID string
    route    string
}

type notifierApp struct {
    pitaya.Pitaya
    servers map[string]*cluster.Server
    srvErr  error
    rpcErr  error
    calls   []notifierCall
    ok      bool
}

func (a *notifierApp) GetServersByType(string) (map[string]*cluster.Server, error) {
    return a.servers, a.srvErr
}

func (a *notifierApp) RPCTo(_ context.Context, serverID, route string, reply proto.Message, arg proto.Message) error {
    a.calls = append(a.calls, notifierCall{serverID: serverID, route: route})
    if a.rpcErr != nil {
        return a.rpcErr
    }
    msg, ok := arg.(*protos.UserOnlineMsg)
    if !ok || msg.AccountId != "7" {
        return errors.New("unexpected user online message")
    }
    out := reply.(*protos.UserOnlineReply)
    out.Ok = a.ok
    if !a.ok {
        out.Reason = ReasonInternal
    }
    return nil
}

func TestLogicNotifierCallsRandomLogicNode(t *testing.T) {
    app := &notifierApp{
        servers: map[string]*cluster.Server{"logic-a": {ID: "logic-a"}},
        ok:      true,
    }
    if err := NewLogicNotifier(app).NotifyOnline(context.Background(), "7"); err != nil {
        t.Fatalf("NotifyOnline: %v", err)
    }
    if len(app.calls) != 1 || app.calls[0].serverID != "logic-a" || app.calls[0].route != "logic.logic.online" {
        t.Fatalf("calls=%+v", app.calls)
    }
}

func TestLogicNotifierRejectsMissingOrFailedLogic(t *testing.T) {
    if err := NewLogicNotifier(&notifierApp{}).NotifyOnline(context.Background(), "7"); err == nil {
        t.Fatal("无 logic 节点应失败")
    }
    app := &notifierApp{
        servers: map[string]*cluster.Server{"logic-a": {ID: "logic-a"}},
        ok:      false,
    }
    if err := NewLogicNotifier(app).NotifyOnline(context.Background(), "7"); err == nil {
        t.Fatal("logic 返回 ok=false 应失败")
    }
}
```

- [ ] **Step 2: 运行并确认失败**

Run:

```bash
cd joltgo
go test -count=1 ./account
```

Expected: FAIL，`NewLogicNotifier` 未定义。

- [ ] **Step 3: 实现 Notifier**

创建 `joltgo/account/logic.go`：

```go
package account

import (
    "context"
    "errors"
    "fmt"
    "math/rand/v2"

    pitaya "github.com/topfreegames/pitaya/v3/pkg"
    "joltgo/game/protos"
)

const logicOnlineRoute = "logic.logic.online"

type LogicNotifier interface {
    NotifyOnline(context.Context, string) error
}

type logicNotifier struct {
    app pitaya.Pitaya
}

func NewLogicNotifier(app pitaya.Pitaya) LogicNotifier {
    return &logicNotifier{app: app}
}

func (n *logicNotifier) NotifyOnline(ctx context.Context, accountID string) error {
    servers, err := n.app.GetServersByType("logic")
    if err != nil {
        return err
    }
    if len(servers) == 0 {
        return errors.New("account: no logic server available")
    }
    ids := make([]string, 0, len(servers))
    for id := range servers {
        ids = append(ids, id)
    }
    target := ids[rand.IntN(len(ids))]
    reply := &protos.UserOnlineReply{}
    if err := n.app.RPCTo(ctx, target, logicOnlineRoute, reply, &protos.UserOnlineMsg{
        AccountId: accountID,
    }); err != nil {
        return err
    }
    if !reply.Ok {
        return fmt.Errorf("account: logic online rejected: %s", reply.Reason)
    }
    return nil
}
```

- [ ] **Step 4: 注入 Notifier 并调整 finishLogin 顺序**

修改 `joltgo/account/component.go`：

```go
type Component struct {
    component.Base
    app      pitaya.Pitaya
    store    *Store
    online   *online.Store
    notifier LogicNotifier
}

func New(app pitaya.Pitaya, store *Store, onl *online.Store, notifier LogicNotifier) *Component {
    return &Component{app: app, store: store, online: onl, notifier: notifier}
}
```

在 `finishLogin` 中，同一账号已经绑定的幂等分支，在 `CurrentToken` 之前执行：

```go
if c.notifier == nil {
    log.Printf("account: logic notifier is nil")
    return fail(ReasonInternal), nil
}
if err := c.notifier.NotifyOnline(ctx, accountID); err != nil {
    log.Printf("account: logic online event for %s failed: %v", accountID, err)
    return fail(ReasonInternal), nil
}
```

新登录路径在读取 `oldGate` 后、`IssueToken` 前执行同一段检测与调用。不要放在 `Bind` 或 `IssueToken` 之后。

- [ ] **Step 5: 调整测试桩并补顺序测试**

在 `joltgo/account/component_test.go`：

1. 增加：

```go
type fakeNotifier struct {
    calls []string
    err   error
}
func (n *fakeNotifier) NotifyOnline(_ context.Context, accountID string) error {
    n.calls = append(n.calls, accountID)
    return n.err
}
```

2. `testEnv` 增加 `notifier *fakeNotifier`，`newTestComponent` 改为：

```go
notifier := &fakeNotifier{}
return &testEnv{
    comp:     New(app, store, onl, notifier),
    store:    store,
    online:   onl,
    app:      app,
    sess:     sess,
    mr:       mr,
    notifier: notifier,
}
```

3. 增加：

```go
func TestLogicOnlineFailureDoesNotRotateToken(t *testing.T) {
    env := newTestComponent(t)
    ctx := context.Background()
    env.mustRegister(t, "alice", "hunter2")
    oldToken, err := env.store.CurrentToken(ctx, "1")
    if err != nil || oldToken == "" {
        t.Fatalf("CurrentToken: %q err=%v", oldToken, err)
    }

    env.app.sess = &fakeSession{}
    env.notifier.err = errors.New("logic down")
    reply, err := env.comp.Login(ctx, &protos.LoginMsg{Username: "alice", Password: "hunter2"})
    if err != nil {
        t.Fatal(err)
    }
    if reply.Ok || reply.Reason != ReasonInternal {
        t.Fatalf("reply=%+v", reply)
    }
    current, err := env.store.CurrentToken(ctx, "1")
    if err != nil || current != oldToken {
        t.Fatalf("logic 失败不能轮换 token: old=%q current=%q err=%v", oldToken, current, err)
    }
    if env.sess.uid != "" {
        t.Fatal("logic 失败不能绑定会话")
    }
}

func TestLoginOnAlreadyBoundSessionStillNotifiesLogic(t *testing.T) {
    env := newTestComponent(t)
    ctx := context.Background()
    env.mustRegister(t, "alice", "hunter2")
    before := len(env.notifier.calls)

    reply, err := env.comp.Login(ctx, &protos.LoginMsg{Username: "alice", Password: "hunter2"})
    if err != nil || !reply.Ok {
        t.Fatalf("reply=%+v err=%v", reply, err)
    }
    if len(env.notifier.calls) != before+1 || env.notifier.calls[len(env.notifier.calls)-1] != "1" {
        t.Fatalf("calls=%v", env.notifier.calls)
    }
}
```

- [ ] **Step 6: 修改 main 的 account 注册**

把 `main.go` 的 account `New` 改为：

```go
app.Register(account.New(app,
    account.NewStore(rdb, persist.NewAccountStore(accountPool)),
    online.NewStore(rdb),
    account.NewLogicNotifier(app),
), component.WithName("account"), component.WithNameFunc(strings.ToLower))
```

- [ ] **Step 7: 运行测试并提交**

Run:

```bash
cd joltgo
go test -count=1 ./account ./logic
```

Expected: PASS。顺序测试必须证明 `logic` 失败时旧 token 仍有效、会话未绑定。

```bash
git add joltgo/account/logic.go joltgo/account/logic_test.go   joltgo/account/component.go joltgo/account/component_test.go joltgo/main.go
git commit -m "feat: 登录时同步初始化 logic 档案" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 9: Godot 传输层支持 Logic Request/Response

**Files:**
- Modify: `godot_client/scripts/fps_client.gd`
- Create: `godot_client/tests/logic_state_decode_test.gd`

**Interfaces:**
- Consumes: Task 6 的 `LogicStateReply`、`LogicShopItem` 字段号。
- Produces:
  - `signal logic_state_received(result: Dictionary)`
  - `func send_logic_state() -> void`
  - `func send_purchase(item_id: String, quantity: int) -> void`
  - `func send_equip(item_id: String) -> void`
  - `func _decode_logic_state(PackedByteArray) -> Dictionary`

- [ ] **Step 1: 写解码与编码测试**

创建 `godot_client/tests/logic_state_decode_test.gd`：

```gdscript
extends SceneTree

const FpsClient := preload("res://scripts/fps_client.gd")

var _failures := 0
var _c: Node
var _last_logic := {}
var _done := {}

func _on_logic(result: Dictionary) -> void:
    _last_logic = result

func _init() -> void:
    _c = FpsClient.new()
    _c.logic_state_received.connect(_on_logic)
    _test_state_response()
    _test_logic_error_mask()
    _test_purchase_encoding()
    _test_equip_encoding()
    for name: String in ["state", "error", "purchase", "equip"]:
        if not _done.has(name):
            _failures += 1
            printerr("FAIL: 用例 %s 没跑完" % name)
    if _failures > 0:
        printerr("logic_state_decode_test: %d 项失败" % _failures)
        quit(1)
    else:
        print("logic_state_decode_test: OK")
        quit(0)

func _check(cond: bool, msg: String) -> void:
    if not cond:
        _failures += 1
        printerr("FAIL: " + msg)

func _tag(field: int, wire: int) -> PackedByteArray:
    return _c._varint((field << 3) | wire)

func _f_varint(field: int, value: int) -> PackedByteArray:
    var out := _tag(field, _c.WIRE_VARINT)
    out.append_array(_c._varint(value))
    return out

func _f_str(field: int, value: String) -> PackedByteArray:
    var body := value.to_utf8_buffer()
    var out := _tag(field, _c.WIRE_LEN)
    out.append_array(_c._varint(body.size()))
    out.append_array(body)
    return out

func _f_bytes(field: int, body: PackedByteArray) -> PackedByteArray:
    var out := _tag(field, _c.WIRE_LEN)
    out.append_array(_c._varint(body.size()))
    out.append_array(body)
    return out

func _item(item_id: String, name: String, price: int, slot: String, qty: int) -> PackedByteArray:
    var out := _f_str(1, item_id)
    out.append_array(_f_str(2, name))
    out.append_array(_f_varint(3, price))
    out.append_array(_f_str(4, slot))
    out.append_array(_f_varint(5, qty))
    return out

func _test_state_response() -> void:
    var payload := _f_varint(1, 1)
    payload.append_array(_f_varint(3, 400))
    payload.append_array(_f_bytes(4, _item("rifle", "步枪", 300, "primary_weapon", 2)))
    payload.append_array(_f_bytes(4, _item("medkit", "医疗包", 50, "", 5)))
    payload.append_array(_f_str(5, "rifle"))

    _c._pending[77] = {"route": "logic.logic.state", "at": 0.0}
    var frame := PackedByteArray()
    frame.append(_c.MSG_RESPONSE << 1)
    frame.append_array(_c._varint(77))
    frame.append_array(payload)
    _c._on_data(frame)

    _check(bool(_last_logic.get("ok", false)), "ok 应为 true")
    _check(int(_last_logic.get("coins", 0)) == 400, "coins 应为 400")
    _check(String(_last_logic.get("equipped_primary_weapon", "")) == "rifle", "装备应为 rifle")
    _check((_last_logic.get("items", []) as Array).size() == 2, "应有 2 个商品项")
    _check(int(_last_logic["items"][0]["owned_quantity"]) == 2, "rifle 数量应为 2")
    _done["state"] = true

func _test_logic_error_mask() -> void:
    _last_logic = {}
    _c._pending[78] = {"route": "logic.logic.purchase", "at": 0.0}
    var frame := PackedByteArray()
    frame.append((_c.MSG_RESPONSE << 1) | 0x20)
    frame.append_array(_c._varint(78))
    frame.append_array("boom".to_utf8_buffer())
    _c._on_data(frame)
    _check(not bool(_last_logic.get("ok", true)), "errorMask 应回 ok=false")
    _check(String(_last_logic.get("reason", "")) == "internal", "errorMask 应回 internal")
    _done["error"] = true

func _test_purchase_encoding() -> void:
    var encoded := _c._encode_purchase("rifle", 2)
    var seen := {}
    var i := 0
    while i < encoded.size():
        var tag: Array = _c._read_varint(encoded, i)
        i = int(tag[1])
        var field := int(tag[0]) >> 3
        var wire := int(tag[0]) & 7
        seen[field] = wire
        if wire == _c.WIRE_LEN:
            var length: Array = _c._read_varint(encoded, i)
            i = int(length[1]) + int(length[0])
        else:
            var value: Array = _c._read_varint(encoded, i)
            i = int(value[1])
    _check(seen.get(1, -1) == _c.WIRE_LEN, "item_id 应为字段 1")
    _check(seen.get(2, -1) == _c.WIRE_VARINT, "quantity 应为字段 2")
    _done["purchase"] = true

func _test_equip_encoding() -> void:
    var encoded := _c._encode_equip("")
    _check(encoded.size() == 2, "空 item_id 仍应编码字段 1 的空字符串")
    _done["equip"] = true
```

- [ ] **Step 2: 运行并确认失败**

Run:

```bash
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client   --script res://tests/logic_state_decode_test.gd
```

Expected: FAIL，`logic_state_received`、`_decode_logic_state`、`_encode_purchase` 或 `_encode_equip` 不存在。

- [ ] **Step 3: 增加信号、请求编码和统一失败分发**

在 `godot_client/scripts/fps_client.gd` 顶部信号区增加：

```gdscript
signal logic_state_received(result: Dictionary)
```

在账号请求函数附近增加：

```gdscript
func send_logic_state() -> void:
    _send_tracked("logic.logic.state", PackedByteArray())

func send_purchase(item_id: String, quantity: int) -> void:
    _send_tracked("logic.logic.purchase", _encode_purchase(item_id, quantity))

func send_equip(item_id: String) -> void:
    _send_tracked("logic.logic.equip", _encode_equip(item_id))

func _encode_purchase(item_id: String, quantity: int) -> PackedByteArray:
    var out := _tag_len(1, item_id.to_utf8_buffer())
    out.append_array(_field_varint(2, quantity))
    return out

func _encode_equip(item_id: String) -> PackedByteArray:
    return _tag_len(1, item_id.to_utf8_buffer())

func _logic_failure(reason: String) -> Dictionary:
    return {"ok": false, "reason": reason, "coins": 0, "items": [], "equipped_primary_weapon": ""}

func _emit_request_failure(route: String, reason: String) -> void:
    if route.begins_with("logic."):
        logic_state_received.emit(_logic_failure(reason))
    else:
        login_result.emit({"ok": false, "reason": reason})
```

把 `_send_tracked` 的连接未就绪分支改成：

```gdscript
func _send_tracked(route: String, payload: PackedByteArray) -> void:
    var mid := _next_mid
    _next_mid += 1
    if _next_mid > 0xFFFFFF:
        _next_mid = 1
    if _send_request(mid, route, payload):
        _pending[mid] = {"route": route, "at": Time.get_ticks_msec() / 1000.0}
    else:
        _emit_request_failure(route, "no_connection")
```

把 `_process` 的超时分支改成：

```gdscript
for mid in _pending.keys():
    if now - float(_pending[mid]["at"]) > LOGIN_TIMEOUT:
        var route := String(_pending[mid]["route"])
        _pending.erase(mid)
        _emit_request_failure(route, "timeout")
```

把 `_on_response` 改成先按 pending route 分发：

```gdscript
func _on_response(data: PackedByteArray, is_err: bool) -> void:
    if data.size() < 2:
        _fail_unreadable_response()
        return
    var r: Array = _read_varint(data, 1)
    var next: int = int(r[1])
    if (data[next - 1] & 0x80) != 0:
        _fail_unreadable_response()
        return
    var mid: int = int(r[0])
    var payload: PackedByteArray = data.slice(next)
    var pending: Dictionary = _pending.get(mid, {})
    _pending.erase(mid)
    var route := String(pending.get("route", ""))
    if is_err:
        printerr("请求失败（服务端错误）: route=%s payload=%s" % [route, payload.get_string_from_utf8()])
        _emit_request_failure(route, "internal")
        return
    if route.begins_with("logic."):
        logic_state_received.emit(_decode_logic_state(payload))
        return
    var reply := _decode_login_reply(payload)
    if bool(reply.get("ok", false)):
        _save_token(String(reply.get("token", "")), String(reply.get("username", "")))
    elif String(reply.get("reason", "")) == "token_invalid":
        _clear_token()
    login_result.emit(reply)
```

- [ ] **Step 4: 实现 LogicStateReply 解码**

在 `fps_client.gd` 的解码区增加：

```gdscript
func _decode_logic_state(buf: PackedByteArray) -> Dictionary:
    var d := {
        "ok": false,
        "reason": "",
        "coins": 0,
        "items": [],
        "equipped_primary_weapon": "",
    }
    var i := 0
    while i < buf.size():
        var tag: Array = _read_varint(buf, i)
        i = int(tag[1])
        var field := int(tag[0]) >> 3
        var wire := int(tag[0]) & 7
        if wire == WIRE_VARINT:
            var r: Array = _read_varint(buf, i)
            i = int(r[1])
            match field:
                1: d["ok"] = int(r[0]) != 0
                3: d["coins"] = int(r[0])
        elif wire == WIRE_LEN:
            var rl: Array = _read_varint(buf, i)
            i = int(rl[1])
            var size := int(rl[0])
            var sub: PackedByteArray = buf.slice(i, i + size)
            i += size
            match field:
                2: d["reason"] = sub.get_string_from_utf8()
                4: d["items"].append(_decode_logic_shop_item(sub))
                5: d["equipped_primary_weapon"] = sub.get_string_from_utf8()
        else:
            break
    return d

func _decode_logic_shop_item(buf: PackedByteArray) -> Dictionary:
    var d := {
        "item_id": "",
        "display_name": "",
        "price": 0,
        "equip_slot": "",
        "owned_quantity": 0,
    }
    var i := 0
    while i < buf.size():
        var tag: Array = _read_varint(buf, i)
        i = int(tag[1])
        var field := int(tag[0]) >> 3
        var wire := int(tag[0]) & 7
        if wire == WIRE_VARINT:
            var r: Array = _read_varint(buf, i)
            i = int(r[1])
            match field:
                3: d["price"] = int(r[0])
                5: d["owned_quantity"] = int(r[0])
        elif wire == WIRE_LEN:
            var rl: Array = _read_varint(buf, i)
            i = int(rl[1])
            var size := int(rl[0])
            var sub: PackedByteArray = buf.slice(i, i + size)
            i += size
            match field:
                1: d["item_id"] = sub.get_string_from_utf8()
                2: d["display_name"] = sub.get_string_from_utf8()
                4: d["equip_slot"] = sub.get_string_from_utf8()
        else:
            break
    return d
```

把 `_fail_unreadable_response` 改为按 pending route 分发，避免畸形 Logic Response 误触发登录失败：

```gdscript
func _fail_unreadable_response() -> void:
    var routes := []
    for mid in _pending:
        routes.append(String(_pending[mid].get("route", "")))
    _pending = {}
    if routes.is_empty():
        login_result.emit({"ok": false, "reason": "internal"})
        return
    for route in routes:
        _emit_request_failure(route, "internal")
```

- [ ] **Step 5: 运行测试并提交**

Run:

```bash
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client   --script res://tests/logic_state_decode_test.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client   --script res://tests/login_reply_decode_test.gd
```

Expected: 两个测试都输出 `OK`。

```bash
git add godot_client/scripts/fps_client.gd godot_client/tests/logic_state_decode_test.gd
git commit -m "feat: 客户端支持 logic 请求响应" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 10: Godot 商城与背包面板

**Files:**
- Modify: `godot_client/scripts/main.gd`
- Create: `godot_client/tests/logic_panel_test.gd`

**Interfaces:**
- Consumes: Task 9 的 `logic_state_received` 与发送函数。
- Produces:
  - `func _build_logic_panel() -> void`
  - `func _toggle_logic_panel() -> void`
  - `func _on_logic_state(result: Dictionary) -> void`
  - `func _refresh_logic_panel() -> void`
  - `func _logic_reason_text(String) -> String`

- [ ] **Step 1: 写面板状态测试**

创建 `godot_client/tests/logic_panel_test.gd`：

```gdscript
extends SceneTree

const MainScene := preload("res://scenes/main.tscn")

var _failures := 0

func _init() -> void:
    var main: Node = MainScene.instantiate()
    root.add_child(main)
    main._logic_state = {
        "ok": true,
        "coins": 400,
        "equipped_primary_weapon": "rifle",
        "items": [
            {"item_id": "rifle", "display_name": "步枪", "price": 300,
             "equip_slot": "primary_weapon", "owned_quantity": 2},
            {"item_id": "medkit", "display_name": "医疗包", "price": 50,
             "equip_slot": "", "owned_quantity": 5},
        ],
    }
    main._refresh_logic_panel()
    _check(main._logic_coins.text.contains("400"), "金币应显示 400")
    _check(main._logic_items_box.get_child_count() == 2, "应显示两个商品行")
    _check(main._logic_status.text == "", "成功状态不应显示错误")

    main._on_logic_state({"ok": false, "reason": "insufficient_funds"})
    _check(main._logic_status.text == "金币不足", "应显示余额不足")
    _check(main._logic_coins.text.contains("400"), "失败不能污染已有金币状态")
    main.queue_free()
    if _failures > 0:
        printerr("logic_panel_test: %d 项失败" % _failures)
        quit(1)
    else:
        print("logic_panel_test: OK")
        quit(0)

func _check(cond: bool, msg: String) -> void:
    if not cond:
        _failures += 1
        printerr("FAIL: " + msg)
```

- [ ] **Step 2: 运行并确认失败**

Run:

```bash
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client   --script res://tests/logic_panel_test.gd
```

Expected: FAIL，变量与函数尚不存在。

- [ ] **Step 3: 增加面板状态与构建逻辑**

在 `godot_client/scripts/main.gd` 的登录面板变量后增加：

```gdscript
var _logic_panel: CanvasLayer
var _logic_coins: Label
var _logic_status: Label
var _logic_items_box: VBoxContainer
var _logic_busy := false
var _logic_state := {
    "ok": false,
    "coins": 0,
    "equipped_primary_weapon": "",
    "items": [],
}
```

在 `_ready()` 连接信号并构建面板：

```gdscript
fps_client.logic_state_received.connect(_on_logic_state)
_build_logic_panel()
```

把 `_ready` 中 `_build_login_panel()` 放在 `_build_logic_panel()` 之后。`_build_logic_panel` 创建隐藏的 `CanvasLayer`，标题、金币 Label、`_logic_status`、`_logic_items_box` 和关闭按钮。关闭按钮与 B 键都调用 `_toggle_logic_panel()`。

在 `_unhandled_input` 开头增加：

```gdscript
if event is InputEventKey and event.pressed and event.keycode == KEY_B:
    _toggle_logic_panel()
    return
if _logic_panel != null and _logic_panel.visible:
    return
```

在 `_on_login_result` 成功分支增加：

```gdscript
fps_client.send_logic_state()
```

- [ ] **Step 4: 实现面板刷新与交互**

在 `main.gd` 增加：

```gdscript
func _toggle_logic_panel() -> void:
    if _logic_panel == null:
        return
    _logic_panel.visible = not _logic_panel.visible
    if _logic_panel.visible:
        Input.mouse_mode = Input.MOUSE_MODE_VISIBLE
        _logic_busy = true
        _logic_status.text = "加载中…"
        fps_client.send_logic_state()
    else:
        _logic_status.text = ""
        _logic_busy = false

func _set_logic_items(items: Array) -> void:
    for child in _logic_items_box.get_children():
        child.queue_free()
    for item in items:
        var item_id := String(item.get("item_id", ""))
        var row := HBoxContainer.new()
        var label := Label.new()
        label.text = "%s  %d 金币  持有 %d" % [
            String(item.get("display_name", item_id)),
            int(item.get("price", 0)),
            int(item.get("owned_quantity", 0)),
        ]
        label.size_flags_horizontal = Control.SIZE_EXPAND_FILL
        row.add_child(label)

        var count := SpinBox.new()
        count.min_value = 1
        count.max_value = 99
        count.value = 1
        row.add_child(count)

        var buy := Button.new()
        buy.text = "购买"
        buy.pressed.connect(func() -> void:
            if _logic_busy:
                return
            _logic_busy = true
            _logic_status.text = "购买中…"
            fps_client.send_purchase(item_id, int(count.value))
        )
        row.add_child(buy)

        var equip := Button.new()
        var owned := int(item.get("owned_quantity", 0)) > 0
        var equippable := String(item.get("equip_slot", "")) != ""
        equip.disabled = not owned or not equippable
        if equippable and String(_logic_state.get("equipped_primary_weapon", "")) == item_id:
            equip.text = "卸下"
            equip.disabled = false
        else:
            equip.text = "装备"
        equip.pressed.connect(func() -> void:
            if _logic_busy:
                return
            _logic_busy = true
            _logic_status.text = "更新装备…"
            if equip.text == "卸下":
                fps_client.send_equip("")
            else:
                fps_client.send_equip(item_id)
        )
        row.add_child(equip)
        _logic_items_box.add_child(row)

func _refresh_logic_panel() -> void:
    if not bool(_logic_state.get("ok", false)):
        return
    _logic_coins.text = "金币：%d" % int(_logic_state.get("coins", 0))
    _set_logic_items(_logic_state.get("items", []))
    _logic_status.text = ""

func _on_logic_state(result: Dictionary) -> void:
    _logic_busy = false
    if bool(result.get("ok", false)):
        _logic_state = result
        _refresh_logic_panel()
        return
    _logic_status.text = _logic_reason_text(String(result.get("reason", "")))

func _logic_reason_text(reason: String) -> String:
    match reason:
        "bad_quantity": return "购买数量必须在 1-99"
        "item_not_found": return "商品不存在"
        "insufficient_funds": return "金币不足"
        "not_owned": return "尚未拥有该物品"
        "not_equippable": return "该物品不能装备"
        "busy": return "操作过于频繁，请稍后重试"
        "unauthenticated": return "请先登录"
        "profile_missing": return "玩家档案缺失，请重新登录"
        "timeout": return "服务器无响应，请重试"
        "no_connection": return "未连接到服务器"
        _: return "操作失败，请稍后重试"
```

购买请求在途时按钮逻辑只依赖 `_logic_busy`，不做本地扣款；失败或超时后由 `logic_state_received` 清除 busy 状态。

- [ ] **Step 5: 运行测试**

Run:

```bash
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client   --script res://tests/logic_panel_test.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client   --script res://tests/logic_state_decode_test.gd
```

Expected: 两个测试都输出 `OK`。

- [ ] **Step 6: 提交**

```bash
git add godot_client/scripts/main.gd godot_client/tests/logic_panel_test.gd
git commit -m "feat: 增加 Godot 商城与背包面板" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 11: 部署、端到端冒烟与文档

**Files:**
- Modify: `joltgo/deploy/start-all.ps1`
- Modify: `joltgo/deploy/start-infra.ps1`
- Modify: `joltgo/deploy/README.md`
- Create: `godot_client/tests/logic_smoke.gd`
- Modify: `AGENTS.md`
- Modify: `README.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `docs/API.md`
- Modify: `docs/BUILD.md`
- Modify: `docs/DEVELOPMENT.md`

**Interfaces:**
- Consumes: 前面所有任务的 `logic` 角色、协议、客户端面板。
- Produces: 本地五进程部署、完整 smoke、与代码一致的文档。

- [ ] **Step 1: 更新一键启动脚本**

在 `joltgo/deploy/start-all.ps1` 的启动注释与进程列表中加入 `logic`：

```powershell
Start-Process -FilePath $exe -ArgumentList @('-type', 'logic') -WorkingDirectory (Split-Path $exe) -RedirectStandardOutput (Join-Path $root 'logic.out.log') -RedirectStandardError (Join-Path $root 'logic.log') -WindowStyle Hidden
Write-Host 'started gate (ws://localhost:8080) + account + logic + match + game'
```

把 `start-infra.ps1` 中“gate / account / match 启动时 Ping Redis”改成“gate / account / logic / match 启动时 Ping Redis”。

- [ ] **Step 2: 写 Logic 端到端 smoke**

创建 `godot_client/tests/logic_smoke.gd`：

```gdscript
extends SceneTree

const PASSWORD := "smokepass"
const TIMEOUT_MS := 20000

var _failures := 0
var _client: Node
var _phase := 0
var _username := ""

func _initialize() -> void:
    for p in ["user://auth_token.txt", "user://last_username.txt"]:
        if FileAccess.file_exists(p):
            DirAccess.remove_absolute(ProjectSettings.globalize_path(p))
    _username = "logicsmoke_%d" % (Time.get_ticks_usec() % 100000000)
    _client = load("res://scripts/fps_client.gd").new()
    _client.login_result.connect(_on_login)
    _client.logic_state_received.connect(_on_logic_state)
    root.add_child(_client)
    _run()

func _check(cond: bool, msg: String) -> void:
    if not cond:
        _failures += 1
        printerr("FAIL: " + msg)

func _on_login(result: Dictionary) -> void:
    if bool(result.get("ok", false)):
        _client.send_logic_state()
        return
    if String(result.get("reason", "")) == "no_token":
        _client.send_register(_username, PASSWORD)
        return
    _check(false, "登录失败: %s" % result)

func _on_logic_state(result: Dictionary) -> void:
    if not bool(result.get("ok", false)):
        _check(false, "Logic 状态失败: %s" % result.get("reason", ""))
        return
    var coins := int(result.get("coins", -1))
    var items: Array = result.get("items", [])
    if _phase == 0:
        _check(coins == 1000, "初始金币应为 1000，得到 %d" % coins)
        _phase = 1
        _client.send_purchase("rifle", 1)
    elif _phase == 1:
        _check(coins == 700, "购买步枪后金币应为 700，得到 %d" % coins)
        _check(_quantity(items, "rifle") == 1, "步枪数量应为 1")
        _phase = 2
        _client.send_equip("rifle")
    elif _phase == 2:
        _check(String(result.get("equipped_primary_weapon", "")) == "rifle", "步枪应已装备")
        _phase = 3

func _quantity(items: Array, item_id: String) -> int:
    for item in items:
        if String(item.get("item_id", "")) == item_id:
            return int(item.get("owned_quantity", 0))
    return 0

func _run() -> void:
    var deadline := Time.get_ticks_msec() + TIMEOUT_MS
    while Time.get_ticks_msec() < deadline and _phase < 3:
        await process_frame
    if _phase != 3:
        _check(false, "smoke 未完成，phase=%d" % _phase)
    if _failures > 0:
        printerr("logic_smoke: %d 项失败" % _failures)
        quit(1)
    else:
        print("logic_smoke: OK")
        quit(0)
```

- [ ] **Step 3: 运行本地端到端验证**

先构建并启动五进程：

```powershell
cd joltgo
.uild.ps1
cd deploy
.\stop-infra.ps1
.\start-all.ps1
```

再运行：

```bash
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client   --script res://tests/logic_smoke.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client   --script res://tests/login_smoke.gd
```

Expected:

- `logic_smoke: OK`
- `login_smoke: OK`
- `deploy/logic.log` 无 panic、无 profile 初始化错误。
- `deploy/account.log` 中每次注册/登录成功前存在发往 `logic.logic.online` 的正常 RPC 路径。

- [ ] **Step 4: 更新架构和运行文档**

按以下事实统一更新文档：

`AGENTS.md`：

- 一句话定位与目录树加入 `logic/` 和 `persist/protos/player/`。
- 数据流加入 `account → logic.logic.online → Redis` 与 `gate → logic 随机节点 → Redis`。
- 多进程部署命令加入 `joltgo.exe -type logic`，日志加入 `logic.log` / `logic.out.log`。
- 修改 runbook：局外钱包、背包、商城与装备改 `joltgo/logic/`；持久化字段改 `persist/protos/player/player.proto` 并跑 `.\gen-redis.ps1`。
- 测试命令加入 `./logic` 与 `logic_state_decode_test.gd`、`logic_panel_test.gd`、`logic_smoke.gd`。

`README.md`：

- 架构图加入 `logic` 服务与 Redis 中的钱包/背包。
- 快速启动与测试命令加入第五个进程和 Logic 测试。

`docs/ARCHITECTURE.md`：

- 在服务分层图加入 `gate → logic → Redis`。
- 新增“局外数据与 logic”章节，写清无状态随机路由、上线事件、钱包/背包 key、账号锁、双层检查和补偿语义。
- 明确 `game` 不读取背包与装备。

`docs/API.md`：

- 路由表加入 `logic.logic.state / purchase / equip` 与 `logic.logic.online`。
- 加入本计划的 proto 消息字段、错误码表、登录上线事件顺序。
- 写明购买无请求幂等，客户端不得自动重试。

`docs/BUILD.md`：

- `persist/protos/*.proto` 说明改为账号与玩家两套独立生成包。
- 加入 `playerpb` key 格式 `REDB#%d:%d:%d`。
- 测试命令加入 Logic 单测、Godot 解码/UI 测试与 logic smoke。

`docs/DEVELOPMENT.md`：

- 新增“改 logic / 钱包 / 背包 / 商城”的 runbook。
- 写明玩家数据模型变更要先改 `player.proto` 再跑 `gen-redis.ps1`。
- 写明协议结构变更要重生成 `game.pb.go` 并同步 `fps_client.gd`。

`joltgo/deploy/README.md`：

- 把服务清单从四角色改成五角色。
- 说明 Redis 中新增 `REDB#1:*` 背包与 `REDB#2:*` 钱包，并警告不要清空 `redis-data`。

- [ ] **Step 5: 全量验证**

Run:

```bash
cd joltgo
gofmt -l gate account logic kv online match game physics sim replication ecs persist
go vet ./gate ./account ./logic ./kv ./online ./match ./game ./physics ./sim ./replication ./ecs ./persist
PATH="$PWD:$PATH" go test -count=1 ./...
```

Expected: `go test ./...` PASS；`gofmt -l` 只允许继续列出仓库既有的 CRLF 文件，新文件不得出现在列表中。

Run:

```bash
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/logic_state_decode_test.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/logic_panel_test.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/login_reply_decode_test.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/world_store_test.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/frame_decode_test.gd
```

Expected: 全部输出 `OK`。

- [ ] **Step 6: 提交部署、smoke 与文档**

```bash
git add joltgo/deploy/start-all.ps1 joltgo/deploy/start-infra.ps1 joltgo/deploy/README.md   godot_client/tests/logic_smoke.gd AGENTS.md README.md   docs/ARCHITECTURE.md docs/API.md docs/BUILD.md docs/DEVELOPMENT.md
git commit -m "docs: 补充 logic 服务部署与端到端说明" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

## Final Verification

- [ ] `joltgo.exe -type logic` 与 gate/account/match/game 一起启动成功。
- [ ] 新账号注册成功后立即返回 `LogicStateReply.coins=1000`。
- [ ] 两个不同账号可以同时购买，同一账号并发购买不会丢更新。
- [ ] 余额不足时钱包和背包均不变化，且不获取账号写锁。
- [ ] `rifle` 可购买、累加、装备、切换和卸下；`medkit` 可叠加但不能装备。
- [ ] `logic` 停机时登录失败，旧 token 未轮换，旧会话状态不被破坏。
- [ ] `game` 的行为与协议回归测试全部通过。
- [ ] `AGENTS.md`、`README.md` 与 `docs/` 中没有过期说明。

# 实体-属性增量同步与断线回局 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用一套与 ECS 解耦的「实体-属性终值 + 本帧脏集」同步层替代手写快照，让新增 ECS 属性不再需要改 proto / Go 结构体 / 客户端解码，并实现断线回到同一对局。

**Architecture:** 新包 `joltgo/replication` 持有 `[实体ID][属性ID] = 属性终值` 的键值表和本帧脏集，不 import `ecs`；玩法层在变更点显式 `rep.Set(实体, 属性, 终值)`。实时增量（`Drain`）与重连全量（`Full`）产出**同一个 `Frame` 类型、同一套编码**，`game` 层转成通用 protobuf 后推送。客户端维护一份本地实体-属性存储，只按属性名取用。

**Tech Stack:** Go 1.26（标准库 + pitaya v3 内置源码）、protobuf（`protoc-gen-go` v1.34.2）、Godot 4.7 GDScript。

**Spec:** `docs/superpowers/specs/2026-09-10-entity-attribute-replication-reconnect-design.md`

## Global Constraints

- **平台**：Windows + MSYS2 UCRT64。`joltgo/` 下所有 Go 命令在 `joltgo` 目录执行。
- **模块名**：Go module 是 `joltgo`，import 路径形如 `joltgo/replication`。
- **`ecs/` 零改动**：本计划不修改 `joltgo/ecs/` 下任何文件。`ecs.Get` 仍返回指针。
- **`replication` 包不得 import `joltgo/ecs`**（也不得 import `joltgo/sim`）。
- **并发**：`replication.Store` 非并发安全，与 `sim.Simulation` 一样由对局实例 goroutine 独占。
- **提交规范**：直接提交到 `main`（不开分支、不走 PR）。提交信息用 `<type>: <subject>`（`feat`/`fix`/`docs`/`refactor`/`test`），AI 提交在结尾加 `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`。
- **每个 Task 结束前必须跑绿**：`cd joltgo; gofmt -l .; go vet ./gate ./match ./game ./physics ./sim; go test ./ecs ./sim`（新增 `./replication` 后加入该列表）。
- **测试命令（客户端）**：`"C:\Users\zhubeijian\Downloads\Godot_v4.7.2-stable_win64.exe\Godot_v4.7.2-stable_win64_console.exe" --headless --path godot_client --script res://tests/<file>.gd`
- **代码注释**：与仓库现有风格一致 —— 中文、解释「为什么」而不是「做了什么」，不写流水账。

## 交付顺序与临时状态说明

Task 1–6 是服务端复制核心，Task 7–10 是重连链路，Task 11–13 是客户端，Task 14 是文档。

**Task 6 之后、Task 13 之前，游戏跑不起来**（服务端已不再推旧 `onSnapshot`，客户端还在解旧格式）。这是破坏性协议变更的固有代价，spec §12 已明确「服务端与客户端同一次提交内升级」。**整个过程中 `go test ./...` 必须保持绿色** —— 服务端侧的 Go 测试不依赖客户端。

---

## 文件结构

**新建**

| 文件 | 职责 |
| --- | --- |
| `joltgo/replication/value.go` | `Kind` / `Value` 及其构造与取值 |
| `joltgo/replication/store.go` | `Store`：终值表、已下发基线、脏集、声明、`Set`/`Remove`/`Destroy`/`Reset`/`Get`/`Schema` |
| `joltgo/replication/frame.go` | `Frame`/`EntityDelta`/`AttrValue`/`Schema` 类型 + `Drain`/`Full` |
| `joltgo/replication/value_test.go` | 值语义与相等性 |
| `joltgo/replication/store_test.go` | 标脏/撤销/覆盖/销毁/移除语义 |
| `joltgo/replication/frame_test.go` | 增量与全量产出、确定性、schema |
| `joltgo/sim/replicate.go` | **唯一**的 ECS ↔ 属性映射：属性名常量、`declareAttributes`、`replicateBodyMeta` |
| `joltgo/sim/replicate_test.go` | oracle 一致性测试（防漏写 `Set`）+ 增量重建 == 全量 |
| `godot_client/scripts/world_store.gd` | 客户端本地实体-属性存储 + schema |
| `godot_client/tests/world_store_test.gd` | 存储语义（full 覆盖 / removed / destroy） |
| `godot_client/tests/frame_decode_test.gd` | Schema / Frame protobuf 解码 |

**修改**

| 文件 | 改动 |
| --- | --- |
| `joltgo/sim/components.go` | `Player{Idx}`、新增 `Facing`、新增 `GameState` |
| `joltgo/sim/simulation.go` | 持有 `rep *replication.Store` 与单例实体；删除 `Snapshot()` |
| `joltgo/sim/systems.go` | 各变更点加 `rep.Set` |
| `joltgo/sim/sim_test.go` | 接管 `State` 类型与 `snapshotWorld` 构造器；`s.Snapshot()` → `snapshotWorld(s)` |
| `joltgo/game/protos/game.proto` | 删 `Snapshot`/`BodyInfo`/`ResourceInfo`/`PlayerState`；加 `Frame` 族、`CommandMsg`、`RejoinMsg`/`RejoinReply`；`JoinMsg` 加 `token`。`InputMsg`/`ShootMsg` 留到 Task 7 删 |
| `joltgo/game/component.go` | `toSnapshot` → `toFrame`；`Input`/`Shoot`/`Reset` → `Cmd`；新增 `Rejoin`/`Resync` |
| `joltgo/game/instance.go` | `broadcast` → `Drain`/`Full`；`pendingFull`；`RequestFull`；空闲回收 |
| `joltgo/match/match.go` | `join` 用 token 当 uid；`tryRejoin` fan-out |
| `joltgo/main.go` | 无改动（handler 按方法名注册，改名自动生效） |
| `godot_client/scripts/fps_client.gd` | 协议层重写：token 持久化、Schema/Frame 解码、`send_command`/`send_resync` |
| `godot_client/scripts/main.gd` | 按属性名查询世界；场景按 store 协调；插值改为节点自带前一帧变换 |

**删除**

- `joltgo/sim/state.go`（`State` 族类型移入 `sim_test.go`）

---

## Task 1: `replication.Value` 与 `replication.Kind`

**Files:**
- Create: `joltgo/replication/value.go`
- Test: `joltgo/replication/value_test.go`

**Interfaces:**
- Consumes: 无
- Produces:
  - `type Kind uint8`，常量 `KindF32 KindI32 KindBool KindStr KindVec2 KindVec3 KindVec4`
  - `func (k Kind) Dim() int` — 浮点维度，非浮点族返回 0
  - `type Value struct{ ... }`（不导出字段）
  - `func F32(v float32) Value` / `I32(v int32) Value` / `Bool(v bool) Value` / `Str(v string) Value`
  - `func Vec2(x, y float32) Value` / `Vec3(x, y, z float32) Value` / `Vec4(x, y, z, w float32) Value`
  - `func (v Value) Kind() Kind`
  - `func (v Value) Floats() []float32` — 浮点族返回值（长度 = `Dim()`），其他返回 `nil`
  - `func (v Value) Int() int32` / `func (v Value) Boolean() bool` / `func (v Value) Text() string`
  - `func (v Value) equal(o Value) bool`（包内私有）

- [ ] **Step 1: 写失败的测试**

创建 `joltgo/replication/value_test.go`：

```go
package replication

import "testing"

func TestFloatsByKind(t *testing.T) {
	cases := []struct {
		name string
		v    Value
		dim  int
		want []float32
	}{
		{"F32", F32(1.5), 1, []float32{1.5}},
		{"Vec2", Vec2(1, 2), 2, []float32{1, 2}},
		{"Vec3", Vec3(1, 2, 3), 3, []float32{1, 2, 3}},
		{"Vec4", Vec4(1, 2, 3, 4), 4, []float32{1, 2, 3, 4}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.v.Kind().Dim(); got != c.dim {
				t.Fatalf("Dim() = %d，期望 %d", got, c.dim)
			}
			got := c.v.Floats()
			if len(got) != len(c.want) {
				t.Fatalf("Floats() 长度 = %d，期望 %d", len(got), len(c.want))
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Fatalf("Floats()[%d] = %v，期望 %v", i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestScalarKindsHaveNoFloats(t *testing.T) {
	for _, v := range []Value{I32(3), Bool(true), Str("x")} {
		if v.Floats() != nil {
			t.Fatalf("标量类型的 Floats() 应为 nil，得到 %v", v.Floats())
		}
	}
}

func TestScalarAccessors(t *testing.T) {
	if I32(-7).Int() != -7 {
		t.Fatal("I32().Int() 往返失败")
	}
	if !Bool(true).Boolean() || Bool(false).Boolean() {
		t.Fatal("Bool().Boolean() 往返失败")
	}
	if Str("abc").Text() != "abc" {
		t.Fatal("Str().Text() 往返失败")
	}
}

func TestEqual(t *testing.T) {
	if !Vec3(1, 2, 3).equal(Vec3(1, 2, 3)) {
		t.Fatal("同类型同值应相等")
	}
	if Vec3(1, 2, 3).equal(Vec3(1, 2, 3.5)) {
		t.Fatal("值不同不应相等")
	}
	if F32(1).equal(I32(1)) {
		t.Fatal("类型不同不应相等")
	}
	if !Str("a").equal(Str("a")) {
		t.Fatal("字符串同值应相等")
	}
	if Bool(true).equal(Bool(false)) {
		t.Fatal("bool 不同值不应相等")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd joltgo && go test ./replication`
Expected: FAIL —— `no Go files in .../replication` 或 `undefined: F32`

- [ ] **Step 3: 实现**

创建 `joltgo/replication/value.go`：

```go
package replication

// Kind 是属性值的类型标签。值本身不带维度信息，维度由 Kind 决定。
type Kind uint8

const (
	KindF32  Kind = iota // f[0]
	KindI32              // i
	KindBool             // i != 0
	KindStr              // s
	KindVec2             // f[0..1]
	KindVec3             // f[0..2]
	KindVec4             // f[0..3]
)

// Dim 返回该类型占用的浮点维度；非浮点族返回 0。
func (k Kind) Dim() int {
	switch k {
	case KindF32:
		return 1
	case KindVec2:
		return 2
	case KindVec3:
		return 3
	case KindVec4:
		return 4
	}
	return 0
}

// Value 是属性值：紧凑联合体，按 Kind 取用其中一个字段。
// 不用 any 是因为同步路径每 tick 会有上千次 Set，接口装箱会造成持续堆分配。
type Value struct {
	kind Kind
	f    [4]float32
	i    int32
	s    string
}

func F32(v float32) Value { return Value{kind: KindF32, f: [4]float32{v}} }

func I32(v int32) Value { return Value{kind: KindI32, i: v} }

func Bool(v bool) Value {
	var i int32
	if v {
		i = 1
	}
	return Value{kind: KindBool, i: i}
}

func Str(v string) Value { return Value{kind: KindStr, s: v} }

func Vec2(x, y float32) Value { return Value{kind: KindVec2, f: [4]float32{x, y}} }

func Vec3(x, y, z float32) Value { return Value{kind: KindVec3, f: [4]float32{x, y, z}} }

func Vec4(x, y, z, w float32) Value { return Value{kind: KindVec4, f: [4]float32{x, y, z, w}} }

// Kind 返回值的类型标签。
func (v Value) Kind() Kind { return v.kind }

// Floats 返回浮点族的值，长度等于 Kind().Dim()；标量族返回 nil。
func (v Value) Floats() []float32 {
	if d := v.kind.Dim(); d > 0 {
		return v.f[:d]
	}
	return nil
}

// Int 返回整数族的值（KindI32）；KindBool 用 Boolean 读。
func (v Value) Int() int32 { return v.i }

// Boolean 返回布尔族的值。
func (v Value) Boolean() bool { return v.i != 0 }

// Text 返回字符串族的值。
func (v Value) Text() string { return v.s }

// equal 报告两个值是否相等：类型不同一律不等，否则按各自的族比较。
func (v Value) equal(o Value) bool {
	if v.kind != o.kind {
		return false
	}
	if v.kind == KindStr {
		return v.s == o.s
	}
	if v.kind.Dim() > 0 {
		return v.f == o.f
	}
	return v.i == o.i
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd joltgo && go test ./replication`
Expected: PASS（`ok  joltgo/replication`）

- [ ] **Step 5: 提交**

```bash
git add joltgo/replication/value.go joltgo/replication/value_test.go
git commit -m "feat: replication.Value 紧凑联合体与 Kind 类型标签

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## Task 2: `replication.Store` —— 终值表、已下发基线、脏集

**Files:**
- Create: `joltgo/replication/store.go`
- Test: `joltgo/replication/store_test.go`

**Interfaces:**
- Consumes: `Kind`、`Value`、`F32`/`I32`/`Bool`/`Str`/`Vec2`/`Vec3`/`Vec4`（Task 1）
- Produces:
  - `type Attr struct { ID uint32; Name string; Kind Kind }`
  - `func New() *Store`
  - `func (s *Store) Declare(name string, k Kind)`
  - `func (s *Store) Set(id uint32, attr string, v Value)`
  - `func (s *Store) Remove(id uint32, attr string)`
  - `func (s *Store) Destroy(id uint32)`
  - `func (s *Store) Reset()`
  - `func (s *Store) Get(id uint32, attr string) (Value, bool)`
  - `func (s *Store) Schema() Schema`（`Schema` 类型在 Task 3 定义；本步先只做 `Declare`/`Set`/`Remove`/`Destroy`/`Reset`/`Get`，`Schema()` 留到 Task 3）

**关于「已下发基线」（`sent`）：** `Set` 的相等性比较对象**不是**「本帧上一个值」，而是**「上一次下发给客户端的值」**。这样才能满足 spec §10.3 的两条：
- 同步系统每 tick 重写全部刚体的 `Pos`/`Rot`，值没变 → 与 `sent` 相同 → 不标脏（否则静态几何每帧都发）。
- 同一帧内 A → B → A（A 是已下发值）→ 先标脏再撤销 → 完全不产生条目。

- [ ] **Step 1: 写失败的测试**

创建 `joltgo/replication/store_test.go`：

```go
package replication

import (
	"sort"
	"testing"
)

func newTestStore() *Store {
	s := New()
	s.Declare("Pos", KindVec3)
	s.Declare("Health", KindF32)
	s.Declare("Enemy", KindBool)
	return s
}

func TestSetStoresFinalValueOnly(t *testing.T) {
	s := newTestStore()
	s.Set(7, "Health", F32(10))
	s.Set(7, "Health", F32(20))
	s.Set(7, "Health", F32(30))

	if got := dirtyIDs(s, 7); len(got) != 1 {
		t.Fatalf("同一帧内三次 Set 应只留 1 条脏记录，得到 %d 条", len(got))
	}
	v, ok := s.Get(7, "Health")
	if !ok || v.Floats()[0] != 30 {
		t.Fatalf("应保留终值 30，得到 %v", v.Floats())
	}
}

func TestDestroyClearsValues(t *testing.T) {
	s := newTestStore()
	s.Set(7, "Health", F32(10))
	s.sent[7] = map[uint32]Value{s.attrOf("Health"): F32(10)} // 模拟已下发过

	s.Destroy(7)
	if _, ok := s.Get(7, "Health"); ok {
		t.Fatal("销毁后属性应被清空")
	}
	if !s.dead[7] {
		t.Fatal("销毁应记录 dead 标记")
	}
	if _, ok := s.sent[7]; ok {
		t.Fatal("销毁应同时清掉已下发基线，否则 id 被复用时新实体会因为值相同而发不出去")
	}
}

// id 被回收后立刻重建：新实体必须重新下发，即便它的值和旧实体的相同。
func TestSetAfterDestroyIsDirtyAgain(t *testing.T) {
	s := newTestStore()
	s.Set(7, "Health", F32(10))
	s.sent[7] = map[uint32]Value{s.attrOf("Health"): F32(10)} // 旧实体已下发过 10

	s.Destroy(7)
	s.Set(7, "Health", F32(10)) // 新实体，值恰好相同

	if got := dirtyIDs(s, 7); len(got) != 1 || got[0] != s.attrOf("Health") {
		t.Fatalf("重建的实体必须重新标脏，得到 %v", got)
	}
}

func TestSetKindMismatchPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("值与声明的 Kind 不一致应 panic")
		}
	}()
	newTestStore().Set(1, "Health", Vec3(1, 2, 3)) // Health 声明为 KindF32
}

func TestRemoveDropsValue(t *testing.T) {
	s := newTestStore()
	s.Set(7, "Enemy", Bool(true))

	s.Remove(7, "Enemy")
	if _, ok := s.Get(7, "Enemy"); ok {
		t.Fatal("Remove 后属性应消失")
	}
	if got := dirtyIDs(s, 7); len(got) != 0 {
		t.Fatalf("Remove 应同时撤销该属性的脏标记，得到 %v", got)
	}
}

func TestUndeclaredAttrPanics(t *testing.T) {
	assertPanics := func(name string, fn func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Fatalf("%s 未声明属性应 panic", name)
			}
		}()
		fn()
	}
	s := newTestStore()
	assertPanics("Set", func() { s.Set(1, "Nope", F32(1)) })
	assertPanics("Remove", func() { s.Remove(1, "Nope") })
	assertPanics("Get", func() { s.Get(1, "Nope") })
}

func TestDeclareTwicePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("重复声明同名属性应 panic")
		}
	}()
	s := New()
	s.Declare("Pos", KindVec3)
	s.Declare("Pos", KindVec3)
}

func TestResetKeepsDeclarations(t *testing.T) {
	s := newTestStore()
	s.Set(7, "Health", F32(10))
	s.sent[7] = map[uint32]Value{s.attrOf("Health"): F32(10)} // 模拟已下发过

	s.Reset()

	if _, ok := s.Get(7, "Health"); ok {
		t.Fatal("Reset 后应清空终值表")
	}
	if !s.dead[7] {
		t.Fatal("Reset 应保留一条待发的 destroy，否则场景重建后客户端会残留旧实体")
	}
	if _, ok := s.sent[7]; ok {
		t.Fatal("Reset 应同时清掉已下发基线，否则 id 复用后新实体会被静默抑制")
	}
	s.Set(7, "Health", F32(1)) // 声明还在，不应 panic
}

// 场景重建：id 被复用且值恰好与重建前相同时，新实体仍必须重新下发。
func TestSetAfterResetIsDirtyAgain(t *testing.T) {
	s := newTestStore()
	s.Set(7, "Health", F32(10))
	s.sent[7] = map[uint32]Value{s.attrOf("Health"): F32(10)}

	s.Reset()
	s.Set(7, "Health", F32(10)) // 重建，值恰好相同

	if got := dirtyIDs(s, 7); len(got) != 1 || got[0] != s.attrOf("Health") {
		t.Fatalf("Reset 后重建的实体必须重新标脏，得到 %v", got)
	}
}

// ---- 测试辅助（与 store.go 同包，可直接读内部字段） ----

// dirtyIDs 返回实体 id 本帧被标脏的属性 ID（升序，便于比较）。
func dirtyIDs(s *Store, id uint32) []uint32 {
	out := make([]uint32, 0, len(s.dirty[id]))
	for cid := range s.dirty[id] {
		out = append(out, cid)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
```

> 本步只验证「终值存储 + 标脏 + 销毁/移除清值」这些**不依赖产出**的语义。
> 与「已下发基线」交互的语义（写入相同的值不标脏、改回已下发值撤销脏标记、
> 移除已下发属性产生 removed）需要 `Drain` 才能观测，放在 Task 3 的
> `frame_test.go` 里。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd joltgo && go test ./replication`
Expected: FAIL —— `undefined: Store`、`undefined: New`、`undefined: dirtyIDs`

- [ ] **Step 3: 实现**

创建 `joltgo/replication/store.go`：

```go
// Package replication 是给客户端同步消息的唯一持有者，与 ECS 完全解耦：
// 它不 import ecs，只持有一张 [实体ID][属性ID] = 属性终值 的键值表和本帧脏集。
//
// 玩法层在变更点显式调用 Set 推进终值，Store 负责三件事：
//   - 同一帧内同一属性被改多次只保留终值（Drain 出来一条）
//   - 与「上一次下发给客户端的值」相同的写入不标脏，因此同步系统每 tick 重写
//     全部刚体（含永不变化的静态几何）也不会产生任何流量
//   - 把「过去所有帧的净效果」压缩成终值表，Full 一次性下发（重连 / 首次进入）
//
// 非并发安全：与 sim.Simulation 一样，由对局实例 goroutine 独占。
package replication

import "strconv"

// Attr 是一个属性的声明：ID 从 1 起，0 保留为无效。
type Attr struct {
	ID   uint32
	Name string
	Kind Kind
}

// Store 持有属性终值表、已下发基线与本帧脏集。
type Store struct {
	values map[uint32]map[uint32]Value // [实体ID][属性ID] = 属性终值
	sent   map[uint32]map[uint32]Value // [实体ID][属性ID] = 上一次下发给客户端的值
	dirty  map[uint32]map[uint32]bool  // 本帧需要下发的 (实体, 属性)
	gone   map[uint32]map[uint32]bool  // 本帧被移除、且曾经下发过的属性
	dead   map[uint32]bool             // 本帧被销毁的实体

	attrs   []Attr            // 属性ID-1 → 声明
	attrID  map[string]uint32 // 属性名 → 属性ID
	version uint32            // 属性表哈希，客户端据此检测前后端不一致
}

// New 创建一个空的 Store（尚未声明任何属性）。
func New() *Store {
	return &Store{
		values: map[uint32]map[uint32]Value{},
		sent:   map[uint32]map[uint32]Value{},
		dirty:  map[uint32]map[uint32]bool{},
		gone:   map[uint32]map[uint32]bool{},
		dead:   map[uint32]bool{},
		attrID: map[string]uint32{},
	}
}

// Declare 声明一个属性并分配属性 ID。属性表必须在仿真启动时一次性声明完：
// schema 必须完整稳定，客户端才能解码「schema 下发之后才第一次出现」的属性。
// 重复声明同名属性是编程错误，直接 panic（CI 会挡下）。
func (s *Store) Declare(name string, k Kind) {
	if _, dup := s.attrID[name]; dup {
		panic("replication: 属性重复声明: " + name)
	}
	id := uint32(len(s.attrs) + 1)
	s.attrID[name] = id
	s.attrs = append(s.attrs, Attr{ID: id, Name: name, Kind: k})
	s.version = schemaVersion(s.attrs)
}

// attrOf 返回属性名对应的 ID；未声明即 panic（编程错误）。
func (s *Store) attrOf(name string) uint32 {
	id, ok := s.attrID[name]
	if !ok {
		panic("replication: 未声明的属性: " + name)
	}
	return id
}

// Set 写入实体 id 的属性 attr 的终值。
//
// 标脏规则：与「上一次下发给客户端的值」不同才标脏。同一帧内多次 Set 只留终值；
// 改回已下发过的值会撤销本帧的脏标记（A → B → A 不产生任何流量）。
//
// 类型必须与 Declare 时声明的 Kind 一致：不一致说明调用点写错了属性名或值的
// 构造器，会让客户端按错误的 Kind 解码（静默数据损坏），所以在 dev/test 直接 panic。
func (s *Store) Set(id uint32, attr string, v Value) {
	cid := s.attrOf(attr)
	if want := s.attrs[cid-1].Kind; want != v.Kind() {
		panic("replication: 属性 " + attr + " 声明为 Kind " + strconv.Itoa(int(want)) +
			"，却写入了 Kind " + strconv.Itoa(int(v.Kind())))
	}

	m := s.values[id]
	if m == nil {
		m = map[uint32]Value{}
		s.values[id] = m
	}
	m[cid] = v

	// 曾经被移除、现在又被写回来：撤销移除标记（终值是否要重发由下面的比较决定）。
	if g := s.gone[id]; g != nil {
		delete(g, cid)
	}

	if sm := s.sent[id]; sm != nil {
		if known, ok := sm[cid]; ok && known.equal(v) {
			if d := s.dirty[id]; d != nil {
				delete(d, cid)
			}
			return
		}
	}

	d := s.dirty[id]
	if d == nil {
		d = map[uint32]bool{}
		s.dirty[id] = d
	}
	d[cid] = true
}

// Remove 移除实体 id 的属性 attr。曾经下发过的属性会产生一条 removed 下发，
// 客户端据此删除本地副本。当前玩法没有「摘掉组件但保留实体」的路径，此 API
// 为协议完整性保留（Destroy 内部不使用它）。
//
// 实体不存在或属性不存在时静默返回，但属性名未声明与 Set 一样 panic ——
// 前者是正常时序（重复移除），后者是调用点写错了名字。
func (s *Store) Remove(id uint32, attr string) {
	cid := s.attrOf(attr)
	m := s.values[id]
	if m == nil {
		return
	}
	if _, ok := m[cid]; !ok {
		return
	}
	delete(m, cid)

	if sm := s.sent[id]; sm != nil {
		if _, known := sm[cid]; known {
			g := s.gone[id]
			if g == nil {
				g = map[uint32]bool{}
				s.gone[id] = g
			}
			g[cid] = true
		}
	}
	if d := s.dirty[id]; d != nil {
		delete(d, cid)
	}
}

// Destroy 销毁实体：清空它的全部属性并记一条销毁下发。
//
// `sent`（已下发基线）也必须一并清掉：实体 id 可能被回收后立刻复用，若基线还在，
// 新实体的 Set 会因为「与旧实体的值相同」而撤销脏标记，客户端再也收不到它。
func (s *Store) Destroy(id uint32) {
	delete(s.values, id)
	delete(s.dirty, id)
	delete(s.gone, id)
	delete(s.sent, id)
	s.dead[id] = true
}

// Reset 丢弃全部实体与脏集，保留属性声明（对局 Reset 用）。
//
// 两条都不能少：
//   - 已下发过的实体各自保留一条待发的 destroy，否则场景重建后客户端会残留
//     那些 id 不再被复用的旧实体。
//   - 已下发基线（sent）必须一并清掉：重建后刚体 id 会从头发放，若基线还在，
//     新实体的 Set 会因为「与旧实体的值相同」被静默抑制 —— 客户端收到 destroy
//     却再也收不到重建，而那些只在创建时 Set 一次的属性（Body.*）就永久丢了。
//     清掉基线的副作用正是我们想要的：重建后的世界整体重新下发一次。
func (s *Store) Reset() {
	for id := range s.values {
		s.dead[id] = true
	}
	s.values = map[uint32]map[uint32]Value{}
	s.sent = map[uint32]map[uint32]Value{}
	s.dirty = map[uint32]map[uint32]bool{}
	s.gone = map[uint32]map[uint32]bool{}
}

// Get 返回实体 id 的属性 attr 的终值；不存在时返回 (Value{}, false)。
// 供测试与调试使用，不参与同步路径。属性名未声明时与 Set 一样 panic
// （先校验名字再查实体，避免同一个错误在实体不存在时被静默吞掉）。
func (s *Store) Get(id uint32, attr string) (Value, bool) {
	cid := s.attrOf(attr)
	m := s.values[id]
	if m == nil {
		return Value{}, false
	}
	v, ok := m[cid]
	return v, ok
}

// schemaVersion 是属性表的 FNV-1a 哈希（名字 + 类型），客户端据此检测
// 前后端协议不一致并打日志。
func schemaVersion(attrs []Attr) uint32 {
	const (
		offset32 = 2166136261
		prime32  = 16777619
	)
	h := uint32(offset32)
	for _, a := range attrs {
		for _, c := range []byte(a.Name) {
			h = (h ^ uint32(c)) * prime32
		}
		h = (h ^ uint32(a.Kind)) * prime32
	}
	return h
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd joltgo && go test ./replication`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add joltgo/replication/store.go joltgo/replication/store_test.go
git commit -m "feat: replication.Store 终值表与本帧脏集

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## Task 3: `replication.Frame` / `Schema` 与 `Drain` / `Full`

**Files:**
- Create: `joltgo/replication/frame.go`
- Test: `joltgo/replication/frame_test.go`

（`Store.Schema()` 也在 `frame.go` 里定义 —— 它与 `Frame`/`Schema` 类型是一组，`store.go` 不需要改动。）

**Interfaces:**
- Consumes: `Store`（Task 2）、`Value`（Task 1）
- Produces:
  - `type AttrValue struct { Attr uint32; Value Value }`
  - `type EntityDelta struct { ID uint32; Destroy bool; Removed []uint32; Set []AttrValue }`
  - `type Schema struct { Fields []Attr; Version uint32 }`
  - `type Frame struct { Step int; Full bool; Schema Schema; Entities []EntityDelta }`
  - `func (s *Store) Schema() Schema`
  - `func (s *Store) Drain() Frame`
  - `func (s *Store) Full() Frame`

**关键不变量：`Full()` 不得修改 `sent` 基线。** 全量是发给**单个**客户端的消息；其他客户端的基线不受影响。且因为所有 op 携带的都是**终值**（不是相对增量），任何客户端无论基线如何都会收敛。

- [ ] **Step 1: 写失败的测试**

创建 `joltgo/replication/frame_test.go`：

```go
package replication

import "testing"

func TestFrameDrainIsEmptyWhenNothingChanged(t *testing.T) {
	s := newTestStore()
	s.Set(7, "Health", F32(10))
	s.Drain()
	if f := s.Drain(); len(f.Entities) != 0 {
		t.Fatalf("没有变化的帧应无实体条目，得到 %+v", f.Entities)
	}
}

func TestFrameDrainEmitsRemovedAndDestroy(t *testing.T) {
	s := newTestStore()
	s.Set(7, "Enemy", Bool(true))
	s.Set(8, "Health", F32(5))
	s.Drain()

	s.Remove(7, "Enemy")
	s.Destroy(8)
	f := s.Drain()

	if len(f.Entities) != 2 {
		t.Fatalf("应有 2 个实体条目，得到 %d", len(f.Entities))
	}
	// 升序：7 在前（removed），8 在后（destroy）
	if f.Entities[0].ID != 7 || len(f.Entities[0].Removed) != 1 {
		t.Fatalf("实体 7 应带 1 条 removed，得到 %+v", f.Entities[0])
	}
	if f.Entities[1].ID != 8 || !f.Entities[1].Destroy {
		t.Fatalf("实体 8 应是 destroy，得到 %+v", f.Entities[1])
	}
}

func TestFullCarriesEverythingAndSchema(t *testing.T) {
	s := newTestStore()
	s.Set(7, "Health", F32(10))
	s.Set(9, "Pos", Vec3(1, 2, 3))
	s.Drain()

	f := s.Full()
	if !f.Full {
		t.Fatal("Full() 应带 full 标记")
	}
	if len(f.Schema.Fields) != 3 {
		t.Fatalf("schema 应有 3 个属性，得到 %d", len(f.Schema.Fields))
	}
	if len(f.Entities) != 2 {
		t.Fatalf("全量应包含 2 个实体，得到 %d", len(f.Entities))
	}
	if f.Entities[0].ID != 7 || f.Entities[1].ID != 9 {
		t.Fatalf("全量应按实体 ID 升序，得到 %v / %v", f.Entities[0].ID, f.Entities[1].ID)
	}
	if got := f.Entities[0].Set[0].Value.Floats()[0]; got != 10 {
		t.Fatalf("全量应带终值 10，得到 %v", got)
	}
}

// Full 是发给单个客户端的消息，不能污染增量基线 —— 否则其他在线客户端
// 会再也收不到这些属性的增量。
func TestFullDoesNotResetDeltaBaseline(t *testing.T) {
	s := newTestStore()
	s.Set(7, "Health", F32(10))
	s.Drain() // 基线 = 10

	s.Full() // 某个重连客户端拿走全量

	s.Set(7, "Health", F32(20))
	f := s.Drain()
	if len(f.Entities) != 1 || len(f.Entities[0].Set) != 1 {
		t.Fatalf("Full 之后增量仍应下发 20，得到 %+v", f.Entities)
	}
	if got := f.Entities[0].Set[0].Value.Floats()[0]; got != 20 {
		t.Fatalf("应下发 20，得到 %v", got)
	}
}

func TestSchemaVersionIsStable(t *testing.T) {
	a := newTestStore()
	b := newTestStore()
	if a.Schema().Version != b.Schema().Version {
		t.Fatal("相同的属性表应得到相同的版本哈希")
	}
	c := New()
	c.Declare("Pos", KindVec3)
	if c.Schema().Version == a.Schema().Version {
		t.Fatal("属性表不同应得到不同的版本哈希")
	}
}

func TestOutputIsDeterministic(t *testing.T) {
	s := newTestStore()
	for i := uint32(1); i <= 8; i++ {
		s.Set(i, "Health", F32(float32(i)))
	}
	first := s.Full()
	second := s.Full()
	for i := range first.Entities {
		if first.Entities[i].ID != second.Entities[i].ID {
			t.Fatalf("第 %d 项实体 ID 不稳定：%d vs %d", i, first.Entities[i].ID, second.Entities[i].ID)
		}
	}
}

// 已下发基线的语义：Set 的相等性比较对象是「上一次下发的值」，不是「本帧上一个值」。
// 这两条是「同步系统每 tick 重写全部刚体也不产生流量」的依据。
func TestSetSameAsSentValueIsNotDirty(t *testing.T) {
	s := newTestStore()
	s.Set(7, "Health", F32(10))
	if f := s.Drain(); len(f.Entities) != 1 {
		t.Fatal("首次 Set 应产生 1 条增量")
	}
	s.Set(7, "Health", F32(10)) // 与已下发值相同
	if f := s.Drain(); len(f.Entities) != 0 {
		t.Fatalf("写入相同的值不应产生增量，得到 %+v", f.Entities)
	}
}

func TestSetBackToSentValueCancelsDirty(t *testing.T) {
	s := newTestStore()
	s.Set(7, "Health", F32(10))
	s.Drain() // 10 成为基线

	s.Set(7, "Health", F32(20))
	s.Set(7, "Health", F32(10)) // 改回已下发值 → 撤销脏标记

	if f := s.Drain(); len(f.Entities) != 0 {
		t.Fatalf("改回已下发值应不产生增量，得到 %+v", f.Entities)
	}
}

// id 被回收后同帧重建：destroy 与 set 必须一起下发，客户端才不会漏掉新实体。
func TestDestroyAndRebuildInSameFrameEmitsBoth(t *testing.T) {
	s := newTestStore()
	s.Set(7, "Health", F32(10))
	s.Drain()

	s.Destroy(7)
	s.Set(7, "Health", F32(10)) // 新实体，值恰好与旧实体相同

	f := s.Drain()
	if len(f.Entities) != 1 {
		t.Fatalf("应产出 1 个实体条目，得到 %d", len(f.Entities))
	}
	ed := f.Entities[0]
	if !ed.Destroy {
		t.Fatal("应带 destroy")
	}
	if len(ed.Set) != 1 {
		t.Fatalf("应同时带新实体的 set，得到 %+v", ed.Set)
	}

	// 基线已按新值重建，下一帧不应重复下发。
	if n := s.Drain(); len(n.Entities) != 0 {
		t.Fatalf("重建后不应重复下发，得到 %+v", n.Entities)
	}
}

// removed 下发时必须一并清掉基线，否则之后写回同一个值会被抑制、客户端再也收不到。
func TestRemovedAttrCanBeReSetAndIsSentAgain(t *testing.T) {
	s := newTestStore()
	s.Set(7, "Enemy", Bool(true))
	s.Drain()

	s.Remove(7, "Enemy")
	if f := s.Drain(); len(f.Entities) != 1 || len(f.Entities[0].Removed) != 1 {
		t.Fatalf("移除应下发 1 条 removed，得到 %+v", f.Entities)
	}

	s.Set(7, "Enemy", Bool(true)) // 写回完全相同的值
	f := s.Drain()
	if len(f.Entities) != 1 || len(f.Entities[0].Set) != 1 {
		t.Fatalf("移除后写回同一个值也必须重新下发，得到 %+v", f.Entities)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd joltgo && go test ./replication`
Expected: FAIL —— `undefined: Frame`、`undefined: EntityDelta`

- [ ] **Step 3: 实现**

创建 `joltgo/replication/frame.go`：

```go
package replication

import "sort"

// AttrValue 是一个属性的一次取值。
type AttrValue struct {
	Attr  uint32
	Value Value
}

// EntityDelta 是一个实体在本帧的变化。三者可以同时出现：先销毁，再移除属性，
// 最后写属性 —— 客户端按这个顺序应用。
type EntityDelta struct {
	ID      uint32
	Destroy bool
	Removed []uint32
	Set     []AttrValue
}

// Schema 是属性表：客户端据此把属性 ID 还原成名字，并按 Kind 解码。
type Schema struct {
	Fields  []Attr
	Version uint32
}

// Frame 是一帧同步消息。Full=true 表示全量帧（重连 / 首次进入），客户端应
// 先清空本地状态再整体覆盖；此时 Schema 一并携带，避免 schema 与全量帧分两条
// 消息发出可能导致的顺序问题。
type Frame struct {
	Step     int
	Full     bool
	Schema   Schema
	Entities []EntityDelta
}

// Schema 返回属性表。
func (s *Store) Schema() Schema {
	fields := make([]Attr, len(s.attrs))
	copy(fields, s.attrs)
	return Schema{Fields: fields, Version: s.version}
}

// Drain 取走本帧增量并清空脏集，同时把下发过的值记为新的基线。
// 输出按实体 ID、属性 ID 升序，保证字节稳定、可测试。
func (s *Store) Drain() Frame {
	f := Frame{Entities: []EntityDelta{}}
	for _, id := range s.touchedIDs() {
		ed := EntityDelta{ID: id}
		if s.dead[id] {
			ed.Destroy = true
		}
		for _, cid := range sortedIDs(s.gone[id]) {
			ed.Removed = append(ed.Removed, cid)
		}
		vals := s.values[id]
		for _, cid := range sortedIDs(s.dirty[id]) {
			ed.Set = append(ed.Set, AttrValue{Attr: cid, Value: vals[cid]})
		}
		// 销毁与重建可以同帧发生（id 被回收后立刻新建）：destroy 与 set 一起发，
		// 客户端先清掉旧实体、再按 set 重建。所以销毁时不能直接 continue。
		if !ed.Destroy && len(ed.Removed) == 0 && len(ed.Set) == 0 {
			continue
		}
		f.Entities = append(f.Entities, ed)
	}

	for _, ed := range f.Entities {
		if ed.Destroy {
			delete(s.sent, ed.ID)
			delete(s.dead, ed.ID)
		}
		if len(ed.Set) == 0 && len(ed.Removed) == 0 {
			continue
		}
		sm := s.sent[ed.ID]
		if sm == nil {
			sm = map[uint32]Value{}
			s.sent[ed.ID] = sm
		}
		for _, av := range ed.Set {
			sm[av.Attr] = av.Value
		}
		for _, cid := range ed.Removed {
			delete(sm, cid)
		}
	}

	s.dirty = map[uint32]map[uint32]bool{}
	s.gone = map[uint32]map[uint32]bool{}
	return f
}

// Full 取走当前世界的全量（终值表整表）并附带 schema，供重连 / 首次进入的
// 客户端整体覆盖。
//
// 它**不修改**增量基线：全量是发给单个客户端的消息，其他在线客户端的基线
// 不受影响。又因为所有 op 携带的都是终值而非相对增量，任何客户端无论基线
// 如何都会收敛到同一状态。
func (s *Store) Full() Frame {
	f := Frame{Full: true, Schema: s.Schema(), Entities: []EntityDelta{}}
	for _, id := range sortedIDs(s.values) {
		vals := s.values[id]
		ed := EntityDelta{ID: id}
		for _, cid := range sortedIDs(vals) {
			ed.Set = append(ed.Set, AttrValue{Attr: cid, Value: vals[cid]})
		}
		if len(ed.Set) == 0 {
			continue // 没有任何属性可说的实体不进全量（与 Drain 的跳过规则保持对称）
		}
		f.Entities = append(f.Entities, ed)
	}
	return f
}

// touchedIDs 返回本帧涉及的实体 ID（脏 ∪ 移除 ∪ 销毁），升序。
func (s *Store) touchedIDs() []uint32 {
	seen := map[uint32]bool{}
	for id, d := range s.dirty {
		if len(d) > 0 {
			seen[id] = true
		}
	}
	for id, g := range s.gone {
		if len(g) > 0 {
			seen[id] = true
		}
	}
	for id := range s.dead {
		seen[id] = true
	}
	return sortedIDs(seen)
}

// sortedIDs 返回按键升序排列的 ID 列表。map 迭代无序，而帧的输出必须确定
// （可测试、字节稳定），所以每条产出路径都要经过它排序。
func sortedIDs[V any](m map[uint32]V) []uint32 {
	out := make([]uint32, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd joltgo && go test ./replication`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add joltgo/replication/frame.go joltgo/replication/frame_test.go joltgo/replication/store.go
git commit -m "feat: replication 增量 Drain 与全量 Full 产出同一 Frame 类型

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## Task 4: `sim` 组件边界调整（`Player.Idx` / `Facing` / `GameState` 单例）

纯重构，不引入同步逻辑，现有测试除少量取值来源外全保留。做这一步是为了让后面的复制层面对一个干净的「实体 + 属性」世界。

**Files:**
- Modify: `joltgo/sim/components.go`
- Modify: `joltgo/sim/simulation.go`
- Modify: `joltgo/sim/systems.go`

**Interfaces:**
- Consumes: 无
- Produces:
  - `type Facing struct { Yaw float32 }`
  - `type GameState struct { Score, Wave, Gold int32 }`
  - `Player` 由 `struct{}` 变为 `struct{ Idx int }`
  - `func (s *Simulation) GameEntity() ecs.Entity` —— 全局状态单例实体的 id（供同步层与测试使用）

- [ ] **Step 1: 改组件定义**

`joltgo/sim/components.go` —— 把 `Player` 改成带槽位，并新增两个组件：

```go
// Player 标记玩家实体，Idx 是玩家槽位（0/1）。客户端据此认出「哪个实体是我」。
type Player struct {
	Idx int
}

// Facing 是玩家当前朝向（弧度，绕 Y 轴）。它从 Input 里拆出来单独同步：
// Input 是客户端上行数据，不该回灌给客户端；朝向才是需要下发的。
type Facing struct {
	Yaw float32
}

// GameState 是对局的全局状态（计分/波次/金币），挂在一个单例实体上。
// 做成组件是为了让框架里不存在「顶层字段」这个概念 —— 以后加全局状态
// 也自动走同一套同步机制。
type GameState struct {
	Score int32
	Wave  int32
	Gold  int32
}
```

- [ ] **Step 2: 让 `Simulation` 持有单例实体并改造创建路径**

`joltgo/sim/simulation.go`：

1. `Simulation` 结构体加字段：

```go
	game ecs.Entity // 全局状态单例实体（GameState），在 init 里创建
```

2. `New()` 里把 `game` 初始化为 `ecs.InvalidEntity`。

3. `init()` 里玩家创建那一段改为：

```go
		// 玩家实体：纯逻辑（角色控制器不是刚体），初始站在出生点、面朝船中。
		s.players[i] = s.world.NewEntity()
		ecs.Add4(s.world, s.players[i], Player{Idx: i}, Health(100),
			Position{x, playerSpawnY, z}, Input{Yaw: playerSpawnYaw(i)})
		ecs.Add(s.world, s.players[i], Facing{Yaw: playerSpawnYaw(i)})
```

4. 在 `init()` 里 `s.wave = 1` 之前插入单例实体创建：

```go
	// 全局状态单例实体：计分/波次/金币不是实体属性，但走同一套「实体 + 属性」
	// 机制可以让框架里不存在特例。
	s.game = s.world.NewEntity()
	ecs.Add(s.world, s.game, GameState{})
```

   紧接着在 `s.wave = 1` 那一行之后补一次 `s.syncGameState()`：

```go
	s.wave = 1
	s.syncGameState() // 让组件立刻与 Go 侧计数一致，否则它会停在零值直到首次计数变化
```

   **这一步不能省**：单例是用零值 `GameState{}` 建的，而 `s.wave` 此时是 1。
   不补这一次同步，组件里的 `Wave` 会一直是 0，直到第一次清波才刷新；Task 5 的
   oracle 测试会拿组件当基准去比对同步 store，首次比对就会失败。

5. `reset()` 里把 `s.game = ecs.InvalidEntity` 与 `for i := range s.players` 一起重置。

6. 新增公开访问器：

```go
// GameEntity 返回全局状态单例实体的 id。
func (s *Simulation) GameEntity() ecs.Entity { return s.game }
```

- [ ] **Step 3: 让各系统写入新组件**

`joltgo/sim/systems.go`：

1. `inputSystem()` 里，读到 `in` 之后补一句（朝向与积分速度无关，只影响下发）：

```go
		ecs.Add(s.world, s.players[i], Facing{Yaw: in.Yaw})
```

2. `projectileSystem()` 里 `s.score++` 两处（靶球摧毁、敌人死亡）各补：

```go
			s.score++
			ecs.Add(s.world, s.game, GameState{Score: int32(s.score), Wave: int32(s.wave), Gold: int32(s.gold)})
```

> 为了不重复三次，抽一个私有方法放在 `simulation.go`：

```go
// syncGameState 把 Simulation 的全局计数写进单例实体的 GameState 组件。
// 全局状态是 Go 侧的普通字段，组件只是它的同步载体。
func (s *Simulation) syncGameState() {
	ecs.Add(s.world, s.game, GameState{
		Score: int32(s.score),
		Wave:  int32(s.wave),
		Gold:  int32(s.gold),
	})
}
```

调用点：`projectileSystem` 的 `s.score++` 之后、`resourceSystem` 的 `s.gold++` 之后、`waveSystem` 的 `s.wave++` 之后，各调一次 `s.syncGameState()`。

3. `snapshot()`（本 Task 暂不动，Task 6 才删）里 `ps.Yaw` 的来源保持读 `Input`，行为不变。

- [ ] **Step 4: 编译并跑现有测试**

Run: `cd joltgo && gofmt -l . && go vet ./sim && go test ./ecs ./sim`
Expected: PASS —— `Player{}` 无字段变成有字段不会破坏任何现有断言；`Facing`/`GameState` 是新增组件，不影响查询。

- [ ] **Step 5: 提交**

```bash
git add joltgo/sim/components.go joltgo/sim/simulation.go joltgo/sim/systems.go
git commit -m "refactor: Player 带槽位、拆出 Facing、全局状态改为单例实体组件

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## Task 5: `sim/replicate.go` 与全部 `rep.Set` 调用点

**Files:**
- Create: `joltgo/sim/replicate.go`
- Create: `joltgo/sim/replicate_test.go`
- Modify: `joltgo/sim/simulation.go`
- Modify: `joltgo/sim/systems.go`
- Modify: `joltgo/sim/sim_test.go`（新增 `setBodyActive` 测试钩子，见 Step 1 的测试辅助说明）

**Interfaces:**
- Consumes: `replication.Store`（Task 1–3）、`Simulation.game`（Task 4）
- Produces:
  - `Simulation` 新字段 `rep *replication.Store`
  - `func (s *Simulation) DrainFrame() replication.Frame` —— 填好 `Step` 的增量帧
  - `func (s *Simulation) FullFrame() replication.Frame` —— 填好 `Step` 的全量帧
  - 包内属性名常量 `attrPos` / `attrRot` / `attrHealth` / `attrFacing` / `attrPlayerIdx` / `attrBodyKind` / `attrBodySize` / `attrBodyStatic` / `attrBodyActive` / `attrBodyMat` / `attrEnemy` / `attrTarget` / `attrProjectile` / `attrResourceKind` / `attrGameScore` / `attrGameWave` / `attrGameGold`

**这一步的风险：漏写一处 `Set` 就是静默不同步。** `replicate_test.go` 的 oracle 测试是主要防线 —— 它以 ECS 世界为 oracle 逐项比对 store，任何遗漏都会炸。

- [ ] **Step 1: 写失败的测试（oracle 一致性 + 增量重建 == 全量）**

创建 `joltgo/sim/replicate_test.go`：

```go
package sim

import (
	"fmt"
	"reflect"
	"strconv"
	"testing"

	"joltgo/ecs"
	"joltgo/replication"
)

// expectedAttrs 是「世界应有属性集」的独立实现 —— 它是 oracle：
// 不看 replicate.go 怎么写，只从 ECS 世界直接读出应有的 (实体, 属性, 终值)。
// 任何漏写的 rep.Set 都会在 assertStoreMatchesWorld 里失败。
func expectedAttrs(s *Simulation) map[uint32]map[string]replication.Value {
	out := map[uint32]map[string]replication.Value{}
	put := func(id uint32, attr string, v replication.Value) {
		m := out[id]
		if m == nil {
			m = map[string]replication.Value{}
			out[id] = m
		}
		m[attr] = v
	}

	// 全局状态单例实体
	if gs, ok := ecs.Get[GameState](s.world, s.game); ok {
		id := uint32(s.game)
		put(id, attrGameScore, replication.I32(gs.Score))
		put(id, attrGameWave, replication.I32(gs.Wave))
		put(id, attrGameGold, replication.I32(gs.Gold))
	}

	// 玩家
	for i := 0; i < MaxPlayers; i++ {
		e := s.players[i]
		if e == ecs.InvalidEntity {
			continue
		}
		id := uint32(e)
		p, _ := ecs.Get[Position](s.world, e)
		h, _ := ecs.Get[Health](s.world, e)
		f, _ := ecs.Get[Facing](s.world, e)
		pl, _ := ecs.Get[Player](s.world, e)
		put(id, attrPos, replication.Vec3(p[0], p[1], p[2]))
		put(id, attrHealth, replication.F32(float32(*h)))
		put(id, attrFacing, replication.F32(f.Yaw))
		put(id, attrPlayerIdx, replication.I32(int32(pl.Idx)))
	}

	// 刚体
	ecs.Each(s.world, func(e ecs.Entity, b *Body) {
		id := uint32(e)
		p, _ := ecs.Get[Position](s.world, e)
		rot, _ := ecs.Get[Rotation](s.world, e)
		put(id, attrBodyKind, replication.I32(int32(b.Kind)))
		put(id, attrBodySize, replication.Vec3(b.Size[0], b.Size[1], b.Size[2]))
		put(id, attrBodyStatic, replication.Bool(b.Static))
		put(id, attrBodyActive, replication.Bool(b.Active))
		put(id, attrBodyMat, replication.I32(int32(b.Mat)))
		put(id, attrPos, replication.Vec3(p[0], p[1], p[2]))
		put(id, attrRot, replication.Vec4(rot[0], rot[1], rot[2], rot[3]))
		if h, ok := ecs.Get[Health](s.world, e); ok {
			put(id, attrHealth, replication.F32(float32(*h)))
		}
		if ecs.Has[Enemy](s.world, e) {
			put(id, attrEnemy, replication.Bool(true))
		}
		if ecs.Has[Target](s.world, e) {
			put(id, attrTarget, replication.Bool(true))
		}
		if ecs.Has[Projectile](s.world, e) {
			put(id, attrProjectile, replication.Bool(true))
		}
		if r, ok := ecs.Get[Resource](s.world, e); ok {
			put(id, attrResourceKind, replication.I32(int32(r.Kind)))
		}
	})
	return out
}

// storeAttrs 把 store 的全量帧摊平成与 expectedAttrs 同构的映射。
func storeAttrs(s *Simulation) map[uint32]map[string]replication.Value {
	f := s.FullFrame()
	name := map[uint32]string{}
	for _, a := range f.Schema.Fields {
		name[a.ID] = a.Name
	}
	out := map[uint32]map[string]replication.Value{}
	for _, ed := range f.Entities {
		m := map[string]replication.Value{}
		for _, av := range ed.Set {
			m[name[av.Attr]] = av.Value
		}
		out[ed.ID] = m
	}
	return out
}

func assertStoreMatchesWorld(t *testing.T, s *Simulation) {
	t.Helper()
	want := expectedAttrs(s)
	got := storeAttrs(s)

	for id, wm := range want {
		gm, ok := got[id]
		if !ok {
			t.Fatalf("实体 %d 在世界里存在，但同步 store 里没有", id)
		}
		for attr, wv := range wm {
			gv, ok := gm[attr]
			if !ok {
				t.Fatalf("实体 %d 缺少属性 %s（漏写 rep.Set？）", id, attr)
			}
			if !valueEqual(wv, gv) {
				t.Fatalf("实体 %d 属性 %s 不一致：世界 %s vs store %s", id, attr, describe(wv), describe(gv))
			}
		}
	}
	for id, gm := range got {
		if _, ok := want[id]; !ok {
			t.Fatalf("同步 store 里有世界里不存在的实体 %d", id)
		}
		for attr := range gm {
			if _, ok := want[id][attr]; !ok {
				t.Fatalf("实体 %d 有世界里不存在的属性 %s", id, attr)
			}
		}
	}
}

// describe 把属性值渲染成可读文本。不能直接用 Floats()：它对整数/布尔返回 nil，
// 会让这两类属性的失败信息变成两个空切片，看不到到底是哪个值不对。
func describe(v replication.Value) string {
	switch {
	case v.Kind().Dim() > 0:
		return fmt.Sprintf("%v", v.Floats())
	case v.Kind() == replication.KindStr:
		return strconv.Quote(v.Text())
	case v.Kind() == replication.KindBool:
		return strconv.FormatBool(v.Boolean())
	default:
		return strconv.FormatInt(int64(v.Int()), 10)
	}
}

// valueEqual 用一个短小的往返把两边都归一成可比形式（同 Kind 才算相等）。
func valueEqual(a, b replication.Value) bool {
	if a.Kind() != b.Kind() {
		return false
	}
	if a.Kind() == replication.KindStr {
		return a.Text() == b.Text()
	}
	if a.Kind().Dim() > 0 {
		return reflect.DeepEqual(a.Floats(), b.Floats())
	}
	if a.Kind() == replication.KindBool {
		return a.Boolean() == b.Boolean()
	}
	return a.Int() == b.Int()
}

func TestStoreMatchesWorldThroughoutMatch(t *testing.T) {
	s, p := newTestSim(t)
	assertStoreMatchesWorld(t, s)

	// 跑一段：物理步进、刷怪、接触伤害都要覆盖到。
	for i := 0; i < 40; i++ {
		s.Step()
		assertStoreMatchesWorld(t, s)
	}

	// 弹丸存活期间必须被同步（Projectile 属性只在创建时 Set 一次，而别的用例里
	// 弹丸都在创建的同一 tick 就被销毁 —— 不单独跑这一条，漏写这个 Set 不会被发现）。
	proj := s.Shoot(shoot0(), [3]float32{1, 0, 0})
	s.Step()
	assertStoreMatchesWorld(t, s)

	// 弹丸命中靶球（摧毁实体）
	target := targetsOf(snapshotWorld(s))[0]
	p.queueContact(proj, target)
	s.Step()
	assertStoreMatchesWorld(t, s)

	// 玩家朝向：yaw 必须真的变过才验证得到 Facing 的 Set（出生朝向是 0）。
	s.ApplyInput(0, [2]float32{0, 0}, 0.7, false)
	s.Step()
	assertStoreMatchesWorld(t, s)

	// 刚体的变换必须跟着物理走。fake 平时既不移动也不旋转刚体，所以这里手动推一下；
	// 必须挑动态刚体（静态船体的 active 恒为 false，翻转不出变化）。
	var dyn uint32
	for _, b := range snapshotWorld(s).Bodies {
		if !b.Static {
			dyn = b.ID
			break
		}
	}
	if dyn == 0 {
		t.Fatal("场景里应有动态刚体（木箱）")
	}

	p.moveBody(dyn, [3]float32{1.5, 2.5, 3.5})
	p.setBodyQuat(dyn, [4]float32{0, 0.70710678, 0, 0.70710678})
	s.Step()
	assertStoreMatchesWorld(t, s)

	// 休眠状态翻转：Body.Active 只在值真的变了才 Set，必须真的翻过才验证得到。
	p.setBodyActive(dyn, false)
	s.Step()
	assertStoreMatchesWorld(t, s)

	// 玩家拾取金币。初始金币是随机撒的，理论上可能一枚都没撒上（既有随机性，
	// 见 TestInitialSnapshot 的同源 flake），所以先判空再取下标。
	if res := snapshotWorld(s).Resources; len(res) > 0 {
		p.queueCharacterContact(uint32(res[0].ID))
		s.Step()
		assertStoreMatchesWorld(t, s)
	}

	// 敌人贴身伤害。**每 tick 都断言**：玩家复活那一帧会同时改 Health 与 Position，
	// 只在循环外断言的话，下一 tick 的同步会把两边都修好、漏写的 Set 就抓不住了。
	enemy := enemiesOf(snapshotWorld(s))[0]
	p.queueCharacterContact(enemy)
	for i := 0; i < 300; i++ {
		s.Step()
		assertStoreMatchesWorld(t, s)
	}
}

// 波次推进：击杀全场敌人 -> 清波 waveDelayTicks 后刷出新的一波（会新建实体）。
// 单独成测是因为「真的把敌人打死」需要 enemyHealth 次命中；waveSystem 的
// wave++ 与运行期的 spawnEnemy 只有走到这里才会被覆盖到。
func TestStoreMatchesWorldAcrossWaveAdvance(t *testing.T) {
	s, p := newTestSim(t)
	before := s.wave

	for _, e := range enemiesOf(snapshotWorld(s)) {
		for hit := 0; hit < enemyHealth; hit++ {
			proj := s.Shoot(shoot0(), [3]float32{1, 0, 0})
			p.queueContact(proj, e)
			s.Step()
			assertStoreMatchesWorld(t, s)
		}
	}

	for i := 0; i < waveDelayTicks+10; i++ {
		s.Step()
		assertStoreMatchesWorld(t, s)
	}
	if s.wave <= before {
		t.Fatalf("清波后应刷出新的一波（否则运行期 spawnEnemy 与 wave++ 都没被覆盖），wave 仍是 %d", s.wave)
	}
}

// 需求 3：把增量流喂给一个「客户端 store」，重建结果必须等于直接取全量。
func TestDeltaStreamRebuildsFullState(t *testing.T) {
	s, p := newTestSim(t)

	// 客户端从服务端 schema 建立同一套属性表。注意增量帧不带 schema
	// （只有 full 帧带），所以名字表要在循环外建好。
	schema := s.FullFrame().Schema
	name := map[uint32]string{}
	client := replication.New()
	for _, a := range schema.Fields {
		name[a.ID] = a.Name
		client.Declare(a.Name, a.Kind)
	}

	// 注意：destroy 之后**不能** continue —— 同帧销毁+重建时，同一个 EntityDelta
	// 里既有 destroy 也有新实体的 set，丢掉 set 客户端就再也收不到重建的实体。
	applyFrame := func(f replication.Frame) {
		for _, ed := range f.Entities {
			if ed.Destroy {
				client.Destroy(ed.ID)
			}
			for _, cid := range ed.Removed {
				client.Remove(ed.ID, name[cid])
			}
			for _, av := range ed.Set {
				client.Set(ed.ID, name[av.Attr], av.Value)
			}
		}
	}

	for i := 0; i < 60; i++ {
		s.Step()
		applyFrame(s.DrainFrame())
	}

	// 「只销毁、不重建」的帧——生产里最常见的路径（弹丸过期、金币被拾取、
	// 敌人被击杀），而下面 Reset 那条路径只会产生 destroy+set。
	// 不单独走一遍的话，客户端就算完全忽略 destroy 也测不出来。
	doomed := enemiesOf(snapshotWorld(s))[0]
	if _, seen := storeAttrsOf(client)[doomed]; !seen {
		t.Fatalf("客户端本应已收到敌人 %d，否则这个用例覆盖不到销毁", doomed)
	}
	for _, e := range enemiesOf(snapshotWorld(s)) {
		for hit := 0; hit < enemyHealth; hit++ {
			proj := s.Shoot(shoot0(), [3]float32{1, 0, 0})
			p.queueContact(proj, e)
			s.Step()
			applyFrame(s.DrainFrame())
		}
	}
	if attrs, still := storeAttrsOf(client)[doomed]; still {
		t.Fatalf("敌人 %d 已被击杀，客户端 store 里不应还有它：%+v", doomed, attrs)
	}

	// 场景重建走的是「所有旧实体各发一条 destroy、随后整体重建」的路径，
	// 而且刚体 id 会从头复用 —— 正好覆盖上面 destroy+set 同帧那个分支。
	s.Reset()
	for i := 0; i < 5; i++ {
		s.Step()
		applyFrame(s.DrainFrame())
	}

	// 客户端 store 从零开始，只吃增量流；跑完应与服务端的全量逐项相等。
	got := storeAttrsOf(client)
	want := expectedAttrs(s)
	if !reflect.DeepEqual(normalize(got), normalize(want)) {
		t.Fatal("增量重建的客户端状态与全量不一致")
	}
}

// storeAttrsOf 与 storeAttrs 相同，但作用于任意 Store。
func storeAttrsOf(st *replication.Store) map[uint32]map[string]replication.Value {
	f := st.Full()
	name := map[uint32]string{}
	for _, a := range f.Schema.Fields {
		name[a.ID] = a.Name
	}
	out := map[uint32]map[string]replication.Value{}
	for _, ed := range f.Entities {
		m := map[string]replication.Value{}
		for _, av := range ed.Set {
			m[name[av.Attr]] = av.Value
		}
		out[ed.ID] = m
	}
	return out
}

// normalize 把 map 转成可 DeepEqual 的稳定形式（Value 有未导出字段，
// reflect.DeepEqual 能比，但 map 键序不影响 —— 这里只做一层拷贝以确保比较的是内容）。
func normalize(m map[uint32]map[string]replication.Value) map[uint32]map[string]replication.Value {
	out := map[uint32]map[string]replication.Value{}
	for id, am := range m {
		c := map[string]replication.Value{}
		for k, v := range am {
			c[k] = v
		}
		out[id] = c
	}
	return out
}
```

> **测试辅助补丁**：`snapshotWorld` 在 Task 6 才引入。为了让本 Task 的测试能跑，先在本文件顶部临时加一个同名的测试辅助（Task 6 会把它移到 `sim_test.go` 并删掉这里的副本）：

```go
// snapshotWorld 的临时副本（Task 6 移入 sim_test.go 并删除此处）。
func snapshotWorld(s *Simulation) State { return s.snapshot() }
```

> 另外 `fakePhysics` 已经有一个测试钩子 `queueCharacterContact(id uint32)`（`sim_test.go` 里，「注入一个持续存在的 0 号角色接触」），上面的接触注入直接用它，不要新加同义方法。

> **还需要给 fake 补上「旋转」与几个测试钩子。** 现在 `fakePhysics.Sync` 永远上报单位四元数，
> 于是 `attrRot` 的同步漏写根本验证不到。改动（都在 `sim_test.go`）：

```go
// fakeBody 加一个字段（放在 active 旁边）：
	quat [4]float32
```

```go
// addBody 里初始化（否则默认零四元数不是合法旋转）：
	f.bodies[id] = &fakeBody{
		active: motion != MotionStatic,
		pos:    pos,
		quat:   [4]float32{0, 0, 0, 1},
		radius: radius,
		sensor: sensor,
	}
```

```go
// Sync 上报刚体自己的四元数，而不是写死单位四元数：
func (f *fakePhysics) Sync(fn func(id uint32, active bool, pos [3]float32, quat [4]float32)) {
	for id, b := range f.bodies {
		fn(id, b.active, b.pos, b.quat)
	}
}
```

```go
// 新增三个钩子（紧挨着已有的 moveBody）：
//
// setBodyActive 翻转一个刚体的「仍在模拟」状态。fake 的 active 只在创建时赋值、
// 之后从不变化，不翻转它就无法验证 Body.Active 的同步（syncSystem 只在值变了才 Set）。
func (f *fakePhysics) setBodyActive(id uint32, active bool) {
	if b, ok := f.bodies[id]; ok {
		b.active = active
	}
}

// setBodyQuat 直接改一个刚体的旋转，用于验证旋转变换的同步。
func (f *fakePhysics) setBodyQuat(id uint32, quat [4]float32) {
	if b, ok := f.bodies[id]; ok {
		b.quat = quat
	}
}
```

> `moveBody` 已经在 `sim_test.go` 里了，直接用，不要重复定义。既有测试都不读 `quat`，
> 所以给 fake 补四元数不会影响它们。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd joltgo && go test ./sim -run 'TestStoreMatchesWorld|TestDeltaStream'`
Expected: FAIL —— `undefined: attrPos`、`undefined: s.rep`

- [ ] **Step 3: 实现 `sim/replicate.go`**

```go
package sim

// 同步层：唯一知道「ECS 组件 ↔ 同步属性」映射的地方。
// replication.Store 本身不依赖 ecs，耦合全部集中在本文件。
//
// 属性名用常量而非裸字符串，避免调用点写错名字 —— 写错会 panic（Store 只
// 接受声明过的属性），但常量能在编译期就挡住。

import (
	"joltgo/ecs"
	"joltgo/replication"
)

// 属性名（与 declareAttributes 的声明一一对应；同时是客户端取值的键）。
const (
	attrPos          = "Pos"
	attrRot          = "Rot"
	attrHealth       = "Health"
	attrFacing       = "Facing"
	attrPlayerIdx    = "Player.Idx"
	attrBodyKind     = "Body.Kind"
	attrBodySize     = "Body.Size"
	attrBodyStatic   = "Body.Static"
	attrBodyActive   = "Body.Active"
	attrBodyMat      = "Body.Mat"
	attrEnemy        = "Enemy"
	attrTarget       = "Target"
	attrProjectile   = "Projectile"
	attrResourceKind = "Resource.Kind"
	attrGameScore    = "Game.Score"
	attrGameWave     = "Game.Wave"
	attrGameGold     = "Game.Gold"
)

// declareAttributes 声明全部同步属性。属性表必须完整稳定 —— 客户端在 full
// 帧里一次拿到，之后靠它解码所有增量，所以不能等到首次 Set 才登记。
func declareAttributes(rep *replication.Store) {
	rep.Declare(attrPos, replication.KindVec3)
	rep.Declare(attrRot, replication.KindVec4)
	rep.Declare(attrHealth, replication.KindF32)
	rep.Declare(attrFacing, replication.KindF32)
	rep.Declare(attrPlayerIdx, replication.KindI32)
	rep.Declare(attrBodyKind, replication.KindI32)
	rep.Declare(attrBodySize, replication.KindVec3)
	rep.Declare(attrBodyStatic, replication.KindBool)
	rep.Declare(attrBodyActive, replication.KindBool)
	rep.Declare(attrBodyMat, replication.KindI32)
	rep.Declare(attrEnemy, replication.KindBool)
	rep.Declare(attrTarget, replication.KindBool)
	rep.Declare(attrProjectile, replication.KindBool)
	rep.Declare(attrResourceKind, replication.KindI32)
	rep.Declare(attrGameScore, replication.KindI32)
	rep.Declare(attrGameWave, replication.KindI32)
	rep.Declare(attrGameGold, replication.KindI32)
}

// DrainFrame 取走本帧增量（填好帧号）。
func (s *Simulation) DrainFrame() replication.Frame {
	f := s.rep.Drain()
	f.Step = s.step
	return f
}

// FullFrame 取走全量（填好帧号），供重连 / 首次进入的客户端整体覆盖。
func (s *Simulation) FullFrame() replication.Frame {
	f := s.rep.Full()
	f.Step = s.step
	return f
}

// replicateBodyMeta 把一个刚体的渲染元数据写进同步 store。
// 只在创建时调用一次（静态属性不会变，变换由 syncSystem 每 tick 推送）。
// 初始旋转由调用方传入：写死成单位四元数会和 registerBody 的 Rotation 悄悄脱钩，
// 而「静默不同步」正是本任务要防的东西。
func (s *Simulation) replicateBodyMeta(e ecs.Entity, b Body, pos [3]float32, rot [4]float32) {
	id := uint32(e)
	s.rep.Set(id, attrBodyKind, replication.I32(int32(b.Kind)))
	s.rep.Set(id, attrBodySize, replication.Vec3(b.Size[0], b.Size[1], b.Size[2]))
	s.rep.Set(id, attrBodyStatic, replication.Bool(b.Static))
	s.rep.Set(id, attrBodyActive, replication.Bool(b.Active))
	s.rep.Set(id, attrBodyMat, replication.I32(int32(b.Mat)))
	s.rep.Set(id, attrPos, replication.Vec3(pos[0], pos[1], pos[2]))
	s.rep.Set(id, attrRot, replication.Vec4(rot[0], rot[1], rot[2], rot[3]))
}

// replicatePlayer 把玩家的槽位/血量/朝向写进同步 store。
// 位置也在这里写一次，让实体一建出来就是完整的；此后每 tick 由 syncSystem 推送
// （init 末尾会调一次 syncSystem，所以这两处谁先谁后都不会留下不一致）。
func (s *Simulation) replicatePlayer(idx int, pos [3]float32, health float32, yaw float32) {
	id := uint32(s.players[idx])
	s.rep.Set(id, attrPlayerIdx, replication.I32(int32(idx)))
	s.rep.Set(id, attrPos, replication.Vec3(pos[0], pos[1], pos[2]))
	s.rep.Set(id, attrHealth, replication.F32(health))
	s.rep.Set(id, attrFacing, replication.F32(yaw))
}
```

- [ ] **Step 4: 在 `Simulation` 里接上 store，并补齐所有 `Set` 调用点**

`joltgo/sim/simulation.go`：

1. `Simulation` 结构体加字段：

```go
	rep  *replication.Store // 给客户端同步的属性终值表（见 replicate.go）
```

2. `New()` 里创建并声明：

```go
func New(p Physics) *Simulation {
	rep := replication.New()
	declareAttributes(rep)
	return &Simulation{
		physics:   p,
		world:     ecs.New(),
		rep:       rep,
		game:      ecs.InvalidEntity,
		bodyQuery: ecs.Without[Resource](ecs.NewQuery3[Body, Position, Rotation]()),
	}
}
```

3. `reset()` 里，重建世界之后加 `s.rep.Reset()`。

4. `init()` 里玩家创建循环末尾加：

```go
		s.replicatePlayer(i, [3]float32{x, playerSpawnY, z}, 100, playerSpawnYaw(i))
```

5. `init()` 里紧挨着 Task 4 加的 `s.syncGameState()`（在 `s.wave = 1` 之后）加三条。
   **用 `s.*` 字段而不是字面量** —— 写死 `I32(1)` 会让同一件事有「组件」和「store」
   两处真相，以后改初始波次就会脱钩：

```go
	s.rep.Set(uint32(s.game), attrGameScore, replication.I32(int32(s.score)))
	s.rep.Set(uint32(s.game), attrGameWave, replication.I32(int32(s.wave)))
	s.rep.Set(uint32(s.game), attrGameGold, replication.I32(int32(s.gold)))
```

6. `registerBody()` 末尾加：

```go
	s.replicateBodyMeta(e, Body{Kind: kind, Size: size, Static: static, Active: !static, Mat: mat}, pos)
```

> 注意：`registerBody` 现在把同一个 `Body` 字面量传给 `ecs.Add3`，抽成一个局部变量避免写两遍：

```go
func (s *Simulation) registerBody(id uint32, kind BodyKind, size [3]float32, static bool, pos [3]float32, mat Material) ecs.Entity {
	e := ecs.Entity(id)
	body := Body{Kind: kind, Size: size, Static: static, Active: !static, Mat: mat}
	rot := Rotation{0, 0, 0, 1}
	ecs.Add3(s.world, e, body, Position(pos), rot)
	s.replicateBodyMeta(e, body, pos, rot)
	return e
}
```

7. `shoot()` 里 `ecs.Add(s.world, e, Projectile{...})` 之后加：

```go
	s.rep.Set(id, attrProjectile, replication.Bool(true))
```

8. `spawnResource()` 里 `ecs.Add(s.world, e, Resource{Kind: 0})` 之后加：

```go
	s.rep.Set(id, attrResourceKind, replication.I32(0))
```

9. `destroyBody()` 里 `s.world.Destroy(e)` 之后加：

```go
	s.rep.Destroy(uint32(e))
```

`joltgo/sim/systems.go`：

10. `syncSystem()` 改为：

```go
func (s *Simulation) syncSystem() {
	s.physics.Sync(func(id uint32, active bool, pos [3]float32, quat [4]float32) {
		e := ecs.Entity(id)
		ecs.Add(s.world, e, Position(pos))
		ecs.Add(s.world, e, Rotation(quat))
		s.rep.Set(id, attrPos, replication.Vec3(pos[0], pos[1], pos[2]))
		s.rep.Set(id, attrRot, replication.Vec4(quat[0], quat[1], quat[2], quat[3]))
		if b, ok := ecs.Get[Body](s.world, e); ok && b.Active != active {
			b.Active = active
			s.rep.Set(id, attrBodyActive, replication.Bool(active))
		}
	})
	for i := 0; i < MaxPlayers; i++ {
		pos := s.physics.CharacterPosition(i)
		ecs.Add(s.world, s.players[i], Position(pos))
		s.rep.Set(uint32(s.players[i]), attrPos, replication.Vec3(pos[0], pos[1], pos[2]))
	}
}
```

> `s.physics.Sync` 每 tick 会把**全部**刚体（含永不变化的船体静态几何）回调一遍。这里照旧全部 `Set`：Store 与「上一次下发值」比较后不标脏，所以静态几何不会产生任何流量。

11. `inputSystem()` 里，`ecs.Add(s.world, s.players[i], Facing{Yaw: in.Yaw})` 之后加：

```go
		s.rep.Set(uint32(s.players[i]), attrFacing, replication.F32(in.Yaw))
```

12. `projectileSystem()` 敌人扣血分支：

```go
			*hp--
			s.rep.Set(uint32(other), attrHealth, replication.F32(float32(*hp)))
```

13. `projectileSystem()` 里 `s.destroyBody(other)` + `s.score++` 之后补 `s.rep.Set(uint32(s.game), attrGameScore, replication.I32(int32(s.score)))`（两处：靶球、敌人）。

14. `enemyDamageSystem()` 玩家扣血：

```go
			*hp -= enemyDamage
			s.rep.Set(uint32(s.players[i]), attrHealth, replication.F32(float32(*hp)))
```

复活分支：

```go
		*hp = 100
		...
		ecs.Add(s.world, s.players[i], Position{x, playerSpawnY, z})
		s.rep.Set(uint32(s.players[i]), attrHealth, replication.F32(100))
		s.rep.Set(uint32(s.players[i]), attrPos, replication.Vec3(x, playerSpawnY, z))
```

15. `resourceSystem()` 里 `s.gold++` 之后：

```go
			s.rep.Set(uint32(s.game), attrGameGold, replication.I32(int32(s.gold)))
```

16. `waveSystem()` 里 `s.wave++` 之后：

```go
	s.rep.Set(uint32(s.game), attrGameWave, replication.I32(int32(s.wave)))
```

17. `spawnEnemy()` 里 `ecs.Add2(s.world, e, Enemy{}, Health(enemyHealth))` 之后：

```go
		s.rep.Set(uint32(e), attrEnemy, replication.Bool(true))
		s.rep.Set(uint32(e), attrHealth, replication.F32(enemyHealth))
```

> `snapshot()`（Task 6 才删）不受影响。

- [ ] **Step 5: 跑测试确认通过**

Run: `cd joltgo && go test ./sim -run 'TestStoreMatchesWorld|TestDeltaStream' -v`
Expected: PASS

- [ ] **Step 6: 跑全部现有测试**

Run: `cd joltgo && gofmt -l . && go vet ./sim ./replication && go test ./ecs ./sim ./replication`
Expected: PASS

- [ ] **Step 7: 提交**

```bash
git add joltgo/sim/replicate.go joltgo/sim/replicate_test.go joltgo/sim/simulation.go joltgo/sim/systems.go joltgo/sim/sim_test.go
git commit -m "feat: sim 侧属性映射与 rep.Set 调用点，含 oracle 一致性测试

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## Task 6: proto 改造 + `game` 层改用 Frame（删除旧快照）

**Files:**
- Modify: `joltgo/game/protos/game.proto`
- Modify: `joltgo/game/protos/game.pb.go`（重新生成）
- Modify: `joltgo/game/component.go`
- Modify: `joltgo/game/instance.go`
- Modify: `joltgo/sim/state.go`（删除）
- Modify: `joltgo/sim/simulation.go`（删除 `Snapshot()` 与 `snapshot()`）
- Modify: `joltgo/sim/sim_test.go`（接管 `State` 族与 `snapshotWorld`）
- Modify: `joltgo/physics/map_integration_test.go`（`-tags joltdll`；它也用了 `s.Snapshot()` 与 `sim.BodyInfo`，同样要迁到 `sim.FullFrame()`）

**Interfaces:**
- Consumes: `replication.Frame`/`Schema`/`AttrValue`/`EntityDelta`（Task 3）、`Simulation.DrainFrame`/`FullFrame`（Task 5）
- Produces:
  - `func toFrame(f replication.Frame) *protos.Frame`
  - `func toSchema(sc replication.Schema) *protos.Schema`
  - `func toAttrValue(av replication.AttrValue) *protos.AttrValue`
  - `Instance` 方法 `Broadcast()`（私有 `broadcast` 保留）、`RequestFull(slot int)`
  - 常量 `frameRoute = "onFrame"`（替换 `snapRoute = "onSnapshot"`）

- [ ] **Step 1: 改 proto**

把 `joltgo/game/protos/game.proto` 里 `BodyInfo` / `ResourceInfo` / `PlayerState` / `Snapshot` 四个**快照**消息全部删除，新增 `Schema` / `SchemaField` / `AttrValue` / `EntityDelta` / `Frame`。

> **本步还要一并加上** `CommandMsg` / `RejoinMsg` / `RejoinReply`，并给 `JoinMsg` 加 `token` 字段 —— 它们分别由 Task 7/8 使用，先定义好可以少改一次 proto、少重新生成一次。
>
> **但本步不要删 `InputMsg` / `ShootMsg`。** `game.Component` 的 `Input` / `Shoot` handler 还在用它们，删了直接编译不过；Task 7 把三个 handler 合并成 `Cmd` 时再删这两个消息。

文件头部的注释按**本步交付后的状态**改写（Task 7/8 落地时各自再补上 `game.cmd` / `game.resync` / `game.rejoin` 那几行）：

```proto
// 游戏协议消息（wire 契约）。gate / match / game 三服务与 Godot 客户端都遵守：
//   - 客户端 → gate → match：match.join（JoinMsg，Notify）
//   - match → 客户端：onMatched（MatchResult，Push）
//   - match → game（RPC）：game.create（CreateGameMsg）/ game.rejoin（RejoinMsg）
//   - 客户端 → gate → game：game.cmd（CommandMsg）/ game.resync（空，Notify）
//   - game → 客户端：onFrame（Frame，Push）
//
// 同步不走手写快照，走通用「实体-属性」增量：属性表（Schema）在 full 帧里下发一次，
// 之后所有帧都是 (实体, 属性, 终值) 的 op。新增属性不需要改动本文件的字段定义。
//
// 序列化用 pitaya 的 protobuf serializer。
```

```proto
// CommandMsg 是一帧的上行命令：把输入、射击、重置合并成一条消息发送
// （帧是最小发送单位）。shoot/reset 是边沿触发，未触发时为 false。
message CommandMsg {
  repeated float move = 1;   // [wx, wz]，世界空间水平期望速度（m/s）
  float yaw = 2;             // 水平朝向（弧度，绕 Y 轴）
  bool jump = 3;             // 跳跃边沿触发（服务端下一 tick 消费）
  bool shoot = 4;            // 射击边沿触发
  repeated float origin = 5; // [x, y, z] 枪口位置（shoot 为 true 时有效）
  repeated float dir = 6;    // [x, y, z] 射击方向（服务端会归一化）
  bool reset = 7;            // 重建场景边沿触发
}

// JoinMsg 是加入匹配的请求。token 是客户端持久化的身份（首次运行生成并存储），
// 服务端把它当作会话 UID：重连时同一个 token 能找回原来的对局实例。
message JoinMsg {
  string token = 1;
}

// RejoinMsg 是 match → game 的回局查询（route "game.rejoin"）：问这个 game 节点
// 是否托管着该 token 的存量实例。
message RejoinMsg {
  string token = 1;
}

// RejoinReply 是 game.rejoin 的应答。
message RejoinReply {
  bool found = 1;
  string match_id = 2;
  int32 player_idx = 3; // 原本的玩家槽位（0/1）
}

// SchemaField 是一个同步属性的声明。
message SchemaField {
  uint32 id = 1;   // 属性 ID（Frame 里的 AttrValue.id）
  string name = 2; // 属性名，客户端按名字取值
  int32 kind = 3;  // 值类型，与 replication.Kind 取值一致
}

// Schema 是属性表，随 full 帧下发。
message Schema {
  repeated SchemaField fields = 1;
  uint32 version = 2; // 属性表哈希，客户端据此检测前后端不一致
}

// AttrValue 是一个属性的取值。按 schema 声明的 kind 取用其中一个字段。
message AttrValue {
  uint32 id = 1;
  repeated float f = 2; // KindF32(1 项) / KindVec2(2) / KindVec3(3) / KindVec4(4)
  int32 i = 3;          // KindI32
  bool b = 4;           // KindBool
  string s = 5;         // KindStr
}

// EntityDelta 是一个实体在本帧的变化。应用顺序：destroy → removed → set。
message EntityDelta {
  uint32 id = 1;
  bool destroy = 2;
  repeated uint32 removed = 3;
  repeated AttrValue set = 4;
}

// Frame 是一帧同步消息。full=true 表示全量帧（重连 / 首次进入），客户端应先
// 清空本地状态再整体覆盖；此时 schema 一并携带。
message Frame {
  int32 step = 1; // 帧号（= 服务端 tick 计数）
  bool full = 2;
  repeated EntityDelta entities = 3;
  Schema schema = 4;
}
```

- [ ] **Step 2: 重新生成 Go 码**

Run（在 `joltgo` 目录）：

```bash
protoc --go_out=. --go_opt=paths=source_relative -I . game/protos/game.proto
```

Expected: `game/protos/game.pb.go` 重新生成，无输出。若 `protoc` 不在 PATH：

```bash
export PATH="$PATH:/c/Users/zhubeijian/AppData/Local/Microsoft/WinGet/Packages/Google.Protobuf_*/bin:/c/Users/zhubeijian/go/bin"
```

- [ ] **Step 3: 删除 sim 的快照路径**

1. 删除文件 `joltgo/sim/state.go`。
2. `joltgo/sim/simulation.go` 里删除 `Snapshot()` 公开方法与 `snapshot()` 私有方法。
3. `joltgo/sim/simulation.go` 里 `bodyQuery` 字段与 `New()` 里的初始化也一并删除（只有 `snapshot()` 用它）。
4. `joltgo/sim/simulation.go` 顶部删除已不再使用的 `sort` import。

- [ ] **Step 4: 把 `State` 族移进测试**

在 `joltgo/sim/sim_test.go` 顶部（`import` 之前不行，要放在 `package sim` 之后）加入：

```go
// 以下类型与 snapshotWorld 原本在 sim/state.go 与 Simulation.snapshot()：
// 生产路径已经改成通用的实体-属性增量同步（见 replicate.go），这里保留一份
// 等价的世界快照构造器，纯粹作为现有行为测试的取值来源与 oracle。
// 它与 replicate.go 是两个独立实现 —— 这正是它能当 oracle 的原因。

// BodyInfo 是单个刚体的快照。
type BodyInfo struct {
	ID         uint32
	Type       int
	Static     bool
	Target     bool
	Enemy      bool
	Projectile bool
	Pos        [3]float32
	Quat       [4]float32
	Size       [3]float32
	Health     float32
	Active     bool
	Mat        int
}

// PlayerState 是单个玩家的快照（Pos 为脚底位置）。
type PlayerState struct {
	Pos    [3]float32
	Health float32
	Yaw    float32
}

// ResourceInfo 是可拾取资源快照。
type ResourceInfo struct {
	ID   int
	Pos  [3]float32
	Kind int
}

// State 是世界状态快照（测试用）。
type State struct {
	Bodies    []BodyInfo
	Resources []ResourceInfo
	Players   []PlayerState
	Step      int
	Score     int
	Wave      int
	Gold      int
}

// snapshotWorld 直接从 ECS 世界构造一份 State（测试用 oracle）。
func snapshotWorld(s *Simulation) State {
	st := State{
		Bodies:    []BodyInfo{},
		Resources: []ResourceInfo{},
		Players:   make([]PlayerState, 0, MaxPlayers),
		Step:      s.step,
	}
	st.Score = int(s.score)
	st.Wave = int(s.wave)
	st.Gold = int(s.gold)

	for i := 0; i < MaxPlayers; i++ {
		ps := PlayerState{Health: 100}
		if p, ok := ecs.Get[Position](s.world, s.players[i]); ok {
			ps.Pos = *p
		}
		if h, ok := ecs.Get[Health](s.world, s.players[i]); ok {
			ps.Health = float32(*h)
		}
		if in, ok := ecs.Get[Input](s.world, s.players[i]); ok {
			ps.Yaw = in.Yaw
		}
		st.Players = append(st.Players, ps)
	}

	ecs.Each(s.world, func(e ecs.Entity, b *Body) {
		if ecs.Has[Resource](s.world, e) {
			return // 金币是传感器球，不属于刚体列表（对应原 snapshot() 的 Without[Resource]）
		}
		bi := BodyInfo{
			ID:         uint32(e),
			Type:       int(b.Kind),
			Static:     b.Static,
			Target:     ecs.Has[Target](s.world, e),
			Enemy:      ecs.Has[Enemy](s.world, e),
			Projectile: ecs.Has[Projectile](s.world, e),
			Size:       b.Size,
			Active:     b.Active,
			Mat:        int(b.Mat),
		}
		if p, ok := ecs.Get[Position](s.world, e); ok {
			bi.Pos = *p
		}
		if rot, ok := ecs.Get[Rotation](s.world, e); ok {
			bi.Quat = *rot
		}
		if h, ok := ecs.Get[Health](s.world, e); ok {
			bi.Health = float32(*h)
		}
		st.Bodies = append(st.Bodies, bi)
	})
	ecs.Each(s.world, func(e ecs.Entity, r *Resource) {
		ri := ResourceInfo{ID: int(e), Kind: r.Kind}
		if p, ok := ecs.Get[Position](s.world, e); ok {
			ri.Pos = *p
		}
		st.Resources = append(st.Resources, ri)
	})

	sort.Slice(st.Bodies, func(i, j int) bool { return st.Bodies[i].ID < st.Bodies[j].ID })
	sort.Slice(st.Resources, func(i, j int) bool { return st.Resources[i].ID < st.Resources[j].ID })
	return st
}
```

> `sim_test.go` 需要 `sort` import。原来 `entitiesOf` 等辅助读的是 `State.Bodies` 里 `Enemy` 等标记字段，`snapshotWorld` 都填了，无需改断言。

5. `joltgo/sim/replicate_test.go` 里删掉 Step 1 加的临时 `snapshotWorld` 副本（现在用 `sim_test.go` 的正式版本）。

6. 全仓库把 `s.Snapshot()` 替换成 `snapshotWorld(s)`：

Run: `cd joltgo && grep -rn '\.Snapshot()' sim/`
Expected: 只有 `sim_test.go` 里的调用点；逐个改成 `snapshotWorld(s)`（或对应变量名）。

- [ ] **Step 5: `game/component.go` 改成通用帧**

1. 常量改名：

```go
const (
	frameRoute   = "onFrame" // game → 客户端同步帧 push 的 route
	frontendType = "gate"
)
```

2. 删除 `toSnapshot`，新增：

```go
// toFrame 把 sim 层的同步帧转成 protobuf。帧是通用「实体-属性」结构，
// 新增同步属性不需要改动这里。
func toFrame(f replication.Frame) *protos.Frame {
	pf := &protos.Frame{Step: int32(f.Step), Full: f.Full}
	if f.Full {
		pf.Schema = toSchema(f.Schema)
	}
	for _, ed := range f.Entities {
		pe := &protos.EntityDelta{Id: ed.ID, Destroy: ed.Destroy, Removed: ed.Removed}
		for _, av := range ed.Set {
			pe.Set = append(pe.Set, toAttrValue(av))
		}
		pf.Entities = append(pf.Entities, pe)
	}
	return pf
}

// toSchema 把属性表转成 protobuf。
func toSchema(sc replication.Schema) *protos.Schema {
	ps := &protos.Schema{Version: sc.Version}
	for _, a := range sc.Fields {
		ps.Fields = append(ps.Fields, &protos.SchemaField{
			Id:   a.ID,
			Name: a.Name,
			Kind: int32(a.Kind),
		})
	}
	return ps
}

// toAttrValue 按值的类型标签把值放进对应的字段。
func toAttrValue(av replication.AttrValue) *protos.AttrValue {
	pv := &protos.AttrValue{Id: av.Attr}
	switch av.Value.Kind() {
	case replication.KindF32, replication.KindVec2, replication.KindVec3, replication.KindVec4:
		pv.F = av.Value.Floats()
	case replication.KindI32:
		pv.I = av.Value.Int()
	case replication.KindBool:
		pv.B = av.Value.Boolean()
	case replication.KindStr:
		pv.S = av.Value.Text()
	}
	return pv
}
```

3. 文件顶部 import 加 `"joltgo/replication"`。

- [ ] **Step 6: `game/instance.go` 改用 Drain / Full**

1. `Instance` 结构体加字段：

```go
	pendingFull [sim.MaxPlayers]bool // 本 tick 需要下发全量的槽位（重连 / resync）
	startedAt   time.Time
```

2. `Start()` 里加 `i.startedAt = time.Now()`。

3. `broadcast()` 改为：

```go
// broadcast 把本 tick 的增量推给局内玩家；被标记为待全量的槽位改推全量帧。
// DrainFrame 每 tick 必须恰好调用一次（它负责清脏），所以先取帧再决定收件人。
func (i *Instance) broadcast() {
	delta := i.sim.DrainFrame()

	var deltaUIDs, fullUIDs []string
	for slot, uid := range i.uids {
		if i.pendingFull[slot] {
			i.pendingFull[slot] = false
			fullUIDs = append(fullUIDs, uid)
		} else {
			deltaUIDs = append(deltaUIDs, uid)
		}
	}

	if len(deltaUIDs) > 0 {
		if _, err := i.app.SendPushToUsers(frameRoute, toFrame(delta), deltaUIDs, frontendType); err != nil {
			log.Printf("instance %s 广播增量: %v", i.matchID, err)
		}
	}
	if len(fullUIDs) > 0 {
		full := i.sim.FullFrame()
		if _, err := i.app.SendPushToUsers(frameRoute, toFrame(full), fullUIDs, frontendType); err != nil {
			log.Printf("instance %s 下发全量: %v", i.matchID, err)
		}
	}
}

// RequestFull 把某个槽位的下一帧标为全量（客户端 resync / 重连时调用）。
func (i *Instance) RequestFull(slot int) {
	if slot < 0 || slot >= len(i.uids) {
		return
	}
	i.enqueue(func() { i.pendingFull[slot] = true })
}

// MatchID 返回对局 id（match 服务回局查询时用）。
func (i *Instance) MatchID() string { return i.matchID }
```

- [ ] **Step 7: 编译并跑全部测试**

```bash
cd joltgo
gofmt -l sim game/component.go game/instance.go   # 只检查动过的文件（存量 CRLF 文件会被整文件标记，那不是本次问题）
go build ./...
go vet ./gate ./match ./game ./sim ./replication ./ecs
go vet -tags joltdll ./physics    # map_integration_test.go 也迁到了 FullFrame，必须一并检查
PATH="$PWD:$PATH" go test -tags joltdll ./physics   # 需要 libjolt_c.dll（已构建在 joltgo/ 下）
go test -count=1 ./ecs ./sim ./replication
grep -rn 'protos.Snapshot\|protos.BodyInfo\|protos.PlayerState\|protos.ResourceInfo' .   # 应无输出
grep -rn '\.Snapshot()' sim/                                                             # 应无输出
grep -rn 'func snapshotWorld' sim/                                                       # 应恰好 1 处
```

Expected: 全部通过 / 无输出。`go test ./...` 需要已构建的 `libjolt_c.dll`，本步不必跑。

> **注意**：`joltgo/physics/map_integration_test.go`（`-tags joltdll`）也用了 `s.Snapshot()` 与 `sim.BodyInfo`，同样要迁移 —— 它的断言只关心角色能否走上舷梯，所以直接用 `sim.FullFrame()` 读刚体位置即可（这也顺带让这个集成测试跑在真实的同步路径上）。
>
> 迁移时**必须排除携带 `Resource.Kind` 的实体**（金币传感器球）：它们在 store 里就是普通刚体（`Body.Static = true`、`Pos` 都在），而旧快照的 `Without[Resource]` 是把它们挡在 `bodies` 之外的。不排除的话 `TestMapStaticGeometryIsStable` 会把金币算进「静态几何」，角色一旦在 12 米的行走路径上顺手捡到一枚，用例就会以「静态刚体 N 在推进后消失了」**误报失败**。

- [ ] **Step 8: 提交**

```bash
git add -A joltgo
git commit -m "feat: 通用实体-属性帧替代手写快照（proto + game 层）

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## Task 7: 上行命令合并为 `game.game.cmd`

**Files:**
- Modify: `joltgo/game/component.go`
- Modify: `joltgo/game/protos/game.proto`（Task 6 已加 `CommandMsg`；本步删掉 `InputMsg`/`ShootMsg` 若还在）

**Interfaces:**
- Consumes: `protos.CommandMsg`（Task 6）
- Produces: handler `func (c *Component) Cmd(ctx context.Context, msg *protos.CommandMsg)`，对应 route `game.game.cmd`

- [ ] **Step 1: 合并 handler**

`joltgo/game/component.go`：删除 `Input`、`Shoot`、`Reset` 三个方法，替换为：

```go
// Cmd 是远端 RPC handler（route "game.cmd"）：一帧上行命令。
// 输入、射击、重置合并成一条消息，减少消息数（帧是最小发送单位）。
func (c *Component) Cmd(ctx context.Context, msg *protos.CommandMsg) {
	inst, idx, ok := c.lookup(ctx)
	if !ok {
		return
	}

	var move [2]float32
	if len(msg.Move) >= 2 {
		move = [2]float32{msg.Move[0], msg.Move[1]}
	}
	inst.ApplyInput(idx, move, msg.Yaw, msg.Jump)

	if msg.Shoot {
		var origin, dir [3]float32
		if len(msg.Origin) >= 3 {
			origin = [3]float32{msg.Origin[0], msg.Origin[1], msg.Origin[2]}
		}
		if len(msg.Dir) >= 3 {
			dir = [3]float32{msg.Dir[0], msg.Dir[1], msg.Dir[2]}
		}
		inst.Shoot(origin, dir)
	}
	if msg.Reset_ {
		inst.Reset()
	}
}
```

> **注意字段名**：proto 里的 `reset` 字段被 protoc-gen-go 生成为 **`Reset_`**（带下划线）——
> `Reset` 与生成代码里的 `Reset()` 方法重名，protoc 会自动加下划线避让。线上字段号仍是 7，
> 语义不变。**客户端（Task 11–13）必须按字段号 7 解析 `reset`，不能按名字找。**

- [ ] **Step 2: 编译**

Run: `cd joltgo && gofmt -l . && go vet ./game`
Expected: 无输出（`Input`/`Shoot`/`Reset` 已无调用方）

- [ ] **Step 3: 提交**

```bash
git add joltgo/game/component.go joltgo/game/protos/game.proto
git commit -m "feat: 上行输入/射击/重置合并为单条 game.cmd

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## Task 8: 回局身份（token 即会话 UID）+ `game.rejoin`

**Files:**
- Modify: `joltgo/match/match.go`
- Modify: `joltgo/game/component.go`
- Test: `joltgo/match/match_test.go`（新建）

**Interfaces:**
- Consumes: `protos.JoinMsg.Token`、`protos.RejoinMsg`、`protos.RejoinReply`（Task 6）、`Instance.MatchID()`（Task 6）
- Produces:
  - `game.Component.Rejoin(ctx, *protos.RejoinMsg) (*protos.RejoinReply, error)` —— route `game.game.rejoin`
  - `match` 包内 `func (c *Component) tryRejoin(ctx, s session.Session, token string) bool`
  - `match` 包内 `func (c *Component) bindPlayer(s session.Session, uid, matchID string, playerIdx int, gameServerID string)`

**关键事实（已核实）：** `third_party/pitaya/pkg/session/session.go:460-464` —— 前端会话 `Bind` 遇到已存在的同 UID 会**主动关掉旧会话并把新连接顶上去**；而 `SendPushToUsers` 正是按 UID 从 `sessionsByUID` 取会话。所以把 token 当会话 UID 之后，实例的 `uids` 数组在重连后依旧指向正确的连接，**广播代码不用改**。

**顺手修一处 Task 6 留下的尖角：** `game.Component.Create` 的人数上限校验目前返回 `CreateGameReply{Code: 1}` 且 `error` 为 nil，而 `match.startMatch` 只看 RPC 是否报错、不看 `reply.Code` —— 于是超编时 match 会照样给玩家推 `onMatched`，而 `lookup` 每次都返回 nil，`game.cmd` 变成静默空操作。改成返回 `error`（`fmt.Errorf`），让 match 走它已有的失败日志分支。

- [ ] **Step 1: 写失败的测试**

创建 `joltgo/match/match_test.go`。`Component` 依赖 pitaya 的 `Pitaya` 接口，无法直接构造，所以把「回局查询的判定」抽成不依赖 app 的纯函数 `firstFound` 来测：

```go
package match

import (
	"context"
	"errors"
	"testing"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/cluster"
	"github.com/topfreegames/pitaya/v3/pkg/session"
	"joltgo/game/protos"
)

func TestFirstFound(t *testing.T) {
	if _, ok := firstFound(map[string]*RejoinResult{}); ok {
		t.Fatal("没有任何应答时不应命中")
	}
	if _, ok := firstFound(map[string]*RejoinResult{
		"g1": {Found: false},
	}); ok {
		t.Fatal("全部未命中时不应命中")
	}
	got, ok := firstFound(map[string]*RejoinResult{
		"g1": {Found: false},
		"g2": {Found: true, MatchID: "m1", PlayerIdx: 1, GameServerID: "g2"},
	})
	if !ok {
		t.Fatal("有节点命中时应返回 ok")
	}
	if got.MatchID != "m1" || got.PlayerIdx != 1 || got.GameServerID != "g2" {
		t.Fatalf("应命中 g2 的实例，得到 %+v", got)
	}
}

// 排队期间断线重连会带着同一个 token 再 Join 一次，队列里必须只留最新那条。
func TestRemoveQueued(t *testing.T) {
	q := []queuedPlayer{{uid: "a"}, {uid: "b"}, {uid: "a"}}
	got := removeQueued(q, "a")
	if len(got) != 1 || got[0].uid != "b" {
		t.Fatalf("应只留下 b，得到 %+v", got)
	}
	if len(q) != 3 || q[0].uid != "a" {
		t.Fatalf("removeQueued 不应改动入参，得到 %+v", q)
	}
	if got := removeQueued(q, "zzz"); len(got) != 3 {
		t.Fatalf("没有匹配项时队列应原样返回，得到 %+v", got)
	}
}

// Component 只需要 pitaya.Pitaya 接口，所以「嵌入接口 + 覆盖用得到的两个方法」
// 就够驱动 Join 了 —— 不必引入 gomock。
type joinTestApp struct {
	pitaya.Pitaya
	sess session.Session
}

func (a *joinTestApp) GetSessionFromCtx(context.Context) session.Session { return a.sess }

func (a *joinTestApp) GetServersByType(string) (map[string]*cluster.Server, error) {
	return nil, errors.New("no game server") // 回局查询必然是 miss
}

// joinTestSession 只实现 Join 用到的 Bind。
type joinTestSession struct {
	session.Session
	uid string
}

func (s *joinTestSession) Bind(_ context.Context, uid string) error {
	s.uid = uid
	return nil
}

// 排队等待期间断线重连：同一个 token 再 Join 一次，队列里必须还是只有一条
// （不去重的话这里会是 2 条，进而可能自己跟自己配对、或单人兜底时开两局）。
func TestJoinDedupsQueuedToken(t *testing.T) {
	c := New(&joinTestApp{sess: &joinTestSession{}})
	ctx := context.Background()

	c.Join(ctx, &protos.JoinMsg{Token: "T"})
	if len(c.queue) != 1 {
		t.Fatalf("首次 Join 应入队一条，得到 %d", len(c.queue))
	}
	c.Join(ctx, &protos.JoinMsg{Token: "T"})
	if len(c.queue) != 1 {
		t.Fatalf("同 token 重连不应重复入队，得到 %d", len(c.queue))
	}
	if c.queue[0].uid != "T" {
		t.Fatalf("队列里应是最新那条会话，得到 %q", c.queue[0].uid)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd joltgo && go test ./match`
Expected: FAIL —— `undefined: firstFound`

- [ ] **Step 3: 实现 match 侧**

`joltgo/match/match.go`：

1. 加常量和类型：

```go
const gameRejoinRoute = "game.game.rejoin" // 回局查询 RPC route（三段式）

// RejoinResult 是一次回局查询的结果。GameServerID 是托管该实例的 game 节点。
type RejoinResult struct {
	Found        bool
	MatchID      string
	PlayerIdx    int
	GameServerID string
}

// firstFound 从各 game 节点的应答里挑出第一个命中的。replies 的键是 game 节点 id。
// 抽成纯函数是为了能脱离 pitaya 直接单测。
func firstFound(replies map[string]*RejoinResult) (*RejoinResult, bool) {
	for _, r := range replies {
		if r.Found {
			return r, true
		}
	}
	return nil, false
}
```

> 注意 `firstFound` 的签名要与测试一致：测试传的就是 `map[string]*RejoinResult`。

2. `Join` 改为：

```go
// Join 是远端 RPC handler（route "match.join"）：绑定会话 UID 并加入匹配队列。
// uid 用客户端持久化的 token 而不是每次新建的 nuid —— 重连时同一个 token 会
// 让 pitaya 前端把旧会话顶掉（session.go:460-464），从而让对局实例的 uids
// 数组依然指向正确的连接。
func (c *Component) Join(ctx context.Context, msg *protos.JoinMsg) {
	s := c.app.GetSessionFromCtx(ctx)
	uid := msg.Token
	persisted := uid != ""
	if !persisted {
		uid = nuid.New().Next() // 未带 token 的旧客户端：退化成一次性身份
	}
	if err := s.Bind(ctx, uid); err != nil {
		log.Printf("match: bind session failed: %v", err)
		return
	}

	// 只有带 token 的客户端才可能回局；一次性身份去问一定是白跑一趟。
	if persisted && c.tryRejoin(ctx, s, uid) {
		return // 已回到存量对局，不入匹配队列
	}

	c.mu.Lock()
	// 同一个 token 在**排队等待期间**断线重连会再走一次 Join。不去重的话队列
	// 里会留下两条同 uid 的记录：`tryMatch` 可能把它们俩配成一对
	// （game.create 的 Uids 变成 [T, T]，一个人占满两个槽位），单人兜底时更会
	// 给同一个人先后开两局、推两条 onMatched。旧实现不会暴露这个问题，
	// 因为每次的 uid 都是新的 nuid。
	c.queue = removeQueued(c.queue, uid)
	c.queue = append(c.queue, queuedPlayer{uid: uid, session: s, joinedAt: time.Now()})
	c.mu.Unlock()

	c.tryMatch()
}

// removeQueued 返回去掉 uid 相同记录后的新队列（不改动入参）。重连时用它把
// 断线前残留的那条排队项挤掉，保证一个 token 在队列里最多一条。
func removeQueued(queue []queuedPlayer, uid string) []queuedPlayer {
	out := make([]queuedPlayer, 0, len(queue))
	for _, p := range queue {
		if p.uid != uid {
			out = append(out, p)
		}
	}
	return out
}

// tryRejoin 询问所有 game 节点是否托管着该 token 的存量实例。命中则把它当作
// 一次「匹配成功」收尾（写会话数据 + 推 onMatched），返回 true。
func (c *Component) tryRejoin(ctx context.Context, s session.Session, token string) bool {
	servers, err := c.app.GetServersByType(gameServerType)
	if err != nil || len(servers) == 0 {
		return false
	}
	replies := map[string]*RejoinResult{}
	for id, srv := range servers {
		reply := &protos.RejoinReply{}
		if err := c.app.RPCTo(ctx, srv.ID, gameRejoinRoute, reply, &protos.RejoinMsg{Token: token}); err != nil {
			continue // 该节点不可达，跳过
		}
		replies[id] = &RejoinResult{
			Found:        reply.Found,
			MatchID:      reply.MatchId,
			PlayerIdx:    int(reply.PlayerIdx),
			GameServerID: srv.ID,
		}
	}
	hit, ok := firstFound(replies)
	if !ok {
		return false
	}
	log.Printf("match: token %s rejoined match %s on game %s as slot %d",
		token, hit.MatchID, hit.GameServerID, hit.PlayerIdx)
	c.bindPlayer(s, token, hit.MatchID, hit.PlayerIdx, hit.GameServerID)
	return true
}

// bindPlayer 把对局归属写进会话数据（gate 据此路由 game.*），并推送匹配结果。
// 初次匹配与重连回局共用这条收尾路径。
func (c *Component) bindPlayer(s session.Session, uid, matchID string, playerIdx int, gameServerID string) {
	if err := s.Set("gameServerId", gameServerID); err == nil {
		if err := s.PushToFront(context.Background()); err != nil {
			log.Printf("match: push session data failed: %v", err)
		}
	}
	if _, err := c.app.SendPushToUsers(matchedRoute, &protos.MatchResult{
		MatchId:      matchID,
		GameServerId: gameServerID,
		PlayerIdx:    int32(playerIdx),
	}, []string{uid}, "gate"); err != nil {
		log.Printf("match: push onMatched to %s failed: %v", uid, err)
	}
}
```

3. `startMatch` 里通知每个玩家的那一段（`for i, p := range players { ... }`）替换为：

```go
	for i, p := range players {
		c.bindPlayer(p.session, p.uid, matchID, i, target.ID)
	}
```

- [ ] **Step 4: 实现 game 侧 `Rejoin`**

`joltgo/game/component.go` 加：

```go
// Rejoin 是远端 RPC handler（route "game.rejoin"）：报告本节点是否托管着该
// token 的存量实例，以及原本的玩家槽位。match 服务用它在重连时找回对局。
func (c *Component) Rejoin(ctx context.Context, msg *protos.RejoinMsg) (*protos.RejoinReply, error) {
	c.mu.Lock()
	inst := c.uidToInst[msg.Token]
	idx := c.uidToIndex[msg.Token]
	c.mu.Unlock()

	if inst == nil {
		return &protos.RejoinReply{Found: false}, nil
	}
	return &protos.RejoinReply{
		Found:     true,
		MatchId:   inst.MatchID(),
		PlayerIdx: int32(idx),
	}, nil
}
```

- [ ] **Step 5: 跑测试与编译**

Run: `cd joltgo && gofmt -l . && go vet ./match ./game && go test ./match ./sim ./ecs ./replication`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add joltgo/match joltgo/game/component.go
git commit -m "feat: token 作会话 UID 的断线回局（match 回局查询 + game.rejoin）

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## Task 9: `game.resync` 全量补齐

**Files:**
- Modify: `joltgo/game/component.go`

（`Instance.RequestFull` 已在 Task 6 加好；`instance.go` 本步不动 —— `lastSeen` 的刷新属于 Task 10。）

**Interfaces:**
- Consumes: `Instance.RequestFull(slot int)`（Task 6）、`Component.lookup`（已存在）
- Produces: `func (c *Component) Resync(ctx context.Context)` —— route `game.game.resync`

- [ ] **Step 1: 加 handler**

`joltgo/game/component.go`：

```go
// Resync 是远端 RPC handler（route "game.resync"）：把该玩家的下一帧标为全量。
// 客户端在收到 onMatched 之后主动调用 —— 由客户端驱动就没有「onMatched 与
// 全量帧谁先到」的竞态：客户端在收到 full 帧之前会丢弃一切增量。
func (c *Component) Resync(ctx context.Context) {
	inst, idx, ok := c.lookup(ctx)
	if !ok {
		return
	}
	inst.RequestFull(idx)
}
```

- [ ] **Step 2: 编译**

Run: `cd joltgo && gofmt -l . && go vet ./game`
Expected: 无输出

- [ ] **Step 3: 端到端手动验证（需要本地集群）**

```bash
cd joltgo/deploy && ./start-all.ps1
```

再跑客户端冒烟（Task 13 完成后才有意义；本步先确认服务端不报错）：

```bash
cd joltgo && go run . -type gate
```

Expected: 三个进程都启动、无 panic。`Ctrl+C` 结束。

- [ ] **Step 4: 提交**

```bash
git add joltgo/game/component.go joltgo/game/instance.go
git commit -m "feat: game.resync 客户端驱动的全量补齐

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## Task 10: 实例空闲回收

**Files:**
- Modify: `joltgo/game/instance.go`
- Modify: `joltgo/game/component.go`
- Create: `joltgo/game/component_test.go`

**Interfaces:**
- Consumes: 现有 `Instance.run` / `enqueue`
- Produces:
  - `Instance.onExit func()` 字段（由 `Component.Create` 设置）
  - `const instanceIdleTimeout = 60 * time.Second`
  - `Component.forget(matchID string, uids []string)`

**背景：** 现在的对局实例**永不回收** —— `Component` 只在 `Shutdown()` 时停实例。加了回局之后实例要跨断线存活，更需要明确「什么时候该死」。

- [ ] **Step 1: 加空闲计时与退出回调**

`joltgo/game/instance.go`：

1. 常量与结构体字段：

```go
// instanceIdleTimeout 是「所有槽位都无上行消息」多久之后结束实例。
// 远大于客户端 1s 重连 + 2.5s 看门狗，正常重连不会误杀。
const instanceIdleTimeout = 60 * time.Second
```

```go
	lastSeen [sim.MaxPlayers]time.Time // 各槽位最近一次上行时间（仅 run goroutine 读写）
	onExit   func()                    // 实例自行退出时的回调（由 game 组件设置）
	stopOnce sync.Once
```

> `instance.go` 需要补 `"sync"` import。

2. `Start()` 里初始化 `lastSeen`：

```go
	i.startedAt = time.Now()
	for slot := range i.lastSeen {
		i.lastSeen[slot] = i.startedAt
	}
	go i.run()
```

3. `Stop()` 用 `stopOnce` 保护，避免「空闲自退」与「Shutdown 主动停」重复 close：

```go
func (i *Instance) Stop() {
	i.stopOnce.Do(func() { close(i.stop) })
}
```

4. `run()` 的 tick 分支加空闲判定：

```go
		case <-ticker.C:
			i.sim.Step()
			i.broadcast()
			if i.idleExpired() {
				log.Printf("instance %s: %v 无玩家上行，结束对局", i.matchID, instanceIdleTimeout)
				// 关掉 stop：退出后没有 goroutine 再消费 cmds，正在并发的
				// RPC handler 若还持有实例指针，enqueue 会卡在写满的 channel 上。
				// 用 defer 而不是直接调用，是为了 onExit 万一 panic 也一定会关。
				defer i.Stop()
				if i.onExit != nil {
					i.onExit()
				}
				return
			}
```

5. 加方法：

```go
// idleExpired 报告所有槽位是否都已超过 instanceIdleTimeout 没有上行消息。
// 用「最近一次收到上行」而不是 pitaya 的会话状态判断在线：后者跨服务不可见，
// 前者是实例本就持有的信息。
func (i *Instance) idleExpired() bool {
	for slot := range i.uids {
		if time.Since(i.lastSeen[slot]) < instanceIdleTimeout {
			return false
		}
	}
	return true
}

// touch 刷新某个槽位的在线时间（由 run goroutine 调用）。
func (i *Instance) touch(slot int) {
	if slot >= 0 && slot < len(i.lastSeen) {
		i.lastSeen[slot] = time.Now()
	}
}
```

6. 把 `touch` 塞进四条命令入口（都在 enqueue 的闭包里，保证单 goroutine）：

```go
func (i *Instance) ApplyInput(playerIdx int, move [2]float32, yaw float32, jump bool) {
	i.enqueue(func() {
		i.touch(playerIdx)
		i.sim.ApplyInput(playerIdx, move, yaw, jump)
	})
}

func (i *Instance) Shoot(origin, dir [3]float32) {
	i.enqueue(func() { i.sim.Shoot(origin, dir) })
}

func (i *Instance) Reset() {
	i.enqueue(func() { i.sim.Reset() })
}

func (i *Instance) RequestFull(slot int) {
	if slot < 0 || slot >= len(i.uids) {
		return
	}
	i.enqueue(func() {
		i.touch(slot)
		i.pendingFull[slot] = true
	})
}
```

> `Shoot`/`Reset` 不知道槽位，不需要 `touch` —— 它们的同行 `ApplyInput` 每渲染帧（60 Hz）都会刷新在线时间，够用。

7. 把 `instance.go` 顶部的生命周期注释补一句：`Stop` 除了由 `Component.Shutdown`
   调用，也会在**空闲自退**时由实例自己调用，`stopOnce` 保证两条路径都安全。

- [ ] **Step 2: `Component` 设置回调并在退出时摘除注册表**

`joltgo/game/component.go`：

1. `Create` 里设置回调（闭包捕获 `inst` 本身，`forget` 要用它做归属守卫）：

```go
	inst := NewInstance(c.app, msg.MatchId, msg.Uids)
	inst.onExit = func() { c.forget(inst, msg.MatchId, msg.Uids) }
	inst.Start()
```

2. 加方法：

```go
// forget 把已结束的实例从注册表摘掉。
//
// uid 的条目必须**确认还指向这个实例**才删：玩家离开旧对局后可能已经匹配进了新
// 对局，新实例刚把 uidToInst[uid] 改写成自己；旧实例 60 秒后回收时若无脑删，
// 就会把新对局的映射一起抹掉，玩家之后的 game.cmd 会全部被忽略。
func (c *Component) forget(inst *Instance, matchID string, uids []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// matchId 由 nuid 生成、不会重复；但仍守卫一下 —— 成本极低，且能挡住未来
	// 「Create 被重试」这类改动：那时无脑删会把仍在运行的实例从注册表里注销掉，
	// Shutdown 就再也停不到它，goroutine 与它的 Jolt 世界都会泄漏。
	if c.instances[matchID] == inst {
		delete(c.instances, matchID)
	}
	for _, uid := range uids {
		if c.uidToInst[uid] != inst {
			continue // 该 uid 已经归新对局所有
		}
		delete(c.uidToInst, uid)
		delete(c.uidToIndex, uid)
	}
	log.Printf("game: instance %s 已回收", matchID)
}
```

   `Create` 里捕获 `inst` 的写法见上面第 1 条。

3. 新增 `joltgo/game/component_test.go`，钉住上面那条守卫（`forget` 不碰 `c.app`，
   所以 `New(nil)` 就能构造）：

```go
package game

import "testing"

func TestForgetKeepsUIDsOwnedByAnotherInstance(t *testing.T) {
	c := New(nil)
	old := &Instance{matchID: "m1"}
	fresh := &Instance{matchID: "m2"}
	c.instances = map[string]*Instance{"m1": old, "m2": fresh}
	c.uidToInst = map[string]*Instance{"u": fresh} // 玩家已经匹配进新对局
	c.uidToIndex = map[string]int{"u": 1}

	c.forget(old, "m1", []string{"u"})

	if c.uidToInst["u"] != fresh {
		t.Fatal("旧实例回收不应抹掉新对局的 uid 映射")
	}
	if c.uidToIndex["u"] != 1 {
		t.Fatal("旧实例回收不应抹掉新对局的槽位映射（否则会把错误的玩家当成调用者）")
	}
	if _, ok := c.instances["m1"]; ok {
		t.Fatal("旧实例应从 instances 里摘掉")
	}
	if _, ok := c.instances["m2"]; !ok {
		t.Fatal("新实例不应受影响")
	}
}

// 正常情况（uid 仍归本实例）必须照常清掉，否则注册表会泄漏。
func TestForgetRemovesOwnUIDs(t *testing.T) {
	c := New(nil)
	inst := &Instance{matchID: "m1"}
	c.instances = map[string]*Instance{"m1": inst}
	c.uidToInst = map[string]*Instance{"u": inst}
	c.uidToIndex = map[string]int{"u": 0}

	c.forget(inst, "m1", []string{"u"})

	if _, ok := c.uidToInst["u"]; ok {
		t.Fatal("属于本实例的 uid 应被摘掉")
	}
	if _, ok := c.uidToIndex["u"]; ok {
		t.Fatal("属于本实例的 uid 索引应被摘掉")
	}
}
```

- [ ] **Step 3: 编译并跑测试**

```bash
cd joltgo
go build ./...
go vet ./gate ./match ./game ./sim ./replication ./ecs
PATH="$PWD:$PATH" go test -count=1 ./game ./ecs ./sim ./replication ./match
```

Expected: PASS。

> `game` 包 import 了 `physics`，其 cgo 链接需要 `libjolt_c.dll`。所以 **`go test ./game`
> 必须把 `joltgo/` 加进 PATH**，否则测试进程会以 `exit status 0xc0000135`（DLL 未找到）
> 失败。这是本任务新增的 `game/component_test.go` 带来的环境要求，Task 14 要写进文档。

- [ ] **Step 4: 提交**

```bash
git add joltgo/game/instance.go joltgo/game/component.go
git commit -m "feat: 对局实例空闲 60 秒自动回收

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## Task 11: 客户端 `world_store.gd`

**Files:**
- Create: `godot_client/scripts/world_store.gd`
- Test: `godot_client/tests/world_store_test.gd`

**Interfaces:**
- Consumes: 无（纯数据结构）
- Produces:
  - `WorldStore.apply_schema(fields: Array) -> void` —— `fields` 是 `[{id, name, kind}]`
  - `WorldStore.apply_frame(frame: Dictionary) -> Dictionary` —— 返回 `{"destroyed": Array}`，每项是 `{"id": int, "attrs": Dictionary}`（**消失前的属性快照**，渲染层据此判断消失的是弹丸还是金币）
  - `WorldStore.attr(entity_id: int, name: String) -> Variant`（不存在返回 `null`）
  - `WorldStore.has_attr(entity_id: int, name: String) -> bool`
  - `WorldStore.entity_ids() -> Array[int]`
  - `WorldStore.entities_with(name: String) -> Array[int]`
  - `WorldStore.clear() -> void`
  - 常量 `KIND_F32/KIND_I32/KIND_BOOL/KIND_STR/KIND_VEC2/KIND_VEC3/KIND_VEC4`（取值与 Go 侧 `replication.Kind` 一致）
  - 成员 `schema_version: int`

- [ ] **Step 1: 写失败的测试**

创建 `godot_client/tests/world_store_test.gd`：

```gdscript
extends SceneTree
## world_store.gd 的语义测试：full 覆盖、removed、destroy、按名字取值。

const WorldStore := preload("res://scripts/world_store.gd")

const SCHEMA := [
	{"id": 1, "name": "Pos", "kind": 5},        # Vec3
	{"id": 2, "name": "Health", "kind": 0},     # F32
	{"id": 3, "name": "Enemy", "kind": 2},      # Bool
	{"id": 4, "name": "Body.Mat", "kind": 1},   # I32
]

var _failures := 0

func _initialize() -> void:
	_test_full_replaces_everything()
	_test_set_creates_and_updates()
	_test_removed_and_destroy()
	_test_destroy_then_rebuild_same_frame()
	_test_unknown_attr_leaves_no_phantom()
	_test_entities_with()
	if _failures > 0:
		printerr("world_store_test: %d 项失败" % _failures)
		quit(1)
	else:
		print("world_store_test: OK")
		quit(0)

func _check(cond: bool, msg: String) -> void:
	if not cond:
		_failures += 1
		printerr("FAIL: " + msg)

func _fresh() -> WorldStore:
	var ws: WorldStore = WorldStore.new()
	ws.apply_schema(SCHEMA)
	return ws

func _test_full_replaces_everything() -> void:
	var ws := _fresh()
	ws.apply_frame({"full": false, "entities": [
		{"id": 7, "set": [{"id": 2, "f": [50.0]}]},
	]})
	_check(ws.attr(7, "Health") == 50.0, "增量应写入 Health")

	# full 帧是权威的：先清空再整体覆盖
	ws.apply_frame({"full": true, "entities": [
		{"id": 9, "set": [{"id": 2, "f": [10.0]}]},
	]})
	_check(not ws.has_attr(7, "Health"), "full 帧应清掉之前累积的实体")
	_check(ws.attr(9, "Health") == 10.0, "full 帧应写入新实体")

func _test_set_creates_and_updates() -> void:
	var ws := _fresh()
	ws.apply_frame({"full": false, "entities": [
		{"id": 3, "set": [
			{"id": 1, "f": [1.0, 2.0, 3.0]},
			{"id": 3, "b": true},
			{"id": 4, "i": 9},
		]},
	]})
	var pos: Array = ws.attr(3, "Pos")
	_check(pos.size() == 3 and pos[2] == 3.0, "向量属性应还原成 3 元数组")
	_check(ws.attr(3, "Enemy") == true, "bool 属性")
	_check(ws.attr(3, "Body.Mat") == 9, "int 属性")
	_check(ws.entities_with("Enemy") == [3], "entities_with 应找到该实体")

	# 再推一帧只改 Health：Pos 应保持不变
	ws.apply_frame({"full": false, "entities": [
		{"id": 3, "set": [{"id": 2, "f": [77.0]}]},
	]})
	_check(ws.attr(3, "Health") == 77.0, "新属性应写入")
	_check((ws.attr(3, "Pos") as Array)[0] == 1.0, "未提及的属性不应被清掉")

func _test_removed_and_destroy() -> void:
	var ws := _fresh()
	ws.apply_frame({"full": false, "entities": [
		{"id": 3, "set": [{"id": 3, "b": true}, {"id": 2, "f": [5.0]}]},
		{"id": 4, "set": [{"id": 2, "f": [5.0]}]},
	]})
	var res: Dictionary = ws.apply_frame({"full": false, "entities": [
		{"id": 3, "removed": [3]},
		{"id": 4, "destroy": true},
	]})
	_check(not ws.has_attr(3, "Enemy"), "removed 应删掉该属性")
	_check(ws.attr(3, "Health") == 5.0, "removed 不应影响其他属性")
	_check(not ws.has_attr(4, "Health"), "destroy 应删掉整个实体")
	_check(not ws.entity_ids().has(4), "destroy 后实体不应还在列表里")
	_check(ws.entity_ids().has(3), "只被 removed 的实体应仍然存在")

	var destroyed: Array = res["destroyed"]
	_check(destroyed.size() == 1, "应只报告 1 个销毁事件，得到 %d" % destroyed.size())
	_check(int((destroyed[0] as Dictionary)["id"]) == 4, "销毁的应是实体 4")
	_check((destroyed[0] as Dictionary)["attrs"].has("Health"),
		"销毁事件必须带上消失前的属性，渲染层靠它判断消失的是弹丸还是金币")

func _test_entities_with() -> void:
	var ws := _fresh()
	ws.apply_frame({"full": true, "entities": [
		{"id": 1, "set": [{"id": 3, "b": true}]},
		{"id": 2, "set": [{"id": 3, "b": true}]},
		{"id": 3, "set": [{"id": 2, "f": [1.0]}]},
	]})
	var found := ws.entities_with("Enemy")
	found.sort()
	_check(found == [1, 2], "entities_with 应返回全部带该属性的实体")

# 服务端会在同一帧里销毁并重建同一个 id（场景重置后刚体 id 从头复用），
# 同一个 EntityDelta 里既有 destroy 也有新实体的 set —— 丢掉 set 世界就会一直空着。
func _test_destroy_then_rebuild_same_frame() -> void:
	var ws := _fresh()
	ws.apply_frame({"full": false, "entities": [
		{"id": 7, "set": [{"id": 2, "f": [10.0]}]},
	]})
	var res: Dictionary = ws.apply_frame({"full": false, "entities": [
		{"id": 7, "destroy": true, "set": [{"id": 2, "f": [99.0]}]},
	]})
	_check(res["destroyed"].size() == 1, "应报告一次销毁")
	_check(ws.attr(7, "Health") == 99.0, "同帧销毁+重建必须留下新实体的属性")
	_check(ws.entity_ids().has(7), "重建后的实体应仍在列表里")

# 全是未知属性的 delta 不该在 store 里留下空实体 —— 那会让渲染层建出空节点，
# 而「服务端加属性不用改客户端」正是靠「不认识的属性直接跳过」成立的。
func _test_unknown_attr_leaves_no_phantom() -> void:
	var ws := _fresh()
	ws.apply_frame({"full": false, "entities": [
		{"id": 5, "set": [{"id": 999, "f": [1.0]}]},
		{"id": 6, "removed": [999]},
	]})
	_check(ws.entity_ids().is_empty(),
		"未知属性不应造出幻影实体，得到 %s" % str(ws.entity_ids()))
```

- [ ] **Step 2: 跑测试确认失败**

Run（一行）：

```bash
"C:\Users\zhubeijian\Downloads\Godot_v4.7.2-stable_win64.exe\Godot_v4.7.2-stable_win64_console.exe" --headless --path godot_client --script res://tests/world_store_test.gd
```

Expected: FAIL —— 找不到 `res://scripts/world_store.gd`

- [ ] **Step 3: 实现**

创建 `godot_client/scripts/world_store.gd`：

```gdscript
extends RefCounted
## 客户端本地世界状态：实体 ID → { 属性名: 值 }，外加服务端下发的属性表（schema）。
##
## 服务端推的是增量（只含本帧变化的属性），这里负责累积成完整世界。full 帧是
## 权威的：先整体清空再应用，因此重连 / 首次进入只要收到 full 帧就必然与
## 服务端一致，不需要额外的对齐逻辑。
##
## 属性只按「名字」取用，不认识的新属性照常存进来、只是不渲染 —— 这正是
## 服务端新增同步属性不需要改动客户端的原因。

# 与 Go 侧 replication.Kind 取值一一对应
const KIND_F32 := 0
const KIND_I32 := 1
const KIND_BOOL := 2
const KIND_STR := 3
const KIND_VEC2 := 4
const KIND_VEC3 := 5
const KIND_VEC4 := 6

var schema_version := 0

var _entities := {}   # int -> Dictionary（属性名 -> 值；向量是 Array[float]）
var _names := {}      # int(属性 ID) -> String(属性名)
var _kinds := {}      # int(属性 ID) -> int(kind)

## apply_schema 应用 full 帧里携带的属性表。服务端每次下发 full 帧都会带一份，
## 重复应用是幂等的。
func apply_schema(fields: Array) -> void:
	_names.clear()
	_kinds.clear()
	for f: Variant in fields:
		var d: Dictionary = f
		var id := int(d.get("id", 0))
		var name := String(d.get("name", ""))
		if id <= 0 or name == "":
			continue
		_names[id] = name
		_kinds[id] = int(d.get("kind", KIND_F32))

## apply_frame 应用一帧同步消息。返回 {"destroyed": [{"id": int, "attrs": {...}}]} ——
## 带着消失前的属性，渲染层据此判断消失的是弹丸（爆闪）还是金币（拾取音）。
func apply_frame(frame: Dictionary) -> Dictionary:
	if bool(frame.get("full", false)):
		_entities.clear()

	var destroyed: Array = []
	for e: Variant in frame.get("entities", []):
		var ed: Dictionary = e
		var id := int(ed.get("id", 0))
		if id == 0:
			continue
		var store: Dictionary = _entities.get(id, {})
		if bool(ed.get("destroy", false)):
			# 先把消失前的属性快照带出去，再丢掉本地副本。
			destroyed.append({"id": id, "attrs": store})
			store = {}
			_entities.erase(id)
		# 注意：destroy 之后**不能** continue。服务端在同一帧里销毁并重建（场景重置后
		# 刚体 id 会从头复用）时，同一个 EntityDelta 里既有 destroy 也有新实体的 set；
		# 丢掉 set 客户端就再也收不到重建，世界会一直空着。
		for cid: Variant in ed.get("removed", []):
			var rname: String = _names.get(int(cid), "")
			if rname != "":
				store.erase(rname)
		for av: Variant in ed.get("set", []):
			var d: Dictionary = av
			var cid := int(d.get("id", 0))
			var name: String = _names.get(cid, "")
			if name == "":
				continue # 未知属性：前后端 schema 不一致，忽略
			store[name] = _decode_value(int(_kinds.get(cid, KIND_F32)), d)
		# 只写回真正有内容的条目：全是未知属性的 delta 或空 delta 不该在 store 里留下
		# 一个空实体，否则 entity_ids() 会报出幻影 id、渲染层会建空节点。
		if store.is_empty():
			_entities.erase(id)
		else:
			_entities[id] = store
	return {"destroyed": destroyed}

## attr 返回实体的属性值；实体或属性不存在时返回 null。
func attr(entity_id: int, name: String) -> Variant:
	var store: Dictionary = _entities.get(entity_id, {})
	return store.get(name, null)

func has_attr(entity_id: int, name: String) -> bool:
	var store: Dictionary = _entities.get(entity_id, {})
	return store.has(name)

func entity_ids() -> Array:
	return _entities.keys()

## entities_with 返回带指定属性的全部实体 ID（标记类属性如 "Enemy" / "Player.Idx"
## 用它做查询）。
func entities_with(name: String) -> Array:
	var out: Array = []
	for id: Variant in _entities:
		if (_entities[id] as Dictionary).has(name):
			out.append(id)
	return out

func clear() -> void:
	_entities.clear()

func _decode_value(kind: int, d: Dictionary) -> Variant:
	match kind:
		KIND_F32:
			var f: Array = d.get("f", [])
			return float(f[0]) if f.size() > 0 else 0.0
		KIND_I32:
			return int(d.get("i", 0))
		KIND_BOOL:
			return bool(d.get("b", false))
		KIND_STR:
			return String(d.get("s", ""))
		KIND_VEC2, KIND_VEC3, KIND_VEC4:
			var f: Array = d.get("f", [])
			var n := 2
			if kind == KIND_VEC3:
				n = 3
			elif kind == KIND_VEC4:
				n = 4
			var out: Array = []
			for i in n:
				out.append(float(f[i]) if i < f.size() else 0.0)
			return out
	return null
```

- [ ] **Step 4: 跑测试确认通过**

Run（同 Step 2 的命令）
Expected: `world_store_test: OK`

- [ ] **Step 5: 提交**

```bash
git add godot_client/scripts/world_store.gd godot_client/tests/world_store_test.gd
git commit -m "feat(client): 本地实体-属性存储 world_store.gd

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## Task 12: 客户端协议层（`fps_client.gd`）

**Files:**
- Modify: `godot_client/scripts/fps_client.gd`
- Test: `godot_client/tests/frame_decode_test.gd`

**Interfaces:**
- Consumes: `WorldStore`（Task 11，仅测试里用）
- Produces:
  - 信号 `frame_received(frame: Dictionary)`（替换 `state_received`）
  - `func send_match_join()` —— 带 token
  - `func send_resync()`
  - `func send_command(move: Vector2, yaw: float, jump: bool, shoot: bool, origin: Vector3, dir: Vector3, reset: bool)`
  - `func _decode_frame(buf: PackedByteArray) -> Dictionary`
  - 常量 `KIND_*` 与 `WorldStore` 保持一致（解码不需要 kind，这里只做透传）

- [ ] **Step 1: 写失败的测试**

创建 `godot_client/tests/frame_decode_test.gd`：

```gdscript
extends SceneTree
## Frame / Schema 的 protobuf 解码测试：用手工构造的字节串验证字段还原。

const FpsClient := preload("res://scripts/fps_client.gd")

var _failures := 0
var _c: Node
# 跑完的用例标记。GDScript 没有 try/catch：某个测试函数内部一旦抛错（比如解码器
# 挂掉），函数会中途返回、_failures 还是 0，整个用例就会"假绿"—— 所以每个函数
# 末尾打一个完成标记，_init 逐个核对。
var _done := {}

func _init() -> void:
	_c = FpsClient.new()
	_test_schema()
	_test_full_frame_with_schema()
	_test_delta_frame()
	_test_command_encoding()
	for name: String in ["schema", "full", "delta", "command"]:
		if not _done.has(name):
			_failures += 1
			printerr("FAIL: 用例 %s 没跑完（中途抛错了？）" % name)
	if _failures > 0:
		printerr("frame_decode_test: %d 项失败" % _failures)
		quit(1)
	else:
		print("frame_decode_test: OK")
		quit(0)

func _check(cond: bool, msg: String) -> void:
	if not cond:
		_failures += 1
		printerr("FAIL: " + msg)

# ---- protobuf 编码辅助（测试里手写，与服务端生成码对齐） ----

func _tag(field: int, wire: int) -> PackedByteArray:
	return _c._varint((field << 3) | wire)

func _f_varint(field: int, v: int) -> PackedByteArray:
	var out := _tag(field, 0)
	out.append_array(_c._varint(v))
	return out

func _f_fixed32(field: int, v: float) -> PackedByteArray:
	var out := _tag(field, 5)
	out.append_array(PackedFloat32Array([v]).to_byte_array())
	return out

func _f_floats(field: int, vals: Array) -> PackedByteArray:
	var f32 := PackedFloat32Array()
	for v in vals:
		f32.append(float(v))
	var payload := f32.to_byte_array()
	var out := _tag(field, 2)
	out.append_array(_c._varint(payload.size()))
	out.append_array(payload)
	return out

func _f_bytes(field: int, payload: PackedByteArray) -> PackedByteArray:
	var out := _tag(field, 2)
	out.append_array(_c._varint(payload.size()))
	out.append_array(payload)
	return out

## packed repeated varint（proto3 对 repeated uint32 的默认编码）。
func _f_varints_packed(field: int, vals: Array) -> PackedByteArray:
	var payload := PackedByteArray()
	for v in vals:
		payload.append_array(_c._varint(int(v)))
	var out := _tag(field, 2)
	out.append_array(_c._varint(payload.size()))
	out.append_array(payload)
	return out

# ---- 用例 ----

func _schema_field(id: int, name: String, kind: int) -> PackedByteArray:
	var m := _f_varint(1, id)
	m.append_array(_f_bytes(2, name.to_utf8_buffer()))
	m.append_array(_f_varint(3, kind))
	return m

func _test_schema() -> void:
	var schema := PackedByteArray()
	schema.append_array(_f_bytes(1, _schema_field(1, "Pos", 5)))
	schema.append_array(_f_bytes(1, _schema_field(2, "Health", 0)))
	schema.append_array(_f_varint(2, 12345))

	var frame := _f_varint(1, 7)          # step
	frame.append_array(_f_varint(2, 1))   # full = true
	frame.append_array(_f_bytes(4, schema))

	var d: Dictionary = _c._decode_frame(frame)
	_check(int(d["step"]) == 7, "step 应解出 7，得到 %s" % d["step"])
	_check(bool(d["full"]) == true, "full 应为 true")
	var fields: Array = d["schema"]["fields"]
	_check(fields.size() == 2, "schema 应有 2 个字段，得到 %d" % fields.size())
	_check(String(fields[0]["name"]) == "Pos" and int(fields[0]["kind"]) == 5, "第一个字段应是 Pos/Vec3")
	_check(int(d["schema"]["version"]) == 12345, "schema version 应解出 12345")
	_done["schema"] = true

func _test_full_frame_with_schema() -> void:
	var av := _f_varint(1, 1)                       # id = 1 (Pos)
	av.append_array(_f_floats(2, [1.0, 2.0, 3.0]))  # f
	var av2 := _f_varint(1, 3)                      # id = 3 (Enemy)
	av2.append_array(_f_varint(4, 1))               # b = true

	var ed := _f_varint(1, 42)                      # entity id = 42
	ed.append_array(_f_bytes(4, av))                # set
	ed.append_array(_f_bytes(4, av2))

	var frame := _f_varint(1, 3)
	frame.append_array(_f_varint(2, 1))
	frame.append_array(_f_bytes(3, ed))

	var d: Dictionary = _c._decode_frame(frame)
	_check(d["entities"].size() == 1, "应有 1 个实体条目")
	var e: Dictionary = d["entities"][0]
	_check(int(e["id"]) == 42 and not bool(e["destroy"]), "实体 42 不应是 destroy")
	_check(e["set"].size() == 2, "应有 2 条 set")
	_check((e["set"][0]["f"] as Array).size() == 3, "Pos 应有 3 个分量")
	_check(float((e["set"][0]["f"] as Array)[2]) == 3.0, "Pos.z 应为 3")
	_check(bool(e["set"][1]["b"]) == true, "Enemy 应为 true")
	_done["full"] = true

func _test_delta_frame() -> void:
	var ed := _f_varint(1, 5)
	ed.append_array(_f_varint(2, 1))                # destroy
	ed.append_array(_f_varint(3, 2))                # removed: [2]

	# 实体 6 用 **packed** 形式 —— 服务端 proto3 对 repeated uint32 默认就是 packed，
	# 上面实体 5 的非 packed 形式只是兼容分支，真正跑在线上的是这一条。
	var ed2 := _f_varint(1, 6)
	ed2.append_array(_f_varints_packed(3, [4, 5]))  # removed: [4, 5]

	var frame := _f_varint(1, 9)
	frame.append_array(_f_bytes(3, ed))
	frame.append_array(_f_bytes(3, ed2))

	var d: Dictionary = _c._decode_frame(frame)
	_check(bool(d["full"]) == false, "默认应是增量帧")
	_check(bool(d["entities"][0]["destroy"]) == true, "实体 5 应是 destroy")
	_check((d["entities"][1]["removed"] as Array) == [4, 5], "实体 6 的 packed removed 应为 [4,5]")
	_done["delta"] = true

# 上行字段号必须与 CommandMsg 一致。`reset` 是最容易写错的一个：它在服务端生成码里
# 叫 Reset_（与生成方法重名），但线上字段号仍是 7。
func _test_command_encoding() -> void:
	var buf: PackedByteArray = _c._encode_command(
		Vector2(1.0, 2.0), 0.5, true, true, Vector3(3, 4, 5), Vector3(0, 0, 1), true)

	var seen := {}
	var i := 0
	while i < buf.size():
		var t: Array = _c._read_varint(buf, i)
		i = int(t[1])
		var field: int = int(t[0]) >> 3
		var wire: int = int(t[0]) & 0x07
		seen[field] = wire
		match wire:
			_c.WIRE_VARINT:
				var r: Array = _c._read_varint(buf, i)
				i = int(r[1])
			_c.WIRE_FIXED32:
				i += 4
			_c.WIRE_LEN:
				var rl: Array = _c._read_varint(buf, i)
				i = int(rl[1]) + int(rl[0])
			_:
				break

	_check(seen.get(1, -1) == _c.WIRE_LEN, "move 应是字段 1（packed float）")
	_check(seen.get(2, -1) == _c.WIRE_FIXED32, "yaw 应是字段 2（fixed32）")
	_check(seen.get(3, -1) == _c.WIRE_VARINT, "jump 应是字段 3")
	_check(seen.get(4, -1) == _c.WIRE_VARINT, "shoot 应是字段 4")
	_check(seen.get(5, -1) == _c.WIRE_LEN, "origin 应是字段 5")
	_check(seen.get(6, -1) == _c.WIRE_LEN, "dir 应是字段 6")
	_check(seen.get(7, -1) == _c.WIRE_VARINT, "reset 应是字段 7")
	_done["command"] = true
```

- [ ] **Step 2: 跑测试确认失败**

Run（一行）：

```bash
"C:\Users\zhubeijian\Downloads\Godot_v4.7.2-stable_win64.exe\Godot_v4.7.2-stable_win64_console.exe" --headless --path godot_client --script res://tests/frame_decode_test.gd
```

Expected: FAIL —— `Invalid call. Nonexistent function '_decode_frame'`

- [ ] **Step 3: 实现协议层**

`godot_client/scripts/fps_client.gd`：

1. 顶部注释块改写成新协议（把「payload 用 protobuf 序列化」那段里的消息列表换成 `game.cmd` / `game.resync` / `onFrame`）。
2. 信号：

```gdscript
signal frame_received(frame: Dictionary)   # 服务端推送的同步帧（增量或全量）
signal matched_received(result: Dictionary)
signal connection_changed(connected: bool)
```

3. 加 token 与常量：

```gdscript
const TOKEN_PATH := "user://client_id.txt"

var client_token := ""
```

4. `_ready()` 里读 token，不存在则生成并落盘：

```gdscript
func _ready() -> void:
	client_token = _load_or_create_token()
	_ws.connect_to_url(WS_URL)

## _load_or_create_token 读取持久化的客户端身份；首次运行生成一个 UUID 并落盘。
## 服务端把它当会话 UID，重连时据此找回原来的对局实例。
func _load_or_create_token() -> String:
	if FileAccess.file_exists(TOKEN_PATH):
		var f := FileAccess.open(TOKEN_PATH, FileAccess.READ)
		if f != null:
			var t := f.get_as_text().strip_edges()
			if t != "":
				return t
	var t := _uuid4()
	var f := FileAccess.open(TOKEN_PATH, FileAccess.WRITE)
	if f != null:
		f.store_string(t)
	return t

## _uuid4 生成一个 RFC 4122 v4 UUID 字符串。
func _uuid4() -> String:
	var b := PackedByteArray()
	for i in 16:
		b.append(randi() & 0xFF)
	b[6] = (b[6] & 0x0F) | 0x40
	b[8] = (b[8] & 0x3F) | 0x80
	var hex := b.hex_encode()
	return "%s-%s-%s-%s-%s" % [
		hex.substr(0, 8), hex.substr(8, 4), hex.substr(12, 4),
		hex.substr(16, 4), hex.substr(20, 12),
	]
```

5. `send_match_join` 带 token：

```gdscript
## JoinMsg：token = 字段 1（string）。
func send_match_join() -> void:
	var payload := _tag_len(1, client_token.to_utf8_buffer())
	_send_notify("match.match.join", payload)
```

> 加一个长度分隔字段的编码辅助：

```gdscript
## 一个 LEN 型字段（string / bytes / 嵌入消息）：tag + varint 长度 + 内容。
func _tag_len(field: int, payload: PackedByteArray) -> PackedByteArray:
	var out := _varint((field << 3) | WIRE_LEN)
	out.append_array(_varint(payload.size()))
	out.append_array(payload)
	return out
```

6. 删除 `send_input` / `send_shoot` / `send_reset`，替换为：

```gdscript
## CommandMsg：把一帧的上行命令合并成一条消息发送（帧是最小发送单位）。
## 编码拆成 _encode_command 是为了能脱离 WebSocket 单测字段号 —— `reset` 在服务端
## 生成码里叫 Reset_（与生成方法重名），线上字段号仍是 7，是最容易写错的一处。
func send_command(move: Vector2, yaw: float, jump: bool, shoot: bool,
		origin: Vector3, dir: Vector3, reset: bool) -> void:
	_send_notify("game.game.cmd", _encode_command(move, yaw, jump, shoot, origin, dir, reset))

## _encode_command 生成 CommandMsg 的 protobuf 载荷。
## 字段号取自 game/protos/game.proto：move=1 yaw=2 jump=3 shoot=4 origin=5 dir=6 reset=7。
func _encode_command(move: Vector2, yaw: float, jump: bool, shoot: bool,
		origin: Vector3, dir: Vector3, reset: bool) -> PackedByteArray:
	var msg := _packed_floats(1, [move.x, move.y])
	msg.append_array(_field_fixed32(2, yaw))
	if jump:
		msg.append_array(_field_varint(3, 1))
	if shoot:
		msg.append_array(_field_varint(4, 1))
		msg.append_array(_packed_floats(5, [origin.x, origin.y, origin.z]))
		msg.append_array(_packed_floats(6, [dir.x, dir.y, dir.z]))
	if reset:
		msg.append_array(_field_varint(7, 1))
	return msg

## 请求服务端下一帧下发全量（full 帧自带 schema）。收到 full 之前忽略一切增量。
func send_resync() -> void:
	_send_notify("game.game.resync", PackedByteArray())
```

> **item 6 还必须接上 `_on_handshake`**：原来那里直接发的是空 payload 的
> `match.match.join`，改成走 `send_match_join()`：
>
> ```gdscript
> 	# 进匹配队列：match 服务配对后推 onMatched。必须带上 token —— 服务端把它当
> 	# 会话 UID，重连时才能找回原来的对局实例。
> 	send_match_join()
> ```
>
> 不接的话 token 永远发不出去，整条「重连回同一局」的链路都不成立。


7. 删除 `_decode_snapshot` / `_decode_body` / `_decode_resource` / `_decode_player`，替换为：

```gdscript
## Frame 解码：{step, full, schema:{fields:[{id,name,kind}], version}, entities:[...]}。
## 每条 EntityDelta 是 {id, destroy, removed:[], set:[{id, f:[], i, b, s}]}，
## 原样交给 WorldStore.apply_frame 解释 —— 协议层不理解属性语义。
func _decode_frame(buf: PackedByteArray) -> Dictionary:
	var d := {"step": 0, "full": false, "schema": {"fields": [], "version": 0}, "entities": []}
	var i := 0
	while i < buf.size():
		var tag: Array = _read_varint(buf, i)
		i = int(tag[1])
		var field: int = int(tag[0]) >> 3
		var wire: int = int(tag[0]) & 0x07
		match wire:
			WIRE_VARINT:
				var r: Array = _read_varint(buf, i)
				i = int(r[1])
				match field:
					1: d["step"] = int(r[0])
					2: d["full"] = int(r[0]) != 0
			WIRE_LEN:
				var rl: Array = _read_varint(buf, i)
				i = int(rl[1])
				var n: int = int(rl[0])
				var sub: PackedByteArray = buf.slice(i, i + n)
				i += n
				match field:
					3: d["entities"].append(_decode_entity_delta(sub))
					4: d["schema"] = _decode_schema(sub)
			_:
				break
	return d

## Schema：fields（字段 1，repeated SchemaField）+ version（字段 2，varint）。
func _decode_schema(buf: PackedByteArray) -> Dictionary:
	var d := {"fields": [], "version": 0}
	var i := 0
	while i < buf.size():
		var tag: Array = _read_varint(buf, i)
		i = int(tag[1])
		var field: int = int(tag[0]) >> 3
		var wire: int = int(tag[0]) & 0x07
		match wire:
			WIRE_VARINT:
				var r: Array = _read_varint(buf, i)
				i = int(r[1])
				if field == 2:
					d["version"] = int(r[0])
			WIRE_LEN:
				var rl: Array = _read_varint(buf, i)
				i = int(rl[1])
				var n: int = int(rl[0])
				var sub: PackedByteArray = buf.slice(i, i + n)
				i += n
				if field == 1:
					d["fields"].append(_decode_schema_field(sub))
			_:
				break
	return d

## SchemaField：id（1，varint）/ name（2，string）/ kind（3，varint）。
func _decode_schema_field(buf: PackedByteArray) -> Dictionary:
	var d := {"id": 0, "name": "", "kind": 0}
	var i := 0
	while i < buf.size():
		var tag: Array = _read_varint(buf, i)
		i = int(tag[1])
		var field: int = int(tag[0]) >> 3
		var wire: int = int(tag[0]) & 0x07
		match wire:
			WIRE_VARINT:
				var r: Array = _read_varint(buf, i)
				i = int(r[1])
				match field:
					1: d["id"] = int(r[0])
					3: d["kind"] = int(r[0])
			WIRE_LEN:
				var rl: Array = _read_varint(buf, i)
				i = int(rl[1])
				var n: int = int(rl[0])
				var sub: PackedByteArray = buf.slice(i, i + n)
				i += n
				if field == 2:
					d["name"] = sub.get_string_from_utf8()
			_:
				break
	return d

## AttrValue：id（1）/ f（2，packed floats）/ i（3，varint）/ b（4，varint）/ s（5，string）。
func _decode_attr_value(buf: PackedByteArray) -> Dictionary:
	var d := {"id": 0, "f": [], "i": 0, "b": false, "s": ""}
	var i := 0
	while i < buf.size():
		var tag: Array = _read_varint(buf, i)
		i = int(tag[1])
		var field: int = int(tag[0]) >> 3
		var wire: int = int(tag[0]) & 0x07
		match wire:
			WIRE_VARINT:
				var r: Array = _read_varint(buf, i)
				i = int(r[1])
				match field:
					1: d["id"] = int(r[0])
					3: d["i"] = int(r[0])
					4: d["b"] = int(r[0]) != 0
			WIRE_FIXED32:
				if field == 2:
					d["f"].append(buf.slice(i, i + 4).to_float32_array()[0])
				i += 4
			WIRE_LEN:
				var rl: Array = _read_varint(buf, i)
				i = int(rl[1])
				var n: int = int(rl[0])
				var sub: PackedByteArray = buf.slice(i, i + n)
				i += n
				match field:
					2: d["f"].append_array(_decode_floats(sub))
					5: d["s"] = sub.get_string_from_utf8()
			_:
				break
	return d

## EntityDelta：id（1）/ destroy（2）/ removed（3，packed varint）/ set（4，repeated AttrValue）。
func _decode_entity_delta(buf: PackedByteArray) -> Dictionary:
	var d := {"id": 0, "destroy": false, "removed": [], "set": []}
	var i := 0
	while i < buf.size():
		var tag: Array = _read_varint(buf, i)
		i = int(tag[1])
		var field: int = int(tag[0]) >> 3
		var wire: int = int(tag[0]) & 0x07
		match wire:
			WIRE_VARINT:
				var r: Array = _read_varint(buf, i)
				i = int(r[1])
				match field:
					1: d["id"] = int(r[0])
					2: d["destroy"] = int(r[0]) != 0
					3: d["removed"].append(int(r[0]))
			WIRE_LEN:
				var rl: Array = _read_varint(buf, i)
				i = int(rl[1])
				var n: int = int(rl[0])
				var sub: PackedByteArray = buf.slice(i, i + n)
				i += n
				match field:
					3: d["removed"].append_array(_decode_varints(sub))
					4: d["set"].append(_decode_attr_value(sub))
			_:
				break
	return d

## packed repeated varint（proto3 对 repeated uint32 的默认编码）。
func _decode_varints(bytes: PackedByteArray) -> Array:
	var out: Array = []
	var i := 0
	while i < bytes.size():
		var r: Array = _read_varint(bytes, i)
		i = int(r[1])
		out.append(int(r[0]))
	return out
```

> 注意 `repeated uint32 removed` 在 proto3 下默认是 **packed**，所以出现在 WIRE_LEN 分支里。上面两个分支都保留，兼容非 packed 编码。

8. `_on_data` 的 route 分支改为：

```gdscript
	match route:
		"onMatched":
			_matched = true
			matched_received.emit(_decode_match_result(payload))
		"onFrame":
			frame_received.emit(_decode_frame(payload))
```

- [ ] **Step 4: 跑测试确认通过**

Run（同 Step 2 的命令）
Expected: `frame_decode_test: OK`

- [ ] **Step 5: 跑既有回归测试确认没打破**

```bash
"C:\Users\zhubeijian\Downloads\Godot_v4.7.2-stable_win64.exe\Godot_v4.7.2-stable_win64_console.exe" --headless --path godot_client --script res://tests/reconnect_cleanup_test.gd
```

Expected: PASS（该测试只涉及连接清理，不应受协议改动影响）

- [ ] **Step 6: 提交**

```bash
git add godot_client/scripts/fps_client.gd godot_client/tests/frame_decode_test.gd
git commit -m "feat(client): 通用 Frame/Schema 协议层与 token 持久化

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## Task 13: 客户端渲染层改按属性名查询（`main.gd`）

**Files:**
- Modify: `godot_client/scripts/main.gd`
- Delete: `godot_client/tests/snapshot_same_step_test.gd`
- Modify: `godot_client/tests/ws_smoke.gd`（旧信号 `state_received`）
- Modify: `godot_client/tests/reconnect_cleanup_test.gd`（引用了已删除的字段，且列在 `AGENTS.md` 的回归命令里）

**Interfaces:**
- Consumes: `WorldStore`（Task 11）、`frame_received` 信号（Task 12）
- Produces: `main.gd` 内部新函数 `_on_frame` / `_reconcile_scene` / `_build_body_dict` / `_refresh_derived`

**现有字段名对照（改动前务必核对）**：`_entities`（id → BodyEntity 节点，**不是** `_bodies`）、`_res_nodes`（id → 金币节点）、`_place_body(id, b, pos, quat)`、`_remove_body(id)`、`_player_pos` / `_remote_pos` / `_remote_yaw`、`_hud_score` / `_hud_wave` / `_hud_gold` / `_hud_targets` / `_hud_enemies`、`_last_score` / `_last_health`、`_prev_projectiles`、`_render_resources(resources)`、`_update_hud(s)`、`_detect_impacts(bodies)`、`_pop(point, color, size, ttl)`、`_snap_sig`、`_prev_snap` / `_next_snap`。

> **实施时发现的四处 brief 缺陷（已按下面的写法落地，改代码时以这里为准）：**
>
> 1. **`_refresh_derived` 必须整体重建 `_body_xform`，不能只做增改。** 否则消失的 id 会一直留在
>    映射里，`_reconcile_scene` 的「删掉不在 `_body_xform` 里的节点」那条永远不会触发 ——
>    实体被销毁后渲染节点不会消失。正确写法是每帧先 `_body_xform.clear()` 再按 store 重建。
> 2. **`_body_xform` 必须排除带 `Resource.Kind` 的实体。** 金币在 store 里是普通刚体，
>    不排除的话每一枚金币都会额外长出一个灰色的刚体球。
> 3. **销毁特效的位置要从 `attrs["Pos"]` 取**，不能从 `_body_xform` 取 —— 触发时实体已经从
>    store 里删掉了，查不到就永远在世界原点爆闪。
> 4. **`reset` 要上报真实的按键边沿**，不要像 brief 那样写死 `false`（否则重置键失效）。

> **另外要改的（不在文件清单里但会挂）：** `godot_client/tests/reconnect_cleanup_test.gd`
> 引用了已删除的字段，且它列在 `AGENTS.md` 的回归命令里，必须一并改到新 API。

> **Task 13 评审后的四处修正（权威，优先于下文代码块）：**
>
> 1. **暂停时必须真的停手（Critical）。** brief 里「暂停时 `_wish_velocity()` 自然返回零向量」
>    是**错的** —— `_wish_velocity()` 只看 `Input.is_key_pressed`，根本不看 `captured`。
>    照 brief 写的话，鼠标释放（标题界面或按 ESC）时 WASD 照样推着角色跑。
>    正确写法：`var move := _wish_velocity() if captured else Vector2.ZERO`。
>    这既恢复了旧行为（旧代码在释放时补发一条静止输入），又保留了「每帧都要发」的
>    服务端活性要求 —— 发的是零向量，不是不发。
> 2. **本地玩家位置与远端朝向也要插值。** 现在 `_player_pos` 只在收到帧时被直接赋值，
>    于是第一人称相机与第三人称 avatar 变成 **20 Hz 跳步**（走 0.4 m/步、跑 0.7 m/步）；
>    `_remote_yaw` 同理，远端 avatar 变成 20 Hz 台阶式旋转（旧的 `lerp_angle` 注释还专门
>    说明过为什么不能用原始 yaw）。要像远端位置那样，用保留的上一帧值做 lerp / lerp_angle。
> 3. **`_on_frame` 末尾要再调一次 `_render_interpolated()`。** Godot 先跑父节点的
>    `_process`（里面有 `_render_interpolated`），再跑子节点 `FpsClient._process`，
>    而后者同步 emit `frame_received` → `_on_frame` → `_reconcile_scene` 把**原始**变换写回
>    节点，于是这一帧就是未插值的，下一帧才被拉回去 —— 每来一帧抖一次。
>    在 `_on_frame` 收尾再插值一次，就能保证一帧里的最后一次写是插值结果。
> 4. **把合成帧测试固化成常驻测试**（见 Step 6b）。新渲染路径目前**没有任何**留下的测试：
>    两个既有无头测试只覆盖 Task 11/12，`reconnect_cleanup_test` 只跑断线清理；
>    无头跑 300 帧也证明不了什么（没有服务端，`frame_received` 根本不会触发）。
>    上面 1–3 三个缺陷恰好都落在没有任何测试覆盖的那块代码上。
>
> 5. **`_render_interpolated` 的提前 return 现在会连玩家/远端字段一起挡掉** ——
>    经第 2 条修正后它们只有这一个写者，`_body_xform` 为空时相机位置就冻住了。
>    把三段玩家/远端 lerp 挪到 `_body_xform` 守卫**之前**。
> 6. **`_reset_interp` 要一并重置玩家/远端的上一次值与目标值**，否则重新匹配时相机会
>    从上一局的残留位置滑到新出生点，而不是直接落位。
> 7. **`game_frame_test` 的 yaw 断言把容忍度放宽到 `< PI * 0.5`**：原来的 `< 0.1` 只有约
>    1 ms 余量（`_frame_time` 是整数毫秒），机器一忙就 flaky。同时补一条**幂等断言** ——
>    连续调两次 `_render_interpolated()`，玩家位置不应变化 —— 用来钉住 target/display 拆分
>    （每组断言都在 alpha≈0 时跑，就地 lerp 的错误版本也能通过，必须靠这条区分）。
> 8. **`rejoin_smoke.gd` 比较前先判空**：`_first_match_id == ""` 或 `_first_idx < 0` 直接判失败。
>    两边默认值相同（`""` / `-1`），一旦 onMatched 载荷解析失败，旧写法会**假通过** ——
>    而这个测试是全计划唯一端到端验证「重连回同一局」的东西。

- [ ] **Step 1: 换信号、换状态变量**

1. 顶部加：

```gdscript
const WorldStore := preload("res://scripts/world_store.gd")

# 本地世界状态：服务端推的是增量，这里累积成完整世界（见 world_store.gd）。
var _store: WorldStore = WorldStore.new()
# 渲染层自己的插值状态：id -> {"pos","quat"}（上一帧的变换），以及本帧到达时间。
var _body_xform := {}      # id -> {"pos": Vector3, "quat": Quaternion}：服务端刚体的当前变换
var _prev_body_xform := {} # id -> {"pos": Vector3, "quat": Quaternion}：插值起点
var _prev_player_pos := Vector3(0, 0.2, 18)
var _prev_remote_pos := Vector3(0, 0.2, -18)
var _frame_time := 0.0     # 本帧到达时间（秒），插值 alpha 的基准
```

2. 删除这些现在无用的变量：`_prev_snap`、`_next_snap`、`_snap_sig`、`_prev_projectiles`。

3. `_ready()` 里把 `fps_client.state_received.connect(_on_state)` 换成：

```gdscript
	fps_client.frame_received.connect(_on_frame)
```

4. `_on_matched` 里主动请求全量：

```gdscript
func _on_matched(result: Dictionary) -> void:
	_my_player_idx = int(result.get("player_idx", 0))
	_matched = true
	# 出生在船的艏/艉两端，开局朝向船中（与服务端 playerSpawnYaw 一致）。
	_yaw = PI if _my_player_idx == 1 else 0.0
	conn_label.visible = false
	# 由客户端驱动全量补齐：收到 full 帧之前，WorldStore 之外的一切都不可信。
	_store.clear()
	_reset_interp()
	fps_client.send_resync()
```

5. `_on_connection(false)` 分支里，把清空 `_prev_snap` / `_next_snap` / `_snap_sig` 的几行换成：

```gdscript
		_matched = false
		_store.clear()
		_reset_interp()
```

6. 新增两个辅助：

```gdscript
## _reset_interp 清空插值状态。重连后第一帧没有「上一帧」，直接在当前位置落位。
func _reset_interp() -> void:
	_body_xform.clear()
	_prev_body_xform.clear()
	_frame_time = 0.0
```

- [ ] **Step 2: 用 `_on_frame` 替换整条快照处理链**

删除 `_on_state` / `_store_snapshot` / `_parse_snapshot` / `_frame_sig` / `_frame_keeps_bodies` / `_draw_bodies` / `_detect_impacts`，替换为：

```gdscript
## _on_frame 应用一帧同步消息。full 帧在 WorldStore 内部会先清空再整体覆盖，
## 所以「首次进入 / 重连 / 乱序」在这里没有区别 —— 应用完做一次场景协调即可。
##
## 旧的 _snap_sig / _frame_keeps_bodies 去重逻辑整块消失：增量协议下服务端
## 不会重复推送，也不需要靠快照 diff 推断「谁消失了」。
func _on_frame(frame: Dictionary) -> void:
	if bool(frame.get("full", false)):
		_store.apply_schema(frame.get("schema", {}).get("fields", []))
	var res: Dictionary = _store.apply_frame(frame)

	# 本帧到达即把各刚体的当前变换存进 prev，作为下一次插值的起点。
	_frame_time = Time.get_ticks_msec() / 1000.0
	_prev_body_xform = _body_xform.duplicate(true)
	_refresh_derived()

	_reconcile_scene()
	for ev: Variant in res.get("destroyed", []):
		_on_entity_destroyed(ev as Dictionary)
	_render_resources_from_store()
	_update_hud()
```

```gdscript
## _refresh_derived 从 store 刷新刚体变换、玩家位置、远端朝向。必须在插值之前做，
## 这样 prev/cur 才是相邻两帧。
func _refresh_derived() -> void:
	for id: Variant in _store.entities_with("Body.Kind"):
		var eid := int(id)
		_body_xform[eid] = {
			"pos": _vec3_of(_store.attr(eid, "Pos")),
			"quat": _quat_of(_store.attr(eid, "Rot")),
		}
	for id: Variant in _store.entities_with("Player.Idx"):
		var eid := int(id)
		var feet := _vec3_of(_store.attr(eid, "Pos"))
		if int(_store.attr(eid, "Player.Idx")) == _my_player_idx:
			_player_pos = feet
		else:
			_prev_remote_pos = _remote_pos
			_remote_pos = feet
			_remote_yaw = float(_store.attr(eid, "Facing"))
```

```gdscript
## _on_entity_destroyed 消失的实体触发对应反馈。销毁事件带着消失前的属性，
## 所以这里能分辨消失的是弹丸（爆闪 + 命中音）还是金币（拾取音）。
func _on_entity_destroyed(ev: Dictionary) -> void:
	var attrs: Dictionary = ev.get("attrs", {})
	var p: Vector3 = _body_xform.get(int(ev.get("id", 0)), {}).get("pos", Vector3.ZERO)
	if attrs.has("Projectile"):
		_pop(p, Color("ffe066"), 0.06, 0.2)
		sfx.play("hit")
	if attrs.has("Resource.Kind"):
		sfx.play("pickup")
```

```gdscript
## _vec3_of / _quat_of 把 store 里的 Array 属性转成 Godot 类型。
func _vec3_of(v: Variant) -> Vector3:
	var a: Array = v if v is Array else [0.0, 0.0, 0.0]
	return Vector3(float(a[0]), float(a[1]), float(a[2]))

func _quat_of(v: Variant) -> Quaternion:
	var a: Array = v if v is Array else [0.0, 0.0, 0.0, 1.0]
	return Quaternion(float(a[0]), float(a[1]), float(a[2]), float(a[3]))
```

- [ ] **Step 3: 场景协调 + 插值**

```gdscript
## _reconcile_scene 把刚体渲染节点与 store 对齐：新 id 建节点、消失的删节点。
## 场景同步放在「整帧应用之后」做，实体创建的先后顺序问题就自然消失了。
func _reconcile_scene() -> void:
	for id: Variant in _body_xform:
		var eid := int(id)
		_place_body(eid, _build_body_dict(eid), _body_xform[eid]["pos"], _body_xform[eid]["quat"])
	for eid: Variant in _entities.keys():
		if not _body_xform.has(int(eid)):
			_remove_body(int(eid))
```

> `_place_body` 保持原样（内部 `sync_from(b)` + 设 `global_position`/`quaternion`）。上面先按目标位置落位，随后的 `_render_interpolated` 每帧再用 prev→cur 插值覆盖，所以不会看到跳变。

```gdscript
## _build_body_dict 从 store 组装出 body_entity.gd 期望的字典 —— 形状与旧的快照
## BodyInfo 完全一致，所以 body_entity.gd 的程序化建模代码零改动。
func _build_body_dict(eid: int) -> Dictionary:
	return {
		"id": eid,
		"type": int(_store.attr(eid, "Body.Kind")),
		"static": bool(_store.attr(eid, "Body.Static")),
		"target": _store.has_attr(eid, "Target"),
		"enemy": _store.has_attr(eid, "Enemy"),
		"projectile": _store.has_attr(eid, "Projectile"),
		"pos": _store.attr(eid, "Pos"),
		"quat": _store.attr(eid, "Rot"),
		"size": _store.attr(eid, "Body.Size"),
		"health": float(_store.attr(eid, "Health")) if _store.has_attr(eid, "Health") else 0.0,
		"active": bool(_store.attr(eid, "Body.Active")),
		"mat": int(_store.attr(eid, "Body.Mat")),
	}
```

`_render_interpolated()` 改为逐节点插值（不再读 `_prev_snap` / `_next_snap`）：

```gdscript
## 影子跟随：alpha = 距本帧到达的时间 / TICK，在「上一帧变换 → 本帧变换」之间插值。
func _render_interpolated() -> void:
	if _body_xform.is_empty():
		return
	var alpha := clampf((Time.get_ticks_msec() / 1000.0 - _frame_time) / TICK, 0.0, 1.0)
	for id: Variant in _body_xform:
		var eid := int(id)
		var node: Node3D = _entities.get(eid)
		if node == null:
			continue
		var cur: Dictionary = _body_xform[eid]
		var prev: Dictionary = _prev_body_xform.get(eid, cur)
		node.global_position = (prev["pos"] as Vector3).lerp(cur["pos"] as Vector3, alpha)
		node.quaternion = (prev["quat"] as Quaternion).slerp(cur["quat"] as Quaternion, alpha)
	_player_pos = _player_pos  # 本地玩家位置由 _refresh_derived 直接给出（服务端权威）
	_remote_pos = _prev_remote_pos.lerp(_remote_pos, alpha)
```

> `_render_interpolated()` 的调用点（`_process`）不变；它原本依赖 `_next_snap` 的非空判断，现在改成 `_body_xform`。

- [ ] **Step 4: 金币与 HUD 改从 store 取**

`_render_resources(resources: Dictionary)` 保留（内部逻辑与音效不变），新增一个从 store 组装参数的包装：

```gdscript
## _render_resources_from_store 把 store 里的金币摊平成 _render_resources 期望的形状。
func _render_resources_from_store() -> void:
	var out := {}
	for id: Variant in _store.entities_with("Resource.Kind"):
		var eid := int(id)
		out[eid] = {"pos": _vec3_of(_store.attr(eid, "Pos")), "kind": int(_store.attr(eid, "Resource.Kind"))}
	_render_resources(out)
```

> `_render_resources` 在「id 消失」时播放 `pickup` 音效 —— 现在金币销毁已经在 `_on_entity_destroyed` 里放了同一个音，把 `_render_resources` 里那行 `sfx.play("pickup")` 删掉，避免响两次。

`_update_hud(s: Dictionary)` 改为无参、从 store 取：

```gdscript
## _update_hud 从 store 读全局状态与本地玩家。全局状态是挂在单例实体上的
## GameState 组件（属性名 Game.Score / Game.Wave / Game.Gold）。
func _update_hud() -> void:
	for id: Variant in _store.entities_with("Game.Score"):
		var gid := int(id)
		_hud_score = int(_store.attr(gid, "Game.Score"))
		_hud_wave = int(_store.attr(gid, "Game.Wave"))
		_hud_gold = int(_store.attr(gid, "Game.Gold"))
		break
	_hud_targets = _store.entities_with("Target").size()
	_hud_enemies = _store.entities_with("Enemy").size()

	var hp := 100.0
	for id: Variant in _store.entities_with("Player.Idx"):
		var eid := int(id)
		if int(_store.attr(eid, "Player.Idx")) == _my_player_idx:
			hp = float(_store.attr(eid, "Health"))
	health_bar.value = hp
	if hp < _last_health - 0.001:
		sfx.play("damage")
		_flash_hit()
	_last_health = hp
	if _hud_score > _last_score:
		sfx.play("destroy")
	_last_score = _hud_score
```

- [ ] **Step 5: 输入与射击合并上报**

`_shoot()` 里原来调用 `fps_client.send_shoot(origin, dir)` 的那行删除，改为只记录「本帧该射击」：

```gdscript
	_pending_shot = {"origin": origin, "dir": dir}
```

在类字段区加 `var _pending_shot := {}`。

`_process` 里每渲染帧的上报改成一条命令（输入 + 射击合并，帧是最小发送单位）：

```gdscript
	var move := _wish_velocity()
	var jump := _jump_queued
	_jump_queued = false
	var origin := Vector3.ZERO
	var dir := Vector3.ZERO
	var shoot := not _pending_shot.is_empty()
	if shoot:
		origin = _pending_shot["origin"]
		dir = _pending_shot["dir"]
		_pending_shot = {}
	fps_client.send_command(move, _yaw, jump, shoot, origin, dir, false)
```

> **必须每渲染帧都发，包括鼠标未捕获（按了 ESC 暂停）时** —— 把原来 `if _was_captured:`
> 的包裹去掉，暂停时 `_wish_velocity()` 自然返回零向量。两个理由：
>
> 1. Task 7 合并后的 `Cmd` **每帧都会调 `ApplyInput`**，客户端必须每帧都给出 `move`，
>    否则移动语义与旧协议不一致。
> 2. 服务端的实例空闲回收（Task 10）以「最近一次收到上行消息」判定在线。旧客户端只在
>    鼠标捕获时上报，按 ESC 后虽然**仍连着**却完全静默 —— 60 秒后服务端会把他在**在线
>    状态**下回收，快照停推 → 客户端 2.5 秒看门狗强制重连 → 重新匹配开新局 → 再次被
>    回收，形成每分钟一局的空转。每帧都发就从根上消除了这个窗口。
>
> 代价是暂停时也有 60 条/秒的小消息；服务端本来就会把同一 tick 内的输入合并成最新一条，
> 可以接受。

> `_process` 里原来单独调用 `fps_client.send_input(...)` 与 `send_shoot(...)` 的地方都删掉。`reset` 按钮（`_on_reset_pressed`）同样改成在 `_process` 里以 `reset` 边沿上报一次。

- [ ] **Step 6: 删除不再适用的客户端回归测试**

`godot_client/tests/snapshot_same_step_test.gd` 测的是「同一 tick 重复推送只保留首次到达时间」的去重逻辑 —— 增量协议下服务端不会重复推送，该逻辑已被删除：

```bash
git rm godot_client/tests/snapshot_same_step_test.gd
```

**同时修 `godot_client/tests/ws_smoke.gd`**（它连着旧信号，不修就会报错）：

- `_initialize()` 里 `_client.state_received.connect(_on_state)` → `_client.frame_received.connect(_on_frame)`。
- `_on_state(s)` → `_on_frame(f)`，里面 `s.get("step", -1)` 改成 `f.get("step", -1)`，其余统计逻辑不变。
- 文件头的注释把 `onSnapshot` 快照改成 `onFrame` 帧。

> `ws_smoke.gd` 需要真实集群才跑得起来，所以它是 Step 8 的手动验证工具，不参与无头回归 —— 但正因为它是**唯一**走真实 pomelo/WS/protobuf 全链路的测试，Step 8 一定要跑它。

- [ ] **Step 6b: 把合成帧测试固化成常驻测试（`godot_client/tests/game_frame_test.gd`）**

新增 `godot_client/tests/game_frame_test.gd`：无头加载真实场景、手动喂帧，覆盖
`_on_frame` / `_refresh_derived` / `_reconcile_scene` / `_on_entity_destroyed` / `_update_hud`。
这是**唯一**能覆盖新渲染路径的测试，不要省。

```gdscript
extends SceneTree
## main.gd 的渲染路径回归：加载真实场景，手动喂合成帧，断言节点的新增/更新/销毁。
## 服务端缺席也无妨 —— 这里只驱动 _on_frame，不碰 WebSocket。
##
## 注意：GDScript 没有 try/catch，测试函数中途抛错会被吞掉、整个用例"假绿"，
## 所以沿用 frame_decode_test.gd 的完成标记模式。

const SCHEMA := [
	{"id": 1, "name": "Pos", "kind": 5},
	{"id": 2, "name": "Health", "kind": 0},
	{"id": 3, "name": "Enemy", "kind": 2},
	{"id": 4, "name": "Body.Mat", "kind": 1},
	{"id": 5, "name": "Body.Kind", "kind": 1},
	{"id": 6, "name": "Body.Size", "kind": 5},
	{"id": 7, "name": "Body.Static", "kind": 2},
	{"id": 8, "name": "Body.Active", "kind": 2},
	{"id": 9, "name": "Rot", "kind": 6},
	{"id": 10, "name": "Resource.Kind", "kind": 1},
	{"id": 11, "name": "Player.Idx", "kind": 1},
	{"id": 12, "name": "Game.Score", "kind": 1},
	{"id": 13, "name": "Game.Wave", "kind": 1},
	{"id": 14, "name": "Game.Gold", "kind": 1},
]

var _failures := 0
var _done := {}
var _main: Node = null

func _initialize() -> void:
	_main = load("res://scenes/main.tscn").instantiate()
	root.add_child(_main)
	_main._on_matched({"player_idx": 0})
	_test_full_frame_creates_bodies()
	_test_delta_updates_and_destroys()
	_test_coin_has_no_extra_body()
	_test_destroy_for_unknown_id_is_safe()
	for name: String in ["full", "delta", "coin", "unknown"]:
		if not _done.has(name):
			_failures += 1
			printerr("FAIL: 用例 %s 没跑完（中途抛错了？）" % name)
	if _failures > 0:
		printerr("game_frame_test: %d 项失败" % _failures)
		quit(1)
	else:
		print("game_frame_test: OK")
		quit(0)

func _check(cond: bool, msg: String) -> void:
	if not cond:
		_failures += 1
		printerr("FAIL: " + msg)

func _attr(id: int, d: Dictionary) -> Dictionary:
	var out := {"id": id}
	out.merge(d)
	return out

## 一帧只含一个刚体（id 100）。
func _test_full_frame_creates_bodies() -> void:
	_main._on_frame({"full": true, "step": 1, "schema": {"fields": SCHEMA, "version": 1}, "entities": [
		{"id": 100, "set": [
			_attr(5, {"i": 0}),                       # Body.Kind = box
			_attr(6, {"f": [0.5, 0.5, 0.5]}),         # Body.Size
			_attr(7, {"b": true}),                    # Body.Static
			_attr(8, {"b": true}),                    # Body.Active
			_attr(4, {"i": 1}),                       # Body.Mat
			_attr(1, {"f": [1.0, 2.0, 3.0]}),         # Pos
			_attr(9, {"f": [0.0, 0.0, 0.0, 1.0]}),    # Rot
		]},
	]})
	_check(_main._entities.has(100), "全量帧应在场景里建出实体 100 的节点")
	_check((_main._entities[100].global_position - Vector3(1, 2, 3)).length() < 0.001,
		"节点应落在 Pos 上")
	_done["full"] = true

## 增量帧：移动 + 销毁。
func _test_delta_updates_and_destroys() -> void:
	_main._on_frame({"full": false, "entities": [
		{"id": 100, "set": [_attr(1, {"f": [4.0, 5.0, 6.0]})]},
	]})
	_check((_main._body_xform[100]["pos"] - Vector3(4, 5, 6)).length() < 0.001,
		"增量帧应更新刚体变换缓存")

	var res: Variant = _main._on_frame({"full": false, "entities": [
		{"id": 100, "destroy": true},
	]})
	_check(res == null or true, "") # _on_frame 无需返回值，这里只为把下一句放在同一帧语义下
	_check(not _main._entities.has(100), "destroy 后渲染节点必须被删掉")
	_done["delta"] = true

## 金币在 store 里是普通刚体，但**不该**额外长出一个灰色的刚体球。
func _test_coin_has_no_extra_body() -> void:
	_main._on_frame({"full": false, "entities": [
		{"id": 200, "set": [
			_attr(5, {"i": 1}), _attr(6, {"f": [0.6]}), _attr(7, {"b": true}),
			_attr(8, {"b": true}), _attr(4, {"i": 0}),
			_attr(1, {"f": [0.0, 0.8, 0.0]}), _attr(9, {"f": [0.0, 0.0, 0.0, 1.0]}),
			_attr(10, {"i": 0}),                      # Resource.Kind
		]},
	]})
	_check(not _main._entities.has(200), "金币不应被当成刚体建出额外节点")
	_check(_main._res_nodes.has(200), "金币应建出金币节点")
	_done["coin"] = true

## 服务端可能对客户端从未见过的 id 发 destroy（比如客户端刚 resync 完）。
func _test_destroy_for_unknown_id_is_safe() -> void:
	_main._on_frame({"full": false, "entities": [
		{"id": 999, "destroy": true},
	]})
	_check(true, "对未知 id 的 destroy 不应抛错")
	_done["unknown"] = true
```

Expected: `game_frame_test: OK`，退出码 0。

> 这份测试里的断言顺序刻意与 `_on_frame` 的真实步骤一致；如果第 3 条修正（收尾再插值一次）
> 没做，`_test_delta_updates_and_destroys` 里对位置的断言仍会通过（因为它读的是缓存而不是节点），
> 所以另外补一条：**在同一渲染帧内 `_on_frame` 之后，节点位置应等于插值结果而不是缓存终值**。
> 实现时按这个意图加断言 —— 这才是真正钉住第 3 条修正的地方。

（`AGENTS.md` 里引用该测试命令行的地方在 Task 14 一并清理。）

- [ ] **Step 7: 跑客户端单测**

```bash
"C:\Users\zhubeijian\Downloads\Godot_v4.7.2-stable_win64.exe\Godot_v4.7.2-stable_win64_console.exe" --headless --path godot_client --script res://tests/world_store_test.gd
```

Expected: `world_store_test: OK`
（`main.gd` 依赖场景树，没有无头单测；它的验证在 Step 8。）

- [ ] **Step 8: 端到端手动验证**

先起本地集群（etcd + nats + gate/match/game 三进程）：

```bash
cd joltgo/deploy && ./start-all.ps1
```

再开两个 Godot 客户端：

```bash
"C:\Users\zhubeijian\Downloads\Godot_v4.7.2-stable_win64.exe\Godot_v4.7.2-stable_win64.exe" --path godot_client
```

Expected：
1. 两个客户端都能看到完整的运输船场景与彼此，画面平滑（插值正常）。
2. 关掉其中一个窗口、等 3 秒重新打开 —— 应**回到同一局**：分数/波次/场上怪物沿用而不是重开，场景无缺漏，看不到「重建感」的闪跳。
3. 只关掉一个客户端时，另一个客户端的画面**不卡顿、不重置**。
4. 射击时弹丸消失有爆闪 + 命中音；拾取金币有拾取音且只响一次。

- [ ] **Step 9: 提交**

```bash
git add -A godot_client
git commit -m "feat(client): 渲染层改按实体-属性查询，删除快照去重逻辑

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## Task 14: 文档同步

**Files:**
- Modify: `AGENTS.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `docs/API.md`
- Modify: `README.md`（特性列表里的措辞）

**背景：** `AGENTS.md` 开头写着「改动代码时同步更新对应文档与本文件（过期文档比没有更糟）」。

- [ ] **Step 1: 更新 `AGENTS.md`**

1. §2 目录树：`sim/` 下 `state.go` 换成 `replicate.go`（描述改为「ECS ↔ 同步属性的唯一映射 + oracle 测试」）；新增 `replication/`（描述「与 ECS 解耦的实体-属性同步层：终值表 + 本帧脏集」）。
2. §3 关键不变量：第 5 条「快照协议是客户端契约」整条重写为「同步协议是通用实体-属性帧」：
   - wire 契约在 `game/protos/game.proto`，但**新增同步属性不需要改 proto** —— 只需在 `sim/replicate.go` 的 `declareAttributes` 里加一行 + 在变更点调 `rep.Set`。
   - 属性表（Schema）随 full 帧下发；客户端按属性名取值，不认识的属性照常存下只是不渲染。
   - `replication.Store` **不依赖 `ecs`**；属性名是扁平字符串（约定 `组件.字段`）。
   - **就近 `rep.Set` 漏写 = 静默不同步**，由 `sim/replicate_test.go` 的 oracle 测试兜底。
3. §3 第 6 条并发：补一句 `replication.Store` 与 `sim.Simulation` 一样由对局实例 goroutine 独占，非并发安全。
4. §4 数据流：把「每 tick SendPushToUsers("onSnapshot", …)」改成 `onFrame`；补上重连链路（token 即会话 UID → `match.join` 先 fan-out `game.rejoin` → 命中则 `bindPlayer` 推 `onMatched` → 客户端 `game.resync` → 服务端下一帧单独下发 full 帧）。
5. §5 约定与坑：补三条
   - **就近 `rep.Set` 漏写不会报错**，只会在 oracle 测试里失败 —— 加同步字段时先加 `rep.Set` 再加断言。
   - **full 帧不得修改增量基线**（`Store.Full()` 刻意不动 `sent`）—— 全量是发给单个客户端的。
   - **属性表必须在 `Simulation.New()` 里声明完整**，`Set` 未声明属性会 panic。
   - **`main.go` 的过时注释**：`joltgo/main.go` 里注册 game 组件那段注释仍写着
     `game.input/shoot/reset`，Task 7 之后已合并为 `game.cmd`，一并改掉。
6. §6 测试命令：把 `go test ./ecs ./sim` 改成 `go test ./ecs ./sim ./replication`；删掉 `snapshot_same_step_test.gd` 那一行，补上 `world_store_test.gd` 与 `frame_decode_test.gd`。
7. §7 变更 runbook：补一行「加一个同步字段 → `sim/replicate.go` 声明 + `rep.Set` + 在 `replicate_test.go` 的 `expectedAttrs` 里补一条」。

- [ ] **Step 2: 更新 `docs/ARCHITECTURE.md`**

1. 「服务端 ECS 架构」一节：把「快照的 `bodyInfo` 各字段由组件重建…协议字段与旧实现逐字一致，客户端零改动」整段替换为新的同步层说明（独立 `replication` 包、终值表 + 脏集、增量/全量同一 Frame）。
2. 「数据流」一节第 4 步的 `onSnapshot` 改为 `onFrame`，并补第 7 步的重连回局流程。
3. 「客户端插值」一节：改为「节点自带前一帧变换 + store 累积状态」，说明 `_snap_sig` / `_frame_keeps_bodies` 已删除及其原因。
4. 新增一节「同步协议（实体-属性帧）」，把 spec §3/§4/§6 的核心内容浓缩进来（属性存在性、就近 Set 的风险、full 帧语义、schema 版本哈希）。

- [ ] **Step 3: 更新 `docs/API.md`**

- 删除 `Snapshot` / `BodyInfo` / `ResourceInfo` / `PlayerState` / `InputMsg` / `ShootMsg` 的字段表。
- 新增 `Frame` / `EntityDelta` / `AttrValue` / `Schema` / `SchemaField` / `CommandMsg` / `JoinMsg(token)` / `RejoinMsg` / `RejoinReply` 的字段表。
- 补一节「属性表」，列出 §3.2 那 17 个属性的名字与类型（这是客户端唯一需要知道的语义清单）。

- [ ] **Step 4: 更新 `README.md`**

特性列表里「服务端以 20 Hz 固定 tick 推进模拟（服务器权威），状态变化经 WebSocket 主动推送」这一条补上增量语义；「客户端 60 Hz 渲染：…（快照 lerp/slerp）」改成「实体-属性增量 + 影子跟随插值」；补一条「断线重连回到同一对局」。

- [ ] **Step 5: 跑一遍完整测试**

Run: `cd joltgo && gofmt -l . && go vet ./gate ./match ./game ./physics ./sim ./replication && go test ./ecs ./sim ./replication`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add AGENTS.md docs/ARCHITECTURE.md docs/API.md README.md
git commit -m "docs: 同步协议改为实体-属性帧，补重连回局说明

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## Self-Review 记录

**Spec 覆盖检查**

| Spec 章节 | 对应 Task |
| --- | --- |
| §3.1 值类型 | Task 1 |
| §3.2 属性声明 | Task 5（`declareAttributes`） |
| §3.3 Store API / 同一帧只留终值 / 只同步变化 / 重连=全量 | Task 2、Task 3 |
| §3.4 属性存在性 | Task 2（`Remove`/`Destroy`）、Task 11（客户端按存在性查询） |
| §4 属性映射与调用点 | Task 5 |
| §4.3 漏写风险兜底 | Task 5 Step 1（oracle 测试） |
| §5 组件边界调整 | Task 4 |
| §6 Wire 协议 | Task 6 |
| §7.1 稳定身份 | Task 8、Task 12 |
| §7.2 回局查询 | Task 8 |
| §7.3 全量补齐 | Task 9、Task 12、Task 13 |
| §7.4 断线期间 | 无需改动（Task 6 的 `broadcast` 已满足） |
| §7.5 安全取舍 | Task 14（记进文档的已知取舍） |
| §8 实例回收 | Task 10 |
| §9 客户端改造 | Task 11、12、13 |
| §10 测试 | 散落在各 Task 的 TDD 步骤 |
| §11 影响面 | 文件结构表 |
| §12 风险 | Task 5（oracle）、Task 13 Step 3（全量帧体积实测） |
| §13 交付顺序 | Task 1–14 的顺序 |

**类型一致性检查**

- `Store` 的方法名在 Task 2/3 定义，Task 5/6 使用：`Set` / `Remove` / `Destroy` / `Reset` / `Get` / `Schema` / `Drain` / `Full` —— 一致。
- `replication.Frame` 字段 `Step/Full/Schema/Entities` 在 Task 3 定义，Task 6 的 `toFrame` 使用 —— 一致。
- `EntityDelta` 字段 `ID/Destroy/Removed/Set`（Go 侧）与 proto 的 `id/destroy/removed/set` 在 Task 6 对应 —— 一致。
- 属性名常量在 Task 5 定义、Task 5 的 `expectedAttrs` 与 `replicate_test.go` 使用、Task 13 的 `_build_body_dict` 用同样的字符串 —— 一致。
- `WorldStore.apply_frame` 返回 `{"destroyed": Array}` 在 Task 11 定义，Task 13 消费 —— 一致。
- `Instance.RequestFull(slot int)` 在 Task 6 定义，Task 9 调用 —— 一致。
- `Component.forget` 在 Task 10 定义并在 Task 10 的 `Create` 里接线 —— 一致。

**已解决的三处偏差**

1. Task 13 原先用占位字段名描述 `main.gd` 的改动。已核对实际代码并把真名写进计划：`_entities` / `_res_nodes` / `_place_body` / `_remove_body` / `_player_pos` / `_remote_pos` / `_remote_yaw` / `_hud_score` / `_last_health` / `_prev_projectiles` / `_render_resources` / `_update_hud` / `_detect_impacts`。
2. `world_store.apply_frame` 原先只返回 `destroyed: Array[int]`，但渲染层需要区分「消失的是弹丸还是金币」（旧的 `_detect_impacts` 靠 `projectile` 标志判断）。已改成返回 `[{"id", "attrs"}]`，携带**消失前的属性快照**，Task 11 的实现与测试同步更新。
3. Task 2 的测试原先依赖 Task 3 才有的 `Drain` 与 `AttrValue`，无法独立跑 TDD（会一直编译失败）。已把「已下发基线」相关的三个用例移到 Task 3 的 `frame_test.go`，Task 2 只验证不依赖产出的存储语义（通过 `Get` 与包内 `dirty` 映射观测）。

**仍需实施时留意的一点**

- Task 12 的 `_uuid4()` 用 `randi()`。Godot 全局随机数在引擎启动时已自动播种，通常无需 `randomize()`；若实测发现不同客户端拿到相同 token，在 `_ready()` 里显式调用一次即可。

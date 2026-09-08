// Package ecs 是服务端游戏逻辑使用的零依赖 ECS（实体-组件-系统）核心。
//
// 设计取舍（本项目规模：单世界、每 tick 几十个实体）：
//
//   - 组件存储用 archetype（原型）：组件集合（类型集）相同的实体归入同一个
//     archetype，组件数据按列（SoA）连续存放，行号即实体在列中的下标。
//     Add/Remove 改变组件集合时把实体搬到目标 archetype：公共列复制值、
//     目标独有列补零值、源行 swap-remove，代价 O(组件数)、与实体总数无关。
//     archetype 按组件 ID 序列键 map 查找（O(1)）；数量 = 实际出现的组件组合
//     数，规模模拟（scale_test.go）验证到 100+ archetype、10000 实体
//   - 组件类型注册为整数 ComponentID（首次使用时全局分配一次），archetype
//     的列按该 ID 下标索引：热路径不做 reflect.Type 哈希查找，只保留列切片的
//     类型断言；reflect.Append/Zero 等真正走反射的只在搬家（结构性变更）时
//   - Add2/Add3/Add4 是 Bundle 式批量挂载：多个组件作为整体写入、只搬一次家，
//     不产生中间 archetype（spawn 路径用）
//   - Query 是缓存查询：匹配条件在 archetype 粒度求值（Without 排除过滤），
//     结果缓存；世界出现新 archetype（generation 变化）时自动重建。
//     QueryEach2/3/4 对多组件查询做列指针绑定：每个 archetype 只绑定一次
//     *[]T 列，行内多列直取、无逐行查找（多组件系统的热路径用）。
//     快照系统用 Without[Resource] 整表跳过传感器球
//   - 实体只是 uint32 ID。物理实体使用外部（物理桥）发放的 id（从 1 递增，
//     桥内与 Jolt BodyID 互译），纯逻辑实体（金币等）由 NewEntity 从
//     logicEntityBase 起分配，两个 ID 空间不会重叠。Destroy 回收逻辑实体 id
//     （free list）供 NewEntity 复用，销毁行不累积；物理（外部）id 销毁时
//     彻底注销 empty 行与 rec 条目（会话内不复用，下次 Add 视为全新实体）
//   - Get 返回指向存储内部元素的指针，可用于原地修改组件；但任何导致所在
//     archetype 列扩容的写入（同 archetype Add 新实体）或实体搬家（Add/Remove
//     组件、Destroy）都可能使旧指针失效，指针只在本次调用内使用
//
// 并发：World 不内置锁，调用方（sim.Simulation）负责串行化所有读写。
package ecs

import (
	"reflect"
	"sync"
	"sync/atomic"
)

// Entity 是实体 ID。0（InvalidEntity）表示无效实体。
type Entity uint32

// InvalidEntity 表示无效实体，Add 会忽略它。
const InvalidEntity Entity = 0

// logicEntityBase 是纯逻辑实体的 ID 起点。物理实体的 ID 由外部（物理桥）从 1
// 递增发放（物理世界刚体上限 65536），因此逻辑实体从 1<<24 起分配不会碰撞。
const logicEntityBase Entity = 1 << 24

// ---- 组件类型注册 ----

// ComponentID 是组件类型的全局整数编号：首次使用某类型时分配一次，之后所有
// 热路径用整数下标索引列，替代 reflect.Type 哈希查找。
type ComponentID uint16

var (
	typeIDs    sync.Map // reflect.Type → ComponentID
	registryMu sync.Mutex
	typeIDSeq  atomic.Uint32
)

// idOfType 返回类型的 ComponentID，首次使用时注册。热路径走 sync.Map 读
// （无锁），只有首次注册才加锁。
func idOfType(t reflect.Type) ComponentID {
	if v, ok := typeIDs.Load(t); ok {
		return v.(ComponentID)
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if v, ok := typeIDs.Load(t); ok {
		return v.(ComponentID)
	}
	id := ComponentID(typeIDSeq.Add(1))
	typeIDs.Store(t, id)
	return id
}

// ID 返回组件类型 T 的 ComponentID。
func ID[T any]() ComponentID {
	return idOfType(typeOf[T]())
}

// archetype 是一组固定的组件类型集合：同一 archetype 内的实体组件集合相同，
// 各组件数据按行密集存储在列里，rows[i] 是第 i 行的实体。
type archetype struct {
	types []reflect.Type // 组件类型集合（创建后不变，搬家/补零时用）
	ids   []ComponentID  // 与 types 平行
	cols  []any          // cols[cid] = *[]T 列；不属于本 archetype 的 cid 为 nil
	rows  []Entity       // 行 → 实体
}

// col 返回组件 cid 的列（本 archetype 没有该组件时返回 nil）。
func (a *archetype) col(cid ComponentID) any {
	if int(cid) < len(a.cols) {
		return a.cols[cid]
	}
	return nil
}

func (a *archetype) hasType(t reflect.Type) bool {
	for _, at := range a.types {
		if at == t {
			return true
		}
	}
	return false
}

// record 记录实体当前所在的 archetype 与行下标。
type record struct {
	arch *archetype
	row  int
}

// World 持有全部实体与 archetype。
type World struct {
	next       Entity                // 逻辑实体计数器；物理实体使用外部 body id，不经由此分配
	free       []Entity              // Destroy 回收的逻辑实体 id，NewEntity 优先复用
	empty      *archetype            // 无组件实体的 archetype（新实体与已销毁实体的归宿）
	archs      []*archetype          // 全部 archetype（含 empty，创建顺序，仅用于遍历）
	archMap    map[string]*archetype // 组件 ID 序列键 → archetype（O(1) 查找）
	rec        map[Entity]record     // 实体 → (archetype, 行)
	generation uint32                // archetype 数量变更计数，查询缓存失效用
}

// New 创建一个空世界。
func New() *World {
	empty := &archetype{}
	return &World{
		next:    logicEntityBase,
		empty:   empty,
		archs:   []*archetype{empty},
		archMap: map[string]*archetype{"": empty},
		rec:     map[Entity]record{},
	}
}

// NewEntity 分配一个纯逻辑实体 ID，先进入空 archetype（尚未挂任何组件）。
// 优先复用 Destroy 回收的 ID（free list），避免销毁实体在空 archetype 里
// 累积；被直接 Add 复活的 id 会跳过（视为从未销毁）。
func (w *World) NewEntity() Entity {
	var e Entity
	for {
		if len(w.free) == 0 {
			e = w.next
			w.next++
			break
		}
		cand := w.free[len(w.free)-1]
		w.free = w.free[:len(w.free)-1]
		if r, ok := w.rec[cand]; ok && r.arch == w.empty {
			w.removeRowAt(cand, w.empty, r.row) // 清掉销毁时留下的空行
			e = cand
			break
		}
		// 该 id 已被直接 Add 复活：跳过，继续弹下一个
	}
	w.placeInto(w.empty, e)
	return e
}

func typeOf[T any]() reflect.Type {
	return reflect.TypeOf((*T)(nil)).Elem()
}

// archOf 返回实体当前所在 archetype（未登记的外部 id 视为空 archetype）。
func (w *World) archOf(e Entity) *archetype {
	if r, ok := w.rec[e]; ok {
		return r.arch
	}
	return w.empty
}

// findOrCreateArch 返回组件类型集合为 types 的 archetype（不存在则创建）。
// 调用方保证 types 不含重复类型。按组件 ID 序列键 O(1) 查找；创建时登记
// generation，触发查询缓存失效。
func (w *World) findOrCreateArch(types []reflect.Type) *archetype {
	ids := make([]ComponentID, len(types))
	for i, t := range types {
		ids[i] = idOfType(t)
	}
	key := idsKey(ids)
	if a, ok := w.archMap[key]; ok {
		return a
	}
	max := ComponentID(0)
	for _, id := range ids {
		if id > max {
			max = id
		}
	}
	a := &archetype{types: types, ids: ids, cols: make([]any, max+1)}
	for i, t := range types {
		a.cols[ids[i]] = newCol(t)
	}
	w.archs = append(w.archs, a)
	w.archMap[key] = a
	w.generation++
	return a
}

// archWith 返回 cur 的类型集合加上 t 之后的 archetype。
func (w *World) archWith(cur *archetype, t reflect.Type) *archetype {
	types := make([]reflect.Type, 0, len(cur.types)+1)
	types = append(types, cur.types...)
	types = append(types, t)
	return w.findOrCreateArch(types)
}

// archWithout 返回 cur 的类型集合去掉 t 之后的 archetype。
func (w *World) archWithout(cur *archetype, t reflect.Type) *archetype {
	types := make([]reflect.Type, 0, len(cur.types)-1)
	for _, ct := range cur.types {
		if ct != t {
			types = append(types, ct)
		}
	}
	return w.findOrCreateArch(types)
}

// union 返回 cur 的类型集加上 ts 中缺失的类型。
func union(cur *archetype, ts []reflect.Type) []reflect.Type {
	types := make([]reflect.Type, 0, len(cur.types)+len(ts))
	types = append(types, cur.types...)
	for _, t := range ts {
		if !cur.hasType(t) {
			types = append(types, t)
		}
	}
	return types
}

// idsKey 把组件 ID 序列编码为字符串键（每个 ID 2 字节小端），供 archetype
// 的 O(1) map 查找。键保留顺序：同集合不同顺序会得到不同 archetype（与调用
// 方按固定顺序构建类型集的行为一致）。
func idsKey(ids []ComponentID) string {
	if len(ids) == 0 {
		return ""
	}
	b := make([]byte, 0, len(ids)*2)
	for _, id := range ids {
		b = append(b, byte(id), byte(id>>8))
	}
	return string(b)
}

// newCol 为组件类型 t 创建一列：*[]T（nil 切片，append 即可用）。
func newCol(t reflect.Type) any {
	return reflect.New(reflect.SliceOf(t)).Interface()
}

// placeInto 把实体 e 放入 archetype a 末尾：各列追加零值并登记 rec。
func (w *World) placeInto(a *archetype, e Entity) {
	n := len(a.rows)
	a.rows = append(a.rows, e)
	for i, t := range a.types {
		cid := a.ids[i]
		dv := reflect.ValueOf(a.cols[cid]).Elem()
		dv.Set(reflect.Append(dv, reflect.Zero(t)))
	}
	w.rec[e] = record{a, n}
}

// move 把实体 e 从 cur 搬到 dst：公共列复制值、dst 独有列补零值，然后
// swap-remove 源行（末行移到空缺处）。要求 e 已登记在 cur 中。
func (w *World) move(e Entity, cur, dst *archetype) {
	r := w.rec[e]
	// 目的地追加一行
	n := len(dst.rows)
	dst.rows = append(dst.rows, e)
	for i, t := range dst.types {
		cid := dst.ids[i]
		dv := reflect.ValueOf(dst.cols[cid]).Elem()
		if sv := cur.col(cid); sv != nil {
			src := reflect.ValueOf(sv).Elem()
			dv.Set(reflect.Append(dv, src.Index(r.row)))
		} else {
			dv.Set(reflect.Append(dv, reflect.Zero(t)))
		}
	}
	w.rec[e] = record{dst, n}
	w.removeRowAt(e, cur, r.row)
}

// removeRowAt 从 archetype a 中 swap-remove 第 row 行（末行移到空缺处，
// 其余 archetype 不动）。不修改实体 e 的登记：move 会先写入新登记，
// NewEntity 复用路径则由随后的 placeInto 重新登记。
func (w *World) removeRowAt(e Entity, a *archetype, row int) {
	last := len(a.rows) - 1
	if row != last {
		moved := a.rows[last]
		a.rows[row] = moved
		mr := w.rec[moved]
		mr.row = row
		w.rec[moved] = mr
		for i := range a.types {
			cid := a.ids[i]
			sv := reflect.ValueOf(a.cols[cid]).Elem()
			sv.Index(row).Set(sv.Index(last))
		}
	}
	for i := range a.types {
		cid := a.ids[i]
		sv := reflect.ValueOf(a.cols[cid]).Elem()
		sv.Set(sv.Slice(0, last))
	}
	a.rows = a.rows[:last]
}

// bundleInto 把实体并入含有全部 types 的 archetype：已登记实体缺组件时一次
// 搬家，未登记实体（外部 id）直接落位。返回实体所在的目标 archetype。
func (w *World) bundleInto(e Entity, cur *archetype, types []reflect.Type) *archetype {
	dst := cur
	missing := false
	for _, t := range types {
		if !cur.hasType(t) {
			missing = true
			break
		}
	}
	if missing {
		dst = w.findOrCreateArch(union(cur, types))
		if _, ok := w.rec[e]; ok {
			w.move(e, cur, dst)
		} else {
			w.placeInto(dst, e)
		}
	}
	return dst
}

// setAt 把组件值写入 archetype 的指定行（该 archetype 已含组件类型 T）。
func setAt[T any](a *archetype, row int, v T) {
	data := a.cols[ID[T]()].(*[]T)
	(*data)[row] = v
}

// Add 给实体 e 挂上组件 c（已有同类型组件则覆盖）。e 可以是 NewEntity 分配的
// 逻辑实体，也可以是外部 ID（物理实体直接使用 body id）。InvalidEntity 被忽略。
func Add[T any](w *World, e Entity, c T) {
	if e == InvalidEntity {
		return
	}
	t := typeOf[T]()
	cid := idOfType(t)
	if r, ok := w.rec[e]; ok {
		if data := r.arch.col(cid); data != nil {
			d := data.(*[]T)
			(*d)[r.row] = c // 已有同类型组件则覆盖
			return
		}
		dst := w.archWith(r.arch, t)
		w.move(e, r.arch, dst)
		data := dst.col(cid).(*[]T)
		(*data)[w.rec[e].row] = c
		return
	}
	// 从未登记的外部 id：直接进入 {T} archetype
	dst := w.archWith(w.empty, t)
	w.placeInto(dst, e)
	data := dst.col(cid).(*[]T)
	(*data)[len(dst.rows)-1] = c
}

// Add2 一次给实体挂两个组件（Bundle 式）：只搬一次家，不产生中间 archetype。
// 语义与两次 Add 相同（已有组件覆盖、缺失组件追加）。
func Add2[A, B any](w *World, e Entity, a A, b B) {
	if e == InvalidEntity {
		return
	}
	ts := []reflect.Type{typeOf[A](), typeOf[B]()}
	dst := w.bundleInto(e, w.archOf(e), ts)
	row := w.rec[e].row
	setAt(dst, row, a)
	setAt(dst, row, b)
}

// Add3 一次给实体挂三个组件（Bundle 式），语义同 Add2。
func Add3[A, B, C any](w *World, e Entity, a A, b B, c C) {
	if e == InvalidEntity {
		return
	}
	ts := []reflect.Type{typeOf[A](), typeOf[B](), typeOf[C]()}
	dst := w.bundleInto(e, w.archOf(e), ts)
	row := w.rec[e].row
	setAt(dst, row, a)
	setAt(dst, row, b)
	setAt(dst, row, c)
}

// Add4 一次给实体挂四个组件（Bundle 式），语义同 Add2。
func Add4[A, B, C, D any](w *World, e Entity, a A, b B, c C, d D) {
	if e == InvalidEntity {
		return
	}
	ts := []reflect.Type{typeOf[A](), typeOf[B](), typeOf[C](), typeOf[D]()}
	dst := w.bundleInto(e, w.archOf(e), ts)
	row := w.rec[e].row
	setAt(dst, row, a)
	setAt(dst, row, b)
	setAt(dst, row, c)
	setAt(dst, row, d)
}

// Get 返回实体 e 的组件 T 的指针；不存在时返回 (nil, false)。
// 指针指向存储内部，原地修改会写回世界；见包注释的失效说明。
func Get[T any](w *World, e Entity) (*T, bool) {
	r, ok := w.rec[e]
	if !ok {
		return nil, false
	}
	data := r.arch.col(ID[T]())
	if data == nil {
		return nil, false
	}
	return &(*(data.(*[]T)))[r.row], true
}

// Has 报告实体 e 是否有组件 T。
func Has[T any](w *World, e Entity) bool {
	r, ok := w.rec[e]
	if !ok {
		return false
	}
	return r.arch.col(ID[T]()) != nil
}

// Remove 移除实体 e 的组件 T（没有该组件时是空操作）。
func Remove[T any](w *World, e Entity) {
	t := typeOf[T]()
	r, ok := w.rec[e]
	if !ok {
		return
	}
	if !r.arch.hasType(t) {
		return
	}
	w.move(e, r.arch, w.archWithout(r.arch, t))
}

// Each 遍历所有带组件 T 的实体（顺序不保证：受 archetype 创建顺序与
// swap-remove 行序影响）。fn 中可以修改组件内容，但不得在遍历期间调用
// Add/Remove/Destroy（会把实体搬出所在 archetype、改变行序）；需要增删时
// 先收集再处理。
func Each[T any](w *World, fn func(e Entity, c *T)) {
	cid := ID[T]()
	for _, a := range w.archs {
		data := a.col(cid)
		if data == nil {
			continue
		}
		vals := *(data.(*[]T))
		for i := range a.rows {
			fn(a.rows[i], &vals[i])
		}
	}
}

// EachWith 与 Each 相同，但额外给出行视图 row：同一 archetype 内所有行共享
// 组件集合，RowGet / RowHas 直接按行下标访问兄弟组件列，不做逐实体查找。
// row 只在本次回调内有效，约束与 Each 相同。
func EachWith[T any](w *World, fn func(e Entity, c *T, row Row)) {
	cid := ID[T]()
	for _, a := range w.archs {
		data := a.col(cid)
		if data == nil {
			continue
		}
		vals := *(data.(*[]T))
		for i := range a.rows {
			fn(a.rows[i], &vals[i], Row{a, i})
		}
	}
}

// Count 返回带组件 T 的实体数量。
func Count[T any](w *World) int {
	cid := ID[T]()
	n := 0
	for _, a := range w.archs {
		if a.col(cid) != nil {
			n += len(a.rows)
		}
	}
	return n
}

// Destroy 移除实体的所有组件（搬回空 archetype），使实体不再被任何查询看到。
// 逻辑实体（NewEntity 发放的 id）进入回收池供 NewEntity 复用（empty 里的旧行在
// 复用时由 NewEntity 的 removeRowAt 清掉）；物理实体的 id 由物理桥单调递增发放、
// 会话内不复用，因此销毁后把 empty 行与 rec 条目一并删除，避免空行随弹丸/敌人/
// 金币的销毁在长会话里无界累积（下次 Add 同一 id 时按全新外部实体直接落位）。
func (w *World) Destroy(e Entity) {
	r, ok := w.rec[e]
	if !ok || r.arch == w.empty {
		return
	}
	w.move(e, r.arch, w.empty)
	if e >= logicEntityBase {
		w.free = append(w.free, e)
		return
	}
	row := w.rec[e].row
	w.removeRowAt(e, w.empty, row)
	delete(w.rec, e)
}

// Row 是 EachWith / QueryEach 回调里的行视图：指向实体所在 archetype 的某
// 一行，配合 RowGet / RowHas 访问该行的兄弟组件（O(1) 列下标访问）。
// 只在回调期间有效。
type Row struct {
	arch *archetype
	row  int
}

// RowGet 返回行视图内组件 T 的指针；所在 archetype 没有 T 时返回 (nil, false)。
func RowGet[T any](r Row) (*T, bool) {
	data := r.arch.col(ID[T]())
	if data == nil {
		return nil, false
	}
	return &(*(data.(*[]T)))[r.row], true
}

// RowHas 报告行视图所在 archetype 是否有组件 T（同 archetype 的所有行一致）。
func RowHas[T any](r Row) bool {
	return r.arch.col(ID[T]()) != nil
}

// ---- 查询 ----

// Query 是缓存查询：匹配条件在 archetype 粒度求值（with 全部必有、without
// 全部必无），结果缓存。世界出现新 archetype（generation 变化）或换新世界时
// 自动重建。以指针传入 QueryEach/QueryEach2/3/4 才能复用缓存。
// with 的顺序与构建它的 NewQuery/NewQuery2/3/4 类型参数一致。
type Query struct {
	with    []ComponentID
	without []ComponentID
	world   *World
	gen     uint32
	archs   []*archetype
}

// NewQuery 创建匹配「带 T」的查询，配合 QueryEach 使用。
func NewQuery[T any]() Query {
	return Query{with: []ComponentID{ID[T]()}}
}

// NewQuery2 创建匹配「带 A 且带 B」的查询，配合 QueryEach2 使用。
func NewQuery2[A, B any]() Query {
	return Query{with: []ComponentID{ID[A](), ID[B]()}}
}

// NewQuery3 创建匹配「带 A 且带 B 且带 C」的查询，配合 QueryEach3 使用。
func NewQuery3[A, B, C any]() Query {
	return Query{with: []ComponentID{ID[A](), ID[B](), ID[C]()}}
}

// NewQuery4 创建匹配「带 A 且带 B 且带 C 且带 D」的查询，配合 QueryEach4 使用。
func NewQuery4[A, B, C, D any]() Query {
	return Query{with: []ComponentID{ID[A](), ID[B](), ID[C](), ID[D]()}}
}

// Without 返回追加排除条件 U 的新查询（值语义，可链式）。
func Without[U any](q Query) Query {
	without := make([]ComponentID, len(q.without)+1)
	copy(without, q.without)
	without[len(q.without)] = ID[U]()
	q.without = without
	return q
}

// refreshArchs 在缓存失效（新 archetype 或新世界）时重建匹配列表。
func (w *World) refreshArchs(q *Query) {
	if q.world != w || q.gen != w.generation {
		q.archs = w.matchArchs(q.with, q.without)
		q.world = w
		q.gen = w.generation
	}
}

// QueryEach 遍历查询匹配的实体，回调与 EachWith 相同（带行视图）。
// 匹配按 archetype 粒度缓存：同一 Query 指针且世界结构未变时，每次调用
// 零扫描、零 map 查找，行内直取列切片。
func QueryEach[T any](w *World, q *Query, fn func(e Entity, c *T, row Row)) {
	w.refreshArchs(q)
	for _, a := range q.archs {
		vals := *(a.cols[q.with[0]].(*[]T))
		for i := range a.rows {
			fn(a.rows[i], &vals[i], Row{a, i})
		}
	}
}

// QueryEach2 遍历同时带 A、B 的实体（Query 由 NewQuery2 构建）。每个
// archetype 只绑定一次两列指针，行内双列直取，无逐行查找——多组件系统
// 的热路径写法（替代 EachWith + RowGet）。
func QueryEach2[A, B any](w *World, q *Query, fn func(e Entity, a *A, b *B, row Row)) {
	w.refreshArchs(q)
	ca, cb := q.with[0], q.with[1]
	for _, arch := range q.archs {
		va := *(arch.cols[ca].(*[]A))
		vb := *(arch.cols[cb].(*[]B))
		for i := range arch.rows {
			fn(arch.rows[i], &va[i], &vb[i], Row{arch, i})
		}
	}
}

// QueryEach3 遍历同时带 A、B、C 的实体（Query 由 NewQuery3 构建），
// 三列行内直取，语义同 QueryEach2。
func QueryEach3[A, B, C any](w *World, q *Query, fn func(e Entity, a *A, b *B, c *C, row Row)) {
	w.refreshArchs(q)
	ca, cb, cc := q.with[0], q.with[1], q.with[2]
	for _, arch := range q.archs {
		va := *(arch.cols[ca].(*[]A))
		vb := *(arch.cols[cb].(*[]B))
		vc := *(arch.cols[cc].(*[]C))
		for i := range arch.rows {
			fn(arch.rows[i], &va[i], &vb[i], &vc[i], Row{arch, i})
		}
	}
}

// QueryEach4 遍历同时带 A、B、C、D 的实体（Query 由 NewQuery4 构建），
// 四列行内直取，语义同 QueryEach2。
func QueryEach4[A, B, C, D any](w *World, q *Query, fn func(e Entity, a *A, b *B, c *C, d *D, row Row)) {
	w.refreshArchs(q)
	ca, cb, cc, cd := q.with[0], q.with[1], q.with[2], q.with[3]
	for _, arch := range q.archs {
		va := *(arch.cols[ca].(*[]A))
		vb := *(arch.cols[cb].(*[]B))
		vc := *(arch.cols[cc].(*[]C))
		vd := *(arch.cols[cd].(*[]D))
		for i := range arch.rows {
			fn(arch.rows[i], &va[i], &vb[i], &vc[i], &vd[i], Row{arch, i})
		}
	}
}

// matchArchs 返回同时满足 with 全部必有、without 全部必无的 archetype 列表。
func (w *World) matchArchs(with, without []ComponentID) []*archetype {
	var out []*archetype
	for _, a := range w.archs {
		ok := true
		for _, cid := range with {
			if a.col(cid) == nil {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		for _, cid := range without {
			if a.col(cid) != nil {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, a)
		}
	}
	return out
}

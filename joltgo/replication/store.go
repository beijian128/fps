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

import "sort"

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
func (s *Store) Set(id uint32, attr string, v Value) {
	cid := s.attrOf(attr)

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
func (s *Store) Destroy(id uint32) {
	delete(s.values, id)
	delete(s.dirty, id)
	delete(s.gone, id)
	s.dead[id] = true
}

// Reset 清空全部实体与脏集，保留属性声明（对局 Reset 用）。
func (s *Store) Reset() {
	s.values = map[uint32]map[uint32]Value{}
	s.sent = map[uint32]map[uint32]Value{}
	s.dirty = map[uint32]map[uint32]bool{}
	s.gone = map[uint32]map[uint32]bool{}
	s.dead = map[uint32]bool{}
}

// Get 返回实体 id 的属性 attr 的终值；不存在时返回 (Value{}, false)。
// 供测试与调试使用，不参与同步路径。
func (s *Store) Get(id uint32, attr string) (Value, bool) {
	m := s.values[id]
	if m == nil {
		return Value{}, false
	}
	v, ok := m[s.attrOf(attr)]
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

// sortedIDs 返回按键升序排列的实体 ID 列表（map 迭代无序，输出必须确定）。
func sortedIDs[V any](m map[uint32]V) []uint32 {
	out := make([]uint32, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

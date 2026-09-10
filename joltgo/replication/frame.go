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

// Package ecs 是服务端游戏逻辑使用的零依赖 ECS（实体-组件-系统）核心。
//
// 设计取舍（本项目规模：单世界、每 tick 几十个实体）：
//
//   - 组件存储用稀疏集（dense 数组 + sparse 索引）：增删 O(1)，遍历走连续数组；
//     Each 按插入顺序迭代（删除采用 swap-remove，会改变顺序，系统不应依赖顺序）
//   - 实体只是 uint32 ID。物理实体使用外部（物理桥）发放的 id（从 1 递增，
//     桥内与 Jolt BodyID 互译），纯逻辑实体（金币等）由 NewEntity 从
//     logicEntityBase 起分配，两个 ID 空间不会重叠
//   - Get 返回指向存储内部元素的指针，可用于原地修改组件；但任何会触发同类型
//     组件存储扩容的写入（Add 新实体）都可能使旧指针失效，指针只在本次调用内使用
//
// 并发：World 不内置锁，调用方（sim.Simulation）负责串行化所有读写。
package ecs

import "reflect"

// Entity 是实体 ID。0（InvalidEntity）表示无效实体。
type Entity uint32

// InvalidEntity 表示无效实体，Add 会忽略它。
const InvalidEntity Entity = 0

// logicEntityBase 是纯逻辑实体的 ID 起点。物理实体的 ID 由外部（物理桥）从 1
// 递增发放（物理世界刚体上限 65536），因此逻辑实体从 1<<24 起分配不会碰撞。
const logicEntityBase Entity = 1 << 24

// storage 是单个组件类型存储的类型擦除接口，供 World 统一管理。
type storage interface {
	remove(e Entity)
	has(e Entity) bool
}

// store 是稀疏集组件存储：dense 保存实体 ID，data 与 dense 一一对应，
// sparse 是实体到 dense 下标的映射。
type store[T any] struct {
	dense  []Entity
	sparse map[Entity]int
	data   []T
}

func newStore[T any]() *store[T] {
	return &store[T]{sparse: map[Entity]int{}}
}

func (s *store[T]) add(e Entity, c T) {
	if i, ok := s.sparse[e]; ok {
		s.data[i] = c // 已存在则覆盖
		return
	}
	s.sparse[e] = len(s.dense)
	s.dense = append(s.dense, e)
	s.data = append(s.data, c)
}

// remove 与末尾元素交换后收缩，O(1)，代价是改变遍历顺序。
func (s *store[T]) remove(e Entity) {
	i, ok := s.sparse[e]
	if !ok {
		return
	}
	last := len(s.dense) - 1
	moved := s.dense[last]
	s.dense[i] = moved
	s.data[i] = s.data[last]
	s.sparse[moved] = i
	s.dense = s.dense[:last]
	s.data = s.data[:last]
	delete(s.sparse, e)
}

func (s *store[T]) has(e Entity) bool {
	_, ok := s.sparse[e]
	return ok
}

// World 持有全部实体与组件存储。
type World struct {
	next   Entity // 逻辑实体计数器；物理实体使用外部 body id，不经由此分配
	stores map[reflect.Type]any
}

// New 创建一个空世界。
func New() *World {
	return &World{next: logicEntityBase, stores: map[reflect.Type]any{}}
}

// NewEntity 分配一个纯逻辑实体 ID（尚未挂任何组件）。
func (w *World) NewEntity() Entity {
	e := w.next
	w.next++
	return e
}

// storeOf 返回组件类型 T 的存储，首次使用时惰性注册。
func storeOf[T any](w *World) *store[T] {
	t := reflect.TypeOf((*T)(nil)).Elem()
	if s, ok := w.stores[t]; ok {
		return s.(*store[T])
	}
	s := newStore[T]()
	w.stores[t] = s
	return s
}

// Add 给实体 e 挂上组件 c（已有同类型组件则覆盖）。e 可以是 NewEntity 分配的
// 逻辑实体，也可以是外部 ID（物理实体直接使用 body id）。InvalidEntity 被忽略。
func Add[T any](w *World, e Entity, c T) {
	if e == InvalidEntity {
		return
	}
	storeOf[T](w).add(e, c)
}

// Get 返回实体 e 的组件 T 的指针；不存在时返回 (nil, false)。
// 指针指向存储内部，原地修改会写回世界；见包注释的失效说明。
func Get[T any](w *World, e Entity) (*T, bool) {
	s, ok := w.stores[reflect.TypeOf((*T)(nil)).Elem()]
	if !ok {
		return nil, false
	}
	st := s.(*store[T])
	i, ok := st.sparse[e]
	if !ok {
		return nil, false
	}
	return &st.data[i], true
}

// Has 报告实体 e 是否有组件 T。
func Has[T any](w *World, e Entity) bool {
	s, ok := w.stores[reflect.TypeOf((*T)(nil)).Elem()]
	if !ok {
		return false
	}
	return s.(*store[T]).has(e)
}

// Remove 移除实体 e 的组件 T（没有该组件时是空操作）。
func Remove[T any](w *World, e Entity) {
	s, ok := w.stores[reflect.TypeOf((*T)(nil)).Elem()]
	if !ok {
		return
	}
	s.(*store[T]).remove(e)
}

// Each 遍历所有带组件 T 的实体。fn 中可以修改组件内容，但不得在遍历期间调用
// Add/Remove/Destroy 改变 T 的存储（会打乱迭代）；需要增删时先收集再处理。
func Each[T any](w *World, fn func(e Entity, c *T)) {
	s, ok := w.stores[reflect.TypeOf((*T)(nil)).Elem()]
	if !ok {
		return
	}
	st := s.(*store[T])
	for i := range st.dense {
		fn(st.dense[i], &st.data[i])
	}
}

// Count 返回带组件 T 的实体数量。
func Count[T any](w *World) int {
	s, ok := w.stores[reflect.TypeOf((*T)(nil)).Elem()]
	if !ok {
		return 0
	}
	return len(s.(*store[T]).dense)
}

// Destroy 移除实体的所有组件，使实体不再被任何查询看到。
func (w *World) Destroy(e Entity) {
	for _, s := range w.stores {
		s.(storage).remove(e)
	}
}

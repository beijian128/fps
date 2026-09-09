package sim

// 快照协议类型：wire 契约在 game/protos/game.proto（protobuf），本文件是 sim 侧
// 的内部结构，由 game 组件转换为 protobuf。字段与 proto 一一对应。

// BodyInfo 是单个刚体的快照。
type BodyInfo struct {
	ID         uint32
	Type       int // 0 = box, 1 = sphere, 2 = enemy capsule
	Static     bool
	Target     bool
	Enemy      bool
	Projectile bool
	Pos        [3]float32
	Quat       [4]float32
	Size       [3]float32
	Health     float32
	Active     bool
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
	Kind int // 0 = 金币
}

// State 是每 tick 广播给客户端的完整状态快照。计分/金币/波次是共享团队状态，
// 只有玩家位置/血量按玩家区分（Players 顺序 = 玩家槽位 0/1）。
type State struct {
	Bodies    []BodyInfo
	Resources []ResourceInfo
	Players   []PlayerState
	Step      int
	Score     int
	Wave      int
	Gold      int
}

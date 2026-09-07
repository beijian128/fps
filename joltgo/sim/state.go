package sim

// 快照协议类型：JSON 字段与旧实现逐字一致，客户端无需任何改动。
// 见 docs/API.md。

// BodyInfo 是单个刚体的快照。
type BodyInfo struct {
	ID         uint32     `json:"id"`
	Type       int        `json:"type"` // 0 = box, 1 = sphere, 2 = enemy capsule
	Static     bool       `json:"static"`
	Target     bool       `json:"target"`
	Enemy      bool       `json:"enemy"`
	Projectile bool       `json:"projectile"`
	Pos        [3]float32 `json:"pos"`
	Quat       [4]float32 `json:"quat"`
	Size       [3]float32 `json:"size"`
	Health     float32    `json:"health"`
	Active     bool       `json:"active"`
}

// PlayerState 是玩家快照（Pos 为脚底位置）。
type PlayerState struct {
	Pos    [3]float32 `json:"pos"`
	Health float32    `json:"health"`
}

// ResourceInfo 是可拾取资源快照。
type ResourceInfo struct {
	ID   int        `json:"id"`
	Pos  [3]float32 `json:"pos"`
	Kind int        `json:"kind"` // 0 = 金币
}

// State 是每 tick 广播给客户端的完整状态快照。
type State struct {
	Bodies    []BodyInfo     `json:"bodies"`
	Resources []ResourceInfo `json:"resources"`
	Player    PlayerState    `json:"player"`
	Step      int            `json:"step"`
	Score     int            `json:"score"`
	Wave      int            `json:"wave"`
	Gold      int            `json:"gold"`
}

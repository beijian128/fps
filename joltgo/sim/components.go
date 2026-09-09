// Package sim 是服务端的 ECS 模拟层：组件定义、每 tick 运行的系统、场景搭建与快照。
//
// 只依赖 ecs 核心与 Physics 接口，不包含任何 cgo 调用——物理世界由调用方
// （package main 的 physics.go）注入实现，因此本包可以用 fake 物理做单元测试。
//
// 实体 ID 约定：物理实体的 id 由物理桥（package main 的 physics.go）从 1 递增
// 发放（桥内翻译成 Jolt BodyID），sim 侧实体 id 即刚体 id；纯逻辑实体（玩家、
// 金币）由 ecs.NewEntity 从 1<<24 起分配，与物理实体不重叠。
package sim

// ---- 组件 ----

// Player 标记玩家实体（每局两个，按 char 槽位 0/1 区分）。玩家是 Jolt 角色
// 控制器，不是刚体，没有 Body。
type Player struct{}

// Input 是玩家实体的最新输入：move 为世界空间水平期望速度（m/s），
// Yaw 是水平朝向（弧度，绕 Y 轴），Jump 是边沿触发，消费后清零。
type Input struct {
	Move [2]float32
	Yaw  float32
	Jump bool
}

// Position 是世界坐标位置（玩家为脚底，刚体为质心）。
type Position [3]float32

// Rotation 是世界坐标旋转四元数（x, y, z, w），仅物理刚体有。
type Rotation [4]float32

// Health 是当前血量（玩家与敌人）。
type Health float32

// Enemy 标记敌人（追击 AI + 贴身伤害），配合 Health 使用。
type Enemy struct{}

// Target 标记可破坏靶球（弹丸命中即摧毁并得分）。
type Target struct{}

// Projectile 标记弹丸，SpawnStep 记录发射时的 tick，用于超时移除。
type Projectile struct {
	SpawnStep int
}

// Resource 标记可拾取资源（金币）：物理上是 Jolt 传感器球——不与刚体碰撞
// （弹丸/箱子穿过），角色控制器接触到即触发拾取。Kind: 0 = 金币。
type Resource struct {
	Kind int
}

// BodyKind 是刚体形状类别，对应快照协议中的 "type" 字段。
type BodyKind int

const (
	BodyBox     BodyKind = 0 // 盒子（地板/墙/箱子）
	BodySphere  BodyKind = 1 // 球（靶球/弹丸）
	BodyCapsule BodyKind = 2 // 胶囊（敌人）
)

// Body 是物理刚体实体的渲染元数据。Static 与 Size 在创建时确定；
// Active（是否仍在模拟、未休眠）由物理同步系统每 tick 刷新。
type Body struct {
	Kind   BodyKind
	Size   [3]float32 // box: 半边长；sphere: 半径在 [0]；capsule: 半径在 [0]、半高在 [1]
	Static bool
	Active bool
}

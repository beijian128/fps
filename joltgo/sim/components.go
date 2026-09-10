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

// Player 标记玩家实体，Idx 是玩家槽位（0/1）。客户端据此认出「哪个实体是我」。
// 玩家是 Jolt 角色控制器，不是刚体，没有 Body。
type Player struct {
	Idx int
}

// Facing 是玩家当前朝向（弧度，绕 Y 轴）。它从 Input 里拆出来单独同步：
// Input 是客户端上行数据，不该回灌给客户端；朝向才是需要下发的。
type Facing struct {
	Yaw float32
}

// GameState 是对局的全局状态，挂在一个单例实体上。做成组件是为了让框架里
// 不存在「顶层字段」这个概念 —— 以后加全局状态也自动走同一套同步机制。
//
// Winner 是对局结果：-1 = 进行中，否则为获胜玩家的槽位（0/1）。击杀/死亡数
// 是**每名玩家自己的**，所以挂在玩家实体的 PlayerScore 上而不是这里。
type GameState struct {
	Winner int32
}

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

// Health 是当前血量（玩家）。
type Health float32

// PlayerScore 是玩家在本局中的战绩（击杀 / 死亡），直接挂在玩家实体上。
type PlayerScore struct {
	Kills  int32
	Deaths int32
}

// PlayerHitbox 标记「跟随某个玩家角色的命中盒刚体」。玩家的移动体是 Jolt 的
// 角色控制器（不是刚体），弹丸（动态刚体 + CCD）看不见它、也不会与它产生刚体
// 接触事件，所以每个玩家额外配一个形状相同的静态胶囊：每 tick 被挪到角色所在
// 处，弹丸靠普通刚体接触命中它 —— 与命中静态几何走的是同一条路径。
//
// 两个角色都会忽略全部命中盒（见 Simulation.init 与 Physics.CharacterIgnoreBody）：
// 命中盒每 tick 才跟随一次，会被角色甩到正前方；对方玩家的命中盒更是隐形墙。
//
// 该刚体不进 ECS 的渲染路径（没有 Body 组件，因此不产生同步流量、客户端不渲染），
// 但它必须在 ECS 里有实体，弹丸系统才能从接触事件认出「打中的是谁」。
type PlayerHitbox struct {
	Idx int
}

// Projectile 标记弹丸，SpawnStep 记录发射时的 tick（用于超时移除），
// Owner 是发射者的玩家槽位（0/1）：弹丸不打自己的命中盒。
type Projectile struct {
	SpawnStep int
	Owner     int
}

// BodyKind 是刚体形状类别，对应快照协议中的 "type" 字段。
type BodyKind int

const (
	BodyBox     BodyKind = 0 // 盒子（地板/墙/箱子）
	BodySphere  BodyKind = 1 // 球（弹丸）
	BodyCapsule BodyKind = 2 // 胶囊（桅杆/烟囱等静态立柱；命中盒不带 Body，不下发）
)

// Body 是物理刚体实体的渲染元数据。Static/Size/Mat 在创建时确定；
// Active（是否仍在模拟、未休眠）由物理同步系统每 tick 刷新。
type Body struct {
	Kind   BodyKind
	Size   [3]float32 // box: 半边长；sphere: 半径在 [0]；capsule: 半径在 [0]、半高在 [1]
	Static bool
	Active bool
	Mat    Material // 视觉材质（见 map.go），随快照下发供客户端配色
}

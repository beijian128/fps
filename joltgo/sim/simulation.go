package sim

// Simulation 组装 ecs 世界、Physics 接口与各系统，对外暴露游戏公开 API。
//
// 并发模型：**单线程所有**。Simulation 不持锁——由调用方（game 包的 Instance
// goroutine）保证同一时刻只有一个 goroutine 访问它：输入通过命令 channel 投递，
// tick 由同一条 goroutine 驱动，全部顺序执行。因此所有方法无需加锁。
// 本包可以用 fake 物理做单元测试（fake 物理同样单线程）。

import (
	"joltgo/ecs"
	"joltgo/replication"
	"math"
)

// TickDT 是模拟 tick 时长（20 Hz）。
const TickDT = 1.0 / 20.0

// MaxPlayers 是每局玩家数。
const MaxPlayers = 2

// 物理调参（游戏侧所有调参都在这里，C++ 包装层不携带任何业务数值）。
const (
	gravityY  = -20.0 // 重力加速度（比真实 9.81 大，跳起/落地更快、手感更利落）
	jumpSpeed = 8.5   // 起跳垂直速度（m/s）

	maxPlayerSpeed = 14.0 // 服务端对客户端上报水平速度的限幅（与客户端 RUN_SPEED 一致）

	characterHalfHeight = 0.5 // 玩家胶囊圆柱半高
	characterRadius     = 0.4 // 玩家胶囊半径
	characterOffsetY    = 0.9 // 胶囊形状上移量（使位置指向脚底）
	characterSpawnY     = 0.0 // 角色控制器初始脚底高度

	projectileRadius  = 0.08 // 弹丸半径（m）
	projectileSpeed   = 60.0 // 弹丸初速（m/s）
	projectileMaxLife = 60   // 弹丸最大存活 tick（20 Hz 下 3 秒）
)

// 对局规则参数（PVP）。
const (
	playerMaxHealth = 100.0 // 满血值（也是复活值）
	playerHitDamage = 34.0  // 每发弹丸命中扣血（3 发击杀）
	killTarget      = 10    // 先到 10 杀获胜
	matchOverTicks  = 100   // 分出胜负后 5 秒自动重开一局（20 Hz × 5）
	hitboxOffsetY   = 0.9   // 命中盒胶囊中心相对角色脚底的抬高量（= characterOffsetY）
	playerSpawnY    = 0.2   // 玩家出生 / 复活点脚底高度
)

// Motion 是刚体运动类型，取值与 Jolt EMotionType 一致（0 静态 / 1 运动学 / 2 动态）。
type Motion int

const (
	MotionStatic Motion = iota
	MotionKinematic
	MotionDynamic
)

// MotionQuality 是高速碰撞检测质量，取值与 Jolt EMotionQuality 一致。
type MotionQuality int

const (
	QualityDiscrete MotionQuality = iota
	QualityLinearCast
)

// Contact 是一对刚体的接触事件（BodyA/BodyB 都是实体 id，即刚体 id）。
// 包装层原样上报所有接触，「是否弹丸命中」由弹丸系统判定。
type Contact struct {
	BodyA uint32
	BodyB uint32
}

// Physics 是物理世界的抽象，只暴露物理层原生能力（世界/刚体/角色控制器/接触
// 事件），隔离 Jolt cgo 调用（package main 实现）。所有游戏业务（敌人/弹丸/
// 靶球/血量/移动策略）都在 sim 侧，不在物理层。刚体 id 即实体 id，由物理层
// 分配、从 1 递增，0 表示失败。一个世界最多两个角色控制器，用 charIdx（0/1）区分。
type Physics interface {
	Create() // 创建物理世界（Reset 前须先 Destroy）
	Destroy()
	SetGravity(x, y, z float32)
	Step(dt float32, collisionSteps int)

	// 刚体（形状 + 位置 + 运动类型；速度/摩擦等属性创建后单独设置）。
	AddBox(hx, hy, hz, x, y, z float32, motion Motion) uint32
	AddSphere(x, y, z, radius float32, motion Motion) uint32
	AddCapsule(x, y, z, halfHeight, radius float32, motion Motion) uint32
	// AddSensorSphere 添加静态传感器球（不与刚体碰撞，角色接触可见——拾取物/触发器）。
	AddSensorSphere(x, y, z, radius float32) uint32
	RemoveBody(id uint32)
	// SetBodyPosition 把刚体瞬移到指定位置（质心）；用于让跟随角色的刚体贴合角色。
	SetBodyPosition(id uint32, x, y, z float32)
	SetBodyVelocity(id uint32, vx, vy, vz float32)
	SetBodyFriction(id uint32, friction float32)
	SetBodyRestitution(id uint32, restitution float32)
	SetBodyMotionQuality(id uint32, quality MotionQuality)

	// Sync 逐个枚举物理世界中的刚体并回调其 id/active/位置/旋转。
	Sync(fn func(id uint32, active bool, pos [3]float32, quat [4]float32))
	// PollContacts 排空接触事件队列（所有刚体对，命中判定由 sim 做）。
	PollContacts() []Contact

	// 角色控制器：形状/出生点/移动策略全部由 sim 决定，物理层只执行。
	// charIdx 是角色槽位（0/1），每个角色有独立的控制器与接触监听器。
	CreateCharacter(charIdx int, halfHeight, radius, offsetY, x, y, z float32)
	SetCharacterDynamicPush(charIdx int, allow bool) // 动态刚体接触时是否允许推动角色
	// CharacterIgnoreBody 让该角色彻底忽略某个刚体（不成障碍、不产生接触事件）。
	// 跟随角色的刚体（命中盒）必须走这里：它会落在角色正前方（对方玩家的命中盒是
	// 隐形墙；自己的命中盒每 tick 才跟随一次），Jolt 会把角色速度清零。
	CharacterIgnoreBody(charIdx int, id uint32)
	CharacterPosition(charIdx int) [3]float32
	SetCharacterPosition(charIdx int, x, y, z float32)
	CharacterVelocity(charIdx int) [3]float32
	SetCharacterVelocity(charIdx int, v [3]float32)
	CharacterOnGround(charIdx int) bool
	UpdateCharacter(charIdx int, dt float32)
}

// Simulation 是 ECS 模拟：世界、物理、玩家实体与全局游戏状态。单线程所有，不加锁。
type Simulation struct {
	physics  Physics
	world    *ecs.World
	players  [MaxPlayers]ecs.Entity
	hitboxes [MaxPlayers]ecs.Entity // 跟随各玩家的命中盒刚体（见 components.go）
	game     ecs.Entity             // 全局状态单例实体（GameState），在 init 里创建
	rep      *replication.Store     // 给客户端同步的属性终值表（见 replicate.go）

	step   int
	score  [MaxPlayers]PlayerScore // 各槽位战绩（PlayerScore 组件只是它的同步载体）
	winner int32                   // -1 = 进行中，否则为获胜槽位
	overAt int                     // 分出胜负的 step（0 = 尚未分出胜负）
}

// New 创建一个空模拟。物理世界在 Init 时由 physics.Create 创建。
func New(p Physics) *Simulation {
	rep := replication.New()
	declareAttributes(rep)
	return &Simulation{
		physics: p,
		world:   ecs.New(),
		rep:     rep,
		game:    ecs.InvalidEntity,
	}
}

// Init 创建物理世界与初始场景。只在启动时调用一次；重复调用等同 Reset（幂等，
// 不会叠加场景或残留旧实体）。
func (s *Simulation) Init() {
	if s.players[0] != ecs.InvalidEntity {
		s.reset()
		return
	}
	s.init()
}

// Shutdown 释放物理世界资源（进程退出前调用一次；调用后不应再 Step/Shoot/DrainFrame）。
func (s *Simulation) Shutdown() {
	s.physics.Destroy()
}

// ApplyInput 记录玩家最新输入（跳跃为边沿触发：服务端在下一个 tick 消费）。
// playerIdx 是玩家槽位（0/1），yaw 是水平朝向（弧度，绕 Y 轴）。
func (s *Simulation) ApplyInput(playerIdx int, move [2]float32, yaw float32, jump bool) {
	if playerIdx < 0 || playerIdx >= MaxPlayers {
		return
	}
	in, ok := ecs.Get[Input](s.world, s.players[playerIdx])
	if !ok {
		return
	}
	in.Move = move
	in.Yaw = yaw
	if jump {
		in.Jump = true
	}
}

// Shoot 由 playerIdx 发射一枚弹丸，返回弹丸 id（0 = 失败）。dir 会被归一化，
// 零向量忽略。弹丸归属由 playerIdx 决定（不是由 origin）：命中结算要区分敌我，
// 而枪口位置是可以被客户端任意伪造的。
func (s *Simulation) Shoot(playerIdx int, origin, dir [3]float32) uint32 {
	if playerIdx < 0 || playerIdx >= MaxPlayers {
		return 0
	}
	return s.shoot(playerIdx, origin, dir)
}

// Reset 销毁并重建整个场景，重置所有游戏状态与输入。
func (s *Simulation) Reset() {
	s.reset()
}

// Step 推进一个模拟 tick（1/20 秒），按固定顺序运行各系统。
func (s *Simulation) Step() {
	s.stepOne()
}

// GameEntity 返回全局状态单例实体的 id。
func (s *Simulation) GameEntity() ecs.Entity { return s.game }

// ---- 内部方法（单线程，调用方保证串行） ----

func (s *Simulation) init() {
	s.physics.Create()
	s.physics.SetGravity(0, gravityY, 0)

	// 两个玩家角色控制器：形状/出生点由 sim 决定；不可被动态刚体推动（撞来的
	// 箱子被弹开，角色位置完全由输入决定——这是游戏规则，不是物理规则）。
	for i := 0; i < MaxPlayers; i++ {
		x, z := playerSpawnXZ(i)
		s.physics.CreateCharacter(i, characterHalfHeight, characterRadius, characterOffsetY, x, characterSpawnY, z)
		s.physics.SetCharacterDynamicPush(i, false)

		// 玩家实体：纯逻辑（角色控制器不是刚体），初始站在出生点、面朝船中。
		s.players[i] = s.world.NewEntity()
		ecs.Add4(s.world, s.players[i], Player{Idx: i}, Health(playerMaxHealth),
			Position{x, playerSpawnY, z}, Input{Yaw: playerSpawnYaw(i)})
		ecs.Add(s.world, s.players[i], Facing{Yaw: playerSpawnYaw(i)})
		ecs.Add(s.world, s.players[i], PlayerScore{})
		s.replicatePlayer(i, [3]float32{x, playerSpawnY, z}, playerMaxHealth, playerSpawnYaw(i))
		s.replicateScore(i, PlayerScore{})
	}

	// 场景几何全部来自 map.go 的部件表（甲板/船体/集装箱/走道/舷梯/桅杆）。
	for _, b := range shipBoxParts {
		id := s.physics.AddBox(b.hx, b.hy, b.hz, b.x, b.y, b.z, MotionStatic)
		s.registerBody(id, BodyBox, [3]float32{b.hx, b.hy, b.hz}, true, [3]float32{b.x, b.y, b.z}, b.mat)
	}
	for _, c := range shipCapsuleParts {
		id := s.physics.AddCapsule(c.x, c.y, c.z, c.half, c.radius, MotionStatic)
		s.registerBody(id, BodyCapsule, [3]float32{c.radius, c.half, 0}, true, [3]float32{c.x, c.y, c.z}, c.mat)
	}

	// 木箱（动态，可被弹丸挡下、可被玩家推开）。
	for _, c := range deckCratePositions {
		id := s.physics.AddBox(0.5, 0.5, 0.5, c[0], c[1], c[2], MotionDynamic)
		s.registerBody(id, BodyBox, [3]float32{0.5, 0.5, 0.5}, false, c, MatCrate)
	}

	// 命中盒：与角色胶囊同形状的静态胶囊，每 tick 被挪到角色所在处（hitboxFollowSystem）。
	// 弹丸是动态刚体 + CCD，只能与刚体产生接触事件 —— 角色控制器不是刚体，所以玩家的
	// 「可被击中体积」必须由这个刚体承担。两个角色都必须忽略两个命中盒：命中盒每 tick
	// 才跟随一次，会被角色甩到正前方；而对方的命中盒对本地角色就是一堵隐形墙。
	// 放在场景之后创建，好让场景刚体的 id 仍然从 1 开始（客户端与服务端都按 id 缓存）。
	for i := 0; i < MaxPlayers; i++ {
		x, z := playerSpawnXZ(i)
		id := s.physics.AddCapsule(x, playerSpawnY+hitboxOffsetY, z, characterHalfHeight, characterRadius, MotionStatic)
		e := ecs.Entity(id)
		ecs.Add(s.world, e, PlayerHitbox{Idx: i})
		s.hitboxes[i] = e
	}
	for i := 0; i < MaxPlayers; i++ {
		for j := 0; j < MaxPlayers; j++ {
			s.physics.CharacterIgnoreBody(i, uint32(s.hitboxes[j]))
		}
	}

	// 全局状态单例实体：对局结果不是任何单个玩家的属性，但走同一套「实体 + 属性」
	// 机制可以让框架里不存在特例。
	s.game = s.world.NewEntity()
	ecs.Add(s.world, s.game, GameState{Winner: -1})
	s.winner = -1
	s.rep.Set(uint32(s.game), attrGameWinner, replication.I32(-1))

	// 让角色立即着地：否则第一次跳跃会在胶囊下落结算时被吞掉。
	for i := 0; i < MaxPlayers; i++ {
		v := [3]float32{0, gravityY / 60.0, 0}
		s.physics.SetCharacterVelocity(i, v)
		s.physics.UpdateCharacter(i, 1.0/60.0)
	}
	// 同步一次变换，让首帧快照带上角色着地后的真实位置；命中盒也一并贴上去。
	s.syncSystem()
	s.hitboxFollowSystem()
}

func (s *Simulation) reset() {
	s.physics.Destroy()
	s.world = ecs.New()
	s.rep.Reset() // 与世界一起重建：清掉终值表与已下发基线，重建后的世界整体重新下发
	for i := range s.players {
		s.players[i] = ecs.InvalidEntity
		s.hitboxes[i] = ecs.InvalidEntity
	}
	s.game = ecs.InvalidEntity
	s.step = 0
	s.score = [MaxPlayers]PlayerScore{}
	s.winner = -1
	s.overAt = 0
	s.init() // init 里统一重置对局状态并搭建场景
}

// syncGameState 把 Simulation 的对局结果写进单例实体的 GameState 组件。
// 全局状态是 Go 侧的普通字段，组件只是它的同步载体。
func (s *Simulation) syncGameState() {
	ecs.Add(s.world, s.game, GameState{Winner: s.winner})
}

func (s *Simulation) stepOne() {
	s.inputSystem()
	// 命中盒必须在物理步进**之前**贴到角色身上：本帧的弹丸碰撞用的是步进开始时的
	// 位置，晚一步贴就是拿上一 tick 的位置去判定命中。
	s.hitboxFollowSystem()
	s.physics.Step(TickDT, 2)
	s.step++
	s.syncSystem()
	s.projectileSystem()
	s.expireProjectilesSystem()
	s.matchSystem()
}

func (s *Simulation) shoot(playerIdx int, origin, dir [3]float32) uint32 {
	dx, dy, dz := dir[0], dir[1], dir[2]
	l := dx*dx + dy*dy + dz*dz
	if l < 1e-9 {
		return 0
	}
	inv := 1.0 / float32(math.Sqrt(float64(l)))
	dx, dy, dz = dx*inv, dy*inv, dz*inv

	// 出生点必须落在发射者自己的命中盒之外：命中盒是实体刚体，弹丸生在它内部会
	// 被当成穿模、卡住或被弹开（第三人称的枪口正好在角色中轴上，必踩这个坑）。
	spawn := s.pushOutsideOwnHitbox(playerIdx, origin, [3]float32{dx, dy, dz})

	// 弹丸的物理配置是游戏规则：小球 + LinearCast（高速不穿薄墙）+ 零摩擦/弹性。
	id := s.physics.AddSphere(spawn[0], spawn[1], spawn[2], projectileRadius, MotionDynamic)
	if id == 0 {
		return 0
	}
	s.physics.SetBodyMotionQuality(id, QualityLinearCast)
	s.physics.SetBodyFriction(id, 0)
	s.physics.SetBodyRestitution(id, 0)
	s.physics.SetBodyVelocity(id, dx*projectileSpeed, dy*projectileSpeed, dz*projectileSpeed)

	e := s.registerBody(id, BodySphere, [3]float32{projectileRadius}, false, spawn, MatDefault)
	ecs.Add(s.world, e, Projectile{SpawnStep: s.step, Owner: playerIdx})
	// 标记类组件只在创建时同步一次（漏写就是静默丢标记）。
	s.rep.Set(id, attrProjectile, replication.Bool(true))
	return id
}

// pushOutsideOwnHitbox 把发射点沿射向挪到发射者命中盒之外。
//
// 命中盒是与角色胶囊同形状的**实体刚体**，弹丸生在它内部会被判成穿模、卡住或被
// 弹开。第一人称的枪口本来就在盒外（原样返回，不该被挪动），但第三人称的枪口正好
// 在角色中轴上 —— 必定踩到。这里沿射向逐步试探到第一个盒外的点：胶囊两端是球面，
// 闭式射线求交要单独处理两个端球，而这里只是算「出生位置」、不是命中判定，几步
// 试探就够且不会写错。
func (s *Simulation) pushOutsideOwnHitbox(playerIdx int, origin, dir [3]float32) [3]float32 {
	const pushStep = 0.02
	const pushMax = 2.0 // 胶囊最长也就 1.8 m，2 m 足够穿出去
	for t := float32(0); t <= pushMax; t += pushStep {
		q := [3]float32{origin[0] + dir[0]*t, origin[1] + dir[1]*t, origin[2] + dir[2]*t}
		if !s.insideOwnHitbox(playerIdx, q) {
			return q
		}
	}
	return [3]float32{origin[0] + dir[0]*pushMax, origin[1] + dir[1]*pushMax, origin[2] + dir[2]*pushMax}
}

// insideOwnHitbox 报告点 q 是否落在该玩家命中盒内（含弹丸半径的膨胀量）。
// 命中盒是沿 Y 轴的胶囊：到轴线段距离 <= r 即在盒内。
func (s *Simulation) insideOwnHitbox(playerIdx int, q [3]float32) bool {
	p := s.physics.CharacterPosition(playerIdx)
	cx, cz := p[0], p[2]
	lo := p[1] + hitboxOffsetY - characterHalfHeight
	hi := p[1] + hitboxOffsetY + characterHalfHeight

	// 轴线段上离 q 最近的点：只有 y 方向要夹取（轴是竖直的）。
	y := q[1]
	if y < lo {
		y = lo
	} else if y > hi {
		y = hi
	}
	dx, dy, dz := q[0]-cx, q[1]-y, q[2]-cz
	r := float32(characterRadius + projectileRadius)
	return dx*dx+dy*dy+dz*dz < r*r
}

// registerBody 用物理层返回的 body id 建立实体并挂上基础组件。
// 实体 id 即 body id，创建时先写入已知的初始位置/旋转/活跃状态，
// 让两次 tick 之间创建的实体（如弹丸）也能立即出现在快照里。
func (s *Simulation) registerBody(id uint32, kind BodyKind, size [3]float32, static bool, pos [3]float32, mat Material) ecs.Entity {
	e := ecs.Entity(id)
	body := Body{Kind: kind, Size: size, Static: static, Active: !static, Mat: mat}
	rot := Rotation{0, 0, 0, 1}
	// Bundle 式挂载：一次搬家进入 {Body,Position,Rotation} archetype。
	ecs.Add3(s.world, e, body, Position(pos), rot)
	s.replicateBodyMeta(e, body, pos, rot)
	return e
}

// destroyBody 移除物理刚体并销毁对应实体（幂等：实体已销毁时跳过）。
func (s *Simulation) destroyBody(e ecs.Entity) {
	if !ecs.Has[Body](s.world, e) {
		return
	}
	s.physics.RemoveBody(uint32(e))
	s.world.Destroy(e)
	s.rep.Destroy(uint32(e)) // 同步侧一并销毁：实体 id 会被回收复用，基线必须清掉
}

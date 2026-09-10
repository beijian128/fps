package sim

// 用 fake 物理（只做运动学积分 + 地板钳制）验证各系统的行为，不依赖 cgo/Jolt DLL。
// fake 的职责是模拟 Physics 接口的契约：发放递增 id、积分速度、上报接触事件。
// 双角色：character 数组按槽位 0/1 存放。

import (
	"math"
	"sort"
	"testing"

	"joltgo/ecs"
)

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
		// 金币传感器球有 Body 但不上屏（旧快照用 Without[Resource] 在 archetype
		// 粒度排除），这里等价地按 Resource 组件跳过。
		if ecs.Has[Resource](s.world, e) {
			return
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

type fakeBody struct {
	active bool
	quat   [4]float32
	pos    [3]float32
	vel    [3]float32
	radius float32 // 球/胶囊半径；盒子为 0（fake 只对球/胶囊生成角色接触）
	sensor bool
}

type fakeCharacter struct {
	pos [3]float32
	vel [3]float32
}

type fakePhysics struct {
	bodies      map[uint32]*fakeBody
	nextID      uint32
	quality     map[uint32]MotionQuality
	friction    map[uint32]float32
	restitution map[uint32]float32
	sensors     map[uint32]bool

	characters     [MaxPlayers]*fakeCharacter
	charCreated    int
	charHalfHeight float32
	charRadius     float32
	charOffsetY    float32
	charSpawn      [2][3]float32
	dynamicPush    bool
	gravity        [3]float32

	charContacts [MaxPlayers][]uint32 // 本 tick 每个角色接触的刚体（UpdateCharacter 时重建）
	sticky       []uint32             // 测试注入的固定接触（作用于 0 号角色）
	contacts     []Contact
	respawns     int
	createCalls  int
}

func newFakePhysics() *fakePhysics { return &fakePhysics{} }

func (f *fakePhysics) Create() {
	f.bodies = map[uint32]*fakeBody{}
	f.nextID = 1
	f.quality = map[uint32]MotionQuality{}
	f.friction = map[uint32]float32{}
	f.restitution = map[uint32]float32{}
	f.sensors = map[uint32]bool{}
	for i := 0; i < MaxPlayers; i++ {
		f.characters[i] = &fakeCharacter{}
	}
	f.charCreated = 0
	f.dynamicPush = true
	f.gravity = [3]float32{}
	f.charContacts = [MaxPlayers][]uint32{}
	f.sticky = nil
	f.contacts = nil
	f.respawns = 0
	f.createCalls++
}

func (f *fakePhysics) Destroy() {}

func (f *fakePhysics) SetGravity(x, y, z float32) { f.gravity = [3]float32{x, y, z} }

func (f *fakePhysics) addBody(pos [3]float32, motion Motion, radius float32, sensor bool) uint32 {
	id := f.nextID
	f.nextID++
	f.bodies[id] = &fakeBody{
		active: motion != MotionStatic,
		pos:    pos,
		quat:   [4]float32{0, 0, 0, 1},
		radius: radius,
		sensor: sensor,
	}
	if sensor {
		f.sensors[id] = true
	}
	return id
}

func (f *fakePhysics) AddBox(hx, hy, hz, x, y, z float32, motion Motion) uint32 {
	return f.addBody([3]float32{x, y, z}, motion, 0, false)
}

func (f *fakePhysics) AddSphere(x, y, z, radius float32, motion Motion) uint32 {
	return f.addBody([3]float32{x, y, z}, motion, radius, false)
}

func (f *fakePhysics) AddCapsule(x, y, z, halfHeight, radius float32, motion Motion) uint32 {
	return f.addBody([3]float32{x, y, z}, motion, radius, false)
}

func (f *fakePhysics) AddSensorSphere(x, y, z, radius float32) uint32 {
	return f.addBody([3]float32{x, y, z}, MotionStatic, radius, true)
}

func (f *fakePhysics) RemoveBody(id uint32) { delete(f.bodies, id) }

func (f *fakePhysics) SetBodyVelocity(id uint32, vx, vy, vz float32) {
	if b, ok := f.bodies[id]; ok {
		b.vel = [3]float32{vx, vy, vz}
	}
}

func (f *fakePhysics) SetBodyFriction(id uint32, friction float32) { f.friction[id] = friction }

func (f *fakePhysics) SetBodyRestitution(id uint32, restitution float32) {
	f.restitution[id] = restitution
}

func (f *fakePhysics) SetBodyMotionQuality(id uint32, quality MotionQuality) {
	f.quality[id] = quality
}

func (f *fakePhysics) CreateCharacter(charIdx int, halfHeight, radius, offsetY, x, y, z float32) {
	f.charCreated++
	f.charHalfHeight = halfHeight
	f.charRadius = radius
	f.charOffsetY = offsetY
	f.charSpawn[charIdx] = [3]float32{x, y, z}
	f.characters[charIdx].pos = [3]float32{x, y, z}
}

func (f *fakePhysics) SetCharacterDynamicPush(charIdx int, allow bool) { f.dynamicPush = allow }

func (f *fakePhysics) CharacterPosition(charIdx int) [3]float32 { return f.characters[charIdx].pos }

func (f *fakePhysics) SetCharacterPosition(charIdx int, x, y, z float32) {
	f.characters[charIdx].pos = [3]float32{x, y, z}
	f.respawns++
}

func (f *fakePhysics) CharacterVelocity(charIdx int) [3]float32 { return f.characters[charIdx].vel }

func (f *fakePhysics) SetCharacterVelocity(charIdx int, v [3]float32) {
	f.characters[charIdx].vel = v
}

func (f *fakePhysics) CharacterOnGround(charIdx int) bool {
	return f.characters[charIdx].pos[1] <= 1e-3
}

// UpdateCharacter 积分角色速度并钳制到地板（y=0），模拟 ExtendedUpdate 的着地；
// 同时重建本 tick 该角色的接触列表（与真实 Jolt 一致：接触是物理事实）。
func (f *fakePhysics) UpdateCharacter(charIdx int, dt float32) {
	c := f.characters[charIdx]
	c.pos[0] += c.vel[0] * dt
	c.pos[1] += c.vel[1] * dt
	c.pos[2] += c.vel[2] * dt
	if c.pos[1] < 0 {
		c.pos[1] = 0
	}

	contacts := make([]uint32, 0, 8)
	for id, b := range f.bodies {
		if b.radius <= 0 {
			continue // fake 只对球/胶囊生成角色接触
		}
		dx := c.pos[0] - b.pos[0]
		dy := c.pos[1] - b.pos[1]
		dz := c.pos[2] - b.pos[2]
		r := characterRadius + b.radius
		if dx*dx+dy*dy+dz*dz < r*r {
			contacts = append(contacts, id)
		}
	}
	if charIdx == 0 {
		contacts = append(contacts, f.sticky...)
	}
	f.charContacts[charIdx] = contacts
}

func (f *fakePhysics) Step(dt float32, collisionSteps int) {
	for _, b := range f.bodies {
		b.pos[0] += b.vel[0] * dt
		b.pos[1] += b.vel[1] * dt
		b.pos[2] += b.vel[2] * dt
	}
}

func (f *fakePhysics) Sync(fn func(id uint32, active bool, pos [3]float32, quat [4]float32)) {
	for id, b := range f.bodies {
		fn(id, b.active, b.pos, b.quat)
	}
}

func (f *fakePhysics) PollContacts() []Contact {
	c := f.contacts
	f.contacts = nil
	return c
}

func (f *fakePhysics) PollCharacterContacts(charIdx int) []uint32 {
	return f.charContacts[charIdx]
}

// ---- 测试钩子 ----

func (f *fakePhysics) queueContact(a, b uint32) {
	f.contacts = append(f.contacts, Contact{BodyA: a, BodyB: b})
}

// queueCharacterContact 注入一个持续存在的 0 号角色接触（每次 UpdateCharacter 都会带上）。
func (f *fakePhysics) queueCharacterContact(id uint32) {
	f.sticky = append(f.sticky, id)
}

func (f *fakePhysics) bodyCount() int { return len(f.bodies) }

func (f *fakePhysics) moveBody(id uint32, pos [3]float32) {
	if b, ok := f.bodies[id]; ok {
		b.pos = pos
		b.vel = [3]float32{}
	}
}

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

func (f *fakePhysics) bodyVel(id uint32) [3]float32 {
	if b, ok := f.bodies[id]; ok {
		return b.vel
	}
	return [3]float32{}
}

func (f *fakePhysics) bodyPos(id uint32) [3]float32 {
	if b, ok := f.bodies[id]; ok {
		return b.pos
	}
	return [3]float32{}
}

// ---- 辅助 ----

func newTestSim(t *testing.T) (*Simulation, *fakePhysics) {
	t.Helper()
	p := newFakePhysics()
	s := New(p)
	s.Init()
	return s, p
}

func enemiesOf(st State) []uint32 {
	var out []uint32
	for _, b := range st.Bodies {
		if b.Enemy {
			out = append(out, b.ID)
		}
	}
	return out
}

func targetsOf(st State) []uint32 {
	var out []uint32
	for _, b := range st.Bodies {
		if b.Target {
			out = append(out, b.ID)
		}
	}
	return out
}

func projectilesOf(st State) []uint32 {
	var out []uint32
	for _, b := range st.Bodies {
		if b.Projectile {
			out = append(out, b.ID)
		}
	}
	return out
}

func near(a, b, tol float32) bool { return math.Abs(float64(a-b)) <= float64(tol) }

// 出生点/射击起点一律从地图取，不写死坐标——地图改了测试不用跟着改。
func spawn0X() float32 { x, _ := playerSpawnXZ(0); return x }
func spawn0Z() float32 { _, z := playerSpawnXZ(0); return z }

// near0 返回 0 号玩家出生点偏移 (dx, y, dz) 处的位置。
func near0(dx, y, dz float32) [3]float32 {
	x, z := playerSpawnXZ(0)
	return [3]float32{x + dx, y, z + dz}
}

// shoot0 是 0 号玩家胸口高度的射击起点。
func shoot0() [3]float32 { return near0(0, 1, 0) }

// sceneBodyCount 是初始快照里的刚体总数：场景静态部件 + 木箱 + 靶球 + 初始敌人。
// 从 map.go 的部件表算出，改地图时测试自动跟随。
func sceneBodyCount() int {
	return len(shipBoxParts) + len(shipCapsuleParts) + len(deckCratePositions) + len(shipTargets) + initialEnemies
}

// player0 返回快照里 0 号玩家状态（Players[0]）。
func player0(st State) PlayerState { return st.Players[0] }

// ---- 测试 ----

func TestInitialSnapshot(t *testing.T) {
	s, p := newTestSim(t)
	st := snapshotWorld(s)

	// 场景：map.go 的部件表（船体/集装箱/走道/舷梯/桅杆）+ 木箱 + 靶球 + 初始敌人，
	// 外加 initialResource 个不上屏的金币传感器球。
	want := sceneBodyCount()
	if p.bodyCount() != want+initialResource {
		t.Fatalf("初始物理刚体数应为 %d（含金币传感器球），得到 %d", want+initialResource, p.bodyCount())
	}
	if len(p.sensors) != initialResource {
		t.Fatalf("金币传感器球应为 %d 个，得到 %d", initialResource, len(p.sensors))
	}
	if len(st.Bodies) != want {
		t.Fatalf("快照刚体数应为 %d（传感器球不上屏），得到 %d", want, len(st.Bodies))
	}
	// 快照 bodies 里不能出现金币的 id（客户端只渲染 resources 列表）。
	resIDs := map[int]bool{}
	for _, r := range st.Resources {
		resIDs[r.ID] = true
	}
	for _, b := range st.Bodies {
		if resIDs[int(b.ID)] {
			t.Fatalf("金币传感器球不应出现在快照 bodies 里，id=%d", b.ID)
		}
	}
	if got := len(enemiesOf(st)); got != initialEnemies {
		t.Fatalf("初始敌人应为 %d，得到 %d", initialEnemies, got)
	}
	if len(targetsOf(st)) != len(shipTargets) {
		t.Fatalf("初始靶球应为 %d，得到 %d", len(shipTargets), len(targetsOf(st)))
	}
	if len(st.Resources) != initialResource {
		t.Fatalf("初始金币应为 %d，得到 %d", initialResource, len(st.Resources))
	}

	if st.Step != 0 || st.Score != 0 || st.Wave != 1 || st.Gold != 0 {
		t.Fatalf("初始状态应为 step=0/score=0/wave=1/gold=0，得到 %+v", st)
	}
	if len(st.Players) != MaxPlayers {
		t.Fatalf("快照应有 %d 个玩家，得到 %d", MaxPlayers, len(st.Players))
	}
	if player0(st).Health != 100 {
		t.Fatalf("0 号玩家初始血量应为 100，得到 %v", player0(st).Health)
	}

	// 敌人快照字段：type=2、health=3、enemy=true。
	for _, id := range enemiesOf(st) {
		var b BodyInfo
		for _, bi := range st.Bodies {
			if bi.ID == id {
				b = bi
			}
		}
		if b.Type != int(BodyCapsule) || b.Health != enemyHealth || !b.Enemy {
			t.Fatalf("敌人快照字段不符：%+v", b)
		}
	}

	// 按 id 升序（客户端依赖稳定输出）。
	for i := 1; i < len(st.Bodies); i++ {
		if st.Bodies[i].ID <= st.Bodies[i-1].ID {
			t.Fatalf("快照刚体应按 id 升序，%d 排在 %d 后", st.Bodies[i].ID, st.Bodies[i-1].ID)
		}
	}

	// 角色配置与重力全部来自 sim 侧调参。
	if p.charCreated != MaxPlayers || p.charHalfHeight != 0.5 || p.charRadius != 0.4 || p.charOffsetY != 0.9 {
		t.Fatalf("两个角色形状都应由 sim 传入（0.5/0.4/0.9），得到 created=%d %v/%v/%v",
			p.charCreated, p.charHalfHeight, p.charRadius, p.charOffsetY)
	}
	if p.charSpawn[0] != near0(0, 0, 0) {
		t.Fatalf("0 号角色出生点应由 sim 传入，得到 %v", p.charSpawn[0])
	}
	if p.dynamicPush {
		t.Fatal("玩家不可被动态刚体推动（游戏规则，sim 侧设置）")
	}
	if p.gravity != [3]float32{0, gravityY, 0} {
		t.Fatalf("重力应由 sim 传入，得到 %v", p.gravity)
	}
}

func TestShootValidatesDirection(t *testing.T) {
	s, p := newTestSim(t)

	if id := s.Shoot(shoot0(), [3]float32{0, 0, 0}); id != 0 {
		t.Fatalf("零向量方向应拒绝，得到 id=%d", id)
	}

	id := s.Shoot(shoot0(), [3]float32{2, 0, 0})
	if id == 0 {
		t.Fatal("有效射击应返回非零 id")
	}
	v := p.bodyVel(id)
	if !near(v[0], projectileSpeed, 1e-3) || !near(v[1], 0, 1e-3) || !near(v[2], 0, 1e-3) {
		t.Fatalf("方向应归一化后乘以初速，得到 %v", v)
	}
	// 弹丸物理配置（LinearCast + 零摩擦/弹性）由 sim 侧设置。
	if p.quality[id] != QualityLinearCast {
		t.Fatalf("弹丸应使用 LinearCast 运动质量，得到 %v", p.quality[id])
	}
	if p.friction[id] != 0 || p.restitution[id] != 0 {
		t.Fatalf("弹丸摩擦/弹性应为 0，得到 %v/%v", p.friction[id], p.restitution[id])
	}
	// 弹丸立即可见于快照（两次 tick 之间创建的实体也有初始位置）。
	for _, b := range snapshotWorld(s).Bodies {
		if b.ID == id && b.Pos != shoot0() {
			t.Fatalf("新弹丸应带初始位置，得到 %v", b.Pos)
		}
	}
}

func TestProjectileHitsTargetScores(t *testing.T) {
	s, p := newTestSim(t)
	target := targetsOf(snapshotWorld(s))[0]
	proj := s.Shoot(shoot0(), [3]float32{1, 0, 0})
	p.queueContact(proj, target)

	s.Step()

	st := snapshotWorld(s)
	if st.Score != 1 {
		t.Fatalf("击碎靶球应得 1 分，得到 %d", st.Score)
	}
	for _, b := range st.Bodies {
		if b.ID == target || b.ID == proj {
			t.Fatalf("靶球与弹丸都应被移除，仍存在 id=%d", b.ID)
		}
	}
}

func TestProjectileAsSecondContactSide(t *testing.T) {
	s, p := newTestSim(t)
	target := targetsOf(snapshotWorld(s))[0]
	proj := s.Shoot(shoot0(), [3]float32{1, 0, 0})
	p.queueContact(target, proj) // 弹丸是接触对的第二方

	s.Step()

	st := snapshotWorld(s)
	if st.Score != 1 {
		t.Fatalf("弹丸在接触对任意一侧都应结算，得到 %d", st.Score)
	}
	for _, b := range st.Bodies {
		if b.ID == target || b.ID == proj {
			t.Fatalf("靶球与弹丸都应被移除，仍存在 id=%d", b.ID)
		}
	}
}

func TestNonProjectileContactsIgnored(t *testing.T) {
	s, p := newTestSim(t)
	st0 := snapshotWorld(s)
	enemy := enemiesOf(st0)[0]
	target := targetsOf(st0)[0]
	p.queueContact(enemy, target) // 与弹丸无关的接触（敌人撞到靶球）

	s.Step()

	st := snapshotWorld(s)
	if st.Score != 0 {
		t.Fatalf("无关接触不应得分，得到 %d", st.Score)
	}
	if len(enemiesOf(st)) != initialEnemies || len(targetsOf(st)) != len(shipTargets) {
		t.Fatal("无关接触不应移除任何实体")
	}
}

func TestSensorDoesNotBlockProjectile(t *testing.T) {
	s, p := newTestSim(t)
	coin := uint32(snapshotWorld(s).Resources[0].ID)
	proj := s.Shoot(shoot0(), [3]float32{1, 0, 0})
	p.queueContact(proj, coin) // 弹丸撞上金币传感器球

	s.Step()

	st := snapshotWorld(s)
	if len(projectilesOf(st)) != 1 {
		t.Fatal("传感器球不应挡住弹丸：弹丸应继续存在")
	}
	if len(st.Resources) != initialResource {
		t.Fatalf("传感器球不应被弹丸摧毁，金币应剩 %d，得到 %d", initialResource, len(st.Resources))
	}
	if st.Score != 0 || st.Gold != 0 {
		t.Fatalf("穿过传感器球不应计分/拾取，得到 score=%d gold=%d", st.Score, st.Gold)
	}
}

func TestNonEnemyContactsNoDamage(t *testing.T) {
	s, p := newTestSim(t)
	// 找一个箱子（动态盒子）注入角色接触：碰到非敌人不应掉血。
	var crate uint32
	for _, b := range snapshotWorld(s).Bodies {
		if !b.Static && !b.Target && !b.Enemy {
			crate = b.ID
			break
		}
	}
	p.queueCharacterContact(crate)
	for i := 0; i < 5; i++ {
		s.Step()
	}
	if h := player0(snapshotWorld(s)).Health; h != 100 {
		t.Fatalf("碰到箱子不应掉血，得到 %v", h)
	}
}

func TestProjectileKillsEnemyAndDropsResource(t *testing.T) {
	s, p := newTestSim(t)
	enemy := enemiesOf(snapshotWorld(s))[0]
	p.moveBody(enemy, near0(2, 1, 0)) // 玩家附近但不贴身（无接触伤害）

	for i := 0; i < 3; i++ {
		proj := s.Shoot(shoot0(), [3]float32{1, 0, 0})
		p.queueContact(proj, enemy)
	}
	s.Step()

	st := snapshotWorld(s)
	if st.Score != 1 {
		t.Fatalf("击杀应得 1 分，得到 %d", st.Score)
	}
	if got := len(enemiesOf(st)); got != initialEnemies-1 {
		t.Fatalf("敌人应剩 %d，得到 %d", initialEnemies-1, got)
	}
	if len(st.Resources) != initialResource+1 {
		t.Fatalf("击杀应掉落金币（%d+1），得到 %d", initialResource, len(st.Resources))
	}
}

func TestPlayerContactDamageAndRespawn(t *testing.T) {
	s, p := newTestSim(t)
	enemy := enemiesOf(snapshotWorld(s))[0]
	p.moveBody(enemy, near0(0, 0, 0)) // 贴到 0 号玩家身上

	for i := 0; i < 10; i++ {
		s.Step()
	}
	if h := player0(snapshotWorld(s)).Health; !near(h, 96, 0.1) {
		t.Fatalf("10 tick 贴身伤害后血量应约 96，得到 %v", h)
	}

	for i := 0; i < 250; i++ {
		s.Step()
	}
	if p.respawns < 1 {
		t.Fatal("血量耗尽后应复活一次")
	}
	if h := player0(snapshotWorld(s)).Health; h <= 0 || h > 100 {
		t.Fatalf("复活后血量应在 (0, 100]，得到 %v", h)
	}
	if p.characters[0].pos[0] != spawn0X() || p.characters[0].pos[2] != spawn0Z() || p.characters[0].pos[1] > playerSpawnY+1e-3 {
		t.Fatalf("复活后应回到出生点（着地），得到 %v", p.characters[0].pos)
	}
}

func TestJumpLiftsPlayer(t *testing.T) {
	s, p := newTestSim(t)
	s.ApplyInput(0, [2]float32{}, 0, true)
	s.Step()

	if p.characters[0].pos[1] <= 1e-3 {
		t.Fatalf("跳跃应让角色离地（v0=%v，g=%v），得到 y=%v", jumpSpeed, gravityY, p.characters[0].pos[1])
	}
	in, _ := ecs.Get[Input](s.world, s.players[0])
	if in.Jump {
		t.Fatal("跳跃输入应在消费后清零")
	}
}

func TestResourcePickup(t *testing.T) {
	s, p := newTestSim(t)
	r := snapshotWorld(s).Resources[0]
	p.characters[0].pos = r.Pos // 传送到金币位置

	s.Step()

	st := snapshotWorld(s)
	if st.Gold < 1 {
		t.Fatalf("踩到金币应至少拾取 1 枚，得到 gold=%d", st.Gold)
	}
	// 守恒：拾取数 = 金币总数 - 场上剩余（其他金币可能随机刷在 1m 内）。
	if st.Gold+len(st.Resources) != initialResource {
		t.Fatalf("金币应守恒（%d = gold + 剩余），得到 gold=%d 剩余=%d",
			initialResource, st.Gold, len(st.Resources))
	}
}

func TestWaveAdvance(t *testing.T) {
	s, p := newTestSim(t)

	// 一次 tick 内击杀全部 3 只（每只 3 发弹丸）。
	for _, enemy := range enemiesOf(snapshotWorld(s)) {
		for i := 0; i < 3; i++ {
			proj := s.Shoot(shoot0(), [3]float32{1, 0, 0})
			p.queueContact(proj, enemy)
		}
	}
	s.Step()

	st := snapshotWorld(s)
	if st.Score != 3 {
		t.Fatalf("全灭应得 3 分，得到 %d", st.Score)
	}
	if got := len(enemiesOf(st)); got != 0 {
		t.Fatalf("场上敌人应为 0，得到 %d", got)
	}
	if len(st.Resources) != initialResource+3 {
		t.Fatalf("3 次掉落后金币应为 %d，得到 %d", initialResource+3, len(st.Resources))
	}

	// 清空 2 秒后刷第 2 波（数量规则：第 1 波 3 只、之后每波 +1 → 第 2 波 4 只）。
	for i := 0; i < waveDelayTicks+5; i++ {
		s.Step()
	}
	st = snapshotWorld(s)
	if st.Wave != 2 {
		t.Fatalf("波次应推进到 2，得到 %d", st.Wave)
	}
	if got := len(enemiesOf(st)); got != initialEnemies+1 {
		t.Fatalf("第 2 波应有 %d 只，得到 %d", initialEnemies+1, got)
	}
}

func TestProjectileExpiry(t *testing.T) {
	s, _ := newTestSim(t)
	s.Shoot(shoot0(), [3]float32{1, 0, 0})
	if got := len(projectilesOf(snapshotWorld(s))); got != 1 {
		t.Fatalf("应有 1 枚弹丸，得到 %d", got)
	}

	for i := 0; i <= projectileMaxLife+1; i++ {
		s.Step()
	}
	if got := len(projectilesOf(snapshotWorld(s))); got != 0 {
		t.Fatalf("超过最大存活时间的弹丸应被移除，还剩 %d", got)
	}
}

func TestEnemyStandsStill(t *testing.T) {
	s, p := newTestSim(t)
	enemy := enemiesOf(snapshotWorld(s))[0]
	before := p.bodyPos(enemy)

	for i := 0; i < 20; i++ {
		s.Step()
	}
	if p.bodyPos(enemy) != before {
		t.Fatalf("怪物不应移动，位置 %v → %v", before, p.bodyPos(enemy))
	}
	if v := p.bodyVel(enemy); v != [3]float32{} {
		t.Fatalf("怪物速度应为 0，得到 %v", v)
	}
	// 快照里的敌人应标记为静态刚体（不可推动）。
	for _, b := range snapshotWorld(s).Bodies {
		if b.ID == enemy && !b.Static {
			t.Fatal("怪物应为静态刚体（不可推动）")
		}
	}
}

func TestApplyInputMovesPlayer(t *testing.T) {
	s, p := newTestSim(t)
	s.ApplyInput(0, [2]float32{8, 0}, 0, false)
	s.Step()

	if !near(p.characters[0].pos[0], 8*TickDT, 1e-4) {
		t.Fatalf("移动输入应驱动角色（+%v），得到 x=%v", 8*TickDT, p.characters[0].pos[0])
	}
}

func TestResetRebuildsScene(t *testing.T) {
	s, p := newTestSim(t)
	s.Shoot(shoot0(), [3]float32{1, 0, 0})
	enemy := enemiesOf(snapshotWorld(s))[0]
	proj := s.Shoot(shoot0(), [3]float32{1, 0, 0})
	p.queueContact(proj, enemy)
	for i := 0; i < 5; i++ {
		s.Step()
	}

	s.Reset()

	st := snapshotWorld(s)
	if st.Step != 0 || st.Score != 0 || st.Gold != 0 || st.Wave != 1 {
		t.Fatalf("Reset 后全局状态应清零，得到 %+v", st)
	}
	if player0(st).Health != 100 {
		t.Fatalf("Reset 后血量应为 100，得到 %v", player0(st).Health)
	}
	want := sceneBodyCount()
	if len(st.Bodies) != want || len(st.Resources) != initialResource || len(enemiesOf(st)) != initialEnemies {
		t.Fatalf("Reset 后场景应重建（%d 刚体/%d 金币/%d 敌人），得到 %d/%d/%d",
			want, initialResource, initialEnemies,
			len(st.Bodies), len(st.Resources), len(enemiesOf(st)))
	}
	// 物理世界重建后刚体 id 从头开始分配。
	if st.Bodies[0].ID != 1 || st.Bodies[len(st.Bodies)-1].ID != uint32(want) {
		t.Fatalf("重建后刚体 id 应从 1 重新分配，得到 %d..%d",
			st.Bodies[0].ID, st.Bodies[len(st.Bodies)-1].ID)
	}
}

func TestSnapshotStableAfterRemovals(t *testing.T) {
	s, p := newTestSim(t)
	target := targetsOf(snapshotWorld(s))[0]
	proj := s.Shoot(shoot0(), [3]float32{1, 0, 0})
	p.queueContact(proj, target)
	s.Step() // 触发 swap-remove 打乱存储顺序

	st := snapshotWorld(s)
	for i := 1; i < len(st.Bodies); i++ {
		if st.Bodies[i].ID <= st.Bodies[i-1].ID {
			t.Fatalf("删除实体后快照仍应按 id 升序，%d 排在 %d 后", st.Bodies[i].ID, st.Bodies[i-1].ID)
		}
	}
}

// ---- 本轮加固新增测试 ----

func TestInputClampedToMaxSpeed(t *testing.T) {
	s, p := newTestSim(t)
	// 客户端上报远超限幅的速度（60,80 → 模长 100）：服务端应封顶到
	// maxPlayerSpeed 且保持方向，而不是照单全收造成超速/穿墙。
	s.ApplyInput(0, [2]float32{60, 80}, 0, false)
	s.Step()
	dx := p.characters[0].pos[0]
	dz := p.characters[0].pos[2] - spawn0Z()
	d := float32(math.Sqrt(float64(dx*dx + dz*dz)))
	if !near(d, maxPlayerSpeed*TickDT, 1e-3) {
		t.Fatalf("水平位移应按 maxPlayerSpeed=%v 限幅，实测 %v", maxPlayerSpeed, d)
	}
	if !near(dx*4, dz*3, 1e-2) {
		t.Fatalf("限幅不应改变方向，实测位移 (%v, %v)", dx, dz)
	}
}

func TestRespawnSameTickConsistent(t *testing.T) {
	s, p := newTestSim(t)
	enemy := enemiesOf(snapshotWorld(s))[0]
	p.moveBody(enemy, near0(0, 0, 0)) // 贴身 → 每 tick 持续伤害
	for i := 0; i < 500 && p.respawns == 0; i++ {
		s.Step()
	}
	if p.respawns == 0 {
		t.Fatal("贴身持续伤害应在 500 tick 内触发复活")
	}
	st := snapshotWorld(s)
	if player0(st).Health != 100 {
		t.Fatalf("复活当 tick 血量应为 100，得到 %v", player0(st).Health)
	}
	if player0(st).Pos != (Position(near0(0, playerSpawnY, 0))) {
		t.Fatalf("复活当 tick 快照位置应已是出生点（不再留在死亡点），得到 %v", player0(st).Pos)
	}
	if p.characters[0].pos != (near0(0, playerSpawnY, 0)) {
		t.Fatalf("物理角色位置应立即回到出生点，得到 %v", p.characters[0].pos)
	}
	if p.characters[0].vel != ([3]float32{}) {
		t.Fatalf("复活应清零角色速度，得到 %v", p.characters[0].vel)
	}
}

func TestInitIsIdempotent(t *testing.T) {
	s, p := newTestSim(t)
	before := p.bodyCount()
	// 造点"噪音"：发弹、推进几 tick。
	s.Shoot(shoot0(), [3]float32{1, 0, 0})
	for i := 0; i < 3; i++ {
		s.Step()
	}

	// 重复 Init 应等同 Reset：重建场景而不是叠加/残留。
	s.Init()
	if p.createCalls != 2 {
		t.Fatalf("重复 Init 应重建物理世界（Create 共 2 次），得到 %d", p.createCalls)
	}
	if got := p.bodyCount(); got != before {
		t.Fatalf("重复 Init 不应叠加场景：刚体 %d → %d", before, got)
	}
	st := snapshotWorld(s)
	if st.Step != 0 || st.Score != 0 || st.Wave != 1 || player0(st).Health != 100 {
		t.Fatalf("重复 Init 后状态应回到初始，得到 %+v", st)
	}
	if len(projectilesOf(st)) != 0 {
		t.Fatalf("重复 Init 后应无残留弹丸，得到 %d", len(projectilesOf(st)))
	}
}

// 全局状态单例在 init 之后必须立刻与 Go 侧计数一致（组件是用零值建的，
// 漏掉这次同步会让 Wave 停在 0 直到首次清波）。
func TestGameStateSyncedAtInit(t *testing.T) {
	s, _ := newTestSim(t)
	gs, ok := ecs.Get[GameState](s.world, s.GameEntity())
	if !ok {
		t.Fatal("init 后应有全局状态单例实体")
	}
	if gs.Score != int32(s.score) || gs.Wave != int32(s.wave) || gs.Gold != int32(s.gold) {
		t.Fatalf("init 后组件应与 Go 侧计数一致：组件 %+v，Go 侧 score=%d wave=%d gold=%d",
			*gs, s.score, s.wave, s.gold)
	}

	// Reset 重建世界后同样要立刻同步：单例是新实体、组件又回到零值。
	s.Reset()
	gs, ok = ecs.Get[GameState](s.world, s.GameEntity())
	if !ok {
		t.Fatal("Reset 后应有全局状态单例实体")
	}
	if gs.Score != int32(s.score) || gs.Wave != int32(s.wave) || gs.Gold != int32(s.gold) {
		t.Fatalf("Reset 后组件应与 Go 侧计数一致：组件 %+v，Go 侧 score=%d wave=%d gold=%d",
			*gs, s.score, s.wave, s.gold)
	}
}

// TestTwoPlayersIndependentInputs 验证两名玩家有独立输入与位置。
func TestTwoPlayersIndependentInputs(t *testing.T) {
	s, p := newTestSim(t)
	s.ApplyInput(0, [2]float32{8, 0}, 0, false)
	s.ApplyInput(1, [2]float32{-8, 0}, 0, false)
	s.Step()

	if p.characters[0].pos[0] <= 0 {
		t.Fatalf("0 号玩家应向右移，得到 x=%v", p.characters[0].pos[0])
	}
	if p.characters[1].pos[0] >= 3 {
		t.Fatalf("1 号玩家应向左移，得到 x=%v", p.characters[1].pos[0])
	}
	st := snapshotWorld(s)
	if len(st.Players) != MaxPlayers {
		t.Fatalf("快照应有 %d 个玩家，得到 %d", MaxPlayers, len(st.Players))
	}
	if st.Players[0].Pos[0] < 0 || st.Players[1].Pos[0] > 3 {
		t.Fatalf("玩家应按各自输入移动：p0=%v p1=%v", st.Players[0].Pos, st.Players[1].Pos)
	}
}

// TestTwoPlayerIndependentDamage 验证敌人贴身只伤害接触它的那个玩家。
func TestTwoPlayerIndependentDamage(t *testing.T) {
	s, p := newTestSim(t)
	enemy := enemiesOf(snapshotWorld(s))[0]
	p.moveBody(enemy, near0(0, 0, 0)) // 贴到 0 号玩家

	for i := 0; i < 10; i++ {
		s.Step()
	}
	st := snapshotWorld(s)
	if h := st.Players[0].Health; h >= 100 {
		t.Fatalf("0 号玩家应被贴身伤害，得到 %v", h)
	}
	if h := st.Players[1].Health; h != 100 {
		t.Fatalf("1 号玩家不应受伤（未接触敌人），得到 %v", h)
	}
}

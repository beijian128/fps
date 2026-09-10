package sim

// 用 fake 物理（只做运动学积分 + 地板钳制）验证各系统的行为，不依赖 cgo/Jolt DLL。
// fake 的职责是模拟 Physics 接口的契约：发放递增 id、积分速度、上报接触事件。
// 双角色：character 数组按槽位 0/1 存放。
//
// PVP 之后玩法只有一条判定链：弹丸（动态刚体）靠接触事件打中跟随玩家的命中盒
// （静态刚体），因此测试普遍用 queueContact 直接投喂弹丸↔命中盒的接触对。

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
	Kills  int32
	Deaths int32
}

// State 是世界状态快照（测试用）。
type State struct {
	Bodies  []BodyInfo
	Players []PlayerState
	Step    int
	Winner  int32
}

// snapshotWorld 直接从 ECS 世界构造一份 State（测试用 oracle）。
func snapshotWorld(s *Simulation) State {
	st := State{
		Bodies:  []BodyInfo{},
		Players: make([]PlayerState, 0, MaxPlayers),
		Step:    s.step,
		Winner:  s.winner,
	}

	for i := 0; i < MaxPlayers; i++ {
		ps := PlayerState{Health: playerMaxHealth}
		if p, ok := ecs.Get[Position](s.world, s.players[i]); ok {
			ps.Pos = *p
		}
		if h, ok := ecs.Get[Health](s.world, s.players[i]); ok {
			ps.Health = float32(*h)
		}
		if in, ok := ecs.Get[Input](s.world, s.players[i]); ok {
			ps.Yaw = in.Yaw
		}
		if sc, ok := ecs.Get[PlayerScore](s.world, s.players[i]); ok {
			ps.Kills, ps.Deaths = sc.Kills, sc.Deaths
		}
		st.Players = append(st.Players, ps)
	}

	// 命中盒（PlayerHitbox）没有 Body 组件，因此天然不在刚体快照里 —— 与它不下发给
	// 客户端是同一件事：它不是可渲染实体，只是一个能被弹丸认出的碰撞体。
	ecs.Each(s.world, func(e ecs.Entity, b *Body) {
		bi := BodyInfo{
			ID:         uint32(e),
			Type:       int(b.Kind),
			Static:     b.Static,
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

	sort.Slice(st.Bodies, func(i, j int) bool { return st.Bodies[i].ID < st.Bodies[j].ID })
	return st
}

type fakeBody struct {
	active bool
	quat   [4]float32
	pos    [3]float32
	vel    [3]float32
	radius float32
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
	// 每个角色忽略的刚体 id（对应真实 Jolt 的 OnContactValidate 忽略名单）。
	// 只用来断言 sim 确实登记了命中盒 —— 漏登记的后果（命中盒变成隐形墙）由
	// physics/pvp_hit_integration_test.go 在真引擎上验证。
	ignored [MaxPlayers]map[uint32]bool
	gravity [3]float32

	contacts    []Contact
	respawns    int
	createCalls int
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
		f.ignored[i] = map[uint32]bool{}
	}
	f.charCreated = 0
	f.dynamicPush = true
	f.gravity = [3]float32{}
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

// SetBodyPosition 瞬移刚体（真实实现是 Jolt BodyInterface::SetPosition）。
// 命中盒每 tick 靠它贴合角色，所以这里必须能把位置搬到任意处。
func (f *fakePhysics) SetBodyPosition(id uint32, x, y, z float32) {
	if b, ok := f.bodies[id]; ok {
		b.pos = [3]float32{x, y, z}
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

func (f *fakePhysics) CharacterIgnoreBody(charIdx int, id uint32) {
	f.ignored[charIdx][id] = true
}

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

// UpdateCharacter 积分角色速度并钳制到地板（y=0），模拟 ExtendedUpdate 的着地。
func (f *fakePhysics) UpdateCharacter(charIdx int, dt float32) {
	c := f.characters[charIdx]
	c.pos[0] += c.vel[0] * dt
	c.pos[1] += c.vel[1] * dt
	c.pos[2] += c.vel[2] * dt
	if c.pos[1] < 0 {
		c.pos[1] = 0
	}
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

// ---- 测试钩子 ----

func (f *fakePhysics) queueContact(a, b uint32) {
	f.contacts = append(f.contacts, Contact{BodyA: a, BodyB: b})
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

// shoot0 是 0 号玩家的第一人称枪口位置：在角色正前方、离命中盒轴线足够远
// （`near0(0, 1, 0)` 是角色中轴上的点，正好落在自己的命中盒里，会被推到盒外）。
func shoot0() [3]float32 { return near0(0.9, 1.2, 0) }

// hitboxPos 返回某个槽位的命中盒此刻应处的物理位置（角色脚底 + hitboxOffsetY）。
func hitboxPos(i int) [3]float32 {
	x, z := playerSpawnXZ(i)
	return [3]float32{x, playerSpawnY + hitboxOffsetY, z}
}

// sceneBodyCount 是初始快照（不含命中盒）里的刚体总数：场景静态部件 + 木箱。
// 从 map.go 的部件表算出，改地图时测试自动跟随。
func sceneBodyCount() int {
	return len(shipBoxParts) + len(shipCapsuleParts) + len(deckCratePositions)
}

// player0 返回快照里 0 号玩家状态（Players[0]）。
func player0(st State) PlayerState { return st.Players[0] }

// ---- 测试 ----

func TestInitialSnapshot(t *testing.T) {
	s, p := newTestSim(t)
	st := snapshotWorld(s)

	// 场景：map.go 的部件表（船体/集装箱/走道/舷梯/桅杆）+ 木箱；物理世界里另有
	// 每个玩家一个命中盒。
	want := sceneBodyCount()
	if p.bodyCount() != want+MaxPlayers {
		t.Fatalf("初始物理刚体数应为 %d（含 %d 个命中盒），得到 %d",
			want+MaxPlayers, MaxPlayers, p.bodyCount())
	}
	if len(st.Bodies) != want {
		t.Fatalf("快照刚体数应为 %d（命中盒不上屏），得到 %d", want, len(st.Bodies))
	}
	snapIDs := map[uint32]bool{}
	for _, b := range st.Bodies {
		snapIDs[b.ID] = true
	}
	for i := 0; i < MaxPlayers; i++ {
		if snapIDs[uint32(s.hitboxes[i])] {
			t.Fatalf("%d 号命中盒不应出现在快照刚体里（它没有 Body 组件）", i)
		}
	}

	if st.Step != 0 || st.Winner != -1 {
		t.Fatalf("初始状态应为 step=0 / winner=-1，得到 step=%d winner=%d", st.Step, st.Winner)
	}
	if len(st.Players) != MaxPlayers {
		t.Fatalf("快照应有 %d 个玩家，得到 %d", MaxPlayers, len(st.Players))
	}
	for i, ps := range st.Players {
		if ps.Health != float32(playerMaxHealth) {
			t.Fatalf("%d 号玩家初始血量应为 %v，得到 %v", i, playerMaxHealth, ps.Health)
		}
		if ps.Kills != 0 || ps.Deaths != 0 {
			t.Fatalf("%d 号玩家初始战绩应为 0/0，得到 %d/%d", i, ps.Kills, ps.Deaths)
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

	// 两个角色都必须忽略**两个**命中盒：只忽略自己的会被对方的命中盒卡住不动。
	for i := 0; i < MaxPlayers; i++ {
		for j := 0; j < MaxPlayers; j++ {
			if !p.ignored[i][uint32(s.hitboxes[j])] {
				t.Fatalf("%d 号角色应忽略 %d 号命中盒（漏登记 = 对手的命中盒变成隐形墙）", i, j)
			}
		}
	}
}

// TestHitboxFollowsCharacter 验证命中盒每 tick 贴到角色身上。
func TestHitboxFollowsCharacter(t *testing.T) {
	s, p := newTestSim(t)
	s.ApplyInput(0, [2]float32{8, 0}, 0, false)
	s.Step()

	c := p.characters[0].pos
	want := [3]float32{c[0], c[1] + hitboxOffsetY, c[2]}
	if got := p.bodyPos(uint32(s.hitboxes[0])); got != want {
		t.Fatalf("命中盒应贴合角色（脚底 + %v），得到 %v，角色 %v", hitboxOffsetY, got, c)
	}
	if s.hitboxes[0] == s.hitboxes[1] {
		t.Fatal("两个玩家应有各自的命中盒")
	}
}

func TestShootValidatesDirection(t *testing.T) {
	s, p := newTestSim(t)

	if id := s.Shoot(0, shoot0(), [3]float32{0, 0, 0}); id != 0 {
		t.Fatalf("零向量方向应拒绝，得到 id=%d", id)
	}
	if id := s.Shoot(MaxPlayers, shoot0(), [3]float32{1, 0, 0}); id != 0 {
		t.Fatalf("非法槽位应拒绝，得到 id=%d", id)
	}
	if id := s.Shoot(-1, shoot0(), [3]float32{1, 0, 0}); id != 0 {
		t.Fatalf("负数槽位应拒绝，得到 id=%d", id)
	}

	id := s.Shoot(0, shoot0(), [3]float32{2, 0, 0})
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

// TestShootFromInsideOwnHitboxSpawnsOutside：起点落在发射者命中盒内时（第三人称
// 的枪口就在角色中轴上，必定踩到），服务端要把出生点推到盒外 —— 命中盒是实体刚体，
// 弹丸生在盒里会被卡住或弹开，等于开不了枪。
func TestShootFromInsideOwnHitboxSpawnsOutside(t *testing.T) {
	s, p := newTestSim(t)

	// 从命中盒正中心朝正下方（低头）开枪：起点在盒的竖直中轴上。
	origin := [3]float32{spawn0X(), playerSpawnY + hitboxOffsetY, spawn0Z()}
	id := s.Shoot(0, origin, [3]float32{0, -1, 0})
	if id == 0 {
		t.Fatal("从命中盒内部射击也应能发射")
	}
	pos := p.bodyPos(id)
	// 沿 -y 推出后，出生点必须已在盒底之下（盒底 = 脚底 + offset - half - 弹丸半径）。
	bottom := float32(playerSpawnY + hitboxOffsetY - characterHalfHeight - projectileRadius)
	if pos[1] > bottom {
		t.Fatalf("出生点应在自己命中盒之外（y <= %v），得到 %v", bottom, pos)
	}
	if pos[0] != origin[0] || pos[2] != origin[2] {
		t.Fatalf("沿射向推出时水平方向不应偏移，得到 %v", pos)
	}
}

// TestShootOutsideHitboxKeptAsIs：起点本来就在自己命中盒外（第一人称枪口就是
// 这样）时不得位移，否则近战/贴墙射击的弹道会莫名其妙地往前跳一截。
func TestShootOutsideHitboxKeptAsIs(t *testing.T) {
	s, p := newTestSim(t)
	// 胸口高度、沿 +x 方向离开角色 2 m 处 —— 显然在盒外。
	origin := [3]float32{spawn0X() + 2, playerSpawnY + 1, spawn0Z()}
	id := s.Shoot(0, origin, [3]float32{1, 0, 0})
	if id == 0 {
		t.Fatal("有效射击应返回非零 id")
	}
	if got := p.bodyPos(id); got != origin {
		t.Fatalf("盒外起点不应被挪动，得到 %v（期望 %v）", got, origin)
	}
}

func TestProjectileAsSecondContactSide(t *testing.T) {
	s, p := newTestSim(t)
	victim := s.hitboxes[1]
	proj := s.Shoot(0, shoot0(), [3]float32{1, 0, 0})
	p.queueContact(uint32(victim), proj) // 弹丸是接触对的第二方

	s.Step()

	st := snapshotWorld(s)
	want := float32(playerMaxHealth - playerHitDamage)
	if h := st.Players[1].Health; !near(h, want, 0.01) {
		t.Fatalf("弹丸在接触对任意一侧都应结算，1 号血量应为 %v，得到 %v", want, h)
	}
	for _, b := range st.Bodies {
		if b.ID == proj {
			t.Fatal("命中后弹丸应被移除")
		}
	}
}

func TestNonProjectileContactsIgnored(t *testing.T) {
	s, p := newTestSim(t)
	st0 := snapshotWorld(s)
	// 与弹丸无关的接触：两个静态刚体之间（例如船体与集装箱）。
	var a, b uint32
	for _, bi := range st0.Bodies {
		if !bi.Static || bi.Projectile {
			continue
		}
		if a == 0 {
			a = bi.ID
		} else {
			b = bi.ID
			break
		}
	}
	p.queueContact(a, b)

	s.Step()

	st := snapshotWorld(s)
	for i, ps := range st.Players {
		if ps.Health != float32(playerMaxHealth) {
			t.Fatalf("无关接触不应造成伤害，%d 号玩家血量 %v", i, ps.Health)
		}
	}
	if len(st.Bodies) != len(st0.Bodies) {
		t.Fatal("无关接触不应移除任何实体")
	}
}

// TestProjectileHitsGroundRemovedWithoutDamage：打中场景几何只销毁弹丸，不掉血。
func TestProjectileHitsGroundRemovedWithoutDamage(t *testing.T) {
	s, p := newTestSim(t)
	deck := snapshotWorld(s).Bodies[0].ID // 第一个静态部件必然是甲板钢板
	proj := s.Shoot(0, shoot0(), [3]float32{0, -1, 0})
	p.queueContact(proj, deck)

	s.Step()

	st := snapshotWorld(s)
	for _, b := range st.Bodies {
		if b.ID == proj {
			t.Fatal("命中地板后弹丸应被移除")
		}
	}
	if h := player0(st).Health; h != float32(playerMaxHealth) {
		t.Fatalf("打地板不应扣血，得到 %v", h)
	}
}

// TestOwnHitboxDoesNotEatProjectile：自己的命中盒既不扣血、也不吃掉弹丸。
func TestOwnHitboxDoesNotEatProjectile(t *testing.T) {
	s, p := newTestSim(t)
	proj := s.Shoot(0, shoot0(), [3]float32{1, 0, 0})
	p.queueContact(proj, uint32(s.hitboxes[0]))

	s.Step()

	st := snapshotWorld(s)
	if h := player0(st).Health; h != float32(playerMaxHealth) {
		t.Fatalf("打中自己的命中盒不应扣血，得到 %v", h)
	}
	if len(projectilesOf(st)) != 1 {
		t.Fatal("打中自己的命中盒不应吃掉弹丸")
	}
}

// TestHitDealsDamage：命中对方扣一发伤害，自己不掉血、不得分。
func TestHitDealsDamage(t *testing.T) {
	s, p := newTestSim(t)
	proj := s.Shoot(0, shoot0(), [3]float32{1, 0, 0})
	p.queueContact(proj, uint32(s.hitboxes[1]))

	s.Step()

	st := snapshotWorld(s)
	if h := st.Players[1].Health; !near(h, playerMaxHealth-playerHitDamage, 0.01) {
		t.Fatalf("中弹者应扣 %v 血，得到 %v", playerHitDamage, h)
	}
	if h := st.Players[0].Health; h != float32(playerMaxHealth) {
		t.Fatalf("开枪者不应掉血，得到 %v", h)
	}
	if st.Players[0].Kills != 0 {
		t.Fatalf("未击杀不应加分，得到 %d", st.Players[0].Kills)
	}
}

// killsToDie 是从满血到死亡所需的弹丸数。
func killsToDie() int { return int(math.Ceil(playerMaxHealth / playerHitDamage)) }

// TestKillScoresAndRespawns：打到血尽即结算击杀，被杀方立刻满血回己方出生点。
func TestKillScoresAndRespawns(t *testing.T) {
	s, p := newTestSim(t)
	// 把 1 号挪开，免得它和 0 号的命中盒叠在一起、后续断言不好读。
	p.moveBody(uint32(s.hitboxes[1]), near0(6, hitboxOffsetY, 0))

	for i := 0; i < killsToDie(); i++ {
		proj := s.Shoot(0, shoot0(), [3]float32{1, 0, 0})
		p.queueContact(proj, uint32(s.hitboxes[1]))
		s.Step()
	}

	st := snapshotWorld(s)
	if st.Players[0].Kills != 1 {
		t.Fatalf("击杀方应得 1 杀，得到 %d", st.Players[0].Kills)
	}
	if st.Players[1].Deaths != 1 {
		t.Fatalf("被杀方应记 1 次死亡，得到 %d", st.Players[1].Deaths)
	}
	if h := st.Players[1].Health; h != float32(playerMaxHealth) {
		t.Fatalf("复活当 tick 血量就应是满血，得到 %v", h)
	}
	if got, want := p.characters[1].pos, hitboxPos(1); got[0] != want[0] || got[2] != want[2] {
		t.Fatalf("被杀方应立即回己方出生点，得到 %v", got)
	}
	if v := p.characters[1].vel; v != ([3]float32{}) {
		t.Fatalf("复活应清零速度，得到 %v", v)
	}
	// 命中盒必须随复活一起搬走：留在旧位置的话，之后飞来的弹丸还会打中一个
	// 「已经复活在别处的人」。
	if got, want := p.bodyPos(uint32(s.hitboxes[1])), hitboxPos(1); got != want {
		t.Fatalf("命中盒应随复活一起回出生点，得到 %v（期望 %v）", got, want)
	}
}

// TestSelfHitIsNotAHit：弹丸路过自己的命中盒既不算命中、也不被吃掉。
// 这是「枪口落在自己体内」的真实形态（第三人称的枪口就在角色中轴上），
// 不是自伤 —— 既不该扣血，也不该记一次死亡。
func TestSelfHitIsNotAHit(t *testing.T) {
	s, p := newTestSim(t)
	for i := 0; i < killsToDie()+2; i++ {
		proj := s.Shoot(1, shoot0(), [3]float32{1, 0, 0})
		p.queueContact(proj, uint32(s.hitboxes[1]))
		s.Step()
	}

	st := snapshotWorld(s)
	if st.Players[1].Kills != 0 {
		t.Fatalf("自伤不应记击杀，得到 %d", st.Players[1].Kills)
	}
	if st.Players[1].Deaths != 0 {
		t.Fatalf("路过自己的命中盒不该记死亡，得到 %d", st.Players[1].Deaths)
	}
	if h := st.Players[1].Health; h != float32(playerMaxHealth) {
		t.Fatalf("路过自己的命中盒不该掉血，得到 %v", h)
	}
}

// TestKillTargetEndsMatch：击杀数到 killTarget 判定胜者，超时后自动重开一局。
func TestKillTargetEndsMatch(t *testing.T) {
	s, p := newTestSim(t)
	// 直接把 0 号推到临界值：单次击杀的完整路径已由 TestKillScoresAndRespawns 覆盖，
	// 这里只验「到线判胜 + 超时重开」。
	s.score[0] = PlayerScore{Kills: int32(killTarget - 1)}
	s.replicateScore(0, s.score[0])

	for i := 0; i < killsToDie(); i++ {
		proj := s.Shoot(0, shoot0(), [3]float32{1, 0, 0})
		p.queueContact(proj, uint32(s.hitboxes[1]))
		s.Step()
	}

	st := snapshotWorld(s)
	if st.Winner != 0 {
		t.Fatalf("到 %d 杀应判 0 号获胜，得到 %d", killTarget, st.Winner)
	}
	gs, ok := ecs.Get[GameState](s.world, s.GameEntity())
	if !ok || gs.Winner != 0 {
		t.Fatalf("GameState 组件应同步胜者，得到 %+v", gs)
	}

	// 5 秒后自动重开：世界重建、战绩清零。
	for i := 0; i < matchOverTicks+2; i++ {
		s.Step()
	}
	st = snapshotWorld(s)
	if st.Winner != -1 || st.Step > matchOverTicks+3 {
		t.Fatalf("超时后应自动重开一局，得到 winner=%d step=%d", st.Winner, st.Step)
	}
	if st.Players[0].Kills != 0 || st.Players[1].Deaths != 0 {
		t.Fatalf("新一局战绩应清零，得到 kills=%d deaths=%d", st.Players[0].Kills, st.Players[1].Deaths)
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

func TestProjectileExpiry(t *testing.T) {
	s, _ := newTestSim(t)
	s.Shoot(0, shoot0(), [3]float32{1, 0, 0})
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
	s.Shoot(0, shoot0(), [3]float32{1, 0, 0})
	proj := s.Shoot(0, shoot0(), [3]float32{1, 0, 0})
	p.queueContact(proj, uint32(s.hitboxes[1]))
	for i := 0; i < 5; i++ {
		s.Step()
	}

	s.Reset()

	st := snapshotWorld(s)
	if st.Step != 0 || st.Winner != -1 {
		t.Fatalf("Reset 后对局状态应清零，得到 step=%d winner=%d", st.Step, st.Winner)
	}
	if h := st.Players[1].Health; h != float32(playerMaxHealth) {
		t.Fatalf("Reset 后血量应为满血，得到 %v", h)
	}
	want := sceneBodyCount()
	if len(st.Bodies) != want {
		t.Fatalf("Reset 后场景应重建（%d 个可渲染刚体），得到 %d", want, len(st.Bodies))
	}
	if got := p.bodyCount(); got != want+MaxPlayers {
		t.Fatalf("Reset 后命中盒也应重建（物理刚体共 %d），得到 %d", want+MaxPlayers, got)
	}
	// 物理世界重建后刚体 id 从头开始分配（命中盒在场景之后创建，因此排在它们后面）。
	if st.Bodies[0].ID != 1 || st.Bodies[len(st.Bodies)-1].ID != uint32(want) {
		t.Fatalf("重建后刚体 id 应从 1 重新分配，得到 %d..%d",
			st.Bodies[0].ID, st.Bodies[len(st.Bodies)-1].ID)
	}
	if uint32(s.hitboxes[0]) <= uint32(want) {
		t.Fatalf("命中盒应在场景之后创建（id > %d），得到 %d", want, s.hitboxes[0])
	}
}

func TestSnapshotStableAfterRemovals(t *testing.T) {
	s, p := newTestSim(t)
	deck := snapshotWorld(s).Bodies[0].ID
	proj := s.Shoot(0, shoot0(), [3]float32{0, -1, 0})
	p.queueContact(proj, deck)
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

func TestInitIsIdempotent(t *testing.T) {
	s, p := newTestSim(t)
	before := p.bodyCount()
	// 造点"噪音"：发弹、推进几 tick。
	s.Shoot(0, shoot0(), [3]float32{1, 0, 0})
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
	if st.Step != 0 || st.Winner != -1 || player0(st).Health != float32(playerMaxHealth) {
		t.Fatalf("重复 Init 后状态应回到初始，得到 step=%d winner=%d hp=%v",
			st.Step, st.Winner, player0(st).Health)
	}
	if len(projectilesOf(st)) != 0 {
		t.Fatalf("重复 Init 后应无残留弹丸，得到 %d", len(projectilesOf(st)))
	}
}

// 全局状态单例在 init 之后必须立刻与 Go 侧字段一致（组件是用零值建的，
// 漏掉这次同步会让客户端在首次分出胜负前就读到「已结束」）。
func TestGameStateSyncedAtInit(t *testing.T) {
	s, _ := newTestSim(t)
	gs, ok := ecs.Get[GameState](s.world, s.GameEntity())
	if !ok {
		t.Fatal("init 后应有全局状态单例实体")
	}
	if gs.Winner != s.winner {
		t.Fatalf("init 后组件应与 Go 侧一致：组件 Winner=%d，Go 侧 winner=%d", gs.Winner, s.winner)
	}

	// Reset 重建世界后同样要立刻同步：单例是新实体、组件又回到零值。
	s.Reset()
	gs, ok = ecs.Get[GameState](s.world, s.GameEntity())
	if !ok {
		t.Fatal("Reset 后应有全局状态单例实体")
	}
	if gs.Winner != s.winner {
		t.Fatalf("Reset 后组件应与 Go 侧一致：组件 Winner=%d，Go 侧 winner=%d", gs.Winner, s.winner)
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

// TestPlayersDamageIndependently 验证只有被命中的那个玩家掉血。
func TestPlayersDamageIndependently(t *testing.T) {
	s, p := newTestSim(t)
	proj := s.Shoot(1, shoot0(), [3]float32{1, 0, 0}) // 1 号打 0 号
	p.queueContact(uint32(s.hitboxes[0]), proj)

	s.Step()

	st := snapshotWorld(s)
	if h := st.Players[0].Health; !near(h, playerMaxHealth-playerHitDamage, 0.01) {
		t.Fatalf("0 号玩家应中弹掉血，得到 %v", h)
	}
	if h := st.Players[1].Health; h != float32(playerMaxHealth) {
		t.Fatalf("1 号玩家不应受伤，得到 %v", h)
	}
}

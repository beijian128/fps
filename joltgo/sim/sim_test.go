package sim

// 用 fake 物理（只做运动学积分 + 地板钳制）验证各系统的行为，不依赖 cgo/Jolt DLL。
// fake 的职责是模拟 Physics 接口的契约：发放递增 id、积分速度、上报接触事件。

import (
	"math"
	"testing"

	"joltgo/ecs"
)

type fakeBody struct {
	active bool
	pos    [3]float32
	vel    [3]float32
	radius float32 // 球/胶囊半径；盒子为 0（fake 只对球/胶囊生成角色接触）
	sensor bool
}

type fakePhysics struct {
	bodies      map[uint32]*fakeBody
	nextID      uint32
	quality     map[uint32]MotionQuality
	friction    map[uint32]float32
	restitution map[uint32]float32
	sensors     map[uint32]bool

	character      [3]float32
	charVel        [3]float32
	charCreated    bool
	charHalfHeight float32
	charRadius     float32
	charOffsetY    float32
	charSpawn      [3]float32
	dynamicPush    bool
	gravity        [3]float32

	charContacts []uint32 // 本 tick 角色接触的刚体（UpdateCharacter 时重建）
	sticky       []uint32 // 测试注入的固定接触
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
	f.character = [3]float32{}
	f.charVel = [3]float32{}
	f.charCreated = false
	f.dynamicPush = true
	f.gravity = [3]float32{}
	f.charContacts = nil
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
	f.bodies[id] = &fakeBody{active: motion != MotionStatic, pos: pos, radius: radius, sensor: sensor}
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

func (f *fakePhysics) CreateCharacter(halfHeight, radius, offsetY, x, y, z float32) {
	f.charCreated = true
	f.charHalfHeight = halfHeight
	f.charRadius = radius
	f.charOffsetY = offsetY
	f.charSpawn = [3]float32{x, y, z}
	f.character = [3]float32{x, y, z}
}

func (f *fakePhysics) SetCharacterDynamicPush(allow bool) { f.dynamicPush = allow }

func (f *fakePhysics) CharacterPosition() [3]float32 { return f.character }

func (f *fakePhysics) SetCharacterPosition(x, y, z float32) {
	f.character = [3]float32{x, y, z}
	f.respawns++
}

func (f *fakePhysics) CharacterVelocity() [3]float32 { return f.charVel }

func (f *fakePhysics) SetCharacterVelocity(v [3]float32) { f.charVel = v }

func (f *fakePhysics) CharacterOnGround() bool { return f.character[1] <= 1e-3 }

// UpdateCharacter 积分角色速度并钳制到地板（y=0），模拟 ExtendedUpdate 的着地；
// 同时重建本 tick 的角色接触列表（与真实 Jolt 一致：接触是物理事实）。
func (f *fakePhysics) UpdateCharacter(dt float32) {
	f.character[0] += f.charVel[0] * dt
	f.character[1] += f.charVel[1] * dt
	f.character[2] += f.charVel[2] * dt
	if f.character[1] < 0 {
		f.character[1] = 0
	}

	contacts := make([]uint32, 0, 8)
	for id, b := range f.bodies {
		if b.radius <= 0 {
			continue // fake 只对球/胶囊生成角色接触
		}
		dx := f.character[0] - b.pos[0]
		dy := f.character[1] - b.pos[1]
		dz := f.character[2] - b.pos[2]
		r := characterRadius + b.radius
		if dx*dx+dy*dy+dz*dz < r*r {
			contacts = append(contacts, id)
		}
	}
	f.charContacts = append(contacts, f.sticky...)
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
		fn(id, b.active, b.pos, [4]float32{0, 0, 0, 1})
	}
}

func (f *fakePhysics) PollContacts() []Contact {
	c := f.contacts
	f.contacts = nil
	return c
}

func (f *fakePhysics) PollCharacterContacts() []uint32 { return f.charContacts }

// ---- 测试钩子 ----

func (f *fakePhysics) queueContact(a, b uint32) {
	f.contacts = append(f.contacts, Contact{BodyA: a, BodyB: b})
}

// queueCharacterContact 注入一个持续存在的角色接触（每次 UpdateCharacter 都会带上）。
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

// ---- 测试 ----

func TestInitialSnapshot(t *testing.T) {
	s, p := newTestSim(t)
	st := s.Snapshot()

	// 场景：5 静态（地板+四墙）+ 18 箱子 + 8 靶球 + 3 敌人 + 6 金币传感器球。
	if p.bodyCount() != 34+initialResource {
		t.Fatalf("初始物理刚体数应为 %d（含金币传感器球），得到 %d", 34+initialResource, p.bodyCount())
	}
	if len(p.sensors) != initialResource {
		t.Fatalf("金币传感器球应为 %d 个，得到 %d", initialResource, len(p.sensors))
	}
	if len(st.Bodies) != 34 {
		t.Fatalf("快照刚体数应为 34（传感器球不上屏），得到 %d", len(st.Bodies))
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
	if len(targetsOf(st)) != 8 {
		t.Fatalf("初始靶球应为 8，得到 %d", len(targetsOf(st)))
	}
	if len(st.Resources) != initialResource {
		t.Fatalf("初始金币应为 %d，得到 %d", initialResource, len(st.Resources))
	}

	if st.Step != 0 || st.Score != 0 || st.Wave != 1 || st.Gold != 0 {
		t.Fatalf("初始状态应为 step=0/score=0/wave=1/gold=0，得到 %+v", st)
	}
	if st.Player.Health != 100 {
		t.Fatalf("初始血量应为 100，得到 %v", st.Player.Health)
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
	if !p.charCreated || p.charHalfHeight != 0.5 || p.charRadius != 0.4 || p.charOffsetY != 0.9 {
		t.Fatalf("角色形状应由 sim 传入（0.5/0.4/0.9），得到 %v/%v/%v",
			p.charHalfHeight, p.charRadius, p.charOffsetY)
	}
	if p.charSpawn != [3]float32{0, 0, 12} {
		t.Fatalf("角色出生点应由 sim 传入，得到 %v", p.charSpawn)
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

	if id := s.Shoot([3]float32{0, 1, 12}, [3]float32{0, 0, 0}); id != 0 {
		t.Fatalf("零向量方向应拒绝，得到 id=%d", id)
	}

	id := s.Shoot([3]float32{0, 1, 12}, [3]float32{2, 0, 0})
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
	for _, b := range s.Snapshot().Bodies {
		if b.ID == id && b.Pos != [3]float32{0, 1, 12} {
			t.Fatalf("新弹丸应带初始位置，得到 %v", b.Pos)
		}
	}
}

func TestProjectileHitsTargetScores(t *testing.T) {
	s, p := newTestSim(t)
	target := targetsOf(s.Snapshot())[0]
	proj := s.Shoot([3]float32{0, 1, 12}, [3]float32{1, 0, 0})
	p.queueContact(proj, target)

	s.Step()

	st := s.Snapshot()
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
	target := targetsOf(s.Snapshot())[0]
	proj := s.Shoot([3]float32{0, 1, 12}, [3]float32{1, 0, 0})
	p.queueContact(target, proj) // 弹丸是接触对的第二方

	s.Step()

	st := s.Snapshot()
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
	st0 := s.Snapshot()
	enemy := enemiesOf(st0)[0]
	target := targetsOf(st0)[0]
	p.queueContact(enemy, target) // 与弹丸无关的接触（敌人撞到靶球）

	s.Step()

	st := s.Snapshot()
	if st.Score != 0 {
		t.Fatalf("无关接触不应得分，得到 %d", st.Score)
	}
	if len(enemiesOf(st)) != initialEnemies || len(targetsOf(st)) != 8 {
		t.Fatal("无关接触不应移除任何实体")
	}
}

func TestSensorDoesNotBlockProjectile(t *testing.T) {
	s, p := newTestSim(t)
	coin := uint32(s.Snapshot().Resources[0].ID)
	proj := s.Shoot([3]float32{0, 1, 12}, [3]float32{1, 0, 0})
	p.queueContact(proj, coin) // 弹丸撞上金币传感器球

	s.Step()

	st := s.Snapshot()
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
	for _, b := range s.Snapshot().Bodies {
		if !b.Static && !b.Target && !b.Enemy {
			crate = b.ID
			break
		}
	}
	p.queueCharacterContact(crate)
	for i := 0; i < 5; i++ {
		s.Step()
	}
	if h := s.Snapshot().Player.Health; h != 100 {
		t.Fatalf("碰到箱子不应掉血，得到 %v", h)
	}
}

func TestProjectileKillsEnemyAndDropsResource(t *testing.T) {
	s, p := newTestSim(t)
	enemy := enemiesOf(s.Snapshot())[0]
	p.moveBody(enemy, [3]float32{2, 1, 12}) // 玩家附近但不贴身（无接触伤害）

	for i := 0; i < 3; i++ {
		proj := s.Shoot([3]float32{0, 1, 12}, [3]float32{1, 0, 0})
		p.queueContact(proj, enemy)
	}
	s.Step()

	st := s.Snapshot()
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
	enemy := enemiesOf(s.Snapshot())[0]
	p.moveBody(enemy, [3]float32{0, 0, 12}) // 贴到玩家身上

	for i := 0; i < 10; i++ {
		s.Step()
	}
	if h := s.Snapshot().Player.Health; !near(h, 96, 0.1) {
		t.Fatalf("10 tick 贴身伤害后血量应约 96，得到 %v", h)
	}

	for i := 0; i < 250; i++ {
		s.Step()
	}
	if p.respawns < 1 {
		t.Fatal("血量耗尽后应复活一次")
	}
	if h := s.Snapshot().Player.Health; h <= 0 || h > 100 {
		t.Fatalf("复活后血量应在 (0, 100]，得到 %v", h)
	}
	if p.character[0] != 0 || p.character[2] != 12 || p.character[1] > playerSpawnY+1e-3 {
		t.Fatalf("复活后应回到出生点（着地），得到 %v", p.character)
	}
}

func TestJumpLiftsPlayer(t *testing.T) {
	s, p := newTestSim(t)
	s.ApplyInput([2]float32{}, true)
	s.Step()

	if p.character[1] <= 1e-3 {
		t.Fatalf("跳跃应让角色离地（v0=%v，g=%v），得到 y=%v", jumpSpeed, gravityY, p.character[1])
	}
	in, _ := ecs.Get[Input](s.world, s.player)
	if in.Jump {
		t.Fatal("跳跃输入应在消费后清零")
	}
}

func TestResourcePickup(t *testing.T) {
	s, p := newTestSim(t)
	r := s.Snapshot().Resources[0]
	p.character = r.Pos // 传送到金币位置

	s.Step()

	st := s.Snapshot()
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
	for _, enemy := range enemiesOf(s.Snapshot()) {
		for i := 0; i < 3; i++ {
			proj := s.Shoot([3]float32{0, 1, 12}, [3]float32{1, 0, 0})
			p.queueContact(proj, enemy)
		}
	}
	s.Step()

	st := s.Snapshot()
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
	st = s.Snapshot()
	if st.Wave != 2 {
		t.Fatalf("波次应推进到 2，得到 %d", st.Wave)
	}
	if got := len(enemiesOf(st)); got != initialEnemies+1 {
		t.Fatalf("第 2 波应有 %d 只，得到 %d", initialEnemies+1, got)
	}
}

func TestProjectileExpiry(t *testing.T) {
	s, _ := newTestSim(t)
	s.Shoot([3]float32{0, 1, 12}, [3]float32{1, 0, 0})
	if got := len(projectilesOf(s.Snapshot())); got != 1 {
		t.Fatalf("应有 1 枚弹丸，得到 %d", got)
	}

	for i := 0; i <= projectileMaxLife+1; i++ {
		s.Step()
	}
	if got := len(projectilesOf(s.Snapshot())); got != 0 {
		t.Fatalf("超过最大存活时间的弹丸应被移除，还剩 %d", got)
	}
}

func TestEnemyStandsStill(t *testing.T) {
	s, p := newTestSim(t)
	enemy := enemiesOf(s.Snapshot())[0]
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
	for _, b := range s.Snapshot().Bodies {
		if b.ID == enemy && !b.Static {
			t.Fatal("怪物应为静态刚体（不可推动）")
		}
	}
}

func TestApplyInputMovesPlayer(t *testing.T) {
	s, p := newTestSim(t)
	s.ApplyInput([2]float32{8, 0}, false)
	s.Step()

	if !near(p.character[0], 8*TickDT, 1e-4) {
		t.Fatalf("移动输入应驱动角色（+%v），得到 x=%v", 8*TickDT, p.character[0])
	}
}

func TestResetRebuildsScene(t *testing.T) {
	s, p := newTestSim(t)
	s.Shoot([3]float32{0, 1, 12}, [3]float32{1, 0, 0})
	enemy := enemiesOf(s.Snapshot())[0]
	proj := s.Shoot([3]float32{0, 1, 12}, [3]float32{1, 0, 0})
	p.queueContact(proj, enemy)
	for i := 0; i < 5; i++ {
		s.Step()
	}

	s.Reset()

	st := s.Snapshot()
	if st.Step != 0 || st.Score != 0 || st.Gold != 0 || st.Wave != 1 {
		t.Fatalf("Reset 后全局状态应清零，得到 %+v", st)
	}
	if st.Player.Health != 100 {
		t.Fatalf("Reset 后血量应为 100，得到 %v", st.Player.Health)
	}
	if len(st.Bodies) != 34 || len(st.Resources) != initialResource || len(enemiesOf(st)) != initialEnemies {
		t.Fatalf("Reset 后场景应重建（34 刚体/6 金币/3 敌人），得到 %d/%d/%d",
			len(st.Bodies), len(st.Resources), len(enemiesOf(st)))
	}
	// 物理世界重建后刚体 id 从头开始分配。
	if st.Bodies[0].ID != 1 || st.Bodies[len(st.Bodies)-1].ID != 34 {
		t.Fatalf("重建后刚体 id 应从 1 重新分配，得到 %d..%d",
			st.Bodies[0].ID, st.Bodies[len(st.Bodies)-1].ID)
	}
}

func TestSnapshotStableAfterRemovals(t *testing.T) {
	s, p := newTestSim(t)
	target := targetsOf(s.Snapshot())[0]
	proj := s.Shoot([3]float32{0, 1, 12}, [3]float32{1, 0, 0})
	p.queueContact(proj, target)
	s.Step() // 触发 swap-remove 打乱存储顺序

	st := s.Snapshot()
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
	s.ApplyInput([2]float32{60, 80}, false)
	s.Step()
	dx := p.character[0]
	dz := p.character[2] - 12
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
	enemy := enemiesOf(s.Snapshot())[0]
	p.moveBody(enemy, [3]float32{0, 0, 12}) // 贴身 → 每 tick 持续伤害
	for i := 0; i < 500 && p.respawns == 0; i++ {
		s.Step()
	}
	if p.respawns == 0 {
		t.Fatal("贴身持续伤害应在 500 tick 内触发复活")
	}
	st := s.Snapshot()
	if st.Player.Health != 100 {
		t.Fatalf("复活当 tick 血量应为 100，得到 %v", st.Player.Health)
	}
	if st.Player.Pos != (Position{0, playerSpawnY, 12}) {
		t.Fatalf("复活当 tick 快照位置应已是出生点（不再留在死亡点），得到 %v", st.Player.Pos)
	}
	if p.character != ([3]float32{0, playerSpawnY, 12}) {
		t.Fatalf("物理角色位置应立即回到出生点，得到 %v", p.character)
	}
	if p.charVel != ([3]float32{}) {
		t.Fatalf("复活应清零角色速度，得到 %v", p.charVel)
	}
}

func TestInitIsIdempotent(t *testing.T) {
	s, p := newTestSim(t)
	before := p.bodyCount()
	// 造点"噪音"：发弹、推进几 tick。
	s.Shoot([3]float32{0, 1, 12}, [3]float32{1, 0, 0})
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
	st := s.Snapshot()
	if st.Step != 0 || st.Score != 0 || st.Wave != 1 || st.Player.Health != 100 {
		t.Fatalf("重复 Init 后状态应回到初始，得到 %+v", st)
	}
	if len(projectilesOf(st)) != 0 {
		t.Fatalf("重复 Init 后应无残留弹丸，得到 %d", len(projectilesOf(st)))
	}
}

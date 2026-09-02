package main

// 游戏状态与规则。网络层（ws.go）只负责帧编解码与转发，所有玩法逻辑都在这里。
//
// 锁约定：公开方法（Init / ApplyInput / Shoot / Reset / Step / Snapshot）内部
// 自带 g.mu 加锁；xLocked 形式的内部方法要求调用方已持有锁，避免双重加锁。
// 这样 tick 循环和 WebSocket 读线程可以安全并发调用。

/*
#cgo CFLAGS: -I${SRCDIR}/wrapper
#cgo LDFLAGS: -L${SRCDIR}/build -ljolt_c
#include "jolt_c.h"
*/
import "C"

import (
	"log"
	"math"
	"math/rand/v2"
	"sync"
)

const tickDT = 1.0 / 20.0 // 模拟 tick 时长（20 Hz）

// PVE 难度参数（低难度：怪物慢、伤害低）。
const (
	enemySpeed      = 2.2  // 怪物追击速度（m/s）
	enemyDamage     = 0.4  // 贴身每 tick 伤害（= 8/s）
	waveDelayTicks  = 40   // 清波后 2 秒刷下一波
	maxResource     = 10   // 场上金币上限
	resourceRadius  = 1.0  // 拾取半径（水平距离，金币悬浮高度固定、垂直差忽略）
	resourceY       = 0.8  // 金币悬浮高度
	initialResource = 6    // 初始金币数量
)

type bodyInfo struct {
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

type playerState struct {
	Pos    [3]float32 `json:"pos"` // feet position
	Health float32    `json:"health"`
}

type resourceInfo struct {
	ID   int        `json:"id"`
	Pos  [3]float32 `json:"pos"`
	Kind int        `json:"kind"` // 0 = 金币
}

type state struct {
	Bodies    []bodyInfo     `json:"bodies"`
	Resources []resourceInfo `json:"resources"`
	Player    playerState    `json:"player"`
	Step      int            `json:"step"`
	Score     int            `json:"score"`
	Wave      int            `json:"wave"`
	Gold      int            `json:"gold"`
}

type Game struct {
	mu           sync.Mutex
	world        *C.JoltWorld
	step         int
	score        int
	playerHealth float32
	projectiles  map[uint32]int // projectile body id -> 发射时的 step

	// PVE：波次 + 资源（金币）。金币物理上不存在，纯逻辑对象。
	wave          int
	waveClearStep int // 场上清空时的 step（0 = 尚未清空）
	gold          int
	resources     []resourceInfo
	nextResID     int

	// 最新一份客户端输入（60 Hz 上报，20 Hz tick 消费）。
	inputMove [2]float32
	inputJump bool
}

func newGame() *Game {
	return &Game{playerHealth: 100, projectiles: map[uint32]int{}}
}

// Init 创建物理世界与初始场景。只在启动时调用一次，之后重建请用 Reset。
func (g *Game) Init() {
	g.initLocked()
}

// ApplyInput 记录客户端最新输入（跳跃为边沿触发：服务端在下一个 tick 消费）。
func (g *Game) ApplyInput(move [2]float32, jump bool) {
	g.mu.Lock()
	g.inputMove = move
	if jump {
		g.inputJump = true
	}
	g.mu.Unlock()
}

// Shoot 发射一枚弹丸，返回弹丸 id（0 = 失败）。
func (g *Game) Shoot(origin, dir [3]float32) uint32 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.shootLocked(origin, dir)
}

// Reset 销毁并重建整个场景，重置所有游戏状态与输入。
func (g *Game) Reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.resetLocked()
}

// Step 推进一个模拟 tick（1/20 秒）：消费输入 → 敌人 AI → 物理 → 弹丸 → 伤害。
func (g *Game) Step() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stepLocked()
}

// Snapshot 返回当前完整状态（序列化由调用方负责）。
func (g *Game) Snapshot() state {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.snapshotLocked()
}

// ---- 以下 *Locked 方法要求调用方持有 g.mu ----

func (g *Game) initLocked() {
	g.world = C.jolt_create()
	if g.world == nil {
		log.Fatal("failed to create Jolt world")
	}

	// Floor.
	C.jolt_add_static_box(g.world, 100, 1, 100, 0, -1, 0)

	// Arena walls.
	C.jolt_add_static_box(g.world, 20, 4, 1, 0, 3, 20)
	C.jolt_add_static_box(g.world, 20, 4, 1, 0, 3, -20)
	C.jolt_add_static_box(g.world, 1, 4, 20, 20, 3, 0)
	C.jolt_add_static_box(g.world, 1, 4, 20, -20, 3, 0)

	// Crates.
	type pos [3]float32
	crates := []pos{
		{-4, 1, 3}, {-3, 1, 4}, {-3.5, 1, 5},
		{4, 1, -2}, {5, 1, -1}, {5, 2, -1.5}, {4, 2, -1},
		{-6, 1, -3}, {-6, 2, -3}, {-6, 3, -3},
		{2, 1, 5}, {3, 1, 5},
		{-2, 1, -5}, {-1, 1, -5}, {-1.5, 2, -5},
		{7, 1, 3}, {7, 2, 3}, {8, 1, 3},
	}
	for _, c := range crates {
		C.jolt_add_dynamic_box(g.world, 0.5, 0.5, 0.5, C.float(c[0]), C.float(c[1]), C.float(c[2]), 0, 0, 0)
	}

	// Destroyable floating targets.
	targets := []pos{
		{-8, 2.5, 0}, {-5, 3, 7}, {3, 3.5, -8}, {8, 2.5, -6},
		{0, 4, 2}, {-3, 2, -8}, {6, 3, 6}, {-7, 3.5, 5},
	}
	for _, t := range targets {
		C.jolt_add_target_sphere(g.world, C.float(t[0]), C.float(t[1]), C.float(t[2]), 0.4)
	}

	// Enemies.
	for i := 0; i < 3; i++ {
		g.spawnEnemyLocked()
	}

	// PVE 初始波次 + 金币资源。
	g.wave = 1
	for i := 0; i < initialResource; i++ {
		g.spawnInitialResourceLocked()
	}

	// Put the character on the ground immediately so the first jump after a
	// reset (or initial load) is recognized instead of being swallowed while
	// the capsule settles.
	C.jolt_update_character(g.world, 0, 0, 0, C.float(1.0/60.0))
}

func (g *Game) resetLocked() {
	C.jolt_destroy(g.world)
	g.world = nil
	g.step = 0
	g.score = 0
	g.playerHealth = 100
	g.projectiles = map[uint32]int{}
	g.wave = 1
	g.waveClearStep = 0
	g.gold = 0
	g.resources = nil
	g.nextResID = 0
	g.inputMove = [2]float32{}
	g.inputJump = false
	g.initLocked()
}

func (g *Game) stepLocked() {
	jump := 0
	if g.inputJump {
		jump = 1
		g.inputJump = false
	}
	C.jolt_update_character(g.world, C.float(g.inputMove[0]), C.float(g.inputMove[1]), C.int(jump), C.float(tickDT))
	g.updateEnemiesLocked()
	C.jolt_step(g.world, C.float(tickDT), 2)
	g.step++
	g.processProjectileHitsLocked()
	g.applyEnemyDamageLocked()
	g.expireProjectilesLocked()
	g.collectResourcesLocked()
	g.advanceWaveLocked()
}

func (g *Game) shootLocked(origin, dir [3]float32) uint32 {
	dx, dy, dz := dir[0], dir[1], dir[2]
	l := dx*dx + dy*dy + dz*dz
	if l < 1e-9 {
		return 0
	}
	inv := 1.0 / float32(math.Sqrt(float64(l)))
	dx, dy, dz = dx*inv, dy*inv, dz*inv

	pid := uint32(C.jolt_fire_projectile(g.world,
		C.float(origin[0]), C.float(origin[1]), C.float(origin[2]),
		C.float(dx), C.float(dy), C.float(dz), C.float(60.0)))
	if pid != 0 {
		g.projectiles[pid] = g.step
	}
	return pid
}

func (g *Game) snapshotLocked() state {
	s := state{
		Bodies:    []bodyInfo{},
		Resources: make([]resourceInfo, len(g.resources)),
		Player:    playerState{Health: g.playerHealth},
		Step:      g.step,
		Score:     g.score,
		Wave:      g.wave,
		Gold:      g.gold,
	}
	copy(s.Resources, g.resources) // 拷贝：序列化发生在锁外，不能共享底层数组
	if g.world == nil {
		return s
	}

	s.Player.Pos = g.getPlayerPosLocked()

	n := int(C.jolt_body_count(g.world))
	s.Bodies = make([]bodyInfo, 0, n)
	for i := 0; i < n; i++ {
		var info C.JoltBodyInfo
		if C.jolt_get_body_info(g.world, C.uint32_t(i), &info) != 1 {
			continue
		}
		s.Bodies = append(s.Bodies, bodyInfo{
			ID:         uint32(info.id),
			Type:       int(info._type),
			Static:     info.is_static != 0,
			Target:     info.is_target != 0,
			Enemy:      info.is_enemy != 0,
			Projectile: info.is_projectile != 0,
			Pos:        [3]float32{float32(info.pos[0]), float32(info.pos[1]), float32(info.pos[2])},
			Quat:       [4]float32{float32(info.quat[0]), float32(info.quat[1]), float32(info.quat[2]), float32(info.quat[3])},
			Size:       [3]float32{float32(info.size[0]), float32(info.size[1]), float32(info.size[2])},
			Health:     float32(info.health),
			Active:     info.active != 0,
		})
	}
	return s
}

func (g *Game) getPlayerPosLocked() [3]float32 {
	var out [3]C.float
	C.jolt_get_character_position(g.world, &out[0])
	return [3]float32{float32(out[0]), float32(out[1]), float32(out[2])}
}

func (g *Game) updateEnemiesLocked() {
	player := g.getPlayerPosLocked()
	n := int(C.jolt_body_count(g.world))
	for i := 0; i < n; i++ {
		var info C.JoltBodyInfo
		if C.jolt_get_body_info(g.world, C.uint32_t(i), &info) != 1 {
			continue
		}
		if info.is_enemy == 0 {
			continue
		}

		dx := player[0] - float32(info.pos[0])
		dz := player[2] - float32(info.pos[2])
		d := float32(math.Sqrt(float64(dx*dx + dz*dz)))
		if d < 0.01 {
			continue
		}
		C.jolt_set_body_velocity(g.world, C.uint32_t(info.id), C.float(dx/d*enemySpeed), 0, C.float(dz/d*enemySpeed))
	}
}

func (g *Game) processProjectileHitsLocked() {
	var hits [64]C.JoltProjectileHit
	n := int(C.jolt_poll_projectile_hits(g.world, &hits[0], 64))
	handled := map[uint32]bool{}
	for i := 0; i < n; i++ {
		proj := uint32(hits[i].projectile_id)
		other := uint32(hits[i].body_id)
		if handled[proj] {
			continue
		}
		handled[proj] = true

		C.jolt_remove_body(g.world, C.uint32_t(proj))
		delete(g.projectiles, proj)

		b := g.findBodyLocked(other)
		if b == nil || b.Projectile {
			continue
		}
		switch {
		case b.Target:
			C.jolt_remove_body(g.world, C.uint32_t(other))
			g.score++
		case b.Enemy:
			if C.jolt_damage_enemy(g.world, C.uint32_t(other), 1) == 1 {
				g.score++
				g.dropResourceLocked(b.Pos) // 击杀掉落金币
			}
		}
	}
}

func (g *Game) findBodyLocked(id uint32) *bodyInfo {
	n := int(C.jolt_body_count(g.world))
	for i := 0; i < n; i++ {
		var info C.JoltBodyInfo
		if C.jolt_get_body_info(g.world, C.uint32_t(i), &info) != 1 {
			continue
		}
		if uint32(info.id) == id {
			return &bodyInfo{
				ID:         uint32(info.id),
				Type:       int(info._type),
				Static:     info.is_static != 0,
				Target:     info.is_target != 0,
				Enemy:      info.is_enemy != 0,
				Projectile: info.is_projectile != 0,
				Pos:        [3]float32{float32(info.pos[0]), float32(info.pos[1]), float32(info.pos[2])},
			}
		}
	}
	return nil
}

func (g *Game) applyEnemyDamageLocked() {
	player := g.getPlayerPosLocked()
	n := int(C.jolt_body_count(g.world))
	for i := 0; i < n; i++ {
		var info C.JoltBodyInfo
		if C.jolt_get_body_info(g.world, C.uint32_t(i), &info) != 1 {
			continue
		}
		if info.is_enemy == 0 {
			continue
		}
		dx := player[0] - float32(info.pos[0])
		dy := player[1] - float32(info.pos[1])
		dz := player[2] - float32(info.pos[2])
		if dx*dx+dy*dy+dz*dz < 1.4*1.4 {
			g.playerHealth -= enemyDamage // 每 tick（= 8/s），低难度
		}
	}
	if g.playerHealth <= 0 {
		g.playerHealth = 100
		C.jolt_set_character_position(g.world, 0, 0.2, 12)
	}
}

func (g *Game) expireProjectilesLocked() {
	const maxLife = 60 // 20 Hz tick 下 60 tick = 3 秒
	for id, spawnStep := range g.projectiles {
		if g.step-spawnStep > maxLife {
			C.jolt_remove_body(g.world, C.uint32_t(id))
			delete(g.projectiles, id)
		}
	}
}

func randRange(a, b float32) float32 {
	return a + rand.Float32()*(b-a)
}

func (g *Game) enemyCountLocked() int {
	n := int(C.jolt_body_count(g.world))
	count := 0
	for i := 0; i < n; i++ {
		var info C.JoltBodyInfo
		if C.jolt_get_body_info(g.world, C.uint32_t(i), &info) != 1 {
			continue
		}
		if info.is_enemy != 0 {
			count++
		}
	}
	return count
}

// 初始金币：随机撒在地图上（离出生点 4m 以外）。
func (g *Game) spawnInitialResourceLocked() {
	for attempt := 0; attempt < 24; attempt++ {
		x := randRange(-16, 16)
		z := randRange(-16, 16)
		dx := x
		dz := z - 12 // 出生点 (0, 0.2, 12)
		if dx*dx+dz*dz >= 4*4 {
			g.nextResID++
			g.resources = append(g.resources, resourceInfo{ID: g.nextResID, Pos: [3]float32{x, resourceY, z}})
			return
		}
	}
}

// 击杀掉落：怪物死亡位置生成金币（带随机偏移，避免叠成一格）。
func (g *Game) dropResourceLocked(at [3]float32) {
	if len(g.resources) >= maxResource {
		return
	}
	g.nextResID++
	g.resources = append(g.resources, resourceInfo{
		ID:   g.nextResID,
		Pos:  [3]float32{at[0] + randRange(-0.4, 0.4), resourceY, at[2] + randRange(-0.4, 0.4)},
		Kind: 0,
	})
}

// 走近拾取金币（水平距离判定；金币悬浮，垂直差值不计）。
func (g *Game) collectResourcesLocked() {
	player := g.getPlayerPosLocked()
	kept := g.resources[:0]
	for _, r := range g.resources {
		dx := player[0] - r.Pos[0]
		dz := player[2] - r.Pos[2]
		if r.Kind == 0 && dx*dx+dz*dz < resourceRadius*resourceRadius {
			g.gold++
			continue
		}
		kept = append(kept, r)
	}
	g.resources = kept
}

// 波次推进：场上清空 2 秒后刷下一波（第 n 波 1+n 只，上限 6 只）。
func (g *Game) advanceWaveLocked() {
	if g.enemyCountLocked() > 0 {
		g.waveClearStep = 0
		return
	}
	if g.waveClearStep == 0 {
		g.waveClearStep = g.step
		return
	}
	if g.step-g.waveClearStep < waveDelayTicks {
		return
	}
	g.wave++
	n := 1 + g.wave
	if n > 6 {
		n = 6
	}
	for i := 0; i < n; i++ {
		g.spawnEnemyLocked()
	}
	g.waveClearStep = 0
}

func (g *Game) spawnEnemyLocked() {
	player := g.getPlayerPosLocked()
	for attempt := 0; attempt < 24; attempt++ {
		x := randRange(-14, 14)
		z := randRange(-14, 14)
		dx := x - player[0]
		dz := z - player[2]
		if dx*dx+dz*dz >= 10*10 {
			C.jolt_add_enemy(g.world, C.float(x), C.float(1.0), C.float(z), C.float(0.35), C.float(0.5))
			return
		}
	}
	C.jolt_add_enemy(g.world, -12, 1.0, -12, 0.35, 0.5)
}

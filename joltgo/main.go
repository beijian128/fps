package main

/*
#cgo CFLAGS: -I${SRCDIR}/wrapper
#cgo LDFLAGS: -L${SRCDIR}/build -ljolt_c
#include "jolt_c.h"
*/
import "C"

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"math"
	"math/rand/v2"
	"net/http"
	"sync"
)

//go:embed web
var webFS embed.FS

var (
	mu           sync.Mutex
	world        *C.JoltWorld
	stepCount    int
	score        int
	playerHealth float32 = 100
	projectiles          = map[uint32]int{}
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

type state struct {
	Bodies []bodyInfo  `json:"bodies"`
	Player playerState `json:"player"`
	Step   int         `json:"step"`
	Score  int         `json:"score"`
}

func getPlayerPos() [3]float32 {
	var out [3]C.float
	C.jolt_get_character_position(world, &out[0])
	return [3]float32{float32(out[0]), float32(out[1]), float32(out[2])}
}

func snapshot() state {
	s := state{Bodies: []bodyInfo{}, Player: playerState{Health: playerHealth}, Step: stepCount, Score: score}
	if world == nil {
		return s
	}

	s.Player.Pos = getPlayerPos()

	n := int(C.jolt_body_count(world))
	s.Bodies = make([]bodyInfo, 0, n)
	for i := 0; i < n; i++ {
		var info C.JoltBodyInfo
		if C.jolt_get_body_info(world, C.uint32_t(i), &info) != 1 {
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("encode: %v", err)
	}
}

func handleState(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	defer mu.Unlock()
	writeJSON(w, snapshot())
}

type stepRequest struct {
	Frames int        `json:"frames"`
	Move   [2]float32 `json:"move"` // world-space horizontal wish velocity
	Jump   bool       `json:"jump"`
}

func handleStep(w http.ResponseWriter, r *http.Request) {
	var req stepRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	frames := req.Frames
	if frames <= 0 {
		frames = 1
	}
	if frames > 240 {
		frames = 240
	}

	mu.Lock()
	defer mu.Unlock()
	for f := 0; f < frames; f++ {
		jump := req.Jump && f == 0
		var jumpFlag C.int
		if jump {
			jumpFlag = 1
		}
		C.jolt_update_character(world, C.float(req.Move[0]), C.float(req.Move[1]), jumpFlag, C.float(1.0/60.0))
		updateEnemies()
		C.jolt_step(world, C.float(1.0/60.0), 1)
		stepCount++
		processProjectileHits()
		applyEnemyDamage()
		expireProjectiles()
	}
	writeJSON(w, snapshot())
}

func updateEnemies() {
	player := getPlayerPos()
	n := int(C.jolt_body_count(world))
	for i := 0; i < n; i++ {
		var info C.JoltBodyInfo
		if C.jolt_get_body_info(world, C.uint32_t(i), &info) != 1 {
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
		const speed = 3.0
		C.jolt_set_body_velocity(world, C.uint32_t(info.id), C.float(dx/d*speed), 0, C.float(dz/d*speed))
	}
}

func processProjectileHits() {
	var hits [64]C.JoltProjectileHit
	n := int(C.jolt_poll_projectile_hits(world, &hits[0], 64))
	handled := map[uint32]bool{}
	for i := 0; i < n; i++ {
		proj := uint32(hits[i].projectile_id)
		other := uint32(hits[i].body_id)
		if handled[proj] {
			continue
		}
		handled[proj] = true

		C.jolt_remove_body(world, C.uint32_t(proj))
		delete(projectiles, proj)

		b := findBody(other)
		if b == nil || b.Projectile {
			continue
		}
		switch {
		case b.Target:
			C.jolt_remove_body(world, C.uint32_t(other))
			score++
		case b.Enemy:
			if C.jolt_damage_enemy(world, C.uint32_t(other), 1) == 1 {
				score++
				spawnEnemy()
			}
		}
	}
}

func applyEnemyDamage() {
	player := getPlayerPos()
	n := int(C.jolt_body_count(world))
	for i := 0; i < n; i++ {
		var info C.JoltBodyInfo
		if C.jolt_get_body_info(world, C.uint32_t(i), &info) != 1 {
			continue
		}
		if info.is_enemy == 0 {
			continue
		}
		dx := player[0] - float32(info.pos[0])
		dy := player[1] - float32(info.pos[1])
		dz := player[2] - float32(info.pos[2])
		if dx*dx+dy*dy+dz*dz < 1.4*1.4 {
			playerHealth -= 0.25
		}
	}
	if playerHealth <= 0 {
		playerHealth = 100
		C.jolt_set_character_position(world, 0, 0.2, 12)
	}
}

func expireProjectiles() {
	const maxLife = 180
	for id, spawnStep := range projectiles {
		if stepCount-spawnStep > maxLife {
			C.jolt_remove_body(world, C.uint32_t(id))
			delete(projectiles, id)
		}
	}
}

type shootRequest struct {
	Origin [3]float32 `json:"origin"`
	Dir    [3]float32 `json:"dir"`
}

type shootResponse struct {
	ProjectileID uint32 `json:"projectileID"`
	State        state  `json:"state"`
}

func handleShoot(w http.ResponseWriter, r *http.Request) {
	var req shootRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	dx, dy, dz := req.Dir[0], req.Dir[1], req.Dir[2]
	l := dx*dx + dy*dy + dz*dz
	if l < 1e-9 {
		http.Error(w, "zero-length direction", http.StatusBadRequest)
		return
	}
	inv := 1.0 / float32(math.Sqrt(float64(l)))
	dx, dy, dz = dx*inv, dy*inv, dz*inv

	mu.Lock()
	defer mu.Unlock()

	pid := uint32(C.jolt_fire_projectile(world,
		C.float(req.Origin[0]), C.float(req.Origin[1]), C.float(req.Origin[2]),
		C.float(dx), C.float(dy), C.float(dz), C.float(60.0)))
	if pid != 0 {
		projectiles[pid] = stepCount
	}

	writeJSON(w, shootResponse{ProjectileID: pid, State: snapshot()})
}

func findBody(id uint32) *bodyInfo {
	n := int(C.jolt_body_count(world))
	for i := 0; i < n; i++ {
		var info C.JoltBodyInfo
		if C.jolt_get_body_info(world, C.uint32_t(i), &info) != 1 {
			continue
		}
		if uint32(info.id) == id {
			b := &bodyInfo{
				ID:         uint32(info.id),
				Type:       int(info._type),
				Static:     info.is_static != 0,
				Target:     info.is_target != 0,
				Enemy:      info.is_enemy != 0,
				Projectile: info.is_projectile != 0,
			}
			return b
		}
	}
	return nil
}

type addRequest struct {
	Kind   string     `json:"kind"`
	Static bool       `json:"static"`
	Pos    [3]float32 `json:"pos"`
	Vel    [3]float32 `json:"vel"`
	Size   [3]float32 `json:"size"`
	Radius float32    `json:"radius"`
}

func handleAdd(w http.ResponseWriter, r *http.Request) {
	var req addRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	mu.Lock()
	defer mu.Unlock()

	switch req.Kind {
	case "box":
		if req.Size[0] <= 0 {
			req.Size = [3]float32{0.5, 0.5, 0.5}
		}
		if req.Static {
			C.jolt_add_static_box(world,
				C.float(req.Size[0]), C.float(req.Size[1]), C.float(req.Size[2]),
				C.float(req.Pos[0]), C.float(req.Pos[1]), C.float(req.Pos[2]))
		} else {
			C.jolt_add_dynamic_box(world,
				C.float(req.Size[0]), C.float(req.Size[1]), C.float(req.Size[2]),
				C.float(req.Pos[0]), C.float(req.Pos[1]), C.float(req.Pos[2]),
				C.float(req.Vel[0]), C.float(req.Vel[1]), C.float(req.Vel[2]))
		}
	case "sphere":
		radius := req.Radius
		if radius <= 0 {
			radius = 0.5
		}
		C.jolt_add_dynamic_sphere(world,
			C.float(req.Pos[0]), C.float(req.Pos[1]), C.float(req.Pos[2]), C.float(radius),
			C.float(req.Vel[0]), C.float(req.Vel[1]), C.float(req.Vel[2]))
	default:
		http.Error(w, "kind must be box or sphere", http.StatusBadRequest)
		return
	}
	writeJSON(w, snapshot())
}

func handleReset(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	defer mu.Unlock()

	C.jolt_destroy(world)
	world = nil
	stepCount = 0
	score = 0
	playerHealth = 100
	projectiles = map[uint32]int{}
	initScene()
	writeJSON(w, snapshot())
}

type gravityRequest struct {
	G [3]float32 `json:"g"`
}

func handleGravity(w http.ResponseWriter, r *http.Request) {
	var req gravityRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	mu.Lock()
	defer mu.Unlock()
	C.jolt_set_gravity(world, C.float(req.G[0]), C.float(req.G[1]), C.float(req.G[2]))
	writeJSON(w, snapshot())
}

func randRange(a, b float32) float32 {
	return a + rand.Float32()*(b-a)
}

func spawnEnemy() {
	player := getPlayerPos()
	for attempt := 0; attempt < 24; attempt++ {
		x := randRange(-14, 14)
		z := randRange(-14, 14)
		dx := x - player[0]
		dz := z - player[2]
		if dx*dx+dz*dz >= 10*10 {
			C.jolt_add_enemy(world, C.float(x), C.float(1.0), C.float(z), C.float(0.35), C.float(0.5))
			return
		}
	}
	C.jolt_add_enemy(world, -12, 1.0, -12, 0.35, 0.5)
}

func initScene() {
	world = C.jolt_create()
	if world == nil {
		log.Fatal("failed to create Jolt world")
	}

	// Floor.
	C.jolt_add_static_box(world, 100, 1, 100, 0, -1, 0)

	// Arena walls.
	C.jolt_add_static_box(world, 20, 4, 1, 0, 3, 20)
	C.jolt_add_static_box(world, 20, 4, 1, 0, 3, -20)
	C.jolt_add_static_box(world, 1, 4, 20, 20, 3, 0)
	C.jolt_add_static_box(world, 1, 4, 20, -20, 3, 0)

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
		C.jolt_add_dynamic_box(world, 0.5, 0.5, 0.5, C.float(c[0]), C.float(c[1]), C.float(c[2]), 0, 0, 0)
	}

	// Destroyable floating targets.
	targets := []pos{
		{-8, 2.5, 0}, {-5, 3, 7}, {3, 3.5, -8}, {8, 2.5, -6},
		{0, 4, 2}, {-3, 2, -8}, {6, 3, 6}, {-7, 3.5, 5},
	}
	for _, t := range targets {
		C.jolt_add_target_sphere(world, C.float(t[0]), C.float(t[1]), C.float(t[2]), 0.4)
	}

	// Enemies.
	for i := 0; i < 3; i++ {
		spawnEnemy()
	}
}

func main() {
	initScene()

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/state", handleState)
	mux.HandleFunc("/api/step", handleStep)
	mux.HandleFunc("/api/shoot", handleShoot)
	mux.HandleFunc("/api/add", handleAdd)
	mux.HandleFunc("/api/reset", handleReset)
	mux.HandleFunc("/api/gravity", handleGravity)
	mux.Handle("/", http.FileServer(http.FS(sub)))

	addr := ":8080"
	log.Printf("Jolt Physics FPS demo listening on http://localhost%s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

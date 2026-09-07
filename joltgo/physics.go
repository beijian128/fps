package main

// cgo 物理桥：把 C 包装层（wrapper/）的 Jolt 原生 API 实现为 sim.Physics 接口。
// 这是服务端唯一允许 cgo 的文件——游戏逻辑（sim/）只依赖 sim.Physics，
// 不接触任何 C 类型，因此可以用 fake 物理做单元测试。
//
// 包装层是纯物理桥（无任何业务概念），本文件负责两件机械性工作：
//   - 类型转换（float32 ↔ C.float、参数顺序、运动类型/质量枚举）
//   - id 翻译：Jolt 原生 BodyID 直接透传会导致「首个刚体 id ≠ 1」且可能撞上
//     ECS 的 InvalidEntity 约定，因此桥层维护双向映射，向 sim 侧发放从 1 递增
//     的实体 id，所有按 id 的操作（SetBodyVelocity / RemoveBody / Sync /
//     PollContacts）在桥内完成翻译

/*
#cgo CFLAGS: -I${SRCDIR}/wrapper
#cgo LDFLAGS: -L${SRCDIR}/build -ljolt_c
#include "jolt_c.h"
*/
import "C"

import (
	"joltgo/sim"
	"log"
	"unsafe"
)

// maxBodies 与包装层 PhysicsSystem::Init 的 maxBodies 一致，用作枚举缓冲上限。
const maxBodies = 65536

// joltPhysics 持有 C 世界的指针。所有方法只在 Simulation 持锁的上下文中
// 被调用（Simulation 的公开方法自带锁），因此自身不需要再加锁。
type joltPhysics struct {
	world  *C.JoltWorld
	next   uint32            // 向 sim 侧发放的实体 id（从 1 递增）
	joltOf map[uint32]uint32 // 实体 id -> Jolt BodyID
	goOf   map[uint32]uint32 // Jolt BodyID -> 实体 id
	idsBuf []uint32          // 刚体枚举缓冲（复用，避免每 tick 分配）
}

func newJoltPhysics() sim.Physics { return &joltPhysics{} }

func (p *joltPhysics) Create() {
	if p.world != nil {
		p.Destroy()
	}
	p.world = C.jolt_create()
	if p.world == nil {
		log.Fatal("failed to create Jolt world")
	}
	p.next = 1
	p.joltOf = make(map[uint32]uint32, maxBodies)
	p.goOf = make(map[uint32]uint32, maxBodies)
	p.idsBuf = make([]uint32, maxBodies)
}

func (p *joltPhysics) Destroy() {
	if p.world == nil {
		return
	}
	C.jolt_destroy(p.world)
	p.world = nil
	p.joltOf = nil
	p.goOf = nil
	p.idsBuf = nil
}

// bind 把 Jolt BodyID 登记为实体 id（0 = 失败，与 Jolt 的无效 id 语义一致）。
func (p *joltPhysics) bind(raw uint32) uint32 {
	if raw == 0 {
		return 0
	}
	id := p.next
	p.next++
	p.joltOf[id] = raw
	p.goOf[raw] = id
	return id
}

func (p *joltPhysics) SetGravity(x, y, z float32) {
	C.jolt_set_gravity(p.world, C.float(x), C.float(y), C.float(z))
}

func (p *joltPhysics) Step(dt float32, collisionSteps int) {
	C.jolt_step(p.world, C.float(dt), C.int(collisionSteps))
}

func (p *joltPhysics) AddBox(hx, hy, hz, x, y, z float32, motion sim.Motion) uint32 {
	raw := uint32(C.jolt_add_box(p.world, C.float(hx), C.float(hy), C.float(hz),
		C.float(x), C.float(y), C.float(z), C.int(motion)))
	return p.bind(raw)
}

func (p *joltPhysics) AddSphere(x, y, z, radius float32, motion sim.Motion) uint32 {
	raw := uint32(C.jolt_add_sphere(p.world, C.float(x), C.float(y), C.float(z), C.float(radius), C.int(motion)))
	return p.bind(raw)
}

func (p *joltPhysics) AddCapsule(x, y, z, halfHeight, radius float32, motion sim.Motion) uint32 {
	raw := uint32(C.jolt_add_capsule(p.world, C.float(x), C.float(y), C.float(z),
		C.float(halfHeight), C.float(radius), C.int(motion)))
	return p.bind(raw)
}

// AddSensorSphere 添加一个静态传感器球：不参与刚体碰撞，但角色控制器能接触到
// （Jolt 原生 sensor 机制），用于拾取物/触发器。
func (p *joltPhysics) AddSensorSphere(x, y, z, radius float32) uint32 {
	raw := uint32(C.jolt_add_sensor_sphere(p.world, C.float(x), C.float(y), C.float(z), C.float(radius)))
	return p.bind(raw)
}

func (p *joltPhysics) RemoveBody(id uint32) {
	raw, ok := p.joltOf[id]
	if !ok {
		return
	}
	C.jolt_remove_body(p.world, C.uint32_t(raw))
	delete(p.joltOf, id)
	delete(p.goOf, raw)
}

func (p *joltPhysics) SetBodyVelocity(id uint32, vx, vy, vz float32) {
	if raw, ok := p.joltOf[id]; ok {
		C.jolt_set_body_velocity(p.world, C.uint32_t(raw), C.float(vx), C.float(vy), C.float(vz))
	}
}

func (p *joltPhysics) SetBodyFriction(id uint32, friction float32) {
	if raw, ok := p.joltOf[id]; ok {
		C.jolt_set_body_friction(p.world, C.uint32_t(raw), C.float(friction))
	}
}

func (p *joltPhysics) SetBodyRestitution(id uint32, restitution float32) {
	if raw, ok := p.joltOf[id]; ok {
		C.jolt_set_body_restitution(p.world, C.uint32_t(raw), C.float(restitution))
	}
}

func (p *joltPhysics) SetBodyMotionQuality(id uint32, quality sim.MotionQuality) {
	if raw, ok := p.joltOf[id]; ok {
		C.jolt_set_body_motion_quality(p.world, C.uint32_t(raw), C.int(quality))
	}
}

// Sync 枚举物理世界全部刚体，翻译成实体 id 后回调位置/旋转/活跃状态。
func (p *joltPhysics) Sync(fn func(id uint32, active bool, pos [3]float32, quat [4]float32)) {
	n := int(C.jolt_get_body_ids(p.world, (*C.uint32_t)(unsafe.Pointer(&p.idsBuf[0])), C.uint32_t(len(p.idsBuf))))
	if n > len(p.idsBuf) {
		n = len(p.idsBuf)
	}
	for i := 0; i < n; i++ {
		raw := p.idsBuf[i]
		id, ok := p.goOf[raw]
		if !ok {
			continue
		}
		var pos [3]C.float
		var quat [4]C.float
		if C.jolt_get_body_transform(p.world, C.uint32_t(raw), &pos[0], &quat[0]) != 1 {
			continue
		}
		active := C.jolt_is_body_active(p.world, C.uint32_t(raw)) != 0
		fn(id, active,
			[3]float32{float32(pos[0]), float32(pos[1]), float32(pos[2])},
			[4]float32{float32(quat[0]), float32(quat[1]), float32(quat[2]), float32(quat[3])})
	}
}

// PollContacts 排空接触事件队列（刚体对），翻译成实体 id；不认识的对子
// （对应刚体已移除）跳过。真正的弹丸命中判定在 sim 侧完成。
func (p *joltPhysics) PollContacts() []sim.Contact {
	var buf [1024]C.JoltContactPair
	var out []sim.Contact
	for {
		n := int(C.jolt_poll_contacts(p.world, &buf[0], 1024))
		if n == 0 {
			return out
		}
		for i := 0; i < n; i++ {
			a, okA := p.goOf[uint32(buf[i].body_a)]
			b, okB := p.goOf[uint32(buf[i].body_b)]
			if okA && okB {
				out = append(out, sim.Contact{BodyA: a, BodyB: b})
			}
		}
		if n < 1024 {
			return out
		}
	}
}

func (p *joltPhysics) CreateCharacter(halfHeight, radius, offsetY, x, y, z float32) {
	if C.jolt_character_create(p.world,
		C.float(halfHeight), C.float(radius), C.float(offsetY),
		C.float(x), C.float(y), C.float(z)) != 1 {
		log.Fatal("failed to create Jolt character")
	}
}

func (p *joltPhysics) SetCharacterDynamicPush(allow bool) {
	v := 0
	if allow {
		v = 1
	}
	C.jolt_character_set_dynamic_push(p.world, C.int(v))
}

func (p *joltPhysics) CharacterPosition() [3]float32 {
	var out [3]C.float
	C.jolt_character_get_position(p.world, &out[0])
	return [3]float32{float32(out[0]), float32(out[1]), float32(out[2])}
}

func (p *joltPhysics) SetCharacterPosition(x, y, z float32) {
	C.jolt_character_set_position(p.world, C.float(x), C.float(y), C.float(z))
}

func (p *joltPhysics) CharacterVelocity() [3]float32 {
	var out [3]C.float
	C.jolt_character_get_velocity(p.world, &out[0])
	return [3]float32{float32(out[0]), float32(out[1]), float32(out[2])}
}

func (p *joltPhysics) SetCharacterVelocity(v [3]float32) {
	C.jolt_character_set_velocity(p.world, C.float(v[0]), C.float(v[1]), C.float(v[2]))
}

func (p *joltPhysics) CharacterOnGround() bool {
	return C.jolt_character_get_ground_state(p.world) == 0 // 0 = EGroundState::OnGround
}

func (p *joltPhysics) UpdateCharacter(dt float32) {
	C.jolt_character_update(p.world, C.float(dt))
}

// PollCharacterContacts 排空本 tick 角色接触到的刚体 id（同一刚体可能重复出现，
// 由调用方去重）。接触是纯物理事实，「碰到谁算伤害/拾取」由 sim 判定。
func (p *joltPhysics) PollCharacterContacts() []uint32 {
	var buf [256]C.uint32_t
	var out []uint32
	for {
		n := int(C.jolt_character_poll_contacts(p.world, &buf[0], 256))
		if n == 0 {
			return out
		}
		for i := 0; i < n; i++ {
			if id, ok := p.goOf[uint32(buf[i])]; ok {
				out = append(out, id)
			}
		}
		if n < 256 {
			return out
		}
	}
}

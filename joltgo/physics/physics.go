// Package physics 是服务端唯一允许 cgo 的包：把 C 包装层（wrapper/）的 Jolt
// 原生 API 实现为 sim.Physics 接口。游戏逻辑（sim/）只依赖 sim.Physics，不接触
// 任何 C 类型，因此可以用 fake 物理做单元测试。
//
// 包装层是纯物理桥（无任何业务概念），本包负责两件机械性工作：
//   - 类型转换（float32 ↔ C.float、参数顺序、运动类型/质量枚举）
//   - id 翻译：Jolt 原生 BodyID 直接透传会导致「首个刚体 id ≠ 1」且可能撞上
//     ECS 的 InvalidEntity 约定，因此桥层维护双向映射，向 sim 侧发放从 1 递增
//     的实体 id，所有按 id 的操作（SetBodyVelocity / RemoveBody / Sync /
//     PollContacts）在桥内完成翻译
//
// 每个 Jolt 世界对应一个 *Physics 实例，互不共享——game 服务里每个对局实例
// 各建一个独立世界（各自动态库状态隔离，见 wrapper 的 JoltWorld 结构）。
package physics

/*
#cgo CFLAGS: -I${SRCDIR}/../wrapper
#cgo LDFLAGS: -L${SRCDIR}/../build -ljolt_c
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

// Physics 持有 C 世界的指针。所有方法只在 Simulation 单线程所有者（game 实例
// goroutine）的上下文中被调用，因此自身不需要再加锁。
type Physics struct {
	world  *C.JoltWorld
	next   uint32            // 向 sim 侧发放的实体 id（从 1 递增）
	joltOf map[uint32]uint32 // 实体 id -> Jolt BodyID
	goOf   map[uint32]uint32 // Jolt BodyID -> 实体 id
	idsBuf []uint32          // 刚体枚举缓冲（复用，避免每 tick 分配）
}

// New 构造一个（尚未创建的）Jolt 物理实现。
func New() sim.Physics { return &Physics{} }

func (p *Physics) Create() {
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

func (p *Physics) Destroy() {
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
func (p *Physics) bind(raw uint32) uint32 {
	if raw == 0 {
		return 0
	}
	id := p.next
	p.next++
	p.joltOf[id] = raw
	p.goOf[raw] = id
	return id
}

func (p *Physics) SetGravity(x, y, z float32) {
	C.jolt_set_gravity(p.world, C.float(x), C.float(y), C.float(z))
}

func (p *Physics) Step(dt float32, collisionSteps int) {
	C.jolt_step(p.world, C.float(dt), C.int(collisionSteps))
}

func (p *Physics) AddBox(hx, hy, hz, x, y, z float32, motion sim.Motion) uint32 {
	raw := uint32(C.jolt_add_box(p.world, C.float(hx), C.float(hy), C.float(hz),
		C.float(x), C.float(y), C.float(z), C.int(motion)))
	return p.bind(raw)
}

func (p *Physics) AddSphere(x, y, z, radius float32, motion sim.Motion) uint32 {
	raw := uint32(C.jolt_add_sphere(p.world, C.float(x), C.float(y), C.float(z), C.float(radius), C.int(motion)))
	return p.bind(raw)
}

func (p *Physics) AddCapsule(x, y, z, halfHeight, radius float32, motion sim.Motion) uint32 {
	raw := uint32(C.jolt_add_capsule(p.world, C.float(x), C.float(y), C.float(z),
		C.float(halfHeight), C.float(radius), C.int(motion)))
	return p.bind(raw)
}

// AddSensorSphere 添加一个静态传感器球：不参与刚体碰撞，但角色控制器能接触到
// （Jolt 原生 sensor 机制），用于拾取物/触发器。
func (p *Physics) AddSensorSphere(x, y, z, radius float32) uint32 {
	raw := uint32(C.jolt_add_sensor_sphere(p.world, C.float(x), C.float(y), C.float(z), C.float(radius)))
	return p.bind(raw)
}

func (p *Physics) RemoveBody(id uint32) {
	raw, ok := p.joltOf[id]
	if !ok {
		return
	}
	C.jolt_remove_body(p.world, C.uint32_t(raw))
	delete(p.joltOf, id)
	delete(p.goOf, raw)
}

func (p *Physics) SetBodyVelocity(id uint32, vx, vy, vz float32) {
	if raw, ok := p.joltOf[id]; ok {
		C.jolt_set_body_velocity(p.world, C.uint32_t(raw), C.float(vx), C.float(vy), C.float(vz))
	}
}

func (p *Physics) SetBodyFriction(id uint32, friction float32) {
	if raw, ok := p.joltOf[id]; ok {
		C.jolt_set_body_friction(p.world, C.uint32_t(raw), C.float(friction))
	}
}

func (p *Physics) SetBodyRestitution(id uint32, restitution float32) {
	if raw, ok := p.joltOf[id]; ok {
		C.jolt_set_body_restitution(p.world, C.uint32_t(raw), C.float(restitution))
	}
}

func (p *Physics) SetBodyMotionQuality(id uint32, quality sim.MotionQuality) {
	if raw, ok := p.joltOf[id]; ok {
		C.jolt_set_body_motion_quality(p.world, C.uint32_t(raw), C.int(quality))
	}
}

// Sync 枚举物理世界全部刚体，翻译成实体 id 后回调位置/旋转/活跃状态。
func (p *Physics) Sync(fn func(id uint32, active bool, pos [3]float32, quat [4]float32)) {
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
func (p *Physics) PollContacts() []sim.Contact {
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

func (p *Physics) CreateCharacter(charIdx int, halfHeight, radius, offsetY, x, y, z float32) {
	if C.jolt_character_create(p.world, C.int(charIdx),
		C.float(halfHeight), C.float(radius), C.float(offsetY),
		C.float(x), C.float(y), C.float(z)) != 1 {
		log.Fatal("failed to create Jolt character")
	}
}

func (p *Physics) SetCharacterDynamicPush(charIdx int, allow bool) {
	v := 0
	if allow {
		v = 1
	}
	C.jolt_character_set_dynamic_push(p.world, C.int(charIdx), C.int(v))
}

func (p *Physics) CharacterPosition(charIdx int) [3]float32 {
	var out [3]C.float
	C.jolt_character_get_position(p.world, C.int(charIdx), &out[0])
	return [3]float32{float32(out[0]), float32(out[1]), float32(out[2])}
}

func (p *Physics) SetCharacterPosition(charIdx int, x, y, z float32) {
	C.jolt_character_set_position(p.world, C.int(charIdx), C.float(x), C.float(y), C.float(z))
}

func (p *Physics) CharacterVelocity(charIdx int) [3]float32 {
	var out [3]C.float
	C.jolt_character_get_velocity(p.world, C.int(charIdx), &out[0])
	return [3]float32{float32(out[0]), float32(out[1]), float32(out[2])}
}

func (p *Physics) SetCharacterVelocity(charIdx int, v [3]float32) {
	C.jolt_character_set_velocity(p.world, C.int(charIdx), C.float(v[0]), C.float(v[1]), C.float(v[2]))
}

func (p *Physics) CharacterOnGround(charIdx int) bool {
	return C.jolt_character_get_ground_state(p.world, C.int(charIdx)) == 0 // 0 = EGroundState::OnGround
}

func (p *Physics) UpdateCharacter(charIdx int, dt float32) {
	C.jolt_character_update(p.world, C.int(charIdx), C.float(dt))
}

// PollCharacterContacts 排空本 tick 指定角色接触到的刚体 id（同一刚体可能重复出现，
// 由调用方去重）。接触是纯物理事实，「碰到谁算伤害/拾取」由 sim 判定。
func (p *Physics) PollCharacterContacts(charIdx int) []uint32 {
	var buf [256]C.uint32_t
	var out []uint32
	for {
		n := int(C.jolt_character_poll_contacts(p.world, C.int(charIdx), &buf[0], 256))
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

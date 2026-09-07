// 纯物理桥：把 Jolt C++ API 封装为 extern "C" 的普通函数，供 Go 侧通过 cgo 调用。
//
// 本层不含任何游戏业务：没有敌人/弹丸/靶球/血量概念，没有角色移动策略
// （跳跃速度、重力、胶囊尺寸全部由 Go 传入），接触事件原样上报刚体对，
// 「谁是弹丸、打中谁算什么」这类判定由 Go 侧 ECS 系统负责。
// 唯一保留的内部状态是刚体 id 登记表（枚举用）与接触事件队列（线程安全转发）。

#include <Jolt/Jolt.h>

#include <Jolt/RegisterTypes.h>
#include <Jolt/Core/Factory.h>
#include <Jolt/Core/TempAllocator.h>
#include <Jolt/Core/JobSystemThreadPool.h>
#include <Jolt/Physics/PhysicsSettings.h>
#include <Jolt/Physics/PhysicsSystem.h>
#include <Jolt/Physics/Body/Body.h>
#include <Jolt/Physics/Body/BodyCreationSettings.h>
#include <Jolt/Physics/Collision/ContactListener.h>
#include <Jolt/Physics/Collision/RayCast.h>
#include <Jolt/Physics/Collision/CastResult.h>
#include <Jolt/Physics/Collision/NarrowPhaseQuery.h>
#include <Jolt/Physics/Collision/Shape/BoxShape.h>
#include <Jolt/Physics/Collision/Shape/SphereShape.h>
#include <Jolt/Physics/Collision/Shape/CapsuleShape.h>
#include <Jolt/Physics/Collision/Shape/RotatedTranslatedShape.h>
#include <Jolt/Physics/Collision/ObjectLayerPairFilterTable.h>
#include <Jolt/Physics/Collision/BroadPhase/BroadPhaseLayerInterfaceTable.h>
#include <Jolt/Physics/Collision/BroadPhase/ObjectVsBroadPhaseLayerFilterTable.h>
#include <Jolt/Physics/Character/CharacterVirtual.h>

#include <algorithm>
#include <mutex>
#include <thread>
#include <vector>

#include "jolt_c.h"

using namespace JPH;
using namespace JPH::literals;

namespace
{
	enum : ObjectLayer
	{
		LAYER_NON_MOVING = 0,
		LAYER_MOVING = 1,
		NUM_OBJECT_LAYERS = 2
	};

	constexpr uint NUM_BROAD_PHASE_LAYERS = 2;

	// 接触监听器把「所有」刚体接触对原样记录到线程安全队列，
	// 由 Go 侧每 tick 排空并自行判定弹丸命中。
	class ContactRecorder : public ContactListener
	{
	public:
		virtual void OnContactAdded(const Body &inBody1, const Body &inBody2, const ContactManifold &inManifold, ContactSettings &ioSettings) override
		{
			ContactPair pair;
			pair.a = inBody1.GetID().GetIndexAndSequenceNumber();
			pair.b = inBody2.GetID().GetIndexAndSequenceNumber();

			std::lock_guard<std::mutex> lock(mtx);
			pairs.push_back(pair);
		}

		struct ContactPair
		{
			uint32_t a;
			uint32_t b;
		};

		std::mutex mtx;
		std::vector<ContactPair> pairs;
	};

	// 角色接触监听器：做两件纯物理层的事——
	//   1. 「动态刚体能否推动角色」开关（对应 Jolt mCanPushCharacter），由 Go 配置；
	//   2. 把角色接触到的刚体 id 记录进队列，由 Go 每 tick 轮询。
	// 「碰到谁算伤害/拾取」等业务判定全部在 Go 侧。
	// 注意：Jolt 对同一接触只回调一次 Added（传感器接触也只走 Added），
	// 持续接触的每步信号走 OnContactSolve（约束求解每步触发），两者都记录。
	class CharacterContactBridge : public CharacterContactListener
	{
	public:
		bool dynamic_can_push = true;
		std::vector<uint32_t> touches;

		void Apply(CharacterContactSettings &ioSettings, const CharacterContact &inContact)
		{
			if (inContact.mMotionTypeB == EMotionType::Dynamic)
				ioSettings.mCanPushCharacter = dynamic_can_push;
		}

		void Record(const CharacterContact &inContact)
		{
			touches.push_back(inContact.mBodyB.GetIndexAndSequenceNumber());
		}

		virtual void OnContactAdded(const CharacterVirtual *inCharacter, const CharacterContact &inContact, CharacterContactSettings &ioSettings) override
		{
			Apply(ioSettings, inContact);
			Record(inContact);
		}

		virtual void OnContactPersisted(const CharacterVirtual *inCharacter, const CharacterContact &inContact, CharacterContactSettings &ioSettings) override
		{
			Apply(ioSettings, inContact);
		}

		virtual void OnContactSolve(const CharacterVirtual *inCharacter, const BodyID &inBodyID2, const SubShapeID &inSubShapeID2, RVec3Arg inContactPosition, Vec3Arg inContactNormal, Vec3Arg inContactVelocity, const PhysicsMaterial *inContactMaterial, Vec3Arg inCharacterVelocity, Vec3 &ioNewCharacterVelocity) override
		{
			touches.push_back(inBodyID2.GetIndexAndSequenceNumber());
		}
	};
}

struct JoltWorld
{
	TempAllocatorImpl *temp_allocator = nullptr;
	JobSystemThreadPool *job_system = nullptr;
	ObjectLayerPairFilterTable *object_pair_filter = nullptr;
	BroadPhaseLayerInterfaceTable *bp_layer_interface = nullptr;
	ObjectVsBroadPhaseLayerFilterTable *object_vs_bp_filter = nullptr;
	PhysicsSystem *physics_system = nullptr;
	CharacterVirtual *character = nullptr;
	ContactRecorder contact_listener;
	CharacterContactBridge character_listener;
	std::vector<BodyID> body_ids; // 刚体 id 登记表（仅身份，不含任何元数据）
};

static void EnsureJoltInitialized()
{
	static bool initialized = false;
	if (initialized)
		return;

	RegisterDefaultAllocator();
	Factory::sInstance = new Factory();
	RegisterTypes();
	initialized = true;
}

extern "C" JoltWorld *jolt_create(void)
{
	EnsureJoltInitialized();

	JoltWorld *w = new JoltWorld();
	if (w == nullptr)
		return nullptr;

	w->temp_allocator = new TempAllocatorImpl(10 * 1024 * 1024);

	const uint num_threads = std::max(2u, std::thread::hardware_concurrency()) - 1;
	w->job_system = new JobSystemThreadPool(cMaxPhysicsJobs, cMaxPhysicsBarriers, num_threads);

	w->object_pair_filter = new ObjectLayerPairFilterTable(NUM_OBJECT_LAYERS);
	w->object_pair_filter->EnableCollision(LAYER_NON_MOVING, LAYER_MOVING);
	w->object_pair_filter->EnableCollision(LAYER_MOVING, LAYER_MOVING);

	w->bp_layer_interface = new BroadPhaseLayerInterfaceTable(NUM_OBJECT_LAYERS, NUM_BROAD_PHASE_LAYERS);
	w->bp_layer_interface->MapObjectToBroadPhaseLayer(LAYER_NON_MOVING, BroadPhaseLayer(0));
	w->bp_layer_interface->MapObjectToBroadPhaseLayer(LAYER_MOVING, BroadPhaseLayer(1));

	w->object_vs_bp_filter = new ObjectVsBroadPhaseLayerFilterTable(
		*w->bp_layer_interface, NUM_BROAD_PHASE_LAYERS, *w->object_pair_filter, NUM_OBJECT_LAYERS);

	w->physics_system = new PhysicsSystem();
	w->physics_system->Init(
		65536, 0, 65536, 10240,
		*w->bp_layer_interface, *w->object_vs_bp_filter, *w->object_pair_filter);
	w->physics_system->SetContactListener(&w->contact_listener);

	return w;
}

extern "C" void jolt_destroy(JoltWorld *w)
{
	if (w == nullptr)
		return;

	delete w->character;
	delete w->physics_system;
	delete w->object_vs_bp_filter;
	delete w->bp_layer_interface;
	delete w->object_pair_filter;
	delete w->job_system;
	delete w->temp_allocator;
	delete w;
}

extern "C" void jolt_step(JoltWorld *w, float dt, int collision_steps)
{
	if (w != nullptr && w->physics_system != nullptr)
		w->physics_system->Update(dt, collision_steps, w->temp_allocator, w->job_system);
}

extern "C" void jolt_set_gravity(JoltWorld *w, float gx, float gy, float gz)
{
	if (w != nullptr && w->physics_system != nullptr)
		w->physics_system->SetGravity(Vec3(gx, gy, gz));
}

// ---- 刚体 ----

static ObjectLayer LayerForMotionType(EMotionType inMotionType)
{
	return inMotionType == EMotionType::Static ? LAYER_NON_MOVING : LAYER_MOVING;
}

// 把新刚体加入世界（静态体不激活）并登记 id。
static BodyID AddBodyToWorld(JoltWorld *w, const BodyCreationSettings &inSettings)
{
	BodyInterface &body_interface = w->physics_system->GetBodyInterface();

	BodyID body_id;
	if (inSettings.mMotionType == EMotionType::Static)
	{
		Body *body = body_interface.CreateBody(inSettings);
		if (body == nullptr)
			return BodyID();
		body_id = body->GetID();
		body_interface.AddBody(body_id, EActivation::DontActivate);
	}
	else
	{
		body_id = body_interface.CreateAndAddBody(inSettings, EActivation::Activate);
	}

	if (!body_id.IsInvalid())
		w->body_ids.push_back(body_id);
	return body_id;
}

extern "C" uint32_t jolt_add_box(JoltWorld *w, float hx, float hy, float hz, float x, float y, float z, int motion_type)
{
	if (w == nullptr || w->physics_system == nullptr)
		return 0;

	BoxShapeSettings shape_settings(Vec3(hx, hy, hz));
	shape_settings.SetEmbedded();
	ShapeRefC shape = shape_settings.Create().Get();

	EMotionType mt = static_cast<EMotionType>(motion_type);
	BodyCreationSettings body_settings(shape, RVec3(x, y, z), Quat::sIdentity(), mt, LayerForMotionType(mt));
	return AddBodyToWorld(w, body_settings).GetIndexAndSequenceNumber();
}

extern "C" uint32_t jolt_add_sphere(JoltWorld *w, float x, float y, float z, float radius, int motion_type)
{
	if (w == nullptr || w->physics_system == nullptr)
		return 0;

	EMotionType mt = static_cast<EMotionType>(motion_type);
	BodyCreationSettings body_settings(new SphereShape(radius), RVec3(x, y, z), Quat::sIdentity(), mt, LayerForMotionType(mt));
	return AddBodyToWorld(w, body_settings).GetIndexAndSequenceNumber();
}

extern "C" uint32_t jolt_add_capsule(JoltWorld *w, float x, float y, float z, float half_height, float radius, int motion_type)
{
	if (w == nullptr || w->physics_system == nullptr)
		return 0;

	EMotionType mt = static_cast<EMotionType>(motion_type);
	BodyCreationSettings body_settings(new CapsuleShape(half_height, radius), RVec3(x, y, z), Quat::sIdentity(), mt, LayerForMotionType(mt));
	return AddBodyToWorld(w, body_settings).GetIndexAndSequenceNumber();
}

extern "C" uint32_t jolt_add_sensor_sphere(JoltWorld *w, float x, float y, float z, float radius)
{
	if (w == nullptr || w->physics_system == nullptr)
		return 0;

	// Jolt 原生 sensor：不参与刚体碰撞响应，但角色控制器的碰撞查询会检测到它
	// （以 mIsSensorB 上报），是「触发器/拾取物」的标准实现方式。
	BodyCreationSettings body_settings(new SphereShape(radius), RVec3(x, y, z), Quat::sIdentity(), EMotionType::Static, LAYER_NON_MOVING);
	body_settings.mIsSensor = true;
	return AddBodyToWorld(w, body_settings).GetIndexAndSequenceNumber();
}

extern "C" void jolt_remove_body(JoltWorld *w, uint32_t body_id)
{
	if (w == nullptr || w->physics_system == nullptr)
		return;

	BodyID id(body_id);
	BodyInterface &body_interface = w->physics_system->GetBodyInterface();
	body_interface.RemoveBody(id);
	body_interface.DestroyBody(id);

	for (size_t i = 0; i < w->body_ids.size(); ++i)
	{
		if (w->body_ids[i] == id)
		{
			w->body_ids.erase(w->body_ids.begin() + i);
			return;
		}
	}
}

extern "C" uint32_t jolt_get_body_ids(JoltWorld *w, uint32_t *out_ids, uint32_t max_ids)
{
	if (w == nullptr || out_ids == nullptr)
		return 0;

	uint32_t n = std::min<uint32_t>((uint32_t)w->body_ids.size(), max_ids);
	for (uint32_t i = 0; i < n; ++i)
		out_ids[i] = w->body_ids[i].GetIndexAndSequenceNumber();
	return n;
}

extern "C" int jolt_get_body_transform(JoltWorld *w, uint32_t body_id, float *out_pos, float *out_quat)
{
	if (w == nullptr || w->physics_system == nullptr || out_pos == nullptr || out_quat == nullptr)
		return 0;

	BodyInterface &body_interface = w->physics_system->GetBodyInterface();
	BodyID id(body_id);

	RVec3 p = body_interface.GetCenterOfMassPosition(id);
	Quat q = body_interface.GetRotation(id);
	out_pos[0] = p.GetX();
	out_pos[1] = p.GetY();
	out_pos[2] = p.GetZ();
	out_quat[0] = q.GetX();
	out_quat[1] = q.GetY();
	out_quat[2] = q.GetZ();
	out_quat[3] = q.GetW();
	return 1;
}

extern "C" int jolt_is_body_active(JoltWorld *w, uint32_t body_id)
{
	if (w == nullptr || w->physics_system == nullptr)
		return 0;
	return w->physics_system->GetBodyInterface().IsActive(BodyID(body_id)) ? 1 : 0;
}

extern "C" void jolt_set_body_velocity(JoltWorld *w, uint32_t body_id, float vx, float vy, float vz)
{
	if (w == nullptr || w->physics_system == nullptr)
		return;
	w->physics_system->GetBodyInterface().SetLinearVelocity(BodyID(body_id), Vec3(vx, vy, vz));
}

extern "C" void jolt_set_body_friction(JoltWorld *w, uint32_t body_id, float friction)
{
	if (w == nullptr || w->physics_system == nullptr)
		return;
	w->physics_system->GetBodyInterface().SetFriction(BodyID(body_id), friction);
}

extern "C" void jolt_set_body_restitution(JoltWorld *w, uint32_t body_id, float restitution)
{
	if (w == nullptr || w->physics_system == nullptr)
		return;
	w->physics_system->GetBodyInterface().SetRestitution(BodyID(body_id), restitution);
}

extern "C" void jolt_set_body_motion_quality(JoltWorld *w, uint32_t body_id, int quality)
{
	if (w == nullptr || w->physics_system == nullptr)
		return;
	w->physics_system->GetBodyInterface().SetMotionQuality(BodyID(body_id), static_cast<EMotionQuality>(quality));
}

extern "C" void jolt_apply_impulse(JoltWorld *w, uint32_t body_id, float ix, float iy, float iz)
{
	if (w == nullptr || w->physics_system == nullptr)
		return;
	w->physics_system->GetBodyInterface().AddImpulse(BodyID(body_id), Vec3(ix, iy, iz));
}

// ---- 接触事件 ----

extern "C" uint32_t jolt_poll_contacts(JoltWorld *w, JoltContactPair *out, uint32_t max_count)
{
	if (w == nullptr || out == nullptr)
		return 0;

	std::vector<ContactRecorder::ContactPair> local;
	{
		std::lock_guard<std::mutex> lock(w->contact_listener.mtx);
		local.swap(w->contact_listener.pairs);
	}

	uint32_t n = std::min<uint32_t>((uint32_t)local.size(), max_count);
	for (uint32_t i = 0; i < n; ++i)
	{
		out[i].body_a = local[i].a;
		out[i].body_b = local[i].b;
	}
	return n;
}

// ---- 射线 ----

extern "C" int jolt_ray_cast(JoltWorld *w, const float *origin, const float *dir, float max_dist, JoltRayResult *out)
{
	if (w == nullptr || w->physics_system == nullptr || origin == nullptr || dir == nullptr || out == nullptr)
		return 0;

	Vec3 direction(dir[0], dir[1], dir[2]);
	if (direction.LengthSq() < 1.0e-12f)
		return 0;
	direction = direction.Normalized() * max_dist;

	RRayCast ray(RVec3(origin[0], origin[1], origin[2]), direction);
	RayCastResult hit;
	if (!w->physics_system->GetNarrowPhaseQuery().CastRay(ray, hit))
	{
		out->hit = 0;
		out->body_id = 0;
		out->distance = max_dist;
		return 1;
	}

	out->hit = 1;
	out->distance = hit.mFraction * max_dist;
	RVec3 point = ray.GetPointOnRay(hit.mFraction);
	out->point[0] = point.GetX();
	out->point[1] = point.GetY();
	out->point[2] = point.GetZ();
	out->body_id = hit.mBodyID.GetIndexAndSequenceNumber();
	return 1;
}

// ---- 角色控制器 ----

extern "C" int jolt_character_create(JoltWorld *w, float half_height, float radius, float offset_y, float x, float y, float z)
{
	if (w == nullptr || w->physics_system == nullptr || w->character != nullptr)
		return 0;

	// 胶囊向上平移 offset_y，使形状底部位于角色位置（脚底）。
	Ref<Shape> shape = RotatedTranslatedShapeSettings(
		Vec3(0.0f, offset_y, 0.0f), Quat::sIdentity(), new CapsuleShape(half_height, radius)).Create().Get();

	CharacterVirtualSettings settings;
	settings.mShape = shape;

	w->character = new CharacterVirtual(&settings, RVec3(x, y, z), Quat::sIdentity(), w->physics_system);
	w->character->SetListener(&w->character_listener);
	return 1;
}

extern "C" void jolt_character_set_dynamic_push(JoltWorld *w, int allow)
{
	if (w == nullptr)
		return;
	w->character_listener.dynamic_can_push = (allow != 0);
}

extern "C" void jolt_character_get_position(JoltWorld *w, float *out_xyz)
{
	if (w == nullptr || w->character == nullptr || out_xyz == nullptr)
		return;

	RVec3 p = w->character->GetPosition();
	out_xyz[0] = p.GetX();
	out_xyz[1] = p.GetY();
	out_xyz[2] = p.GetZ();
}

extern "C" void jolt_character_set_position(JoltWorld *w, float x, float y, float z)
{
	if (w == nullptr || w->character == nullptr)
		return;
	w->character->SetPosition(RVec3(x, y, z));
}

extern "C" void jolt_character_get_velocity(JoltWorld *w, float *out_xyz)
{
	if (w == nullptr || w->character == nullptr || out_xyz == nullptr)
		return;

	Vec3 v = w->character->GetLinearVelocity();
	out_xyz[0] = v.GetX();
	out_xyz[1] = v.GetY();
	out_xyz[2] = v.GetZ();
}

extern "C" void jolt_character_set_velocity(JoltWorld *w, float vx, float vy, float vz)
{
	if (w == nullptr || w->character == nullptr)
		return;
	w->character->SetLinearVelocity(Vec3(vx, vy, vz));
}

extern "C" int jolt_character_get_ground_state(JoltWorld *w)
{
	if (w == nullptr || w->character == nullptr)
		return 3; // InAir
	return (int)w->character->GetGroundState();
}

extern "C" void jolt_character_update(JoltWorld *w, float dt)
{
	if (w == nullptr || w->character == nullptr)
		return;

	CharacterVirtual::ExtendedUpdateSettings settings;
	w->character->ExtendedUpdate(dt, w->physics_system->GetGravity(), settings,
		BroadPhaseLayerFilter(), ObjectLayerFilter(), BodyFilter(), ShapeFilter(), *w->temp_allocator);
}

extern "C" uint32_t jolt_character_poll_contacts(JoltWorld *w, uint32_t *out_ids, uint32_t max_ids)
{
	if (w == nullptr || out_ids == nullptr)
		return 0;

	// 角色更新在单线程（Go tick goroutine）内完成，无需加锁；swap 保持空队列。
	std::vector<uint32_t> local;
	local.swap(w->character_listener.touches);

	uint32_t n = std::min<uint32_t>((uint32_t)local.size(), max_ids);
	for (uint32_t i = 0; i < n; ++i)
		out_ids[i] = local[i];
	return n;
}

// C wrapper around the Jolt Physics C++ API for consumption from Go via cgo.

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

	constexpr uint64 PROJECTILE_USER_DATA = 0x0BADBEEF;
}

struct BodyRecord
{
	uint32_t id;
	int type;       // 0 = box, 1 = sphere, 2 = enemy capsule
	int is_static;
	int is_target;
	int is_enemy;
	int is_projectile;
	float health;
	Vec3 size;
	BodyID body_id;
};

struct HitPair
{
	BodyID projectile;
	BodyID other;
};

class ProjectileContactListener : public ContactListener
{
public:
	virtual void OnContactAdded(const Body &inBody1, const Body &inBody2, const ContactManifold &inManifold, ContactSettings &ioSettings) override
	{
		const uint64 u1 = inBody1.GetUserData();
		const uint64 u2 = inBody2.GetUserData();
		if (u1 != PROJECTILE_USER_DATA && u2 != PROJECTILE_USER_DATA)
			return;

		HitPair pair;
		pair.projectile = (u1 == PROJECTILE_USER_DATA) ? inBody1.GetID() : inBody2.GetID();
		pair.other = (u1 == PROJECTILE_USER_DATA) ? inBody2.GetID() : inBody1.GetID();

		std::lock_guard<std::mutex> lock(mtx);
		hits.push_back(pair);
	}

	std::mutex mtx;
	std::vector<HitPair> hits;
};

struct JoltWorld
{
	TempAllocatorImpl *temp_allocator = nullptr;
	JobSystemThreadPool *job_system = nullptr;
	ObjectLayerPairFilterTable *object_pair_filter = nullptr;
	BroadPhaseLayerInterfaceTable *bp_layer_interface = nullptr;
	ObjectVsBroadPhaseLayerFilterTable *object_vs_bp_filter = nullptr;
	PhysicsSystem *physics_system = nullptr;
	CharacterVirtual *character = nullptr;
	ProjectileContactListener contact_listener;
	std::vector<BodyRecord> bodies;
	uint32_t next_id = 1;
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

static uint32_t AddBody(JoltWorld *w, BodyID inBodyID, int inType, int inIsStatic, int inIsTarget, int inIsEnemy, int inIsProjectile, float inHealth, Vec3Arg inSize)
{
	uint32_t id = w->next_id++;

	BodyRecord rec;
	rec.id = id;
	rec.type = inType;
	rec.is_static = inIsStatic;
	rec.is_target = inIsTarget;
	rec.is_enemy = inIsEnemy;
	rec.is_projectile = inIsProjectile;
	rec.health = inHealth;
	rec.size = inSize;
	rec.body_id = inBodyID;
	w->bodies.push_back(rec);

	return id;
}

static uint32_t FindId(const JoltWorld *w, BodyID inBodyID)
{
	for (const BodyRecord &rec : w->bodies)
		if (rec.body_id == inBodyID)
			return rec.id;
	return 0;
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

	// Player capsule: total height 1.8 m (half height 0.5 + 2 * radius 0.4),
	// shifted up so the bottom of the shape sits at the character's position (feet).
	Ref<Shape> standing_shape = RotatedTranslatedShapeSettings(
		Vec3(0.0f, 0.9f, 0.0f), Quat::sIdentity(), new CapsuleShape(0.5f, 0.4f)).Create().Get();

	CharacterVirtualSettings character_settings;
	character_settings.mShape = standing_shape;
	character_settings.mMaxSlopeAngle = DegreesToRadians(50.0f);
	w->character = new CharacterVirtual(&character_settings, RVec3(0.0f, 0.2f, 12.0f), Quat::sIdentity(), w->physics_system);

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

extern "C" uint32_t jolt_add_static_box(JoltWorld *w, float hx, float hy, float hz, float x, float y, float z)
{
	if (w == nullptr || w->physics_system == nullptr)
		return 0;

	BoxShapeSettings shape_settings(Vec3(hx, hy, hz));
	shape_settings.SetEmbedded();
	ShapeRefC shape = shape_settings.Create().Get();

	BodyCreationSettings body_settings(shape, RVec3(x, y, z), Quat::sIdentity(), EMotionType::Static, LAYER_NON_MOVING);
	BodyInterface &body_interface = w->physics_system->GetBodyInterface();
	Body *body = body_interface.CreateBody(body_settings);
	if (body == nullptr)
		return 0;

	body_interface.AddBody(body->GetID(), EActivation::DontActivate);
	return AddBody(w, body->GetID(), 0, 1, 0, 0, 0, 0.0f, Vec3(hx, hy, hz));
}

extern "C" uint32_t jolt_add_dynamic_box(JoltWorld *w, float hx, float hy, float hz, float x, float y, float z, float vx, float vy, float vz)
{
	if (w == nullptr || w->physics_system == nullptr)
		return 0;

	BoxShapeSettings shape_settings(Vec3(hx, hy, hz));
	shape_settings.SetEmbedded();
	ShapeRefC shape = shape_settings.Create().Get();

	BodyCreationSettings body_settings(shape, RVec3(x, y, z), Quat::sIdentity(), EMotionType::Dynamic, LAYER_MOVING);
	BodyInterface &body_interface = w->physics_system->GetBodyInterface();
	BodyID body_id = body_interface.CreateAndAddBody(body_settings, EActivation::Activate);
	body_interface.SetLinearVelocity(body_id, Vec3(vx, vy, vz));

	return AddBody(w, body_id, 0, 0, 0, 0, 0, 0.0f, Vec3(hx, hy, hz));
}

extern "C" uint32_t jolt_add_dynamic_sphere(JoltWorld *w, float x, float y, float z, float radius, float vx, float vy, float vz)
{
	if (w == nullptr || w->physics_system == nullptr)
		return 0;

	BodyCreationSettings body_settings(new SphereShape(radius), RVec3(x, y, z), Quat::sIdentity(), EMotionType::Dynamic, LAYER_MOVING);
	BodyInterface &body_interface = w->physics_system->GetBodyInterface();
	BodyID body_id = body_interface.CreateAndAddBody(body_settings, EActivation::Activate);
	body_interface.SetLinearVelocity(body_id, Vec3(vx, vy, vz));

	return AddBody(w, body_id, 1, 0, 0, 0, 0, 0.0f, Vec3(radius, 0.0f, 0.0f));
}

extern "C" uint32_t jolt_add_target_sphere(JoltWorld *w, float x, float y, float z, float radius)
{
	if (w == nullptr || w->physics_system == nullptr)
		return 0;

	BodyCreationSettings body_settings(new SphereShape(radius), RVec3(x, y, z), Quat::sIdentity(), EMotionType::Static, LAYER_NON_MOVING);
	BodyInterface &body_interface = w->physics_system->GetBodyInterface();
	Body *body = body_interface.CreateBody(body_settings);
	if (body == nullptr)
		return 0;

	body_interface.AddBody(body->GetID(), EActivation::DontActivate);
	return AddBody(w, body->GetID(), 1, 1, 1, 0, 0, 0.0f, Vec3(radius, 0.0f, 0.0f));
}

extern "C" uint32_t jolt_add_enemy(JoltWorld *w, float x, float y, float z, float radius, float half_height)
{
	if (w == nullptr || w->physics_system == nullptr)
		return 0;

	BodyCreationSettings body_settings(new CapsuleShape(half_height, radius), RVec3(x, y, z), Quat::sIdentity(), EMotionType::Dynamic, LAYER_MOVING);
	BodyInterface &body_interface = w->physics_system->GetBodyInterface();
	BodyID body_id = body_interface.CreateAndAddBody(body_settings, EActivation::Activate);

	return AddBody(w, body_id, 2, 0, 0, 1, 0, 3.0f, Vec3(radius, half_height, 0.0f));
}

extern "C" uint32_t jolt_fire_projectile(JoltWorld *w, float ox, float oy, float oz, float dx, float dy, float dz, float speed)
{
	if (w == nullptr || w->physics_system == nullptr)
		return 0;

	BodyCreationSettings body_settings(new SphereShape(0.08f), RVec3(ox, oy, oz), Quat::sIdentity(), EMotionType::Dynamic, LAYER_MOVING);
	body_settings.mMotionQuality = EMotionQuality::LinearCast;
	body_settings.mFriction = 0.0f;
	body_settings.mRestitution = 0.0f;

	BodyInterface &body_interface = w->physics_system->GetBodyInterface();
	BodyID body_id = body_interface.CreateAndAddBody(body_settings, EActivation::Activate);
	body_interface.SetLinearVelocity(body_id, Vec3(dx, dy, dz) * speed);
	body_interface.SetUserData(body_id, PROJECTILE_USER_DATA);

	return AddBody(w, body_id, 1, 0, 0, 0, 1, 0.0f, Vec3(0.08f, 0.0f, 0.0f));
}

extern "C" uint32_t jolt_body_count(JoltWorld *w)
{
	if (w == nullptr)
		return 0;
	return (uint32_t)w->bodies.size();
}

extern "C" int jolt_get_body_info(JoltWorld *w, uint32_t index, JoltBodyInfo *out)
{
	if (w == nullptr || w->physics_system == nullptr || out == nullptr)
		return 0;
	if (index >= w->bodies.size())
		return 0;

	const BodyRecord &rec = w->bodies[index];
	BodyInterface &body_interface = w->physics_system->GetBodyInterface();

	out->id = rec.id;
	out->type = rec.type;
	out->is_static = rec.is_static;
	out->is_target = rec.is_target;
	out->is_enemy = rec.is_enemy;
	out->is_projectile = rec.is_projectile;
	out->health = rec.health;
	out->active = body_interface.IsActive(rec.body_id) ? 1 : 0;

	RVec3 p = body_interface.GetCenterOfMassPosition(rec.body_id);
	Quat q = body_interface.GetRotation(rec.body_id);
	out->pos[0] = p.GetX();
	out->pos[1] = p.GetY();
	out->pos[2] = p.GetZ();
	out->quat[0] = q.GetX();
	out->quat[1] = q.GetY();
	out->quat[2] = q.GetZ();
	out->quat[3] = q.GetW();
	out->size[0] = rec.size.GetX();
	out->size[1] = rec.size.GetY();
	out->size[2] = rec.size.GetZ();

	return 1;
}

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
	out->body_id = FindId(w, hit.mBodyID);
	return 1;
}

extern "C" void jolt_apply_impulse(JoltWorld *w, uint32_t body_id, float ix, float iy, float iz)
{
	if (w == nullptr || w->physics_system == nullptr)
		return;

	for (const BodyRecord &rec : w->bodies)
	{
		if (rec.id == body_id)
		{
			w->physics_system->GetBodyInterface().AddImpulse(rec.body_id, Vec3(ix, iy, iz));
			return;
		}
	}
}

extern "C" void jolt_remove_body(JoltWorld *w, uint32_t body_id)
{
	if (w == nullptr || w->physics_system == nullptr)
		return;

	for (size_t i = 0; i < w->bodies.size(); ++i)
	{
		if (w->bodies[i].id == body_id)
		{
			BodyInterface &body_interface = w->physics_system->GetBodyInterface();
			body_interface.RemoveBody(w->bodies[i].body_id);
			body_interface.DestroyBody(w->bodies[i].body_id);
			w->bodies.erase(w->bodies.begin() + i);
			return;
		}
	}
}

extern "C" void jolt_set_body_velocity(JoltWorld *w, uint32_t body_id, float vx, float vy, float vz)
{
	if (w == nullptr || w->physics_system == nullptr)
		return;

	for (const BodyRecord &rec : w->bodies)
	{
		if (rec.id == body_id)
		{
			w->physics_system->GetBodyInterface().SetLinearVelocity(rec.body_id, Vec3(vx, vy, vz));
			return;
		}
	}
}

extern "C" int jolt_damage_enemy(JoltWorld *w, uint32_t body_id, float amount)
{
	if (w == nullptr || w->physics_system == nullptr)
		return 0;

	for (size_t i = 0; i < w->bodies.size(); ++i)
	{
		BodyRecord &rec = w->bodies[i];
		if (rec.id == body_id && rec.is_enemy)
		{
			rec.health -= amount;
			if (rec.health <= 0.0f)
			{
				BodyInterface &body_interface = w->physics_system->GetBodyInterface();
				body_interface.RemoveBody(rec.body_id);
				body_interface.DestroyBody(rec.body_id);
				w->bodies.erase(w->bodies.begin() + i);
				return 1;
			}
			return 0;
		}
	}
	return 0;
}

extern "C" uint32_t jolt_poll_projectile_hits(JoltWorld *w, JoltProjectileHit *out, uint32_t max_count)
{
	if (w == nullptr || out == nullptr)
		return 0;

	std::vector<HitPair> local;
	{
		std::lock_guard<std::mutex> lock(w->contact_listener.mtx);
		local.swap(w->contact_listener.hits);
	}

	uint32_t n = 0;
	for (const HitPair &pair : local)
	{
		if (n >= max_count)
			break;

		uint32_t projectile_id = FindId(w, pair.projectile);
		uint32_t other_id = FindId(w, pair.other);
		if (projectile_id == 0 || other_id == 0)
			continue;

		out[n].projectile_id = projectile_id;
		out[n].body_id = other_id;
		++n;
	}
	return n;
}

extern "C" void jolt_update_character(JoltWorld *w, float wish_x, float wish_z, int jump, float dt)
{
	if (w == nullptr || w->character == nullptr)
		return;

	CharacterVirtual *c = w->character;
	Vec3 velocity = c->GetLinearVelocity();
	float vy = velocity.GetY();

	if (c->GetGroundState() == CharacterVirtual::EGroundState::OnGround)
	{
		vy = 0.0f;
		if (jump)
			vy = 8.0f;
	}

	vy += w->physics_system->GetGravity().GetY() * dt;
	c->SetLinearVelocity(Vec3(wish_x, vy, wish_z));

	CharacterVirtual::ExtendedUpdateSettings settings;
	c->ExtendedUpdate(dt, w->physics_system->GetGravity(), settings,
		BroadPhaseLayerFilter(), ObjectLayerFilter(), BodyFilter(), ShapeFilter(), *w->temp_allocator);
}

extern "C" void jolt_get_character_position(JoltWorld *w, float *out_xyz)
{
	if (w == nullptr || w->character == nullptr || out_xyz == nullptr)
		return;

	RVec3 p = w->character->GetPosition();
	out_xyz[0] = p.GetX();
	out_xyz[1] = p.GetY();
	out_xyz[2] = p.GetZ();
}

extern "C" void jolt_set_character_position(JoltWorld *w, float x, float y, float z)
{
	if (w == nullptr || w->character == nullptr)
		return;
	w->character->SetPosition(RVec3(x, y, z));
}

extern "C" void jolt_set_gravity(JoltWorld *w, float gx, float gy, float gz)
{
	if (w != nullptr && w->physics_system != nullptr)
		w->physics_system->SetGravity(Vec3(gx, gy, gz));
}

extern "C" void jolt_step(JoltWorld *w, float dt, int collision_steps)
{
	if (w != nullptr && w->physics_system != nullptr)
		w->physics_system->Update(dt, collision_steps, w->temp_allocator, w->job_system);
}

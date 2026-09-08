#ifndef JOLT_C_H
#define JOLT_C_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* 纯物理桥：只把 Jolt 原生物理能力透传给 Go 侧，不携带任何游戏业务概念。
 *
 * 敌人/弹丸/靶球/血量/波次/角色移动策略（跳跃速度、重力、胶囊尺寸）等全部
 * 由 Go 侧 ECS 决定；本层只关心「世界、刚体、角色控制器、接触事件」这些
 * 物理事实。刚体用 Jolt 原生 BodyID 标识（uint32 值；Jolt 保证合法 BodyID
 * 非 0，因此 0 统一表示失败）。
 */

/* Opaque handle to a Jolt physics world. */
typedef struct JoltWorld JoltWorld;

/* 刚体运动类型，取值与 JPH::EMotionType 一致。 */
#define JOLT_MOTION_STATIC    0
#define JOLT_MOTION_KINEMATIC 1
#define JOLT_MOTION_DYNAMIC   2

/* 高速运动碰撞检测质量，取值与 JPH::EMotionQuality 一致。 */
#define JOLT_QUALITY_DISCRETE     0
#define JOLT_QUALITY_LINEAR_CAST  1

/* 一对刚体的接触事件（双方都是 Jolt BodyID 的 uint32 值）。 */
typedef struct JoltContactPair {
	uint32_t body_a;
	uint32_t body_b;
} JoltContactPair;

/* Result of a ray cast (body_id 是 Jolt BodyID 的 uint32 值)。 */
typedef struct JoltRayResult {
	int hit;
	uint32_t body_id;
	float point[3];
	float distance;
} JoltRayResult;

/* ---- 世界 ---- */

JoltWorld *jolt_create(void);
void jolt_destroy(JoltWorld *w);
void jolt_step(JoltWorld *w, float dt, int collision_steps);
void jolt_set_gravity(JoltWorld *w, float gx, float gy, float gz);

/* ---- 刚体：返回 Jolt BodyID 的 uint32 值（0 = 失败） ---- */

uint32_t jolt_add_box(JoltWorld *w, float hx, float hy, float hz, float x, float y, float z, int motion_type);
uint32_t jolt_add_sphere(JoltWorld *w, float x, float y, float z, float radius, int motion_type);
uint32_t jolt_add_capsule(JoltWorld *w, float x, float y, float z, float half_height, float radius, int motion_type);

/* 传感器静态球（Jolt 原生 sensor：不参与刚体碰撞响应，也不产生刚体接触事件，
   但角色控制器的碰撞查询能看到它——用于拾取物/触发器等）。 */
uint32_t jolt_add_sensor_sphere(JoltWorld *w, float x, float y, float z, float radius);

void jolt_remove_body(JoltWorld *w, uint32_t body_id);

/* 枚举当前世界全部刚体 id（写入 out_ids，最多 max_ids 个，返回写入数量）。 */
uint32_t jolt_get_body_ids(JoltWorld *w, uint32_t *out_ids, uint32_t max_ids);

/* 查询刚体状态（位置为质心，旋转为四元数 x,y,z,w）。 */
int jolt_get_body_transform(JoltWorld *w, uint32_t body_id, float *out_pos, float *out_quat);
int jolt_is_body_active(JoltWorld *w, uint32_t body_id);

/* 修改刚体（对应 Jolt BodyInterface 的 Set 系列）。 */
void jolt_set_body_velocity(JoltWorld *w, uint32_t body_id, float vx, float vy, float vz);
void jolt_set_body_friction(JoltWorld *w, uint32_t body_id, float friction);
void jolt_set_body_restitution(JoltWorld *w, uint32_t body_id, float restitution);
void jolt_set_body_motion_quality(JoltWorld *w, uint32_t body_id, int quality);
void jolt_apply_impulse(JoltWorld *w, uint32_t body_id, float ix, float iy, float iz);

/* ---- 接触事件：取出接触监听器记录的刚体对（不区分业务角色，命中判定由 Go 侧做）。

   jolt_poll_contacts / jolt_character_poll_contacts 每次从队列头部取出最多
   max_count/max_ids 条，剩余留在队列供下一次调用；调用方循环调用直至返回 0
   即视为排空（Go 侧按 1024 / 256 分块循环读取）。 ---- */

uint32_t jolt_poll_contacts(JoltWorld *w, JoltContactPair *out, uint32_t max_count);

/* ---- 射线检测（原生返回 BodyID；是否为"命中目标"由 Go 侧决定） ---- */

int jolt_ray_cast(JoltWorld *w, const float *origin, const float *dir, float max_dist, JoltRayResult *out);

/* ---- 角色控制器：形状与出生点由 Go 传入，移动策略由 Go 计算 ---- */

/* 创建胶囊角色：half_height/radius 是胶囊圆柱半高与半径，offset_y 是把形状
   上移的量（让 GetPosition() 指向脚底），(x,y,z) 是初始脚底位置。重复创建失败。 */
int jolt_character_create(JoltWorld *w, float half_height, float radius, float offset_y, float x, float y, float z);

/* 动态刚体接触时是否允许推动角色（对应 Jolt CharacterContactSettings::mCanPushCharacter）。
   静态几何不受影响。 */
void jolt_character_set_dynamic_push(JoltWorld *w, int allow);

void jolt_character_get_position(JoltWorld *w, float *out_xyz);
void jolt_character_set_position(JoltWorld *w, float x, float y, float z);
void jolt_character_get_velocity(JoltWorld *w, float *out_xyz);
void jolt_character_set_velocity(JoltWorld *w, float vx, float vy, float vz);

/* 着地状态，取值与 JPH::CharacterVirtual::EGroundState 一致：
   0 = OnGround, 1 = OnSteepGround, 2 = NotSupported, 3 = InAir。 */
int jolt_character_get_ground_state(JoltWorld *w);

/* ExtendedUpdate：按世界重力推进角色一步（移动速度请先通过
   jolt_character_set_velocity 设置）。 */
void jolt_character_update(JoltWorld *w, float dt);

/* 取走本 tick 角色接触到的刚体 id（接触建立时 + 每步接触求解时都会记录，
   同一刚体可能出现多次；单线程调用）。每次取最多 max_ids 条，剩余留待下次，
   Go 侧循环取到 0 即排空并自行去重。 */
uint32_t jolt_character_poll_contacts(JoltWorld *w, uint32_t *out_ids, uint32_t max_ids);

#ifdef __cplusplus
}
#endif

#endif

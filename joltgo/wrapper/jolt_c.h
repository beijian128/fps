#ifndef JOLT_C_H
#define JOLT_C_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Opaque handle to a Jolt physics world. */
typedef struct JoltWorld JoltWorld;

/* Snapshot of a single rigid body, used to stream the world to the web client. */
typedef struct JoltBodyInfo {
	uint32_t id;
	int type;         /* 0 = box, 1 = sphere, 2 = enemy capsule */
	int is_static;
	int is_target;
	int is_enemy;
	int is_projectile;
	float pos[3];
	float quat[4];    /* x, y, z, w */
	float size[3];    /* box: half extents; sphere: radius in [0]; capsule: radius in [0], half height in [1] */
	float health;     /* only meaningful for enemies */
	int active;
} JoltBodyInfo;

/* Result of a ray cast. */
typedef struct JoltRayResult {
	int hit;
	uint32_t body_id;
	float point[3];
	float distance;
} JoltRayResult;

/* A projectile-vs-body contact reported by the contact listener. */
typedef struct JoltProjectileHit {
	uint32_t projectile_id;
	uint32_t body_id;
} JoltProjectileHit;

/* Create / destroy a physics world (and its character controller). */
JoltWorld *jolt_create(void);
void jolt_destroy(JoltWorld *w);

/* Add bodies. Each returns a body id, or 0 on failure. */
uint32_t jolt_add_static_box(JoltWorld *w, float hx, float hy, float hz, float x, float y, float z);
uint32_t jolt_add_dynamic_box(JoltWorld *w, float hx, float hy, float hz, float x, float y, float z, float vx, float vy, float vz);
uint32_t jolt_add_dynamic_sphere(JoltWorld *w, float x, float y, float z, float radius, float vx, float vy, float vz);
uint32_t jolt_add_target_sphere(JoltWorld *w, float x, float y, float z, float radius);
uint32_t jolt_add_enemy(JoltWorld *w, float x, float y, float z, float radius, float half_height);
uint32_t jolt_fire_projectile(JoltWorld *w, float ox, float oy, float oz, float dx, float dy, float dz, float speed);

/* Enumerate the bodies currently in the world. */
uint32_t jolt_body_count(JoltWorld *w);
int jolt_get_body_info(JoltWorld *w, uint32_t index, JoltBodyInfo *out);

/* Cast a ray (origin + normalized direction) up to max_dist. Returns 1 on success. */
int jolt_ray_cast(JoltWorld *w, const float *origin, const float *dir, float max_dist, JoltRayResult *out);

/* Mutate bodies. */
void jolt_apply_impulse(JoltWorld *w, uint32_t body_id, float ix, float iy, float iz);
void jolt_remove_body(JoltWorld *w, uint32_t body_id);
void jolt_set_body_velocity(JoltWorld *w, uint32_t body_id, float vx, float vy, float vz);
int jolt_damage_enemy(JoltWorld *w, uint32_t body_id, float amount); /* returns 1 if the enemy died */

/* Drain projectile contacts recorded by the contact listener. Returns count written to out. */
uint32_t jolt_poll_projectile_hits(JoltWorld *w, JoltProjectileHit *out, uint32_t max_count);

/* Update the player's capsule character controller. wish_x/wish_z are horizontal world-space velocity. */
void jolt_update_character(JoltWorld *w, float wish_x, float wish_z, int jump, float dt);
void jolt_get_character_position(JoltWorld *w, float *out_xyz);
void jolt_set_character_position(JoltWorld *w, float x, float y, float z);

/* Set gravity and advance the simulation. */
void jolt_set_gravity(JoltWorld *w, float gx, float gy, float gz);
void jolt_step(JoltWorld *w, float dt, int collision_steps);

#ifdef __cplusplus
}
#endif

#endif

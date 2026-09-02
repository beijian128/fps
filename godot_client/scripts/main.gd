extends Node3D
## 游戏主场景：输入采集、相机（第一/第三人称切换）、快照插值渲染、HUD、音效。
##
## 分层：
##   FpsClient（子节点）— WebSocket 传输层，发 state_received / connection_changed 信号
##   BodyEntity（class_name）— 每个服务端刚体一个渲染节点（卡通怪物/简单体）
##   Sfx（子节点）— 程序化音效
##
## 渲染节奏：服务端 20 Hz 推送快照，本场景 60 Hz 渲染。对运动刚体和玩家位置做
## 影子跟随插值——渲染时刻滞后一个 tick，在最近两帧快照之间 lerp / slerp。
##
## 卡通资源：金币（走近拾取，自转+浮动动画）；V 键切换第一人称（持枪 viewmodel）
## 与第三人称（玩家人形 Avatar）。

const TICK := 0.05          # 服务端模拟 tick（20 Hz）
const EYE_HEIGHT := 1.6
const WALK_SPEED := 8.0
const RUN_SPEED := 14.0
const LOOK_SPEED := 0.0022
const PITCH_LIMIT := PI / 2.0 - 0.05
# 用 preload 而不是 class_name：纯命令行运行时（未在编辑器里导入过）也能解析。
const BodyEntityScript := preload("res://scripts/body_entity.gd")

@onready var fps_client: Node = $FpsClient
@onready var sfx: Node = $Sfx

var camera: Camera3D
var stat_label: Label
var health_bar: ProgressBar
var overlay: Control
var conn_label: Label

# 第一人称持枪（相机子节点）：枪部件 + 双手 + 枪口闪光。
var _viewmodel: Node3D
var _vm_base_pos: Vector3
var _muzzle_flash: MeshInstance3D
# 第三人称玩家人形 Avatar。
var _avatar: Node3D
var _third_person := false

# 最近两帧快照（影子跟随）。每帧存 step、到达时间 t、{ id -> {info,pos,quat} }、
# 玩家脚底位置、resources。同一 tick 的重复推送只保留首次到达时间。
var _prev_snap := {}
var _next_snap := {}

# 实体缓存：id -> BodyEntity；金币：id -> {node, base}
var _entities := {}
var _res_nodes := {}

var _player_pos := Vector3(0, 0.2, 12)
var _yaw := 0.0
var _pitch := 0.0
var _jump_held := false
var _jump_queued := false
var _was_captured := false

var _fps_ema := 60.0
var _hud_score := 0
var _hud_wave := 1
var _hud_gold := 0
var _hud_targets := 0
var _hud_enemies := 0
var _last_score := 0
var _last_health := 100.0
var _prev_projectiles := {}  # id -> Vector3
var _hit_flash: ColorRect
# 自动化测试钩子：无头环境无法真正捕获鼠标，设置该环境变量后视作已捕获。
var _capture_override := OS.get_environment("JOLT_FORCE_CAPTURE") != ""

func _ready() -> void:
	_build_world()
	_build_avatar()
	_build_viewmodel()
	_build_hud()
	fps_client.state_received.connect(_on_state)
	fps_client.connection_changed.connect(_on_connection)

func _on_connection(connected: bool) -> void:
	conn_label.visible = not connected
	if not connected:
		_prev_snap = {}
		_next_snap = {}
		for id in _res_nodes:
			_res_nodes[id]["node"].queue_free()
		_res_nodes = {}

func _process(delta: float) -> void:
	if delta > 0.0:
		_fps_ema += (1.0 / delta - _fps_ema) * 0.08

	# 跳跃在按下瞬间排队，随下一帧输入上报给服务端。
	var space := Input.is_key_pressed(KEY_SPACE)
	if space and not _jump_held:
		_jump_queued = true
		_jump_held = true
		sfx.play("jump")
	if not space:
		_jump_held = false

	_render_interpolated()
	_anim_resources(delta)
	_update_camera()
	stat_label.text = "SCORE %d\nWAVE %d\nGOLD %d\nTARGETS %d\nENEMIES %d\nFPS %d" % [
		_hud_score, _hud_wave, _hud_gold, _hud_targets, _hud_enemies, roundi(_fps_ema),
	]

	# 仅锁定时上报移动输入（每渲染帧一次 ≈ 60 Hz）；释放时补一条静止输入，
	# 避免服务端沿用上次的速度继续移动。
	var captured := _capture_override or Input.mouse_mode == Input.MOUSE_MODE_CAPTURED
	overlay.visible = not captured
	if captured:
		fps_client.send_input(_wish_velocity(), _jump_queued)
		_jump_queued = false
	elif _was_captured:
		fps_client.send_input(Vector2.ZERO, false)
	_was_captured = captured

func _input(event: InputEvent) -> void:
	if Input.mouse_mode != Input.MOUSE_MODE_CAPTURED:
		return
	if event is InputEventMouseMotion:
		_apply_look(event.relative)
	elif event is InputEventMouseButton and event.button_index == MOUSE_BUTTON_LEFT and event.pressed:
		_shoot()
	elif event is InputEventKey and event.pressed and event.keycode == KEY_V:
		_third_person = not _third_person
		_apply_camera_mode()

func _unhandled_input(event: InputEvent) -> void:
	if Input.mouse_mode == Input.MOUSE_MODE_CAPTURED:
		return
	if event is InputEventMouseButton and event.pressed:
		Input.mouse_mode = Input.MOUSE_MODE_CAPTURED

func _apply_look(rel: Vector2) -> void:
	_yaw -= rel.x * LOOK_SPEED
	_pitch = clampf(_pitch - rel.y * LOOK_SPEED, -PITCH_LIMIT, PITCH_LIMIT)

func _apply_camera_mode() -> void:
	_avatar.visible = _third_person
	_viewmodel.visible = not _third_person

func _update_camera() -> void:
	if _third_person:
		# 相机绕玩家旋转：用第一人称的朝向，位置退到角色身后。
		var head := _player_pos + Vector3(0, 1.35, 0)
		var back := Vector3(
			sin(_yaw) * cos(_pitch),
			-sin(_pitch),
			cos(_yaw) * cos(_pitch))
		camera.global_position = head + back * 2.6
		_avatar.rotation.y = _yaw
	_avatar.global_position = _player_pos
	camera.rotation = Vector3(_pitch, _yaw, 0.0)
	if not _third_person:
		camera.global_position = _player_pos + Vector3(0, EYE_HEIGHT, 0)

## 世界空间水平期望速度：相机偏航 -> 前后/左右向量。
func _wish_velocity() -> Vector2:
	var f := 0.0
	var s := 0.0
	if Input.is_key_pressed(KEY_W):
		f += 1.0
	if Input.is_key_pressed(KEY_S):
		f -= 1.0
	if Input.is_key_pressed(KEY_D):
		s += 1.0
	if Input.is_key_pressed(KEY_A):
		s -= 1.0
	var speed := RUN_SPEED if Input.is_key_pressed(KEY_SHIFT) else WALK_SPEED
	var fx := -sin(_yaw)
	var fz := -cos(_yaw)
	var rx := cos(_yaw)
	var rz := -sin(_yaw)
	var wx := fx * f + rx * s
	var wz := fz * f + rz * s
	var l := Vector2(wx, wz).length()
	if l > 1e-6:
		wx = wx / l * speed
		wz = wz / l * speed
	return Vector2(wx, wz)

# ---- 快照 → 渲染 ----

func _on_state(s: Dictionary) -> void:
	_store_snapshot(s)

## 推流时序维护：tick 连续则滚动双缓冲；跳号（重置/重连）清空插值缓冲。
func _store_snapshot(s: Dictionary) -> void:
	if _next_snap.is_empty():
		_next_snap = _parse_snapshot(s, Time.get_ticks_msec() / 1000.0)
		_render_resources(_next_snap["resources"])
		return
	var last_step := int(_next_snap["step"])
	var step := int(s.get("step", 0))
	if step == last_step:
		return
	if step == last_step + 1:
		_prev_snap = _next_snap
	else:
		_prev_snap = {}
	_next_snap = _parse_snapshot(s, Time.get_ticks_msec() / 1000.0)
	_update_hud(s)
	_detect_impacts(_next_snap["bodies"])
	_render_resources(_next_snap["resources"])

func _parse_snapshot(s: Dictionary, t: float) -> Dictionary:
	var bodies := {}
	for b: Variant in s.get("bodies", []):
		var bd: Dictionary = b
		var pos: Array = bd.get("pos", [0.0, 0.0, 0.0])
		var q: Array = bd.get("quat", [0.0, 0.0, 0.0, 1.0])
		bodies[int(bd.get("id", 0))] = {
			"info": bd,
			"pos": Vector3(float(pos[0]), float(pos[1]), float(pos[2])),
			"quat": Quaternion(float(q[0]), float(q[1]), float(q[2]), float(q[3])),
		}
	var resources := {}
	for r: Variant in s.get("resources", []):
		var rd: Dictionary = r
		var rp: Array = rd.get("pos", [0.0, 0.8, 0.0])
		resources[int(rd.get("id", 0))] = {
			"pos": Vector3(float(rp[0]), float(rp[1]), float(rp[2])),
			"kind": int(rd.get("kind", 0)),
		}
	var player: Dictionary = s.get("player", {})
	var pos: Array = player.get("pos", [0.0, 0.2, 12.0])
	var feet := Vector3(float(pos[0]), float(pos[1]), float(pos[2]))
	return {
		"step": int(s.get("step", 0)), "t": t,
		"bodies": bodies, "player": feet, "resources": resources,
	}

## 影子跟随：alpha = 距新快照到达的时间 / TICK，在 prev/next 之间插值。
func _render_interpolated() -> void:
	if _next_snap.is_empty():
		return
	if _prev_snap.is_empty():
		_draw_bodies(_next_snap, _next_snap, 0.0)
		_player_pos = _next_snap["player"]
		return
	var alpha := clampf((Time.get_ticks_msec() / 1000.0 - float(_next_snap["t"])) / TICK, 0.0, 1.0)
	_draw_bodies(_prev_snap, _next_snap, alpha)
	_player_pos = (_prev_snap["player"] as Vector3).lerp(_next_snap["player"] as Vector3, alpha)

func _draw_bodies(from_snap: Dictionary, to_snap: Dictionary, alpha: float) -> void:
	var from_bodies: Dictionary = from_snap["bodies"]
	var to_bodies: Dictionary = to_snap["bodies"]
	var seen := {}
	for id in to_bodies.keys():
		seen[id] = true
		var bd: Dictionary = to_bodies[id]["info"]
		if from_bodies.has(id):
			_place_body(id, bd,
				(from_bodies[id]["pos"] as Vector3).lerp(to_bodies[id]["pos"] as Vector3, alpha),
				(from_bodies[id]["quat"] as Quaternion).slerp(to_bodies[id]["quat"] as Quaternion, alpha))
		else:
			_place_body(id, bd, to_bodies[id]["pos"], to_bodies[id]["quat"])
	for id in from_bodies.keys():
		if not seen.has(id):
			_remove_body(id)

func _place_body(id: int, b: Dictionary, pos: Vector3, quat: Quaternion) -> void:
	if not _entities.has(id):
		_entities[id] = BodyEntityScript.new()
		add_child(_entities[id])
	var e = _entities[id]
	e.sync_from(b)
	e.global_position = pos
	e.quaternion = quat

func _remove_body(id: int) -> void:
	if _entities.has(id):
		var e = _entities[id]
		e.queue_free()
		_entities.erase(id)

# ---- 金币资源 ----

func _render_resources(resources: Dictionary) -> void:
	var seen := {}
	for id in resources.keys():
		seen[id] = true
		if not _res_nodes.has(id):
			_res_nodes[id] = _spawn_coin(resources[id]["pos"])
	for id in _res_nodes.keys():
		if not seen.has(id):
			var e = _res_nodes[id]
			e.node.queue_free()
			_res_nodes.erase(id)
			sfx.play("pickup")

func _spawn_coin(base: Vector3) -> Dictionary:
	var root := Node3D.new()
	root.name = "Coin"

	var coin := MeshInstance3D.new()
	var cm := CylinderMesh.new()
	cm.top_radius = 0.22
	cm.bottom_radius = 0.22
	cm.height = 0.05
	cm.radial_segments = 22
	coin.mesh = cm
	coin.rotation_degrees = Vector3(90, 0, 0)
	coin.cast_shadow = GeometryInstance3D.SHADOW_CASTING_SETTING_ON
	var gold_mat := StandardMaterial3D.new()
	gold_mat.albedo_color = Color("ffd75e")
	gold_mat.emission_enabled = true
	gold_mat.emission = Color("ff9c3f")
	gold_mat.emission_energy_multiplier = 0.35
	gold_mat.roughness = 0.35
	gold_mat.metalness = 0.5
	coin.material_override = gold_mat
	root.add_child(coin)

	var ring := MeshInstance3D.new()
	var rm := CylinderMesh.new()
	rm.top_radius = 0.13
	rm.bottom_radius = 0.13
	rm.height = 0.06
	rm.radial_segments = 18
	ring.mesh = rm
	ring.rotation_degrees = Vector3(90, 0, 0)
	ring.cast_shadow = GeometryInstance3D.SHADOW_CASTING_SETTING_OFF
	var ring_mat := StandardMaterial3D.new()
	ring_mat.albedo_color = Color("fff2b8")
	ring_mat.emission_enabled = true
	ring_mat.emission = Color("ffd75e")
	ring_mat.emission_energy_multiplier = 0.6
	ring.material_override = ring_mat
	root.add_child(ring)

	root.position = base
	add_child(root)
	return {"node": root, "base": base}

func _anim_resources(delta: float) -> void:
	var now := Time.get_ticks_msec() / 1000.0
	for id in _res_nodes:
		var e = _res_nodes[id]
		e.node.rotation.y += delta * 2.2
		e.node.position.y = e.base.y + sin(now * 2.6 + float(id)) * 0.06

func _update_hud(s: Dictionary) -> void:
	_hud_score = int(s.get("score", 0))
	_hud_wave = int(s.get("wave", 1))
	_hud_gold = int(s.get("gold", 0))
	_hud_targets = 0
	_hud_enemies = 0
	for b: Variant in s.get("bodies", []):
		var bd: Dictionary = b
		if bd.get("target", false):
			_hud_targets += 1
		if bd.get("enemy", false):
			_hud_enemies += 1
	var player: Dictionary = s.get("player", {})
	var hp := float(player.get("health", 100.0))
	health_bar.value = hp
	if hp < _last_health - 0.001:
		sfx.play("damage")
		_flash_hit()
	_last_health = hp
	if _hud_score > _last_score:
		sfx.play("destroy")
	_last_score = _hud_score

## 受击红闪：全屏红色快速淡入淡出。
func _flash_hit() -> void:
	var tw := _hit_flash.create_tween()
	tw.tween_property(_hit_flash, "color:a", 0.28, 0.03)
	tw.tween_property(_hit_flash, "color:a", 0.0, 0.35)

## 弹丸从最新快照里消失即视为命中：在原位置播放爆闪效果。
func _detect_impacts(to_bodies: Dictionary) -> void:
	var current := {}
	for id in to_bodies.keys():
		var bd: Dictionary = to_bodies[id]["info"]
		if bd.get("projectile", false):
			current[id] = to_bodies[id]["pos"]
	for id in _prev_projectiles:
		if not current.has(id):
			_pop(_prev_projectiles[id], Color("ffe066"), 0.06, 0.2)
			sfx.play("hit")
	_prev_projectiles = current

func _shoot() -> void:
	# 弹道从枪口/角色胸口出发，收敛到准星 60 m 处的目标点：
	# 近处看起来从枪口呼啸，远处弹道仍落在准星上。
	var cam_forward := -camera.global_transform.basis.z
	var aim := camera.global_position + cam_forward * 60.0
	var origin: Vector3
	if _third_person:
		origin = _player_pos + Vector3(0, 1.4, 0)
	else:
		origin = _muzzle_flash.global_position
	var dir := (aim - origin).normalized()
	sfx.play("shoot")
	_pop(origin + dir * 0.7, Color("ffe066"), 0.08, 0.08)
	# 枪口闪光 + 后坐。
	if not _third_person:
		_muzzle_flash.visible = true
		_viewmodel.position = _vm_base_pos + Vector3(0, 0, 0.045)
		var tw := create_tween()
		tw.tween_property(_viewmodel, "position", _vm_base_pos, 0.08)
		create_tween().tween_property(_muzzle_flash, "visible", false, 0.0).set_delay(0.05)
	fps_client.send_shoot(origin, dir)

func _on_reset_pressed() -> void:
	_last_score = 0
	_last_health = 100.0
	_prev_projectiles = {}
	fps_client.send_reset()

func _pop(point: Vector3, color: Color, size: float, ttl: float) -> void:
	var m := SphereMesh.new()
	m.radius = size
	m.height = size * 2.0
	m.radial_segments = 12
	m.rings = 8
	var mat := StandardMaterial3D.new()
	mat.shading_mode = BaseMaterial3D.SHADING_MODE_UNSHADED
	mat.transparency = BaseMaterial3D.TRANSPARENCY_ALPHA
	mat.albedo_color = color
	var mi := MeshInstance3D.new()
	mi.mesh = m
	mi.material_override = mat
	mi.global_position = point
	add_child(mi)
	var tw := mi.create_tween()
	tw.set_parallel(true)
	tw.tween_property(mi, "scale", Vector3.ONE * 4.0, ttl)
	tw.tween_property(mat, "albedo_color:a", 0.0, ttl)
	tw.finished.connect(mi.queue_free)

# ---- 场景搭建 ----

func _add_part(parent: Node3D, mesh: Mesh, color: Color, pos: Vector3, shadow := true) -> MeshInstance3D:
	var mi := MeshInstance3D.new()
	mi.mesh = mesh
	mi.position = pos
	mi.cast_shadow = GeometryInstance3D.SHADOW_CASTING_SETTING_ON if shadow else GeometryInstance3D.SHADOW_CASTING_SETTING_OFF
	var mat := StandardMaterial3D.new()
	mat.albedo_color = color
	mat.roughness = 0.6
	mat.metalness = 0.0
	mi.material_override = mat
	parent.add_child(mi)
	return mi

func _build_world() -> void:
	camera = Camera3D.new()
	camera.name = "Camera"
	camera.fov = 75.0
	camera.near = 0.05
	camera.far = 300.0
	camera.rotation_order = EULER_ORDER_YXZ
	camera.current = true
	add_child(camera)

	var env := Environment.new()
	env.background_mode = Environment.BG_COLOR
	env.background_color = Color("0d1117")
	env.ambient_light_source = Environment.AMBIENT_SOURCE_COLOR
	env.ambient_light_color = Color("dce6ff")
	env.ambient_light_energy = 1.0
	env.fog_enabled = true
	env.fog_mode = Environment.FOG_MODE_DEPTH
	env.fog_light_color = Color("0d1117")
	env.fog_light_energy = 1.0
	env.fog_depth_begin = 20.0
	env.fog_depth_end = 90.0
	var we := WorldEnvironment.new()
	we.environment = env
	add_child(we)

	var sun := DirectionalLight3D.new()
	sun.name = "Sun"
	sun.light_energy = 2.0
	sun.shadow_enabled = true
	sun.look_at_from_position(Vector3(12, 24, 10), Vector3.ZERO, Vector3.UP)
	add_child(sun)

	# 地面网格参考线。
	var grid := MeshInstance3D.new()
	grid.name = "Grid"
	grid.mesh = _grid_mesh()
	var grid_mat := StandardMaterial3D.new()
	grid_mat.shading_mode = BaseMaterial3D.SHADING_MODE_UNSHADED
	grid_mat.albedo_color = Color("2b323b")
	grid.material_override = grid_mat
	grid.position = Vector3(0, 0.01, 0)
	add_child(grid)

func _grid_mesh() -> ArrayMesh:
	var verts := PackedVector3Array()
	for i in range(-20, 21):
		var v := float(i)
		verts.append(Vector3(v, 0.0, -20.0))
		verts.append(Vector3(v, 0.0, 20.0))
		verts.append(Vector3(-20.0, 0.0, v))
		verts.append(Vector3(20.0, 0.0, v))
	var arrays := []
	arrays.resize(Mesh.ARRAY_MAX)
	arrays[Mesh.ARRAY_VERTEX] = verts
	var mesh := ArrayMesh.new()
	mesh.add_surface_from_arrays(Mesh.PRIMITIVE_LINES, arrays)
	return mesh

## 玩家人形 Avatar（第三人称可见）：大头 + 棒球帽 + 圆身体 + 短腿 + 双肩包。
func _build_avatar() -> void:
	_avatar = Node3D.new()
	_avatar.name = "Avatar"
	_avatar.visible = false
	add_child(_avatar)

	var skin := Color("ffd9b3")
	var shirt := Color("6fb3ff")
	var pants := Color("35548c")
	var cap := Color("e0708f")

	# 头 + 帽 + 脸。
	var head := SphereMesh.new()
	head.radius = 0.24
	head.height = 0.48
	head.radial_segments = 20
	head.rings = 12
	_add_part(_avatar, head, skin, Vector3(0, 1.08, 0))

	var cap_top := CylinderMesh.new()
	cap_top.top_radius = 0.235
	cap_top.bottom_radius = 0.27
	cap_top.height = 0.1
	_add_part(_avatar, cap_top, cap, Vector3(0, 1.24, 0))

	var brim := CylinderMesh.new()
	brim.top_radius = 0.34
	brim.bottom_radius = 0.34
	brim.height = 0.025
	var brim_mi := _add_part(_avatar, brim, cap, Vector3(0, 1.2, -0.16))
	brim_mi.scale = Vector3(1.0, 1.0, 1.45)

	for sx in [-1.0, 1.0]:
		var eye := SphereMesh.new()
		eye.radius = 0.032
		eye.height = 0.064
		_add_part(_avatar, eye, Color("2a2433"), Vector3(sx * 0.09, 1.11, -0.215))

	var mouth := SphereMesh.new()
	mouth.radius = 0.035
	mouth.height = 0.07
	var mmi := _add_part(_avatar, mouth, Color("2a2433"), Vector3(0, 1.0, -0.22))
	mmi.scale = Vector3(1.5, 0.6, 0.5)

	# 身体 + 手 + 腿 + 背包。
	var torso := SphereMesh.new()
	torso.radius = 0.3
	torso.height = 0.6
	torso.radial_segments = 20
	torso.rings = 12
	var tmi := _add_part(_avatar, torso, shirt, Vector3(0, 0.42, 0))
	tmi.scale = Vector3(1.0, 1.3, 0.92)

	for sx in [-1.0, 1.0]:
		var arm := SphereMesh.new()
		arm.radius = 0.07
		arm.height = 0.14
		_add_part(_avatar, arm, skin, Vector3(sx * 0.32, 0.52, 0))
		var leg := CapsuleMesh.new()
		leg.radius = 0.07
		leg.height = 0.32
		leg.radial_segments = 12
		leg.rings = 5
		_add_part(_avatar, leg, pants, Vector3(sx * 0.12, 0.16, 0))

	var pack := BoxMesh.new()
	pack.size = Vector3(0.34, 0.4, 0.18)
	_add_part(_avatar, pack, Color("7ec850"), Vector3(0, 0.56, 0.28))

## 第一人称持枪 viewmodel（相机子节点）：卡通小手枪 + 双手手套。
func _build_viewmodel() -> void:
	_viewmodel = Node3D.new()
	_viewmodel.name = "Viewmodel"
	camera.add_child(_viewmodel)
	_viewmodel.position = Vector3(0.22, -0.22, -0.55)
	_vm_base_pos = _viewmodel.position

	var gun_body := Color("3a4a5f")
	var gun_metal := Color("9aa7b5")
	var gun_wood := Color("d98a4e")
	var glove := Color("6a7fc9")

	# 枪身 / 枪管 / 消音器。
	var body := BoxMesh.new()
	body.size = Vector3(0.05, 0.07, 0.34)
	_add_part(_viewmodel, body, gun_body, Vector3(0, 0.02, 0), false)

	var barrel := BoxMesh.new()
	barrel.size = Vector3(0.035, 0.035, 0.24)
	_add_part(_viewmodel, barrel, gun_metal, Vector3(0, 0.045, -0.27), false)

	var suppressor := CylinderMesh.new()
	suppressor.top_radius = 0.028
	suppressor.bottom_radius = 0.028
	suppressor.height = 0.07
	var sup := _add_part(_viewmodel, suppressor, gun_metal, Vector3(0, 0.045, -0.42), false)
	sup.rotation_degrees = Vector3(90, 0, 0)

	# 握把 / 弹匣 / 枪托 / 准星。
	var grip := BoxMesh.new()
	grip.size = Vector3(0.04, 0.12, 0.055)
	var gmi := _add_part(_viewmodel, grip, gun_wood, Vector3(0, -0.07, 0.09), false)
	gmi.rotation_degrees = Vector3(-14, 0, 0)

	var mag := BoxMesh.new()
	mag.size = Vector3(0.035, 0.11, 0.06)
	var mmi := _add_part(_viewmodel, mag, gun_body, Vector3(0, -0.06, -0.04), false)
	mmi.rotation_degrees = Vector3(8, 0, 0)

	var sight := BoxMesh.new()
	sight.size = Vector3(0.015, 0.035, 0.03)
	_add_part(_viewmodel, sight, gun_metal, Vector3(0, 0.09, -0.08), false)

	# 双手手套 + 袖口。
	var hand_r := SphereMesh.new()
	hand_r.radius = 0.06
	hand_r.height = 0.12
	_add_part(_viewmodel, hand_r, glove, Vector3(0.015, -0.1, 0.09), false)

	var hand_l := SphereMesh.new()
	hand_l.radius = 0.055
	hand_l.height = 0.11
	_add_part(_viewmodel, hand_l, glove, Vector3(0.03, -0.045, -0.16), false)

	var cuff := CylinderMesh.new()
	cuff.top_radius = 0.05
	cuff.bottom_radius = 0.058
	cuff.height = 0.06
	var cuff_r := _add_part(_viewmodel, cuff, gun_wood, Vector3(0.07, -0.12, 0.11), false)
	cuff_r.rotation_degrees = Vector3(0, 0, 60)

	# 枪口闪光（射击时短暂显示）。
	_muzzle_flash = MeshInstance3D.new()
	var fm := SphereMesh.new()
	fm.radius = 0.05
	fm.height = 0.1
	fm.radial_segments = 10
	fm.rings = 8
	_muzzle_flash.mesh = fm
	var fmat := StandardMaterial3D.new()
	fmat.shading_mode = BaseMaterial3D.SHADING_MODE_UNSHADED
	fmat.albedo_color = Color("ffd75e")
	fmat.emission_enabled = true
	fmat.emission = Color("ffc24f")
	fmat.emission_energy_multiplier = 2.0
	_muzzle_flash.material_override = fmat
	_muzzle_flash.position = Vector3(0, 0.045, -0.47)
	_muzzle_flash.visible = false
	_viewmodel.add_child(_muzzle_flash)

func _build_hud() -> void:
	var layer := CanvasLayer.new()
	layer.name = "HUD"
	add_child(layer)

	stat_label = Label.new()
	stat_label.position = Vector2(16, 12)
	stat_label.mouse_filter = Control.MOUSE_FILTER_IGNORE
	stat_label.add_theme_font_size_override("font_size", 15)
	stat_label.add_theme_color_override("font_shadow_color", Color(0, 0, 0, 0.8))
	stat_label.add_theme_constant_override("shadow_offset_x", 1)
	stat_label.add_theme_constant_override("shadow_offset_y", 1)
	layer.add_child(stat_label)

	health_bar = ProgressBar.new()
	health_bar.max_value = 100.0
	health_bar.value = 100.0
	health_bar.show_percentage = false
	health_bar.mouse_filter = Control.MOUSE_FILTER_IGNORE
	health_bar.set_anchors_preset(Control.PRESET_BOTTOM_LEFT)
	health_bar.offset_left = 16.0
	health_bar.offset_top = -30.0
	health_bar.offset_right = 236.0
	health_bar.offset_bottom = -18.0
	layer.add_child(health_bar)

	var cross := ColorRect.new()
	cross.color = Color(1, 1, 1, 0.9)
	cross.mouse_filter = Control.MOUSE_FILTER_IGNORE
	cross.set_anchors_preset(Control.PRESET_CENTER)
	cross.offset_left = -3.0
	cross.offset_top = -3.0
	cross.offset_right = 3.0
	cross.offset_bottom = 3.0
	layer.add_child(cross)

	_hit_flash = ColorRect.new()
	_hit_flash.color = Color(1, 0.15, 0.1, 0.0)
	_hit_flash.mouse_filter = Control.MOUSE_FILTER_IGNORE
	_hit_flash.set_anchors_preset(Control.PRESET_FULL_RECT)
	layer.add_child(_hit_flash)

	var hint := Label.new()
	hint.text = "WASD 移动 · 鼠标瞄准 · 左键射击 · Space 跳跃 · Shift 奔跑 · V 切换视角 · ESC 释放鼠标"
	hint.mouse_filter = Control.MOUSE_FILTER_IGNORE
	hint.add_theme_font_size_override("font_size", 13)
	hint.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
	hint.set_anchors_preset(Control.PRESET_CENTER_BOTTOM)
	hint.offset_left = -420.0
	hint.offset_top = -40.0
	hint.offset_right = 420.0
	hint.offset_bottom = -20.0
	layer.add_child(hint)

	var reset_btn := Button.new()
	reset_btn.text = "Reset"
	reset_btn.set_anchors_preset(Control.PRESET_TOP_RIGHT)
	reset_btn.offset_left = -110.0
	reset_btn.offset_top = 12.0
	reset_btn.offset_right = -14.0
	reset_btn.offset_bottom = 44.0
	reset_btn.pressed.connect(_on_reset_pressed)
	layer.add_child(reset_btn)

	overlay = ColorRect.new()
	overlay.color = Color(Color("0d1117"), 0.85)
	overlay.mouse_filter = Control.MOUSE_FILTER_IGNORE
	overlay.set_anchors_preset(Control.PRESET_FULL_RECT)
	layer.add_child(overlay)

	var center := CenterContainer.new()
	center.mouse_filter = Control.MOUSE_FILTER_IGNORE
	center.set_anchors_preset(Control.PRESET_FULL_RECT)
	overlay.add_child(center)

	var vbox := VBoxContainer.new()
	vbox.mouse_filter = Control.MOUSE_FILTER_IGNORE
	vbox.alignment = BoxContainer.ALIGNMENT_CENTER
	center.add_child(vbox)

	var title := Label.new()
	title.text = "Jolt FPS Demo"
	title.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
	title.add_theme_font_size_override("font_size", 32)
	vbox.add_child(title)

	conn_label = Label.new()
	conn_label.text = "正在连接服务器…"
	conn_label.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
	conn_label.add_theme_color_override("font_color", Color("e5484d"))
	conn_label.visible = false
	vbox.add_child(conn_label)

	for t in ["点击进入游戏并锁定鼠标", "PVE 打怪：清空怪物自动刷下一波，击杀掉落金币，靠近自动拾取", "W A S D 移动 · Space 跳跃 · Shift 奔跑 · V 切换第一/第三人称 · Reset 重开"]:
		var l := Label.new()
		l.text = t
		l.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
		l.add_theme_color_override("font_color", Color("9da7b3"))
		vbox.add_child(l)

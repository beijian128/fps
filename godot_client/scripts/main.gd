extends Node3D
## 游戏主场景：输入采集、相机（第一/第三人称切换）、帧插值渲染、HUD、音效。
##
## 分层：
##   FpsClient（子节点）— WebSocket 传输层，发 frame_received / connection_changed 信号
##   WorldStore — 本地世界状态（实体-属性增量累积成完整世界）
##   BodyEntity（class_name）— 每个服务端刚体一个渲染节点（卡通怪物/简单体）
##   Sfx（子节点）— 程序化音效
##
## 渲染节奏：服务端 20 Hz 推送增量帧，本场景 60 Hz 渲染。渲染层按「属性名」从
## WorldStore 查询实体（不认识的新属性照常累积、只是不渲染），对运动刚体和玩家位置
## 做影子跟随插值——在相邻两帧的变换之间 lerp / slerp。
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
const WorldStore := preload("res://scripts/world_store.gd")

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
# 远端玩家 Avatar（局内另一名玩家，始终可见）。
var _remote_avatar: Node3D
var _third_person := false

# 本地世界状态：服务端推的是增量，这里累积成完整世界（见 world_store.gd）。
var _store: WorldStore = WorldStore.new()
# 渲染层自己的插值状态：id -> {"pos","quat"}（上一帧的变换），以及本帧到达时间。
var _body_xform := {}      # id -> {"pos": Vector3, "quat": Quaternion}：服务端刚体的当前变换
var _prev_body_xform := {} # id -> {"pos": Vector3, "quat": Quaternion}：插值起点
# 玩家位置 / 远端朝向的插值状态：_prev_* 是上一帧的显示值（插值起点），_*_target 是服务端
# 刚下发的权威值；_player_pos / _remote_pos / _remote_yaw 是插值后的显示值（相机与 avatar
# 读它）。target 必须与显示值分开存：_render_interpolated 一帧内会被调两次（_process
# 一次、_on_frame 收尾一次），若把插值结果写回 target，第二次调用会拿结果再插一次。
var _prev_player_pos := Vector3(0, 0.2, 18)
var _player_pos_target := Vector3(0, 0.2, 18)
var _prev_remote_pos := Vector3(0, 0.2, -18)
var _remote_pos_target := Vector3(0, 0.2, -18)
var _prev_remote_yaw := 0.0
var _remote_yaw_target := 0.0
var _frame_time := 0.0     # 本帧到达时间（秒），插值 alpha 的基准

# 实体缓存：id -> BodyEntity；金币：id -> {node, base}
var _entities := {}
var _res_nodes := {}

var _player_pos := Vector3(0, 0.2, 18)
var _remote_pos := Vector3(0, 0.2, -18)
var _remote_yaw := 0.0
var _my_player_idx := 0
var _matched := false
var _yaw := 0.0
var _pitch := 0.0
var _jump_held := false
var _jump_queued := false
# 本帧待上报的射击 / 重置：与输入合并成一条 game.cmd 发出，帧是最小发送单位。
var _pending_shot := {}
var _pending_reset := false

var _fps_ema := 60.0
var _hud_score := 0
var _hud_wave := 1
var _hud_gold := 0
var _hud_targets := 0
var _hud_enemies := 0
var _last_score := 0
var _last_health := 100.0
var _hit_flash: ColorRect
# 自动化测试钩子：无头环境无法真正捕获鼠标，设置该环境变量后视作已捕获。
var _capture_override := OS.get_environment("JOLT_FORCE_CAPTURE") != ""

func _ready() -> void:
	_build_world()
	_build_avatar()
	_build_remote_avatar()
	_build_viewmodel()
	_build_hud()
	fps_client.frame_received.connect(_on_frame)
	fps_client.matched_received.connect(_on_matched)
	fps_client.connection_changed.connect(_on_connection)

func _on_matched(result: Dictionary) -> void:
	_my_player_idx = int(result.get("player_idx", 0))
	_matched = true
	# 出生在船的艏/艉两端，开局朝向船中（与服务端 playerSpawnYaw 一致）。
	_yaw = PI if _my_player_idx == 1 else 0.0
	conn_label.visible = false
	# 由客户端驱动全量补齐：收到 full 帧之前，WorldStore 之外的一切都不可信。
	_store.clear()
	_reset_interp()
	fps_client.send_resync()

func _on_connection(connected: bool) -> void:
	if connected:
		conn_label.text = "正在匹配…"
		conn_label.visible = true
	else:
		conn_label.text = "正在连接服务器…"
		conn_label.visible = true
		_matched = false
		_store.clear()
		_reset_interp()
		for id in _res_nodes:
			_res_nodes[id]["node"].queue_free()
		_res_nodes = {}
		# 刚体渲染节点一并清空：重连前不知道哪些 id 还会复用，全部交给重连后的
		# full 帧重新协调（_reset_interp 已清掉插值状态，首帧直接落位不跳变）。
		for id in _entities:
			_entities[id].queue_free()
		_entities = {}

## _reset_interp 清空插值状态。重连后第一帧没有「上一帧」，直接在当前位置落位。
func _reset_interp() -> void:
	_body_xform.clear()
	_prev_body_xform.clear()
	_frame_time = 0.0

func _process(delta: float) -> void:
	if delta > 0.0:
		_fps_ema += (1.0 / delta - _fps_ema) * 0.08

	# 鼠标是否锁定（标题遮罩 / 按了 ESC 时未锁定）。
	var captured := _capture_override or Input.mouse_mode == Input.MOUSE_MODE_CAPTURED

	# 跳跃在按下瞬间排队，随下一帧命令上报给服务端。未锁定时（标题遮罩/鼠标
	# 已释放）不排队也不播音效，避免把过期的跳跃误发出去或空播音效。
	var space := Input.is_key_pressed(KEY_SPACE)
	if space and not _jump_held:
		_jump_held = true
		if captured:
			_jump_queued = true
			sfx.play("jump")
	if not space:
		_jump_held = false

	_render_interpolated()
	_anim_resources(delta)
	_update_camera()
	stat_label.text = "SCORE %d\nWAVE %d\nGOLD %d\nTARGETS %d\nENEMIES %d\nFPS %d" % [
		_hud_score, _hud_wave, _hud_gold, _hud_targets, _hud_enemies, roundi(_fps_ema),
	]

	overlay.visible = not captured

	# 每渲染帧都上报一条命令（输入 + 射击 + 重置合并成一条，帧是最小发送单位），
	# **包括鼠标未捕获（按了 ESC 暂停）时**。两个理由：
	#   1. 服务端合并后的 Cmd 每帧都调 ApplyInput，客户端必须每帧都给出 move；
	#   2. 服务端按「最近一次上行消息」判定实例空闲（60 s）。暂停时若停止上报，
	#      玩家虽然仍连着却完全静默 —— 60 s 后服务端会在**在线**状态下把他回收，
	#      画面停推 → 2.5 s 看门狗强制重连 → 重新匹配开新局 → 再次被回收，形成
	#      每分钟一局的空转。每帧都发就从根上消除了这个窗口。
	# 但「每帧都发」不等于「暂停时还在推着人跑」：_wish_velocity() 只看 Input 按键，
	# 根本不看 captured，照 raw 调用会让 WASD 在鼠标释放后照样驱动角色（85% 不透明的
	# 暂停面板下面，服务端在实时执行输入）。旧行为是释放鼠标时补发一条静止输入，
	# 这里等价地发零向量 —— 是「发零」，不是「不发」。
	var move := _wish_velocity() if captured else Vector2.ZERO
	var jump := _jump_queued
	_jump_queued = false
	var origin := Vector3.ZERO
	var dir := Vector3.ZERO
	var shoot := not _pending_shot.is_empty()
	if shoot:
		origin = _pending_shot["origin"]
		dir = _pending_shot["dir"]
		_pending_shot = {}
	var reset := _pending_reset
	_pending_reset = false
	fps_client.send_command(move, _yaw, jump, shoot, origin, dir, reset)

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
	elif event is InputEventKey and event.pressed and event.keycode == KEY_ESCAPE:
		# 显式释放鼠标（不依赖引擎对 ESC 的默认行为）。
		Input.mouse_mode = Input.MOUSE_MODE_VISIBLE

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
	_remote_avatar.global_position = _remote_pos
	_remote_avatar.rotation.y = _remote_yaw
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

# ---- 帧 → 渲染 ----

## _on_frame 应用一帧同步消息。full 帧在 WorldStore 内部会先清空再整体覆盖，
## 所以「首次进入 / 重连 / 乱序」在这里没有区别 —— 应用完做一次场景协调即可。
##
## 旧的 _snap_sig / _frame_keeps_bodies 去重逻辑整块消失：增量协议下服务端
## 不会重复推送，也不需要靠快照 diff 推断「谁消失了」。
func _on_frame(frame: Dictionary) -> void:
	if bool(frame.get("full", false)):
		_store.apply_schema(frame.get("schema", {}).get("fields", []))
	var res: Dictionary = _store.apply_frame(frame)

	# 本帧到达即把各刚体的当前变换存进 prev，作为下一次插值的起点。
	_frame_time = Time.get_ticks_msec() / 1000.0
	_prev_body_xform = _body_xform.duplicate(true)
	_refresh_derived()

	_reconcile_scene()
	for ev: Variant in res.get("destroyed", []):
		_on_entity_destroyed(ev as Dictionary)
	_render_resources_from_store()
	_update_hud()
	# 收尾再插值一次。Godot 先跑父节点 _process（里面已有一次 _render_interpolated）
	# 再跑子节点 FpsClient._process，而后者同步 emit frame_received → 这里；上面
	# _reconcile_scene 刚把**原始**变换写进节点，若不在此收尾，本渲染帧画的就是未插值
	# 的跳变，下一帧才被拉回去 —— 每来一帧抖一次。让一帧里对节点的最后一次写是插值结果。
	_render_interpolated()

## _refresh_derived 从 store 刷新刚体变换、玩家位置、远端朝向。必须在插值之前做，
## 这样 prev/cur 才是相邻两帧。
func _refresh_derived() -> void:
	# 每帧从零重建：store 里已经没有的实体必须从 _body_xform 里消失，_reconcile_scene
	# 才能据此删掉它的节点。不能假设「消失」都走 destroyed —— 实体的最后一个已知属性
	# 被 removed 而没 destroy 时，它是静默地从 store 里淡出的。
	var next := {}
	for id: Variant in _store.entities_with("Body.Kind"):
		var eid := int(id)
		# 金币也是普通刚体（Body.Kind / Pos 都在 store 里），这里必须排除：金币由
		# _res_nodes 单独渲染，不排除就会在金币上再叠一个灰色刚体球。
		if _store.has_attr(eid, "Resource.Kind"):
			continue
		next[eid] = {
			"pos": _vec3_of(_store.attr(eid, "Pos")),
			"quat": _quat_of(_store.attr(eid, "Rot")),
		}
	_body_xform = next
	for id: Variant in _store.entities_with("Player.Idx"):
		var eid := int(id)
		var feet := _vec3_of(_store.attr(eid, "Pos"))
		if int(_store.attr(eid, "Player.Idx")) == _my_player_idx:
			# 本地玩家：像刚体一样保留上一帧的显示值作为插值起点，否则第一人称相机
			# 与第三人称 avatar 会按 20 Hz 跳步（走 0.4 m/步、跑 0.7 m/步）。
			_prev_player_pos = _player_pos
			_player_pos_target = feet
		else:
			_prev_remote_pos = _remote_pos
			_remote_pos_target = feet
			_prev_remote_yaw = _remote_yaw
			_remote_yaw_target = float(_store.attr(eid, "Facing"))

## _on_entity_destroyed 消失的实体触发对应反馈。销毁事件带着消失前的属性，
## 所以这里能分辨消失的是弹丸（爆闪 + 命中音）还是金币（拾取音）。
func _on_entity_destroyed(ev: Dictionary) -> void:
	var attrs: Dictionary = ev.get("attrs", {})
	# 此刻实体已从 store 移除（_body_xform 里也没有它），爆闪位置只能取销毁事件带的
	# 「消失前属性」。从未渲染过的 id（destroy 一个客户端没见过的实体）attrs 为空，
	# 既没有 Projectile 也没有 Resource.Kind —— 这里自然落成 no-op，不会索引空节点。
	var p: Vector3 = _vec3_of(attrs.get("Pos"))
	if attrs.has("Projectile"):
		_pop(p, Color("ffe066"), 0.06, 0.2)
		sfx.play("hit")
	if attrs.has("Resource.Kind"):
		sfx.play("pickup")

## _vec3_of / _quat_of 把 store 里的 Array 属性转成 Godot 类型。
func _vec3_of(v: Variant) -> Vector3:
	var a: Array = v if v is Array else [0.0, 0.0, 0.0]
	return Vector3(float(a[0]), float(a[1]), float(a[2]))

func _quat_of(v: Variant) -> Quaternion:
	var a: Array = v if v is Array else [0.0, 0.0, 0.0, 1.0]
	return Quaternion(float(a[0]), float(a[1]), float(a[2]), float(a[3]))

## _reconcile_scene 把刚体渲染节点与 store 对齐：新 id 建节点、消失的删节点。
## 场景同步放在「整帧应用之后」做，实体创建的先后顺序问题就自然消失了。
func _reconcile_scene() -> void:
	for id: Variant in _body_xform:
		var eid := int(id)
		_place_body(eid, _build_body_dict(eid), _body_xform[eid]["pos"], _body_xform[eid]["quat"])
	for eid: Variant in _entities.keys():
		if not _body_xform.has(int(eid)):
			_remove_body(int(eid))

## _build_body_dict 从 store 组装出 body_entity.gd 期望的字典 —— 形状与旧的快照
## BodyInfo 完全一致，所以 body_entity.gd 的程序化建模代码零改动。
func _build_body_dict(eid: int) -> Dictionary:
	return {
		"id": eid,
		"type": int(_store.attr(eid, "Body.Kind")),
		"static": bool(_store.attr(eid, "Body.Static")),
		"target": _store.has_attr(eid, "Target"),
		"enemy": _store.has_attr(eid, "Enemy"),
		"projectile": _store.has_attr(eid, "Projectile"),
		"pos": _store.attr(eid, "Pos"),
		"quat": _store.attr(eid, "Rot"),
		"size": _store.attr(eid, "Body.Size"),
		"health": float(_store.attr(eid, "Health")) if _store.has_attr(eid, "Health") else 0.0,
		"active": bool(_store.attr(eid, "Body.Active")),
		"mat": int(_store.attr(eid, "Body.Mat")),
	}

## 影子跟随：alpha = 距本帧到达的时间 / TICK，在「上一帧变换 → 本帧变换」之间插值。
func _render_interpolated() -> void:
	if _body_xform.is_empty():
		return
	var alpha := clampf((Time.get_ticks_msec() / 1000.0 - _frame_time) / TICK, 0.0, 1.0)
	for id: Variant in _body_xform:
		var eid := int(id)
		var node: Node3D = _entities.get(eid)
		if node == null:
			continue
		var cur: Dictionary = _body_xform[eid]
		var prev: Dictionary = _prev_body_xform.get(eid, cur)
		node.global_position = (prev["pos"] as Vector3).lerp(cur["pos"] as Vector3, alpha)
		node.quaternion = (prev["quat"] as Quaternion).slerp(cur["quat"] as Quaternion, alpha)
	# 本地玩家位置与远端位置/朝向同样在 prev→target 之间插值：直接赋 raw 值会让
	# 第一人称相机与 avatar 按 20 Hz 跳步。yaw 是角度，用最短角插值避免 180° 附近跳变。
	_player_pos = _prev_player_pos.lerp(_player_pos_target, alpha)
	_remote_pos = _prev_remote_pos.lerp(_remote_pos_target, alpha)
	_remote_yaw = lerp_angle(_prev_remote_yaw, _remote_yaw_target, alpha)

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

## _render_resources_from_store 把 store 里的金币摊平成 _render_resources 期望的形状。
func _render_resources_from_store() -> void:
	var out := {}
	for id: Variant in _store.entities_with("Resource.Kind"):
		var eid := int(id)
		out[eid] = {"pos": _vec3_of(_store.attr(eid, "Pos")), "kind": int(_store.attr(eid, "Resource.Kind"))}
	_render_resources(out)

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
	gold_mat.metallic = 0.5
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

## _update_hud 从 store 读全局状态与本地玩家。全局状态是挂在单例实体上的
## GameState 组件（属性名 Game.Score / Game.Wave / Game.Gold）。
func _update_hud() -> void:
	for id: Variant in _store.entities_with("Game.Score"):
		var gid := int(id)
		_hud_score = int(_store.attr(gid, "Game.Score"))
		_hud_wave = int(_store.attr(gid, "Game.Wave"))
		_hud_gold = int(_store.attr(gid, "Game.Gold"))
		break
	_hud_targets = _store.entities_with("Target").size()
	_hud_enemies = _store.entities_with("Enemy").size()

	var hp := 100.0
	for id: Variant in _store.entities_with("Player.Idx"):
		var eid := int(id)
		if int(_store.attr(eid, "Player.Idx")) == _my_player_idx:
			hp = float(_store.attr(eid, "Health"))
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
	# 射击与移动/跳跃合并进本渲染帧的一条 game.cmd：这里只记下待上报的弹道。
	_pending_shot = {"origin": origin, "dir": dir}

func _on_reset_pressed() -> void:
	_last_score = 0
	_last_health = 100.0
	# reset 是一个边沿：下一渲染帧与输入合并成一条命令上报一次。
	_pending_reset = true

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
	add_child(mi)
	mi.global_position = point
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
	mat.metallic = 0.0
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

	# 海面：船外的深色水面（纯装饰，无碰撞，碰撞几何全在服务端）。
	var sea := MeshInstance3D.new()
	sea.name = "Sea"
	var pm := PlaneMesh.new()
	pm.size = Vector2(600, 600)
	sea.mesh = pm
	var sea_mat := StandardMaterial3D.new()
	sea_mat.albedo_color = Color("0a1b2a")
	sea_mat.roughness = 0.18
	sea_mat.metallic = 0.55
	sea.material_override = sea_mat
	sea.position = Vector3(0, -1.4, 0)
	add_child(sea)

	# 甲板钢板拼缝参考线（与 map.go 的甲板尺寸一致）。
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
	var step := 2.0
	var x := -13.0
	while x <= 13.0:
		verts.append(Vector3(x, 0.0, -22.0))
		verts.append(Vector3(x, 0.0, 22.0))
		x += step
	var z := -22.0
	while z <= 22.0:
		verts.append(Vector3(-13.0, 0.0, z))
		verts.append(Vector3(13.0, 0.0, z))
		z += step
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
	_build_humanoid(_avatar, Color("6fb3ff"), Color("e0708f"))

## 远端玩家 Avatar：复用同一套人形，换颜色区分，始终可见。
func _build_remote_avatar() -> void:
	_remote_avatar = Node3D.new()
	_remote_avatar.name = "RemoteAvatar"
	add_child(_remote_avatar)
	_build_humanoid(_remote_avatar, Color("ff9f43"), Color("4caf50"))

func _build_humanoid(parent: Node3D, shirt: Color, cap: Color) -> void:
	var skin := Color("ffd9b3")
	var pants := Color("35548c")

	# 头 + 帽 + 脸。
	var head := SphereMesh.new()
	head.radius = 0.24
	head.height = 0.48
	head.radial_segments = 20
	head.rings = 12
	_add_part(parent, head, skin, Vector3(0, 1.08, 0))

	var cap_top := CylinderMesh.new()
	cap_top.top_radius = 0.235
	cap_top.bottom_radius = 0.27
	cap_top.height = 0.1
	_add_part(parent, cap_top, cap, Vector3(0, 1.24, 0))

	var brim := CylinderMesh.new()
	brim.top_radius = 0.34
	brim.bottom_radius = 0.34
	brim.height = 0.025
	var brim_mi := _add_part(parent, brim, cap, Vector3(0, 1.2, -0.16))
	brim_mi.scale = Vector3(1.0, 1.0, 1.45)

	for sx in [-1.0, 1.0]:
		var eye := SphereMesh.new()
		eye.radius = 0.032
		eye.height = 0.064
		_add_part(parent, eye, Color("2a2433"), Vector3(sx * 0.09, 1.11, -0.215))

	var mouth := SphereMesh.new()
	mouth.radius = 0.035
	mouth.height = 0.07
	var mmi := _add_part(parent, mouth, Color("2a2433"), Vector3(0, 1.0, -0.22))
	mmi.scale = Vector3(1.5, 0.6, 0.5)

	# 身体 + 手 + 腿 + 背包。
	var torso := SphereMesh.new()
	torso.radius = 0.3
	torso.height = 0.6
	torso.radial_segments = 20
	torso.rings = 12
	var tmi := _add_part(parent, torso, shirt, Vector3(0, 0.42, 0))
	tmi.scale = Vector3(1.0, 1.3, 0.92)

	for sx in [-1.0, 1.0]:
		var arm := SphereMesh.new()
		arm.radius = 0.07
		arm.height = 0.14
		_add_part(parent, arm, skin, Vector3(sx * 0.32, 0.52, 0))
		var leg := CapsuleMesh.new()
		leg.radius = 0.07
		leg.height = 0.32
		leg.radial_segments = 12
		leg.rings = 5
		_add_part(parent, leg, pants, Vector3(sx * 0.12, 0.16, 0))

	var pack := BoxMesh.new()
	pack.size = Vector3(0.34, 0.4, 0.18)
	_add_part(parent, pack, Color("7ec850"), Vector3(0, 0.56, 0.28))

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

	for t in ["点击进入游戏并锁定鼠标", "运输船 PVE：艏艉两个出生区，中部集装箱堆可跳上去，两舷高架走道可俯瞰全船", "清空怪物自动刷下一波，击杀掉落金币，靠近自动拾取", "W A S D 移动 · Space 跳跃 · Shift 奔跑 · V 切换第一/第三人称 · Reset 重开", "匹配机制：凑齐 2 名玩家开局，10 秒无人加入则单人开局"]:
		var l := Label.new()
		l.text = t
		l.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
		l.add_theme_color_override("font_color", Color("9da7b3"))
		vbox.add_child(l)

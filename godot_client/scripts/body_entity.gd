extends Node3D
class_name BodyEntity
## 单个服务端刚体的可视化节点（全部程序化卡通模型，无外部美术资源）。
##
## - 简单刚体（箱子/靶球/弹丸）：单个 MeshInstance3D 子节点
## - 怪物：组装式卡通怪兽（大扁球身体 + 大眼睛 + 小角 + 短腿），带跑动弹跳
##
## 网格/材质只在形状签名变化时重建；位置/旋转由调用方每帧写入。

const MONSTER_BODY := Color("b78ef0")   # 紫粉身体
const MONSTER_BELLY := Color("efe0ff")
const MONSTER_DARK := Color("4a3a5a")   # 眼睛
const MONSTER_HORN := Color("ffd75e")

var _sig := ""
var _simple_mesh: MeshInstance3D = null
var _bob: Node3D = null
var _bob_phase := 0.0

func sync_from(body: Dictionary) -> void:
	var new_sig := _signature(body)
	if new_sig != _sig:
		_sig = new_sig
		_rebuild(body)

func _process(delta: float) -> void:
	if _bob != null and _bob.is_inside_tree():
		_bob_phase += delta * 7.0
		_bob.position.y = absf(sin(_bob_phase)) * 0.12 - 0.02

func _rebuild(body: Dictionary) -> void:
	_clear_all()
	if body.get("enemy", false):
		_build_monster(body)
	else:
		_build_simple(body)

func _clear_all() -> void:
	for child in get_children():
		remove_child(child)
		child.queue_free()
	_simple_mesh = null
	_bob = null

func _build_simple(body: Dictionary) -> void:
	var mi := MeshInstance3D.new()
	mi.name = "Mesh"
	mi.mesh = _make_mesh(body)
	mi.material_override = _make_material(body)
	add_child(mi)
	_simple_mesh = mi

func _build_monster(body: Dictionary) -> void:
	var r := maxf(0.25, float((body.get("size", [0.35, 0.5, 0.0]) as Array)[0]))

	_bob = Node3D.new()
	_bob.name = "Bob"
	add_child(_bob)

	# 身体：大扁球，肚皮更浅色的小球叠在前下方。
	var body_mesh := SphereMesh.new()
	body_mesh.radius = r
	body_mesh.height = r * 2.0
	body_mesh.radial_segments = 20
	body_mesh.rings = 10
	var bm := _add_part(_bob, body_mesh, MONSTER_BODY, Vector3(0, 0, 0))
	bm.scale = Vector3(1.05, 0.82, 1.0)

	var belly_mesh := SphereMesh.new()
	belly_mesh.radius = r * 0.72
	belly_mesh.height = r * 1.44
	var belly := _add_part(_bob, belly_mesh, MONSTER_BELLY, Vector3(0, -0.12, -r * 0.32))
	belly.scale = Vector3(1.0, 0.8, 0.72)

	# 大眼睛：白球 + 黑瞳（面朝 -z，与相机/角色朝向约定一致）。
	for sx in [-1.0, 1.0]:
		var eye := SphereMesh.new()
		eye.radius = r * 0.30
		eye.height = r * 0.60
		var em := _add_part(_bob, eye, Color.WHITE, Vector3(sx * r * 0.42, r * 0.28, -r * 0.74))
		em.scale = Vector3(1.0, 1.15, 0.62)

		var pupil := SphereMesh.new()
		pupil.radius = r * 0.14
		pupil.height = r * 0.28
		_add_part(_bob, pupil, MONSTER_DARK, Vector3(sx * r * 0.42, r * 0.27, -r * 0.9))

	# 小嘴（黑椭圆）。
	var mouth := SphereMesh.new()
	mouth.radius = r * 0.16
	mouth.height = r * 0.32
	var mm := _add_part(_bob, mouth, MONSTER_DARK, Vector3(0, -r * 0.18, -r * 0.82))
	mm.scale = Vector3(1.5, 0.62, 0.55)

	# 头顶黄色小角（CylinderMesh top_radius=0 即锥体）。
	var horn := CylinderMesh.new()
	horn.top_radius = 0.0
	horn.bottom_radius = r * 0.20
	horn.height = r * 0.55
	horn.radial_segments = 10
	for sx in [-1.0, 1.0]:
		var hm := _add_part(_bob, horn, MONSTER_HORN, Vector3(sx * r * 0.42, r * 0.78, 0))
		hm.rotation_degrees = Vector3(0, 0, sx * -16)

	# 两只小短腿（地面滚动感由弹跳动画承担）。
	var foot := SphereMesh.new()
	foot.radius = r * 0.22
	foot.height = r * 0.44
	_add_part(_bob, foot, MONSTER_DARK, Vector3(-r * 0.4, -r * 0.74, 0))
	_add_part(_bob, foot, MONSTER_DARK, Vector3(r * 0.4, -r * 0.74, 0))

func _add_part(parent: Node3D, mesh: Mesh, color: Color, pos: Vector3) -> MeshInstance3D:
	var mi := MeshInstance3D.new()
	mi.mesh = mesh
	mi.position = pos
	mi.cast_shadow = GeometryInstance3D.SHADOW_CASTING_SETTING_OFF
	var mat := StandardMaterial3D.new()
	mat.albedo_color = color
	mat.roughness = 0.55
	mat.metalness = 0.0
	mi.material_override = mat
	parent.add_child(mi)
	return mi

func _make_mesh(b: Dictionary) -> Mesh:
	var size: Array = b.get("size", [0.0, 0.0, 0.0])
	match int(b.get("type", 0)):
		2:  # 胶囊敌人：Jolt 半径 + 圆柱半高 -> Godot 总高。
			var r := maxf(0.05, float(size[0]))
			var half := maxf(0.05, float(size[1]))
			var cm := CapsuleMesh.new()
			cm.radius = r
			cm.height = (half + r) * 2.0
			cm.radial_segments = 16
			cm.rings = 6
			return cm
		1:  # 球：靶球或弹丸。
			var r2 := maxf(0.05, float(size[0]))
			var sm := SphereMesh.new()
			sm.radius = r2
			sm.height = r2 * 2.0
			if b.get("projectile", false):
				sm.radial_segments = 10
				sm.rings = 8
			else:
				sm.radial_segments = 24
				sm.rings = 16
			return sm
		_:  # 箱子 / 地板 / 墙：size 是半边长。
			var bm := BoxMesh.new()
			bm.size = Vector3(float(size[0]) * 2.0, float(size[1]) * 2.0, float(size[2]) * 2.0)
			return bm

func _make_material(b: Dictionary) -> StandardMaterial3D:
	var mat := StandardMaterial3D.new()
	if b.get("projectile", false):
		mat.shading_mode = BaseMaterial3D.SHADING_MODE_UNSHADED
		mat.albedo_color = Color("ffe066")
	elif b.get("enemy", false):
		mat.albedo_color = MONSTER_BODY
		mat.roughness = 0.55
	elif b.get("target", false):
		mat.albedo_color = Color("e5484d")
		mat.emission_enabled = true
		mat.emission = Color("550000")
		mat.roughness = 0.35
		mat.metalness = 0.1
	elif b.get("static", false):
		mat.albedo_color = Color("6e7681")
		mat.roughness = 0.9
	else:
		mat.albedo_color = Color("d08a4e")
		mat.roughness = 0.65
		mat.metalness = 0.05
	return mat

func _signature(b: Dictionary) -> String:
	var size: Array = b.get("size", [0.0, 0.0, 0.0])
	return "%d|%s|%s|%s|%s|%.5f" % [
		int(b.get("type", 0)), b.get("static", false), b.get("target", false),
		b.get("enemy", false), b.get("projectile", false), float(size[0]),
	]

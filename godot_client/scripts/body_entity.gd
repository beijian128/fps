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

## 场景视觉材质表：键与服务端 sim/map.go 的 Material 编号一一对应（只能追加）。
## 标记类刚体（弹丸/怪物/靶球）由各自的标志位先决定外观，不看材质号。
const MATS := {
	1: {"color": "4a545e", "rough": 0.80, "metal": 0.25}, # 甲板钢板
	2: {"color": "333d47", "rough": 0.70, "metal": 0.35}, # 船体外板
	3: {"color": "2f6fb5", "rough": 0.45, "metal": 0.40}, # 集装箱 · 蓝
	4: {"color": "b5443a", "rough": 0.45, "metal": 0.40}, # 集装箱 · 红
	5: {"color": "3f8f5a", "rough": 0.45, "metal": 0.40}, # 集装箱 · 绿
	6: {"color": "c07a2c", "rough": 0.45, "metal": 0.40}, # 集装箱 · 橙
	7: {"color": "8a6a3a", "rough": 0.75, "metal": 0.30}, # 高架走道格栅
	8: {"color": "9aa7b5", "rough": 0.40, "metal": 0.60}, # 栏杆
	9: {"color": "d08a4e", "rough": 0.65, "metal": 0.05}, # 木箱
	10: {"color": "6b7683", "rough": 0.50, "metal": 0.55}, # 桅杆/烟囱/系缆桩
	11: {"color": "556069", "rough": 0.80, "metal": 0.30}, # 舷梯踏步
}
## 集装箱材质号：额外加一圈顶/底包边，让方盒子看起来像货柜。
const CONTAINER_MATS := [3, 4, 5, 6]

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
	_add_container_trim(body)

## 集装箱的顶/底包边：在方盒子上下各套一圈略大、略暗的薄框，一眼能看出是货柜。
## 包边必须明显凸出箱体表面（y 方向探出半个厚度、xz 方向放大 2%）——与箱面共面
## 会 z-fighting，在平顶/侧面拉出满屏摩尔纹条纹。
func _add_container_trim(body: Dictionary) -> void:
	if not CONTAINER_MATS.has(int(body.get("mat", 0))):
		return
	var size: Array = body.get("size", [0.0, 0.0, 0.0])
	var sx := float(size[0])
	var sy := float(size[1])
	var sz := float(size[2])
	if sx <= 0.0 or sy <= 0.0 or sz <= 0.0:
		return
	var base := MATS[int(body.get("mat", 0))]["color"] as String
	for sign_y in [-1.0, 1.0]:
		var rim := MeshInstance3D.new()
		var bm := BoxMesh.new()
		bm.size = Vector3(sx * 2.0 * 1.02, 0.12, sz * 2.0 * 1.02)
		rim.mesh = bm
		rim.position.y = sign_y * sy
		rim.cast_shadow = GeometryInstance3D.SHADOW_CASTING_SETTING_ON
		var mat := StandardMaterial3D.new()
		mat.albedo_color = Color(base).darkened(0.45)
		mat.roughness = 0.5
		mat.metallic = 0.5
		rim.material_override = mat
		add_child(rim)

func _build_monster(body: Dictionary) -> void:
	var size_a: Array = body.get("size", [0.35, 0.5, 0.0])
	var r := maxf(0.25, float(size_a[0]))
	var half := maxf(0.0, float(size_a[1]))

	# 服务端敌人胶囊的 pos 是胶囊中心（底部 = 中心 - (半高 + 半径)），而卡通模型的
	# 最低点是腿部（局部约 -0.96*r）。把整个视觉组下移 (半高 + 半径) - 0.96*r，
	# 让怪物脚底贴地、与物理胶囊一致，而不是悬浮在半空。
	var ground := Node3D.new()
	ground.name = "GroundAnchor"
	ground.position.y = -(half + r) + r * 0.96
	add_child(ground)

	_bob = Node3D.new()
	_bob.name = "Bob"
	ground.add_child(_bob)

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
	mat.metallic = 0.0
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
		mat.metallic = 0.1
	else:
		# 场景刚体按服务端下发的材质号配色（甲板/船体/集装箱/走道/木箱…）。
		var m: Dictionary = MATS.get(int(b.get("mat", 0)), {})
		if m.is_empty():
			# 未标材质的刚体：静态用钢灰、动态用木色。
			if b.get("static", false):
				mat.albedo_color = Color("6e7681")
				mat.roughness = 0.9
			else:
				mat.albedo_color = Color("d08a4e")
				mat.roughness = 0.65
				mat.metallic = 0.05
		else:
			mat.albedo_color = Color(m.get("color", "6e7681"))
			mat.roughness = float(m.get("rough", 0.6))
			mat.metallic = float(m.get("metal", 0.0))
	return mat

func _signature(b: Dictionary) -> String:
	var size: Array = b.get("size", [0.0, 0.0, 0.0])
	return "%d|%s|%s|%s|%s|%d|%.5f|%.5f|%.5f" % [
		int(b.get("type", 0)), b.get("static", false), b.get("target", false),
		b.get("enemy", false), b.get("projectile", false), int(b.get("mat", 0)),
		float(size[0]), float(size[1]), float(size[2]),
	]

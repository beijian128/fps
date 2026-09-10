extends Node3D
class_name BodyEntity
## 单个服务端刚体的可视化节点（全部程序化卡通模型，无外部美术资源）。
##
## 每个可渲染刚体一个简单体（箱子/圆柱/球/弹丸），形状由 Body.Kind + Body.Size 决定。
## 玩家与远端玩家不走这里 —— 它们由 main.gd 的人形 Avatar 渲染。
##
## 网格/材质只在形状签名变化时重建；位置/旋转由调用方每帧写入。

## 场景视觉材质表：键与服务端 sim/map.go 的 Material 编号一一对应（只能追加）。
## 标记类刚体（弹丸）由标志位先决定外观，不看材质号。
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

func sync_from(body: Dictionary) -> void:
	var new_sig := _signature(body)
	if new_sig != _sig:
		_sig = new_sig
		_rebuild(body)

func _rebuild(body: Dictionary) -> void:
	_clear_all()
	_build_simple(body)

func _clear_all() -> void:
	for child in get_children():
		remove_child(child)
		child.queue_free()
	_simple_mesh = null

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

func _make_mesh(b: Dictionary) -> Mesh:
	var size: Array = b.get("size", [0.0, 0.0, 0.0])
	match int(b.get("type", 0)):
		2:  # 胶囊（桅杆/烟囱等立柱）：Jolt 半径 + 圆柱半高 -> Godot 总高。
			var r := maxf(0.05, float(size[0]))
			var half := maxf(0.05, float(size[1]))
			var cm := CapsuleMesh.new()
			cm.radius = r
			cm.height = (half + r) * 2.0
			cm.radial_segments = 16
			cm.rings = 6
			return cm
		1:  # 球：弹丸。
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
	return "%d|%s|%s|%d|%.5f|%.5f|%.5f" % [
		int(b.get("type", 0)), b.get("static", false),
		b.get("projectile", false), int(b.get("mat", 0)),
		float(size[0]), float(size[1]), float(size[2]),
	]

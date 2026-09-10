extends SceneTree
## main.gd 渲染路径回归：加载真实场景、手动喂合成帧，断言节点的新增/更新/销毁，
## 以及两条时序要求 —— 本地/远端插值、以及「一帧内最后一次写必须是插值结果」。
## 服务端缺席也无妨：这里只驱动 _on_frame，不碰 WebSocket。
##
## 注意：GDScript 没有 try/catch，测试函数中途抛错会被吞掉、整个用例"假绿"，
## 所以沿用 frame_decode_test.gd 的完成标记模式。

const SCHEMA := [
	{"id": 1, "name": "Pos", "kind": 5},
	{"id": 2, "name": "Health", "kind": 0},
	{"id": 3, "name": "Enemy", "kind": 2},
	{"id": 4, "name": "Body.Mat", "kind": 1},
	{"id": 5, "name": "Body.Kind", "kind": 1},
	{"id": 6, "name": "Body.Size", "kind": 5},
	{"id": 7, "name": "Body.Static", "kind": 2},
	{"id": 8, "name": "Body.Active", "kind": 2},
	{"id": 9, "name": "Rot", "kind": 6},
	{"id": 10, "name": "Resource.Kind", "kind": 1},
	{"id": 11, "name": "Player.Idx", "kind": 1},
	{"id": 12, "name": "Game.Score", "kind": 1},
	{"id": 13, "name": "Game.Wave", "kind": 1},
	{"id": 14, "name": "Game.Gold", "kind": 1},
	{"id": 15, "name": "Facing", "kind": 0},
]

# 用例完成标记。GDScript 无 try/catch：某个测试函数中途抛错会静默返回、_failures 仍为 0，
# 整个用例就"假绿"—— 每个函数末尾打标记，这里逐个核对。
var _failures := 0
var _done := {}
var _main: Node = null

func _initialize() -> void:
	_run()

func _run() -> void:
	_main = load("res://scenes/main.tscn").instantiate()
	root.add_child(_main)
	# _initialize 阶段 root 还没进树，_ready 不会触发（HUD 节点全是 null）；等两帧让
	# 场景真正入树、_ready 跑完（与 reconnect_cleanup_test.gd 同一套路）。
	await process_frame
	await process_frame
	_main._on_matched({"player_idx": 0})
	_test_full_frame_creates_bodies()
	_test_delta_updates_and_destroys()
	_test_last_write_in_frame_is_interpolated()
	_test_local_player_and_remote_yaw_interpolated()
	_test_coin_has_no_extra_body()
	_test_destroy_for_unknown_id_is_safe()
	for name: String in ["full", "delta", "interp_node", "interp_player", "coin", "unknown"]:
		if not _done.has(name):
			_failures += 1
			printerr("FAIL: 用例 %s 没跑完（中途抛错了？）" % name)
	if _failures > 0:
		printerr("game_frame_test: %d 项失败" % _failures)
		quit(1)
	else:
		print("game_frame_test: OK")
		quit(0)

func _check(cond: bool, msg: String) -> void:
	if not cond:
		_failures += 1
		printerr("FAIL: " + msg)

func _attr(id: int, d: Dictionary) -> Dictionary:
	var out := {"id": id}
	out.merge(d)
	return out

## 一个可渲染刚体的最小属性集（Body.Kind/Size/Static/Active/Mat + Pos/Rot）。
func _body_attrs(kind: int, size: Array, pos: Vector3, mat: int = 1) -> Array:
	return [
		_attr(5, {"i": kind}),                        # Body.Kind
		_attr(6, {"f": size}),                        # Body.Size
		_attr(7, {"b": true}),                        # Body.Static
		_attr(8, {"b": true}),                        # Body.Active
		_attr(4, {"i": mat}),                         # Body.Mat
		_attr(1, {"f": [pos.x, pos.y, pos.z]}),       # Pos
		_attr(9, {"f": [0.0, 0.0, 0.0, 1.0]}),        # Rot
	]

## 玩家实体属性：刚体属性之外再带 Player.Idx / Health / Facing。
## 必须带全刚体属性 —— _refresh_derived 会把它们当刚体渲染，_build_body_dict 会逐项取用。
func _player_attrs(idx: int, pos: Vector3, facing: float) -> Array:
	var out := _body_attrs(0, [0.4, 1.8, 0.4], pos, 0)
	out.append(_attr(11, {"i": idx}))                 # Player.Idx
	out.append(_attr(2, {"f": [100.0]}))              # Health
	out.append(_attr(15, {"f": [facing]}))            # Facing
	return out

## 一帧只含一个刚体（id 100）。
func _test_full_frame_creates_bodies() -> void:
	_main._on_frame({"full": true, "step": 1, "schema": {"fields": SCHEMA, "version": 1}, "entities": [
		{"id": 100, "set": _body_attrs(0, [0.5, 0.5, 0.5], Vector3(1, 2, 3))},
	]})
	_check(_main._entities.has(100), "全量帧应在场景里建出实体 100 的节点")
	_check((_main._entities[100].global_position - Vector3(1, 2, 3)).length() < 0.001,
		"节点应落在 Pos 上")
	_done["full"] = true

## 增量帧：移动 + 销毁。
func _test_delta_updates_and_destroys() -> void:
	_main._on_frame({"full": false, "entities": [
		{"id": 100, "set": [_attr(1, {"f": [4.0, 5.0, 6.0]})]},
	]})
	_check((_main._body_xform[100]["pos"] - Vector3(4, 5, 6)).length() < 0.001,
		"增量帧应更新刚体变换缓存")

	_main._on_frame({"full": false, "entities": [
		{"id": 100, "destroy": true},
	]})
	_check(not _main._entities.has(100), "destroy 后渲染节点必须被删掉")
	_done["delta"] = true

## 钉住修正 #3：Godot 先跑父节点 _process（内含 _render_interpolated）再跑子节点
## FpsClient._process，后者同步 emit frame_received → _on_frame → _reconcile_scene 把
## **原始**变换写进节点。若 _on_frame 收尾不再插值一次，本渲染帧画的就永远是 raw 值，
## 下一帧才被拉回去 —— 每来一帧抖一下。
func _test_last_write_in_frame_is_interpolated() -> void:
	var a := Vector3(0, 0, 0)
	var b := Vector3(20, 0, 0)
	_main._on_frame({"full": false, "entities": [
		{"id": 101, "set": _body_attrs(0, [0.5, 0.5, 0.5], a)},
	]})
	# 再推一帧把 101 瞬移到 B：_on_frame 内 prev=A、cur=B，_reconcile_scene 先把节点写成 B，
	# 收尾 _render_interpolated 用刚到达的 alpha(≈0) 把节点拉回 ≈A。
	_main._on_frame({"full": false, "entities": [
		{"id": 101, "set": [_attr(1, {"f": [b.x, b.y, b.z]})]},
	]})
	var node: Node3D = _main._entities.get(101)
	_check(node != null, "实体 101 的渲染节点应存在")
	if node != null:
		var d_a := (node.global_position - a).length()
		var d_b := (node.global_position - b).length()
		_check(d_b > 0.5,
			"帧内最后一次写不应是原始缓存值 %s；实际 %s" % [b, node.global_position])
		_check(d_a < d_b,
			"帧内最后一次写应是插值结果（更接近 prev %s 而非 cur %s）；实际 %s"
			% [a, b, node.global_position])
	_done["interp_node"] = true

## 钉住修正 #2：本地玩家位置与远端朝向必须插值。
## 之前 _player_pos 收到帧就直接赋 raw，第一人称相机与第三人称 avatar 按 20 Hz 跳步
## （走 0.4 m/步、跑 0.7 m/步）；_remote_yaw 同理 —— 远端 avatar 台阶式旋转，
## 180° 附近还会因角度回绕翻一下。
func _test_local_player_and_remote_yaw_interpolated() -> void:
	var local_a := Vector3(0, 0.2, 18)
	var local_b := Vector3(0, 0.2, 10)
	var remote_a := Vector3(0, 0.2, -18)
	var remote_b := Vector3(0, 0.2, -10)
	_main._on_frame({"full": false, "entities": [
		{"id": 300, "set": _player_attrs(0, local_a, 0.0)},
		{"id": 301, "set": _player_attrs(1, remote_a, 0.0)},
	]})
	# 第二帧：本地瞬移到 B；远端瞬移到 B 且 Facing 从 0 转到 PI（正好 180°）。
	_main._on_frame({"full": false, "entities": [
		{"id": 300, "set": [_attr(1, {"f": [local_b.x, local_b.y, local_b.z]})]},
		{"id": 301, "set": [
			_attr(1, {"f": [remote_b.x, remote_b.y, remote_b.z]}),
			_attr(15, {"f": [PI]}),
		]},
	]})
	var d_local_a: float = (_main._player_pos - local_a).length()
	var d_local_b: float = (_main._player_pos - local_b).length()
	_check(d_local_a < d_local_b,
		"本地玩家位置应是插值结果（更接近上一帧 %s 而非 raw %s）；实际 %s"
		% [local_a, local_b, _main._player_pos])
	# 远端朝向必须走 lerp_angle：raw 赋值会让它从 0 直接硬跳到 PI。
	var remote_yaw: float = _main._remote_yaw
	var yaw_err: float = absf(angle_difference(0.0, remote_yaw))
	_check(yaw_err < 0.1,
		"远端朝向应是插值结果（≈上一帧 0），而不是 raw 值 PI；实际 %s" % remote_yaw)
	_done["interp_player"] = true

## 金币在 store 里是普通刚体，但**不该**额外长出一个灰色的刚体球。
func _test_coin_has_no_extra_body() -> void:
	_main._on_frame({"full": false, "entities": [
		{"id": 200, "set": [
			_attr(5, {"i": 1}), _attr(6, {"f": [0.6]}), _attr(7, {"b": true}),
			_attr(8, {"b": true}), _attr(4, {"i": 0}),
			_attr(1, {"f": [0.0, 0.8, 0.0]}), _attr(9, {"f": [0.0, 0.0, 0.0, 1.0]}),
			_attr(10, {"i": 0}),                      # Resource.Kind
		]},
	]})
	_check(not _main._entities.has(200), "金币不应被当成刚体建出额外节点")
	_check(_main._res_nodes.has(200), "金币应建出金币节点")
	_done["coin"] = true

## 服务端可能对客户端从未见过的 id 发 destroy（比如客户端刚 resync 完）。
## 真正的断言是「没抛错」—— 由上面的 _done 完成标记保证（抛错就到不了这一行）。
func _test_destroy_for_unknown_id_is_safe() -> void:
	_main._on_frame({"full": false, "entities": [
		{"id": 999, "destroy": true},
	]})
	_done["unknown"] = true

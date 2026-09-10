extends SceneTree
## 开发回归测试：验证断线时 _on_connection(false) 会把刚体渲染实体、本地世界
## （WorldStore）与插值状态全部清空（否则旧刚体以冻结姿势残留到重连后的 full 帧
## 才被协调掉，视觉上是整场重建的闪跳）。
## 运行：godot --headless --path <项目> --script res://tests/reconnect_cleanup_test.gd
##（无需服务端：直接驱动主场景内部状态。）

func _initialize() -> void:
	_run()

func _check(cond: bool, msg: String) -> bool:
	if not cond:
		print("RECONNECT_CLEANUP FAIL: ", msg)
		quit(1)
		return false
	return true

func _run() -> void:
	var game = load("res://scenes/main.tscn").instantiate()
	root.add_child(game)
	await process_frame
	await process_frame

	# 模拟连接中残留的状态：一个渲染刚体节点 + 一帧插值状态 + 本地世界。
	var be = preload("res://scripts/body_entity.gd").new()
	root.add_child(be)
	game._entities[7] = be
	game._body_xform = {7: {"pos": Vector3.ZERO, "quat": Quaternion()}}
	game._prev_body_xform = {7: {"pos": Vector3.ZERO, "quat": Quaternion()}}
	game._store.apply_schema([{"id": 1, "name": "Pos", "kind": 5}])
	game._store.apply_frame({"full": false, "entities": [
		{"id": 7, "set": [{"id": 1, "f": [1.0, 2.0, 3.0]}]},
	]})

	game._on_connection(false)
	await process_frame

	if not _check(game._entities.is_empty(), "断线后 _entities 应被清空"): return
	# 节点已被 queue_free：用 is_instance_valid 检查（对已释放对象调方法会抛错）。
	if not _check(not is_instance_valid(be), "刚体渲染节点应被释放"): return
	# 插值状态与本地世界必须一起清空：重连后第一帧没有「上一帧」，直接在服务端下发的
	# full 帧位置落位，而不是从断线前的旧变换插值过去（那会是一段可见的跳变）。
	if not _check(game._body_xform.is_empty() and game._prev_body_xform.is_empty(),
			"断线后插值状态应清空"): return
	if not _check(game._store.entity_ids().is_empty(), "断线后本地世界应清空"): return

	print("RECONNECT_CLEANUP PASS")
	quit(0)

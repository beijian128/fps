extends SceneTree
## 开发回归测试：验证断线时 _on_connection(false) 会把刚体渲染实体、金币节点与
## "上一帧弹丸"全部清空（否则旧刚体以冻结姿势残留、重连后旧弹丸消失被误判命中）。
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

	# 模拟连接中残留的状态：一个渲染刚体节点 + 一枚金币节点 + 上一帧的弹丸。
	var be = preload("res://scripts/body_entity.gd").new()
	root.add_child(be)
	game._entities[7] = be
	var coin := Node3D.new()
	root.add_child(coin)
	game._res_nodes[5] = {"node": coin}
	game._prev_projectiles = {3: Vector3.ZERO}
	game._next_snap = {"step": 40, "bodies": {}}
	game._prev_snap = {"step": 39, "bodies": {}}

	game._on_connection(false)
	await process_frame

	if not _check(game._entities.is_empty(), "断线后 _entities 应被清空"): return
	if not _check(game._prev_projectiles.is_empty(), "断线后 _prev_projectiles 应被清空"): return
	if not _check(game._res_nodes.is_empty(), "断线后金币节点表应被清空"): return
	# 节点已被 queue_free：用 is_instance_valid 检查（对已释放对象调方法会抛错）。
	if not _check(not is_instance_valid(be), "刚体渲染节点应被释放"): return
	if not _check(not is_instance_valid(coin), "金币节点应被释放"): return
	if not _check(game._next_snap.is_empty() and game._prev_snap.is_empty(), "断线后快照缓冲应清空"): return
	if not _check(game._snap_sig == "", "断线后内容摘要应重置"): return

	print("RECONNECT_CLEANUP PASS")
	quit(0)

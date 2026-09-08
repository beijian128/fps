extends SceneTree
## 开发回归测试（不进发布包）：验证快照时序——"同 step 但内容已变"的补推帧会被
## 接受。修复前客户端在 _store_snapshot 里按 step 逐字去重，服务端 shoot/reset 后
## 的"立即补推"（与上一帧同 step、多了一枚弹丸）会被丢掉，导致弹丸延迟一个 tick
## 才渲染。
##
## 运行：godot --headless --path <项目> --script res://tests/snapshot_same_step_test.gd
## 注意：该测试直接驱动主场景的 _store_snapshot，请在服务端未启动时运行
##（避免真实快照与合成快照互相干扰）。

func _initialize() -> void:
	_run()

func _check(cond: bool, msg: String) -> bool:
	if not cond:
		print("SNAP_DEDUP FAIL: ", msg)
		quit(1)
		return false
	return true

func _run() -> void:
	var game = load("res://scenes/main.tscn").instantiate()
	root.add_child(game)
	await process_frame
	await process_frame

	var base := {
		"bodies": [],
		"resources": [],
		"player": {"pos": [0.0, 0.2, 12.0], "health": 100.0},
		"step": 50, "score": 0, "wave": 1, "gold": 0,
	}
	var proj := {"id": 3, "type": 1, "static": false, "target": false, "enemy": false,
		"projectile": true, "pos": [1.0, 1.0, -1.0], "quat": [0.0, 0.0, 0.0, 1.0],
		"size": [0.08, 0.0, 0.0], "health": 0.0, "active": true}

	# 1. 首帧（step 50，无刚体）。
	game._store_snapshot(base)
	if not _check(not game._next_snap.is_empty(), "first snapshot not stored"): return
	if not _check(int(game._next_snap["step"]) == 50, "first step mismatch"): return

	# 2. 同 step 50、但多了一枚弹丸（服务端 shoot 后的立即补推）→ 必须被接受。
	var f2 := base.duplicate(true)
	f2["bodies"] = [proj]
	game._store_snapshot(f2)
	if not _check((game._next_snap["bodies"] as Dictionary).has(3), "same-step new body was dropped"): return

	# 3. 同 step 50、内容完全相同 → 忽略（内容与到达时间 t 都不变）。
	var t_before: float = game._next_snap["t"]
	game._store_snapshot(f2)
	if not _check((game._next_snap["bodies"] as Dictionary).size() == 1, "duplicate changed content"): return
	if not _check(absf(game._next_snap["t"] - t_before) < 0.0001, "duplicate refreshed arrival t"): return

	# 4. 迟到的旧内容帧（同 step、删掉了已有弹丸）→ 跳过，不回退。
	var f3 := base.duplicate(true)
	game._store_snapshot(f3)
	if not _check((game._next_snap["bodies"] as Dictionary).has(3), "stale same-step frame regressed bodies"): return

	# 5. 下一 tick（step 51）→ 正常滚动双缓冲。
	var f4 := base.duplicate(true)
	f4["step"] = 51
	f4["player"] = {"pos": [0.5, 0.2, 12.0], "health": 100.0}
	game._store_snapshot(f4)
	if not _check(int(game._next_snap["step"]) == 51, "step+1 scroll failed"): return
	if not _check((game._next_snap["bodies"] as Dictionary).is_empty(), "step+1 bodies mismatch"): return

	print("SNAP_DEDUP PASS")
	quit(0)

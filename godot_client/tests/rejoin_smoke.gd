extends SceneTree
## 断线回局冒烟测试（需要真实集群，同 ws_smoke.gd）。
##
## 验证本设计的核心验收点：同一个客户端 token 重连之后回到**同一局** ——
## 同一个 match_id、同一个 player_idx，并且收到 full 帧把世界重建出来。
## 这是全计划里唯一端到端验证「重连回同一局」的地方，改动匹配/回局链路后必跑。
##
## 运行：先起 etcd+nats + gate/match/game 三进程，再：
##   godot --headless --path <项目> --script res://tests/rejoin_smoke.gd

var _first_match_id := ""
var _first_idx := -1
var _second_match_id := ""
var _second_idx := -1
var _frames := 0
var _full_frames := 0

func _initialize() -> void:
	_run()

func _wait_connected(c: Node, ms: int) -> bool:
	var deadline := Time.get_ticks_msec() + ms
	while not c.connected and Time.get_ticks_msec() < deadline:
		await process_frame
	return c.connected

func _wait_until(fn: Callable, ms: int) -> bool:
	var deadline := Time.get_ticks_msec() + ms
	while not fn.call() and Time.get_ticks_msec() < deadline:
		await process_frame
	return fn.call()

func _new_client() -> Node:
	var c: Node = load("res://scripts/fps_client.gd").new()
	root.add_child(c)
	return c

func _run() -> void:
	# ---- 第一次连接 + 匹配 ----
	var c1 := _new_client()
	if not await _wait_connected(c1, 5000):
		print("REJOIN first ws open failed")
		quit(1)
		return
	var got1 := [false]
	var first_full := [0]
	c1.frame_received.connect(func(f: Dictionary) -> void:
		if bool(f.get("full", false)):
			first_full[0] += 1)
	c1.matched_received.connect(func(r: Dictionary) -> void:
		_first_match_id = String(r.get("match_id", ""))
		_first_idx = int(r.get("player_idx", -1))
		got1[0] = true
		c1.send_resync())
	if not await _wait_until(func() -> bool: return got1[0], 15000):
		print("REJOIN first match failed")
		quit(1)
		return
	print("REJOIN first match_id=", _first_match_id, " idx=", _first_idx)
	# 解析失败时拿到的就是默认值（"" / -1），与「真值」无从区分。若两边都解析失败，
	# 末尾的 match_id/idx 比较会因为「等于对方」而假通过 —— 而这是全计划唯一端到端
	# 验证「重连回同一局」的证据，必须显式判失败。
	if _first_match_id == "" or _first_idx < 0:
		printerr("FAIL: 首次 onMatched 载荷未解出 match_id/player_idx（match_id=%s idx=%d）"
			% [_first_match_id, _first_idx])
		quit(1)
		return
	await _wait_until(func() -> bool: return first_full[0] >= 1, 5000)
	if first_full[0] < 1:
		printerr("FAIL: 首次进入也应收到一帧 full（resync），得到 %d" % first_full[0])
		quit(1)
		return

	# ---- 断线：丢掉旧客户端，用同一个 token 重新建一个 ----
	# token 存在 user://client_id.txt，同一进程内两次读取拿到同一个值，
	# 正是重连时服务端用来找回对局实例的身份。
	c1.queue_free()
	await process_frame

	var c2 := _new_client()
	var got2 := [false]
	c2.matched_received.connect(func(r: Dictionary) -> void:
		_second_match_id = String(r.get("match_id", ""))
		_second_idx = int(r.get("player_idx", -1))
		got2[0] = true
		# 与 main.gd 的 _on_matched 一致：收到匹配结果就主动请求全量。
		# 服务端不会主动补发 —— 客户端不 resync 的话只会一直收到增量。
		c2.send_resync())
	c2.frame_received.connect(func(f: Dictionary) -> void:
		_frames += 1
		if bool(f.get("full", false)):
			_full_frames += 1)

	if not await _wait_connected(c2, 5000):
		print("REJOIN second ws open failed")
		quit(1)
		return
	if not await _wait_until(func() -> bool: return got2[0], 15000):
		print("REJOIN second match failed")
		quit(1)
		return
	print("REJOIN second match_id=", _second_match_id, " idx=", _second_idx)
	# 同上：比较前先确认第二次载荷真的解析出了值，否则两边的默认值相同会假通过。
	if _second_match_id == "" or _second_idx < 0:
		printerr("FAIL: 重连 onMatched 载荷未解出 match_id/player_idx（match_id=%s idx=%d）"
			% [_second_match_id, _second_idx])
		quit(1)
		return

	# 客户端收到 onMatched 后会发 resync，服务端下一帧回 full。
	await _wait_until(func() -> bool: return _frames >= 20, 8000)

	var ok := true
	if _second_match_id != _first_match_id:
		printerr("FAIL: 重连应回到同一局，得到 %s 期望 %s" % [_second_match_id, _first_match_id])
		ok = false
	if _second_idx != _first_idx:
		printerr("FAIL: 重连应回到同一槽位，得到 %d 期望 %d" % [_second_idx, _first_idx])
		ok = false
	if _full_frames < 1:
		printerr("FAIL: 重连后应至少收到一帧 full（resync），得到 %d" % _full_frames)
		ok = false

	print("REJOIN frames=", _frames, " full_frames=", _full_frames)
	print("REJOIN ", "OK" if ok else "FAILED")
	quit(0 if ok else 1)

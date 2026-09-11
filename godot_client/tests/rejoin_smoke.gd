extends SceneTree
## 断线回局冒烟测试（需要真实集群，同 ws_smoke.gd）。
##
## 验证本设计的核心验收点：同一个账号重连之后回到**同一局** ——
## 同一个 match_id、同一个 player_idx，并且收到 full 帧把世界重建出来。
## 这是全计划里唯一端到端验证「重连回同一局」的地方，改动匹配/回局链路后必跑。
##
## 运行：先起 etcd + nats + redis + gate/account/match/game 四进程，再：
##   godot --headless --path <项目> --script res://tests/rejoin_smoke.gd
##
## 身份现在来自登录：JoinMsg 已经不带任何凭证，match 只认会话绑定的 uid
## （未绑定的 join 会被静默忽略）。所以两次连接都必须先认证：第一次注册一个
## 新账号，第二次靠落盘的 token 走 resume —— 后者正是真实的断线重连路径。

const PASSWORD := "rejoinpass"

var _first_match_id := ""
var _first_idx := -1
var _second_match_id := ""
var _second_idx := -1
var _frames := 0
var _full_frames := 0
var _username := ""

func _initialize() -> void:
	_run()

## _clear_saved_credentials 抹掉本地凭证，让本轮从「没登录过」开始。
##
## 不清的话第一个客户端会直接 resume 成上一轮的账号，而那个账号很可能还挂着
## 上一轮的对局实例 —— 第一次 join 就走回局分支，测的就不是「新开一局再回去」了。
func _clear_saved_credentials() -> void:
	for p in ["user://auth_token.txt", "user://last_username.txt"]:
		if FileAccess.file_exists(p):
			DirAccess.remove_absolute(ProjectSettings.globalize_path(p))

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

## _on_auth 是两次连接共用的认证回调：本地无凭证就注册，认证成功就进匹配。
##
## state 用字典回传结果（"ok" / "fail"），调用方 await 它出现再往下走。
## match.join 只能在这里发 —— 会话绑定之前发出去的 join 会被 match 丢掉。
func _on_auth(c: Node, result: Dictionary, state: Dictionary) -> void:
	if bool(result.get("ok", false)):
		state["ok"] = true
		c.send_match_join()
		return
	var reason := String(result.get("reason", ""))
	if reason == "no_token" and not state.has("registering"):
		state["registering"] = true
		c.send_register(_username, PASSWORD)
		return
	state["fail"] = reason

func _run() -> void:
	_clear_saved_credentials()
	_username = "rejoin_%d" % (Time.get_ticks_usec() % 100000000)

	# ---- 第一次连接：注册 + 匹配 ----
	var c1 := _new_client()
	var got1 := [false]
	var first_full := [0]
	var auth1 := {}
	# 信号必须在任何 await 之前连上：握手应答可能在下一帧就回来。
	c1.login_result.connect(func(r: Dictionary) -> void: _on_auth(c1, r, auth1))
	c1.frame_received.connect(func(f: Dictionary) -> void:
		if bool(f.get("full", false)):
			first_full[0] += 1)
	c1.matched_received.connect(func(r: Dictionary) -> void:
		_first_match_id = String(r.get("match_id", ""))
		_first_idx = int(r.get("player_idx", -1))
		got1[0] = true
		c1.send_resync())

	if not await _wait_connected(c1, 5000):
		print("REJOIN first ws open failed")
		quit(1)
		return
	if not await _wait_until(func() -> bool: return auth1.has("ok") or auth1.has("fail"), 10000):
		print("REJOIN first login timed out (no LoginReply)")
		quit(1)
		return
	if auth1.has("fail"):
		printerr("FAIL: 首次注册失败 reason=%s" % auth1["fail"])
		quit(1)
		return
	print("REJOIN registered user=", _username)

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

	# ---- 断线：丢掉旧客户端，新客户端用落盘的 token 走 resume ----
	# token 存在 user://auth_token.txt，第一个客户端注册成功时写下的。新客户端
	# 握手后会自动带它发 account.resume，服务端据此把会话绑回同一个账号 ——
	# 而 match 的回局查询正是按账号 uid 定址的（见 match.tryRejoin）。
	c1.queue_free()
	await process_frame

	var c2 := _new_client()
	var got2 := [false]
	var auth2 := {}
	c2.login_result.connect(func(r: Dictionary) -> void: _on_auth(c2, r, auth2))
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
	if not await _wait_until(func() -> bool: return auth2.has("ok") or auth2.has("fail"), 10000):
		print("REJOIN second login timed out (no LoginReply)")
		quit(1)
		return
	if auth2.has("fail"):
		printerr("FAIL: 重连 resume 失败 reason=%s" % auth2["fail"])
		quit(1)
		return
	# 重连必须走 resume 而不是重新注册：注册一个新账号的话 uid 变了，回局查询
	# 必然落空，「回到同一局」就无从谈起（会静默变成开新局，看起来像通过）。
	if auth2.has("registering"):
		printerr("FAIL: 重连应凭本地 token 走 resume，却退回了注册（token 没落盘？）")
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

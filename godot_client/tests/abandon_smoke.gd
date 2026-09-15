extends SceneTree
## 放弃对局的端到端冒烟测试（需要真实集群，同 rejoin_smoke.gd）。
##
## 验收两条**只能端到端验证**的语义：
##   1. 登录进大厅时 `match.pending` 能报出那场没打完的局（同一个账号、同一 match_id）；
##   2. 放弃对局（`match.abandon`）**只释放当前玩家** —— 他自己不再被回局查询命中、
##      也不再被塞回那一局，而**对手那一局照常收到帧**（实例没有被终结）。
##
## 运行：先起 etcd + nats + redis + gate/account/match/game 五进程，再：
##   godot --headless --path <项目> --script res://tests/abandon_smoke.gd

const PairHelper := preload("res://tests/pair_helper.gd")
const PASSWORD := "abandonpass"

var _match_id := ""
var _my_idx := -1
var _username := ""
var _peer = null            # 对等客户端：单人兜底已删除，必须有第二个真实玩家才能开局

func _initialize() -> void:
	_run()

## _clear_saved_credentials 抹掉本地凭证，让本轮从「没登录过」开始。
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

func _persist_token(token: String, name: String) -> void:
	var f := FileAccess.open("user://auth_token.txt", FileAccess.WRITE)
	if f != null:
		f.store_string(token)
		f.close()
	var u := FileAccess.open("user://last_username.txt", FileAccess.WRITE)
	if u != null:
		u.store_string(name)
		u.close()

func _fail(msg: String) -> void:
	printerr("FAIL: " + msg)
	quit(1)

func _run() -> void:
	_clear_saved_credentials()
	_username = "abandon_%d" % (Time.get_ticks_usec() % 100000000)

	# ---- 第一步：注册 + 匹配进局 ----
	var c1 := _new_client()
	var matched1 := [false]
	var auth1 := {}
	var registering := [false]
	c1.login_result.connect(func(r: Dictionary) -> void:
		if bool(r.get("ok", false)):
			auth1["ok"] = true
			c1.send_match_join()
			return
		if String(r.get("reason", "")) == "no_token" and not registering[0]:
			registering[0] = true
			c1.send_register(_username, PASSWORD)
			return
		auth1["fail"] = String(r.get("reason", "")))
	c1.matched_received.connect(func(r: Dictionary) -> void:
		_match_id = String(r.get("match_id", ""))
		_my_idx = int(r.get("player_idx", -1))
		matched1[0] = true
		c1.send_resync())

	_peer = PairHelper.new(root, "abandonpeer")

	if not await _wait_connected(c1, 5000):
		_fail("首个 WS 没连上")
		return
	if not await _wait_until(func() -> bool: return auth1.has("ok") or auth1.has("fail"), 10000):
		_fail("首次登录超时（没有 LoginReply）")
		return
	if auth1.has("fail"):
		_fail("首次注册失败 reason=%s" % auth1["fail"])
		return
	if not await _wait_until(func() -> bool: return matched1[0], 15000):
		_fail("首次匹配失败（对等玩家没进来？）")
		return
	if _match_id == "" or _my_idx < 0:
		_fail("首次 onMatched 载荷没解出 match_id/player_idx（match_id=%s idx=%d）" % [_match_id, _my_idx])
		return
	print("ABANDON first match_id=", _match_id, " idx=", _my_idx)

	# ---- 第二步：客户端「重启」（丢掉旧连接，用落盘凭证 resume）----
	# 对等客户端注册时会把 auth_token.txt 覆盖成它自己的凭证，所以先把 c1 的写回去。
	_persist_token(c1.client_token, _username)
	c1.queue_free()
	await process_frame

	var c2 := _new_client()
	var auth2 := {}
	var pending := [{}]
	var matched2 := [false]
	c2.login_result.connect(func(r: Dictionary) -> void:
		if bool(r.get("ok", false)):
			auth2["ok"] = true
			# 与真实界面一致：进大厅的第一件事就是问「有没有没打完的局」。
			c2.send_pending_match()
			return
		auth2["fail"] = String(r.get("reason", "")))
	c2.pending_match_received.connect(func(r: Dictionary) -> void: pending[0] = r)
	c2.matched_received.connect(func(_r: Dictionary) -> void: matched2[0] = true)

	if not await _wait_connected(c2, 5000):
		_fail("重连 WS 没连上")
		return
	if not await _wait_until(func() -> bool: return auth2.has("ok") or auth2.has("fail"), 10000):
		_fail("resume 超时")
		return
	if auth2.has("fail"):
		_fail("resume 失败 reason=%s" % auth2["fail"])
		return
	if not await _wait_until(func() -> bool: return not pending[0].is_empty(), 8000):
		_fail("match.pending 没有应答")
		return

	var pending_found := bool(pending[0].get("found", false))
	var pending_id := String(pending[0].get("match_id", ""))
	print("ABANDON pending found=", pending_found, " match_id=", pending_id)
	if not pending_found:
		_fail("同一账号重连后应查到那场没打完的局（found=false）")
		return
	if pending_id != _match_id:
		_fail("pending 报的对局应是同一局：得到 %s 期望 %s" % [pending_id, _match_id])
		return
	# 查询不能顺手把人塞回对局 —— 玩家还没做选择。
	if matched2[0]:
		_fail("match.pending 不该把人推进对局（onMatched 提前到了）")
		return

	# ---- 第三步：放弃对局 ----

	var abandon := [{}]
	c2.abandon_match_received.connect(func(r: Dictionary) -> void: abandon[0] = r)
	c2.send_abandon_match()
	if not await _wait_until(func() -> bool: return not abandon[0].is_empty(), 8000):
		_fail("match.abandon 没有应答")
		return
	print("ABANDON reply ok=", abandon[0].get("ok", false), " reason=", abandon[0].get("reason", ""))
	if not bool(abandon[0].get("ok", false)):
		_fail("放弃对局应成功，得到 %s" % str(abandon[0]))
		return

	# ---- 第四步：对手那一局照常继续（实例没有被终结）----
	var peer_client: Node = _peer.client
	var peer_frames := [0]
	peer_client.frame_received.connect(func(_f: Dictionary) -> void: peer_frames[0] += 1)
	var deadline := Time.get_ticks_msec() + 2000
	while Time.get_ticks_msec() < deadline:
		await process_frame
	print("ABANDON peer frames after abandon=", peer_frames[0])
	if peer_frames[0] < 20:
		_fail("放弃后对手应继续收到帧（2 秒 ≥ 40 帧），得到 %d —— 实例被终结了？"
			% peer_frames[0])
		return

	# ---- 第五步：被释放的人不再被领回那一局 ----
	var pending2 := [{}]
	c2.pending_match_received.connect(func(r: Dictionary) -> void: pending2[0] = r)
	c2.send_pending_match()
	if not await _wait_until(func() -> bool: return not pending2[0].is_empty(), 8000):
		_fail("放弃后再查 match.pending 没有应答")
		return
	if bool(pending2[0].get("found", true)):
		_fail("放弃后不该再查到那场对局（found=true）")
		return

	# join 也不能把他送回旧局：他现在只会进匹配队列（队列里只有他一个人 → 不会开局）。
	c2.send_match_join()
	var rejoined := [false]
	c2.matched_received.connect(func(_r: Dictionary) -> void: rejoined[0] = true)
	await _wait_until(func() -> bool: return rejoined[0], 3000)
	if rejoined[0]:
		_fail("放弃后 match.join 不该把他送回旧局（收到 onMatched）")
		return

	# 收尾：把他从队列里取消掉，别给下一轮的冒烟测试留一个幽灵排队者。
	var cancelled := [{}]
	c2.match_cancel_received.connect(func(r: Dictionary) -> void: cancelled[0] = r)
	c2.send_cancel_match()
	if not await _wait_until(func() -> bool: return not cancelled[0].is_empty(), 8000):
		_fail("收尾的 match.cancel 没有应答")
		return
	if String(cancelled[0].get("reason", "")) != "cancelled":
		_fail("收尾取消失败：%s" % str(cancelled[0]))
		return

	print("ABANDON OK")
	quit(0)

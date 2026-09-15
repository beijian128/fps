extends SceneTree
## 真实集群上的界面闭环冒烟（需要 etcd + NATS + redis + gate/account/logic/match/game/gm 六进程）：
##   登录页注册 → 进大厅（**没有自动匹配**）→ 拉到档案与商城状态 → 底部条进搜索中
##   → 第二个客户端加入队列 → 配对成功进对局（HUD 可见）
##
## 它驱动的是**真正的主场景**（main.tscn + 真 FpsClient），所以顺便覆盖了 main↔UI 的接线；
## 单机跑不了（没有第二个玩家），所以对等客户端由 tests/pair_helper.gd 起。
##
## 不覆盖：结算层（要打满 10 杀才触发，属于人工验证；见 docs/plans 的验证记录）。
##
## 运行：Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client \
##         --script res://tests/ui_smoke.gd

const MainScene := preload("res://scenes/main.tscn")
const ScreenManager := preload("res://ui/screen_manager.gd")
const PairHelper := preload("res://tests/pair_helper.gd")

const TIMEOUT_MS := 40000

var _failures := 0
var _done := {}
var _main: Node = null
var _peer = null
var _username := ""

func _initialize() -> void:
	_run()

func _run() -> void:
	for p in ["user://auth_token.txt", "user://last_username.txt"]:
		if FileAccess.file_exists(p):
			DirAccess.remove_absolute(ProjectSettings.globalize_path(p))

	_main = MainScene.instantiate()
	root.add_child(_main)
	await process_frame
	await process_frame
	_username = "uismoke_%d" % (Time.get_ticks_usec() % 100000000)

	# ---- 登录页：注册 ----
	_check(_main.ui.state() == ScreenManager.State.LOGIN, "没有凭证时应停在登录页")
	# 先等握手完成：WebSocket 没连上时发请求会被 `_send_tracked` 判成 no_connection，
	# 登录页只会显示「未连接到服务器」，看起来像注册失败。
	# 注意要等的是 **pomelo 握手完成**（`_handshaken`）而不只是 WS 打开：握手之前发出去的
	# 请求同样会被判成 no_connection。
	if not await _wait_until(func() -> bool:
			return bool(_main.fps_client.connected) and bool(_main.fps_client._handshaken), 10000):
		_fail("等握手完成超时")
		return
	_main.ui.login_screen.set_username_for_test(_username)
	_main.ui.login_screen.set_password_for_test("smokepass")
	_main.ui.login_screen.submit(true)
	if not await _wait_state(ScreenManager.State.LOBBY, TIMEOUT_MS):
		_fail("注册后没有进大厅（state=%d，登录页提示=%s）" % [
			_main.ui.state(), _main.ui.login_screen.error_text()])
		return
	_done["lobby"] = true
	_check(_main.ui.shell.visible, "大厅应可见")
	_check(not _main.ui.login_screen.visible, "登录页应隐藏")
	# 登录成功后**不该**自动匹配（底部条仍应是「开始匹配」）
	_check(_main.ui.match_bar.start_visible(), "登录后应停在空闲态，不该自动匹配")

	# ---- 档案与商城状态要真的拉回来（走 gate → logic）----
	if not await _wait_until(func() -> bool: return not _main.ui.profile().is_empty(), TIMEOUT_MS):
		_fail("没等到个人档案（logic.profile 没通？）")
		return
	if not await _wait_until(func() -> bool: return not _main.ui.logic_state().is_empty(), TIMEOUT_MS):
		_fail("没等到商城状态（logic.state 没通？）")
		return
	_done["data"] = true
	_check(int(_main.ui.profile().get("level", 0)) >= 1, "新账号应至少 1 级")
	_check(int(_main.ui.logic_state().get("coins", -1)) == 1000, "新账号应有 1000 金币")
	_check(_main.ui.shell.top_text().begins_with(_username), "顶栏应显示用户名，得到 %s" % _main.ui.shell.top_text())
	# 商城页挂上卡片（目录 4 件：rifle/pistol/shotgun/medkit）
	_main.ui.intent_show_page("shop")
	await process_frame
	_check(_main.ui.page_node_name() == "ShopScreen", "应显示商城子界面")
	_check(_main.ui.page_visible("shop") and not _main.ui.shell.visible,
		"子界面应整屏铺开且大厅隐藏")
	var shop: Control = _main.ui.shop_screen
	_check(shop.cards().size() >= 4, "商城应至少有 4 件商品，得到 %d" % shop.cards().size())
	_main.ui.intent_close_page()
	await process_frame
	_check(_main.ui.current_page() == "" and _main.ui.shell.visible, "返回大厅应回到入口页")

	# ---- 匹配：对等客户端 + 本机进队 ----
	_peer = PairHelper.new(root, "uismokepeer")
	_main.ui.intent_start_match()
	# 等的是「收到队列状态**或**已经配对成功」：对等客户端可能早就在队列里，于是本机入队后
	# 同一个 tick 就被配走 —— 那一 tick 里 `tryMatch` 先于 `pushMatchStatus`，队列已空、
	# 没有状态可推。这不是失败，是立即配对。
	if not await _wait_until(_matched_or_status, TIMEOUT_MS):
		_fail("既没收到 onMatchStatus、也没匹配上")
		return
	_done["status"] = true
	if _main.ui.match_status().size() > 0:
		_check(_main.ui.match_bar.queue_text().begins_with("● 搜索中"), "应进入搜索中")
	if not await _wait_state(ScreenManager.State.IN_MATCH, TIMEOUT_MS):
		_fail("没有配对成功（对等客户端 registered=%s matched=%s）" % [_peer.registered, _peer.matched])
		return
	_done["match"] = true
	_check(_main.ui.hud.visible, "对局中 HUD 应可见")
	_check(not _main.ui.shell.visible, "对局中大厅应隐藏")
	_check(_main.ui.result_overlay.visible == false, "对局中不该显示结算层")

	_finish()

func _wait_state(wanted: int, timeout_ms: int) -> bool:
	return await _wait_until(func() -> bool: return _main.ui.state() == wanted, timeout_ms)

## _matched_or_status 匹配阶段的「有进展」判据：收到了队列状态，或者已经配对进局。
func _matched_or_status() -> bool:
	return _main.ui.match_status().size() > 0 or _main.ui.state() == ScreenManager.State.IN_MATCH

func _wait_until(fn: Callable, timeout_ms: int) -> bool:
	var deadline := Time.get_ticks_msec() + timeout_ms
	while Time.get_ticks_msec() < deadline:
		if fn.call():
			return true
		await process_frame
	return fn.call()

func _check(cond: bool, msg: String) -> void:
	if not cond:
		_failures += 1
		printerr("FAIL: " + msg)

func _fail(msg: String) -> void:
	_check(false, msg)
	_finish()

func _finish() -> void:
	for name: String in ["lobby", "data", "status", "match"]:
		if not _done.has(name):
			_failures += 1
			printerr("FAIL: 步骤 %s 没跑完（中途失败了？）" % name)
	if _failures > 0:
		printerr("ui_smoke: %d 项失败" % _failures)
		quit(1)
	else:
		print("ui_smoke: OK")
		quit(0)

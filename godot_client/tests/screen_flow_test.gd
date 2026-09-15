extends SceneTree
## 屏幕状态机：登录后进大厅（**不自动匹配**），匹配 / 取消 / 进局 / 结算各自落到正确状态。
##
## 用假 FpsClient 驱动（`main.ui.setup(fake, main)` 在 _ready 之后重新绑定）—— 不碰 WebSocket。
## 为什么必须重新绑定：`@onready var fps_client = $FpsClient` 会在 _ready 时把提前塞进去的
## 假客户端覆盖回真实节点，所以接线写在 screen_manager.setup() 里、测试再调一次它就换过来了。
##
## 运行：Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client \
##         --script res://tests/screen_flow_test.gd

const MainScene := preload("res://scenes/main.tscn")
const ScreenManager := preload("res://ui/screen_manager.gd")

var _failures := 0
var _done := {}
var _main: Node = null
var _fake: Node = null

class FakeClient:
	extends Node

	signal frame_received(frame: Dictionary)
	signal matched_received(result: Dictionary)
	signal connection_changed(connected: bool)
	signal login_result(result: Dictionary)
	signal logic_state_received(result: Dictionary)
	signal profile_received(result: Dictionary)
	signal match_cancel_received(result: Dictionary)
	signal match_status_received(result: Dictionary)
	signal match_ended_received(result: Dictionary)

	var client_token := ""
	var calls: Array[String] = []
	var last_args := {}

	func send_register(u: String, p: String) -> void:
		calls.append("register")
		last_args["register"] = [u, p]

	func send_login(_u: String, _p: String) -> void:
		calls.append("login")

	func send_resume() -> void:
		calls.append("resume")

	func send_logic_state() -> void:
		calls.append("logic_state")

	func send_profile() -> void:
		calls.append("profile")

	func send_purchase(item_id: String, qty: int) -> void:
		calls.append("purchase")
		last_args["purchase"] = [item_id, qty]

	func send_equip(item_id: String) -> void:
		calls.append("equip")
		last_args["equip"] = item_id

	func send_match_join() -> void:
		calls.append("join")

	func send_cancel_match() -> void:
		calls.append("cancel")

	func send_resync() -> void:
		calls.append("resync")

	func send_command(_move: Vector2, _yaw: float, _jump: bool, _shoot: bool,
			_origin: Vector3, _dir: Vector3) -> void:
		pass

func _initialize() -> void:
	_run()

func _run() -> void:
	_main = MainScene.instantiate()
	root.add_child(_main)
	# _initialize 阶段 root 还没进树，_ready 不会触发；等两帧让场景真正入树。
	await process_frame
	await process_frame

	_fake = FakeClient.new()
	_main.add_child(_fake)
	_main.ui.setup(_fake, _main)

	# 1) 没有本地凭证 → 登录页
	_check(_main.ui.state() == ScreenManager.State.LOGIN, "没有凭证时应停在登录页")
	_check(_main.ui.login_screen.visible, "登录页应可见")
	_check(not _main.ui.shell.visible, "登录页阶段大厅应隐藏")

	# 2) 点注册 → 走 register
	_main.ui.intent_register("u1", "p1")
	_check(_fake.calls == ["register"], "注册意图应调 send_register，得到 %s" % str(_fake.calls))

	# 3) 登录成功 → 进大厅；拉一次 logic.state 与 profile；**不自动匹配**
	_fake.calls.clear()
	_fake.login_result.emit({"ok": true, "username": "u1", "account_id": "7"})
	_check(_main.ui.state() == ScreenManager.State.LOBBY, "登录成功后应进大厅")
	_check(_main.ui.shell.visible, "大厅应可见")
	_check(not _main.ui.login_screen.visible, "登录页应隐藏")
	_check(_fake.calls.has("logic_state"), "进大厅应拉一次商城状态")
	_check(_fake.calls.has("profile"), "进大厅应拉一次个人档案")
	_check(not _fake.calls.has("join"), "登录成功后**不该**自动匹配，得到 %s" % str(_fake.calls))

	# 4) 开始匹配 → MATCHING + join
	_fake.calls.clear()
	_main.ui.intent_start_match()
	_check(_main.ui.state() == ScreenManager.State.MATCHING, "点开始匹配后应进入搜索中")
	_check(_fake.calls == ["join"], "应发一次 match.join，得到 %s" % str(_fake.calls))

	# 5) 队列状态进快照
	_fake.match_status_received.emit({"queued_players": 3, "waited_seconds": 12})
	_check(int(_main.ui.match_status().get("queued_players", 0)) == 3, "队列人数应进快照")
	_check(int(_main.ui.match_status().get("waited_seconds", 0)) == 12, "等待秒数应进快照")

	# 6) 取消 → 回大厅
	_fake.calls.clear()
	_main.ui.intent_cancel_match()
	_check(_fake.calls == ["cancel"], "取消应发 match.cancel，得到 %s" % str(_fake.calls))
	_fake.match_cancel_received.emit({"ok": true, "reason": "cancelled"})
	_check(_main.ui.state() == ScreenManager.State.LOBBY, "取消成功后应回大厅")

	# 6b) 取消时已经被配对走（already_matched）→ 保持搜索中，等 onMatched
	_main.ui.intent_start_match()
	_fake.match_cancel_received.emit({"ok": false, "reason": "already_matched"})
	_check(_main.ui.state() == ScreenManager.State.MATCHING,
		"already_matched 不该回大厅（onMatched 马上到）")

	# 7) 匹配成功 → 对局中：大厅隐藏、HUD 可见
	_fake.matched_received.emit({"match_id": "m1", "game_server_id": "g1", "player_idx": 0})
	_check(_main.ui.state() == ScreenManager.State.IN_MATCH, "onMatched 后应进入对局中")
	_check(not _main.ui.shell.visible, "对局中大厅应隐藏")
	_check(_main.ui.hud.visible, "对局中 HUD 应可见")

	# 8) 结算 → RESULT；回大厅会再拉一次 profile
	_fake.calls.clear()
	_fake.match_ended_received.emit({
		"match_id": "m1", "winner_slot": 0, "duration_seconds": 84,
		"slots": [{"uid": "7", "kills": 10, "deaths": 3}, {"uid": "8", "kills": 3, "deaths": 10}],
	})
	_check(_main.ui.state() == ScreenManager.State.RESULT, "onMatchEnded 后应进入结算态")
	_check(_main.ui.result_overlay.visible, "结算层应可见")
	_check(not _fake.calls.has("join"), "结算不得自动重新入队，得到 %s" % str(_fake.calls))
	_fake.calls.clear()
	_main.ui.intent_back_to_lobby()
	_check(_main.ui.state() == ScreenManager.State.LOBBY, "回大厅后应处于 LOBBY")
	_check(_fake.calls.has("profile"), "回大厅应重拉一次档案")

	_done["flow"] = true
	_finish()

func _check(cond: bool, msg: String) -> void:
	if not cond:
		_failures += 1
		printerr("FAIL: " + msg)

func _finish() -> void:
	if not _done.has("flow"):
		_failures += 1
		printerr("FAIL: 用例没跑完（中途抛错了？）")
	if _failures > 0:
		printerr("screen_flow_test: %d 项失败" % _failures)
		quit(1)
	else:
		print("screen_flow_test: OK")
		quit(0)

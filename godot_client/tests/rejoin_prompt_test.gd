extends SceneTree
## 进大厅时的「有没有没打完的局」询问框：查询 → 弹框 → 回到对局 / 放弃对局 / 稍后决定。
##
## 用假 FpsClient 驱动（`main.ui.setup(fake, main)` 在 _ready 之后重新绑定）—— 不碰 WebSocket。
## 覆盖四件容易做错的事：
##   1. 登录成功后**必须**问一次（不然玩家永远看不到这个框）；
##   2. 框只在**大厅**里出现（对局中/结算中不弹）；
##   3. 「回到对局」走的就是 match.join（服务端回局分支），并且离开大厅状态；
##   4. 「放弃对局」的结果以服务端应答为准 —— 失败时**保留**框并给提示，不假装成功。
##
## 运行：Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client \
##         --script res://tests/rejoin_prompt_test.gd

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
	signal pending_match_received(result: Dictionary)
	signal abandon_match_received(result: Dictionary)

	var client_token := ""
	var calls: Array[String] = []

	func send_logic_state() -> void: calls.append("logic_state")
	func send_profile() -> void: calls.append("profile")
	func send_match_join() -> void: calls.append("join")
	func send_cancel_match() -> void: calls.append("cancel")
	func send_pending_match() -> void: calls.append("pending")
	func send_abandon_match() -> void: calls.append("abandon")
	func send_resync() -> void: calls.append("resync")
	func send_register(_u: String, _p: String) -> void: pass
	func send_login(_u: String, _p: String) -> void: pass
	func send_purchase(_i: String, _q: int) -> void: pass
	func send_equip(_i: String) -> void: pass
	func send_command(_m: Vector2, _y: float, _j: bool, _s: bool,
			_origin: Vector3, _dir: Vector3) -> void: pass

func _initialize() -> void:
	_run()

func _run() -> void:
	_main = MainScene.instantiate()
	root.add_child(_main)
	await process_frame
	await process_frame
	_fake = FakeClient.new()
	_main.add_child(_fake)
	_main.ui.setup(_fake, _main)

	# 0) 传输层真的实现了这两条请求（界面驱动的是假客户端，这里单独确认真客户端没漏）。
	_check(_main.fps_client.has_method("send_pending_match")
			and _main.fps_client.has_method("send_abandon_match"),
		"FpsClient 应实现 send_pending_match / send_abandon_match")

	# 1) 登录成功 → 进大厅的第一件事就是问「有没有没打完的局」
	_fake.calls.clear()
	_fake.login_result.emit({"ok": true, "username": "u1", "account_id": "7"})
	_check(_main.ui.state() == ScreenManager.State.LOBBY, "登录成功后应进大厅")
	_check(_fake.calls.has("pending"),
		"进大厅应问一次「有没有没打完的局」，得到 %s" % str(_fake.calls))
	_check(not _main.ui.rejoin_prompt_visible(), "还没收到应答时不该弹框")

	# 2) 没有存量对局 → 大厅照常，什么都不弹
	_fake.pending_match_received.emit({"found": false, "match_id": ""})
	_check(not _main.ui.has_pending_match(), "found=false 时不该记成有存量对局")
	_check(not _main.ui.rejoin_prompt_visible(), "没有存量对局时不该弹框")

	# 3) 有存量对局 → 弹框，并且把对局 id 显示出来
	_fake.pending_match_received.emit({"found": true, "match_id": "m-42"})
	_check(_main.ui.has_pending_match(), "found=true 时应记住有存量对局")
	_check(_main.ui.pending_match_id() == "m-42",
		"应记住对局 id，得到 %s" % _main.ui.pending_match_id())
	_check(_main.ui.rejoin_prompt_visible(), "有存量对局时应弹询问框")
	_check(_main.ui.rejoin_prompt.visible, "询问框节点应可见")
	_check(_main.ui.shell.visible, "询问框是浮层，大厅仍在它背后")
	_check(_main.ui.rejoin_prompt.info_text().contains("m-42"),
		"框里应显示对局 id，得到「%s」" % _main.ui.rejoin_prompt.info_text())
	# 焦点落在「回到对局」：破坏性的那个（放弃）不该是敲一下回车就命中的默认选项。
	_check(_main.ui.rejoin_prompt.focus_button() == "reconnect",
		"焦点应在「回到对局」上，得到「%s」" % _main.ui.rejoin_prompt.focus_button())
	# 根节点必须铺满父级：锚点漏了 anchors_preset=15 的话，尺寸是 0×0 —— 界面会静默
	# 塌进左上角一小块，不报错、也不会被别的断言挡下，所以这里钉一下。
	_check(_main.ui.rejoin_prompt.size == _main.get_viewport().get_visible_rect().size,
		"询问框根节点应铺满视口，得到 %s" % str(_main.ui.rejoin_prompt.size))
	_check(_main.ui.rejoin_prompt.get_node("Center/Panel").size.x >= 500.0,
		"对话框面板应有正常宽度，得到 %s" % str(_main.ui.rejoin_prompt.get_node("Center/Panel").size))

	# 4) 稍后决定（ESC）：收起框、留在大厅、对局仍在服务端（没有被放弃）
	_fake.calls.clear()
	_main.ui.intent_escape()
	_check(not _main.ui.rejoin_prompt_visible(), "ESC 应收起询问框")
	_check(_main.ui.state() == ScreenManager.State.LOBBY, "收起后应留在大厅")
	_check(_main.ui.has_pending_match(), "稍后决定不是放弃 —— 对局仍然挂着")
	_check(not _fake.calls.has("abandon"), "ESC 绝不能顺手放弃对局，得到 %s" % str(_fake.calls))
	_check(_main.ui.toast.last_text().contains("开始匹配"),
		"提示要说清「怎么回到这一局」，得到「%s」" % _main.ui.toast.last_text())
	_check(_main.ui.shell.focus_active_entry() == "profile",
		"焦点应交回大厅入口卡，得到「%s」" % _main.ui.shell.focus_active_entry())

	# 5) 「回到对局」：走 match.join（服务端回局分支送回原局），离开大厅进入匹配态
	_main.ui.on_pending_match({"found": true, "match_id": "m-42"})
	_check(_main.ui.rejoin_prompt_visible(), "重新收到应答后应再次弹框")
	_fake.calls.clear()
	_main.ui.intent_rejoin_match()
	_check(_fake.calls == ["join"], "回到对局应发一次 match.join，得到 %s" % str(_fake.calls))
	_check(_main.ui.state() == ScreenManager.State.MATCHING, "点回到对局后应进入搜索态")
	_check(not _main.ui.rejoin_prompt_visible(), "点完应立刻收起询问框")
	_check(_main.ui.toast.last_text().contains("回到原来的对局"),
		"应说明正在回到原对局，得到「%s」" % _main.ui.toast.last_text())
	_fake.matched_received.emit({"match_id": "m-42", "game_server_id": "g1", "player_idx": 1})
	_check(_main.ui.state() == ScreenManager.State.IN_MATCH, "onMatched 后应回到对局")
	_fake.calls.clear()

	# 6) 对局中收到迟到的 pending 应答：不许弹框盖住战场
	_fake.pending_match_received.emit({"found": true, "match_id": "m-42"})
	_check(not _main.ui.rejoin_prompt_visible(), "对局中不该弹出进大厅的询问框")

	# 7) 回到大厅，弹框 → 放弃对局失败：**保留**框并给失败提示（不假装成功）
	_main.ui.intent_back_to_lobby()
	_fake.pending_match_received.emit({"found": true, "match_id": "m-42"})
	_check(_main.ui.rejoin_prompt_visible(), "回到大厅后应再次弹框")
	_fake.calls.clear()
	_main.ui.intent_abandon_match()
	_check(_fake.calls == ["abandon"], "放弃对局应发一次 match.abandon，得到 %s" % str(_fake.calls))
	_fake.abandon_match_received.emit({"ok": false, "reason": "internal"})
	_check(_main.ui.rejoin_prompt_visible(), "放弃失败时应保留询问框（否则玩家以为已经放弃）")
	_check(_main.ui.toast.last_text().contains("重试"),
		"放弃失败应提示可重试，得到「%s」" % _main.ui.toast.last_text())
	_check(_main.ui.has_pending_match(), "放弃失败后仍然有存量对局")

	# 8) 再试一次成功：收起框、清掉存量对局、明确告知不再计入本局战绩
	_fake.abandon_match_received.emit({"ok": true, "reason": "released"})
	_check(not _main.ui.rejoin_prompt_visible(), "放弃成功后应收起询问框")
	_check(not _main.ui.has_pending_match(), "放弃成功后不该再有存量对局")
	_check(_main.ui.state() == ScreenManager.State.LOBBY, "放弃后仍在大厅")
	_check(_main.ui.toast.last_text().contains("不再计入"),
		"应说清放弃的后果，得到「%s」" % _main.ui.toast.last_text())

	# 9) 再点开始匹配：正常进队列（服务端此时查不到他的存量对局）
	_fake.calls.clear()
	_main.ui.intent_start_match()
	_check(_fake.calls == ["join"], "放弃后照常可以重新匹配，得到 %s" % str(_fake.calls))

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
		printerr("rejoin_prompt_test: %d 项失败" % _failures)
		quit(1)
	else:
		print("rejoin_prompt_test: OK")
		quit(0)

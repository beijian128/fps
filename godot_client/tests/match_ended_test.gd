extends SceneTree
## 本局结束后客户端必须进入结算态，而且**不自动重新入队**。
##
## 两件事分属两层，各自断言：
##   - 界面状态：screen_manager 订阅 match_ended_received → RESULT（结算层可见、HUD 与大厅隐藏）
##   - 本地世界：main 订阅同一个信号 → 清空实体缓存与插值状态（下一局的实体 id 会从头开始，
##     残留节点会串台）
## 测试里注入假客户端驱动界面层；世界清理直接调 main 的处理函数（真实接线由冒烟测试覆盖）。
##
## 运行：Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client \
##         --script res://tests/match_ended_test.gd

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

	func send_logic_state() -> void:
		calls.append("logic_state")

	func send_profile() -> void:
		calls.append("profile")

	func send_match_join() -> void:
		calls.append("join")

	func send_cancel_match() -> void:
		calls.append("cancel")

	func send_pending_match() -> void:
		calls.append("pending")

	func send_abandon_match() -> void:
		calls.append("abandon")

	func send_resync() -> void:
		calls.append("resync")

	func send_purchase(_i: String, _q: int) -> void:
		pass

	func send_equip(_i: String) -> void:
		pass

	func send_register(_u: String, _p: String) -> void:
		pass

	func send_login(_u: String, _p: String) -> void:
		pass

	func send_command(_m: Vector2, _y: float, _j: bool, _s: bool,
			_origin: Vector3, _dir: Vector3) -> void:
		pass

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

	# 进大厅 → 匹配 → 进局
	_fake.login_result.emit({"ok": true, "username": "u1"})
	_check(_main.ui.state() == ScreenManager.State.LOBBY, "登录成功后应在大厅")
	_main.ui.intent_start_match()
	_fake.matched_received.emit({"match_id": "m1", "game_server_id": "g1", "player_idx": 0})
	_check(_main.ui.state() == ScreenManager.State.IN_MATCH, "onMatched 后应处于对局态")
	_check(_main.ui.hud.visible, "对局中 HUD 应可见")

	# 造一点本地世界状态，验证结算时会清掉。
	_main._entities[100] = Node3D.new()
	_main.add_child(_main._entities[100])
	_fake.calls.clear()

	var ended := {
		"match_id": "m1", "winner_slot": 0, "duration_seconds": 84,
		"slots": [{"uid": "7", "kills": 10, "deaths": 3}, {"uid": "8", "kills": 3, "deaths": 10}],
	}
	_fake.match_ended_received.emit(ended)

	_check(_main.ui.state() == ScreenManager.State.RESULT, "onMatchEnded 后应进入结算态")
	_check(_main.ui.result_overlay.visible, "结算层应可见")
	_check(not _main.ui.shell.visible, "结算时大厅应隐藏")
	_check(not _main.ui.hud.visible, "结算时 HUD 应隐藏")
	# 胜负与比分必须读**自己槽位**（自己 10 杀、对手 3 杀 → 你 10 : 3）
	_check(_main.ui.result_overlay.headline() == "胜 利",
		"胜者槽位是自己时应显示胜利，得到 %s" % _main.ui.result_overlay.headline())
	_check(_main.ui.result_overlay.score_text() == "你 10 : 3 对手",
		"比分应读自己槽位，得到 %s" % _main.ui.result_overlay.score_text())
	_check(_main.ui.result_overlay.duration_text() == "用时 84 秒",
		"时长文案不对，得到 %s" % _main.ui.result_overlay.duration_text())
	_check(not _fake.calls.has("join"), "结算不得自动重新入队，得到 %s" % str(_fake.calls))
	_check(not _fake.calls.has("profile"), "结算瞬间不该额外拉档案（回大厅时再拉）")

	# 世界清理（main 那一层）
	_main._on_client_match_ended(ended)
	_check(_main._entities.is_empty(), "结算应清空本地实体缓存")

	# 回大厅 → 拉一次档案（这局刚记进战绩）
	_fake.calls.clear()
	_main.ui.intent_back_to_lobby()
	_check(_main.ui.state() == ScreenManager.State.LOBBY, "回大厅后应处于大厅")
	_check(_fake.calls.has("profile"), "回大厅应重拉一次档案")

	# 底部状态条：搜索中显示队列人数与等待时长，并把主按钮换成取消
	_main.ui.intent_start_match()
	_fake.match_status_received.emit({"queued_players": 3, "waited_seconds": 12})
	_check(_main.ui.match_bar.queue_text() == "● 搜索中 · 队列 3 人 · 已等待 12s",
		"队列文案不对，得到 %s" % _main.ui.match_bar.queue_text())
	_check(_main.ui.match_bar.cancel_visible() and not _main.ui.match_bar.start_visible(),
		"搜索中应显示取消、隐藏开始匹配")
	_fake.match_cancel_received.emit({"ok": true, "reason": "cancelled"})
	_check(_main.ui.match_bar.start_visible(), "取消后应恢复「开始匹配」")
	_check(_main.ui.match_bar.last_result_text().begins_with("上一局：胜 10 : 3"),
		"空闲时应显示上一局摘要，得到 %s" % _main.ui.match_bar.last_result_text())

	_done["ended"] = true
	_finish()

func _check(cond: bool, msg: String) -> void:
	if not cond:
		_failures += 1
		printerr("FAIL: " + msg)

func _finish() -> void:
	if not _done.has("ended"):
		_failures += 1
		printerr("FAIL: 用例没跑完（中途抛错了？）")
	if _failures > 0:
		printerr("match_ended_test: %d 项失败" % _failures)
		quit(1)
	else:
		print("match_ended_test: OK")
		quit(0)

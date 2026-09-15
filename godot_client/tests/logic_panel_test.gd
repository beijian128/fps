extends SceneTree
## 商城 / 背包的「请求时机」与「意图 → 协议调用」：界面层只发意图，真正发请求的是
## screen_manager，所以这里用假客户端驱动它，断言的是两件事：
##
##   1. 什么时候**不**该请求 —— 没登录时不请求 state；在途（或已有快照）时不重复请求；
##      切页面本身不产生请求（切页面只换渲染）。
##   2. 购买 / 装备的意图是否按预期参数落到 fps_client 上。
##
## 卡片渲染（数量步进器、禁用条件、装备文案）由 tests/item_grid_test.gd 覆盖。
##
## 运行：Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client \
##         --script res://tests/logic_panel_test.gd

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

	func send_logic_state() -> void:
		calls.append("logic_state")

	func send_profile() -> void:
		calls.append("profile")

	func send_purchase(item_id: String, quantity: int) -> void:
		calls.append("purchase")
		last_args["purchase"] = [item_id, quantity]

	func send_equip(item_id: String) -> void:
		calls.append("equip")
		last_args["equip"] = item_id

	func send_match_join() -> void:
		calls.append("join")

	func send_cancel_match() -> void:
		calls.append("cancel")

	func send_resync() -> void:
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

	# 1) 没登录：切页面不该请求任何东西（登录前根本进不了大厅，但意图层要挡住）
	_check(_main.ui.state() == ScreenManager.State.LOGIN, "没有凭证时应停在登录页")
	_main.ui.intent_show_page("shop")
	_check(not _fake.calls.has("logic_state"), "未登录时切页面不该请求商城状态")
	_check(not _fake.calls.has("profile"), "未登录时切页面不该请求档案")

	# 2) 登录成功：恰好各拉一次
	_fake.calls.clear()
	_fake.login_result.emit({"ok": true, "username": "u1"})
	_check(_fake.calls.count("logic_state") == 1, "进大厅应恰好请求一次商城状态，得到 %s" % str(_fake.calls))
	_check(_fake.calls.count("profile") == 1, "进大厅应恰好请求一次档案，得到 %s" % str(_fake.calls))

	# 3) 已有快照：切页面 + 收到数据变化都不再重复请求
	_fake.calls.clear()
	_main.ui.intent_show_page("shop")
	_main.ui.intent_show_page("bag")
	_main.ui.intent_show_page("profile")
	_check(_main.ui.current_page() == "profile", "当前页应切到 profile")
	_check(_fake.calls.is_empty(), "切页面不该产生任何请求，得到 %s" % str(_fake.calls))
	_check(_main.ui.logic_state().is_empty(), "还没有状态时应是空快照")

	# 4) 服务端推来状态 → 进快照（界面从这里读）
	_fake.logic_state_received.emit({
		"ok": true, "coins": 1000, "equipped_primary_weapon": "pistol",
		"items": [
			{"item_id": "rifle", "display_name": "步枪", "price": 300,
				"equip_slot": "primary_weapon", "owned_quantity": 0},
		],
	})
	_check(int(_main.ui.logic_state().get("coins", 0)) == 1000, "金币应进快照")
	_check(_fake.calls.is_empty(), "收到状态不该触发新请求，得到 %s" % str(_fake.calls))

	# 5) 购买 / 装备意图 → 参数原样落到协议层
	_main.ui.intent_purchase("medkit", 5)
	_check(_fake.last_args.get("purchase", []) == ["medkit", 5],
		"购买意图应带 item_id 与数量，得到 %s" % str(_fake.last_args.get("purchase", [])))
	_main.ui.intent_equip("rifle")
	_check(String(_fake.last_args.get("equip", "")) == "rifle", "装备意图应带 item_id")

	_done["logic"] = true
	_finish()

func _check(cond: bool, msg: String) -> void:
	if not cond:
		_failures += 1
		printerr("FAIL: " + msg)

func _finish() -> void:
	if not _done.has("logic"):
		_failures += 1
		printerr("FAIL: 用例没跑完（中途抛错了？）")
	if _failures > 0:
		printerr("logic_panel_test: %d 项失败" % _failures)
		quit(1)
	else:
		print("logic_panel_test: OK")
		quit(0)

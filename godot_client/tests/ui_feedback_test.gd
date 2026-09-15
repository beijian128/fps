extends SceneTree
## 交互反馈契约：可点性（鼠标手型）、提示文案、工具提示、按键入口、动作回音（通知栈）。
##
## 运行：Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client \
##         --script res://tests/ui_feedback_test.gd
##
## 为什么单独立一条测试：这些东西丢了**不会报错** —— 按钮还在、还能点，只是玩家不知道能点、
## 点完也没有回音。上一版界面就是栽在这里（结构全对、反馈几乎没有），所以用测试钉住。

const MainScene := preload("res://scenes/main.tscn")
const ScreenManager := preload("res://ui/screen_manager.gd")
const Toast := preload("res://ui/toast.gd")

## 每个屏幕里必须存在的「可点控件」路径（相对该屏幕根节点）。
const INTERACTIVE := {
	"res://ui/login_screen.tscn": ["Center/Card/Rows/User", "Center/Card/Rows/Pass",
		"Center/Card/Rows/Actions/Login", "Center/Card/Rows/Actions/Register"],
	"res://ui/shell.tscn": ["Root/Body/Column/Entries/EntryProfile",
		"Root/Body/Column/Entries/EntryShop", "Root/Body/Column/Entries/EntryBag"],
	"res://ui/profile_screen.tscn": ["Columns/Identity/Rows/Back"],
	"res://ui/shop_screen.tscn": ["Rows/Header/Back"],
	"res://ui/bag_screen.tscn": ["Rows/Header/Back", "Rows/Header/ToShop"],
	"res://ui/match_status_bar.tscn": ["Row/StartMatch", "Row/CancelMatch"],
	"res://ui/item_card.tscn": ["Rows/Footer/QtyMinus", "Rows/Footer/QtyPlus", "Rows/Footer/Action"],
	"res://ui/pause_screen.tscn": ["Center/Panel/Rows/Actions/ResetDefaults",
		"Center/Panel/Rows/Actions/Resume",
		"Center/Panel/Rows/Tabs/操作/Rows/SensitivityRow/Line/SensitivitySlider"],
	"res://ui/result_overlay.tscn": ["Center/Panel/Rows/Actions/BackToLobby",
		"Center/Panel/Rows/Actions/PlayAgain"],
	"res://ui/rejoin_prompt.tscn": ["Center/Panel/Rows/Actions/Reconnect",
		"Center/Panel/Rows/Actions/Abandon"],
}

## 每个屏幕必须有一行「怎么操作」的提示（文案里至少出现其中一个关键词）。
const HINT_KEYS := ["ESC", "Enter", "点卡片", "WASD"]

var _failures := 0
var _done := {}
var _main: Node = null
var _fake: Node = null

## 假客户端：本用例只测界面反馈，不该真的连服务端（真客户端的「发不出去」会立刻变成
## 一条「购买失败」提示，那是另一条要覆盖的行为，不该混进来）。
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
	func send_purchase(item_id: String, quantity: int) -> void: calls.append("purchase:%s:%d" % [item_id, quantity])
	func send_equip(item_id: String) -> void: calls.append("equip:%s" % item_id)
	func send_match_join() -> void: calls.append("join")
	func send_cancel_match() -> void: calls.append("cancel")
	func send_pending_match() -> void: calls.append("pending")
	func send_abandon_match() -> void: calls.append("abandon")
	func send_resync() -> void: calls.append("resync")

func _initialize() -> void:
	_test_clickable()
	_test_hints_present()
	_test_tooltips()
	_run_flow()

func _run_flow() -> void:
	_main = MainScene.instantiate()
	root.add_child(_main)
	await process_frame
	_fake = FakeClient.new()
	root.add_child(_fake)
	_main.ui.setup(_fake, _main)
	_main.ui.on_login_result({"ok": true, "username": "u1", "account_id": "1"})
	_main.ui.on_logic_state({"ok": true, "coins": 1000, "equipped_primary_weapon": "",
		"items": [{"item_id": "rifle", "display_name": "步枪", "price": 300,
			"owned_quantity": 0, "equip_slot": "primary_weapon"}]})
	await process_frame

	var toast: Control = _main.ui.toast
	toast.clear()

	# 1) 点「购买」→ 立刻有「正在购买…」，服务端状态回来后变成确定的「已购买」
	_main.ui.intent_purchase("rifle", 1)
	_check(toast.count() == 1 and toast.last_text().begins_with("正在购买"),
		"点击购买后应立刻有反馈，得到 %s" % str(toast.texts()))
	_main.ui.on_logic_state({"ok": true, "coins": 700, "equipped_primary_weapon": "",
		"items": [{"item_id": "rifle", "display_name": "步枪", "price": 300,
			"owned_quantity": 1, "equip_slot": "primary_weapon"}]})
	_check(toast.last_text().begins_with("已购买") and toast.last_text().contains("300"),
		"状态回来后应确认「已购买…-300 金币」，得到 %s" % toast.last_text())

	# 2) 重复内容不叠加：连点三次同一件事只留一条（否则屏幕会被同一条提示刷满）
	var before: int = toast.count()
	_main.ui.notify("测试提示", Toast.Kind.INFO)
	_main.ui.notify("测试提示", Toast.Kind.INFO)
	_main.ui.notify("测试提示", Toast.Kind.INFO)
	_check(toast.count() == before + 1, "同一条提示不该叠加，得到 %d" % (toast.count() - before))

	# 3) 装备：状态里装备槽变成它才算成功
	_main.ui.intent_equip("rifle")
	_main.ui.on_logic_state({"ok": true, "coins": 700, "equipped_primary_weapon": "rifle",
		"items": [{"item_id": "rifle", "display_name": "步枪", "price": 300,
			"owned_quantity": 1, "equip_slot": "primary_weapon"}]})
	_check(toast.last_text() == "已装备 步枪", "装备后应确认，得到 %s" % toast.last_text())

	# 4) 掉线 / 重连必须有回音（界面不动的话玩家会以为是自己点坏了）
	_main.ui.on_connection_changed(false)
	_check(toast.last_text().contains("断开"), "掉线应有提示，得到 %s" % toast.last_text())
	_main.ui.on_connection_changed(true)
	_check(toast.last_text().contains("重新连接"), "重连应有提示，得到 %s" % toast.last_text())

	# 5) ESC：在大厅按 ESC 不该有任何副作用；开了子界面则返回入口页
	_main.ui.intent_escape()
	_check(_main.ui.current_page() == "" and _main.ui.state() == ScreenManager.State.LOBBY,
		"大厅按 ESC 不该改变状态")
	_main.ui.intent_show_page("shop")
	_main.ui.intent_escape()
	_check(_main.ui.current_page() == "", "子界面按 ESC 应返回大厅入口页")

	# 6) 匹配 / 取消也有回音
	_main.ui.intent_start_match()
	_check(toast.last_text().contains("匹配队列"), "开始匹配应有提示，得到 %s" % toast.last_text())
	_main.ui.on_cancel_reply({"reason": "cancelled"})
	_check(toast.last_text() == "已取消匹配", "取消匹配应有提示，得到 %s" % toast.last_text())
	_done["flow"] = true
	_finish()

## _test_clickable 可点控件必须给出手型光标 —— 这是最便宜的「这里能点」提示。
func _test_clickable() -> void:
	for path: String in INTERACTIVE.keys():
		var scene: PackedScene = load(path)
		_check(scene != null, "加载不了场景 %s" % path)
		if scene == null:
			continue
		var node: Node = scene.instantiate()
		for sub: String in INTERACTIVE[path]:
			var target := node.get_node_or_null(sub) as Control
			_check(target != null, "%s 里找不到 %s" % [path, sub])
			if target != null:
				_check(target.mouse_default_cursor_shape == Control.CURSOR_POINTING_HAND,
					"%s 的 %s 应显示手型光标（当前 %d）"
						% [path.get_file(), sub.get_file(), target.mouse_default_cursor_shape])
		node.free()
	_done["clickable"] = true

## _test_hints_present 每个主界面都要有一行「怎么操作」的提示文字。
func _test_hints_present() -> void:
	for path in ["res://ui/shell.tscn", "res://ui/profile_screen.tscn", "res://ui/shop_screen.tscn",
			"res://ui/bag_screen.tscn", "res://ui/hud.tscn", "res://ui/rejoin_prompt.tscn"]:
		var scene: PackedScene = load(path)
		if scene == null:
			continue
		var node: Node = scene.instantiate()
		var found := _find_hint(node)
		_check(not found.is_empty(), "%s 缺一行操作提示（ESC / Enter / 点卡片 / WASD）" % path.get_file())
		node.free()
	_done["hints"] = true

func _find_hint(node: Node) -> String:
	if node is Label and not node.text.strip_edges().is_empty():
		for key in HINT_KEYS:
			if String(node.text).contains(key):
				return String(node.text)
	for child in node.get_children():
		var hit := _find_hint(child)
		if not hit.is_empty():
			return hit
	return ""

## _test_tooltips 关键控件要有工具提示（设置项尤其：光看「安全区」不知道它管什么）。
func _test_tooltips() -> void:
	var expectations := {
		"res://ui/pause_screen.tscn": [".../SafeAreaSlider", ".../UiScaleSlider",
			".../SensitivitySlider", ".../HitFlashSlider"],
		"res://ui/item_card.tscn": ["Rows/Footer/Action", "Rows/Footer/QtyPlus"],
		"res://ui/match_status_bar.tscn": ["Row/StartMatch", "Row/CancelMatch"],
		"res://ui/rejoin_prompt.tscn": ["Center/Panel/Rows/Actions/Reconnect",
			"Center/Panel/Rows/Actions/Abandon"],
	}
	for path: String in expectations.keys():
		var scene: PackedScene = load(path)
		if scene == null:
			continue
		var node: Node = scene.instantiate()
		for sub: String in expectations[path]:
			var target := node.get_node_or_null(sub) if not sub.begins_with("...") \
				else _find_first_slider(node, sub.get_file())
			_check(target != null, "%s 里找不到 %s" % [path.get_file(), sub])
			if target != null:
				_check(not String(target.tooltip_text).is_empty(),
					"%s 的 %s 应有工具提示" % [path.get_file(), sub.get_file()])
		node.free()
	_done["tooltips"] = true

func _find_first_slider(node: Node, wanted: String) -> Node:
	if node is HSlider and String(node.name) == wanted:
		return node
	for child in node.get_children():
		var hit := _find_first_slider(child, wanted)
		if hit != null:
			return hit
	return null

func _check(cond: bool, msg: String) -> void:
	if not cond:
		_failures += 1
		printerr("FAIL: " + msg)

func _finish() -> void:
	for name: String in ["clickable", "hints", "tooltips", "flow"]:
		if not _done.has(name):
			_failures += 1
			printerr("FAIL: 用例 %s 没跑完（中途抛错了？）" % name)
	if _failures > 0:
		printerr("ui_feedback_test: %d 项失败" % _failures)
		quit(1)
	else:
		print("ui_feedback_test: OK")
		quit(0)

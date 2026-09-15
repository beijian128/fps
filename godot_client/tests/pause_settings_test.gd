extends SceneTree
## 对局内设置层：ESC 开合、离开对局自动收起、四项偏好真的落到消费者身上。
##
## 运行：Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client \
##         --script res://tests/pause_settings_test.gd
##
## 两条容易踩空的点，这里都钉住了：
##   1. 设置层**不暂停对局**（状态仍是 IN_MATCH）—— 写「已暂停」是骗玩家；
##   2. 偏好要真的生效（界面缩放写窗口、安全区写 HUD），不是只改了一个变量。

const MainScene := preload("res://scenes/main.tscn")
const ScreenManager := preload("res://ui/screen_manager.gd")
const Settings := preload("res://ui/settings.gd")

var _failures := 0
var _done := {}
var _main: Node = null

func _initialize() -> void:
	_run()

func _run() -> void:
	_main = MainScene.instantiate()
	# 必须在 _ready 之前换掉偏好实例：首次 bind 才会走「滑块值 0 被 min_value 夹上去」那条路径，
	# 而那正是当初偷偷把灵敏度写成 0.2、界面缩放写成 0.8 的地方。晚一步换就测不到它了。
	# persist = false：设置面板会写 user://settings.cfg，测试不该动开发者的真实偏好。
	_main.get_node("UI")._settings = Settings.new(false)
	root.add_child(_main)
	await process_frame
	await process_frame

	# 首次绑定后偏好必须原样（绑定不该产生任何写入）
	_check(absf(_main.ui.settings().value("mouse_sensitivity") - 1.0) < 0.001,
		"绑定设置面板不该改灵敏度，得到 %f" % _main.ui.settings().value("mouse_sensitivity"))
	_check(absf(_main.ui.settings().value("ui_scale") - 1.0) < 0.001,
		"绑定设置面板不该改界面缩放，得到 %f" % _main.ui.settings().value("ui_scale"))
	_check(absf(root.content_scale_factor - 1.0) < 0.001,
		"绑定设置面板不该改窗口缩放，得到 %f" % root.content_scale_factor)

	# 断开真实客户端的信号：本机连着的集群可能在任何一帧推回登录/匹配结果，
	# 把测试推进的状态覆盖掉（本用例只验证界面状态机与偏好，不验证网络）。
	_sever_client_signals(_main.fps_client)

	_main.ui.on_login_result({"ok": true, "username": "tester", "account_id": "7"})
	_main.ui.on_matched({"match_id": "m1", "player_idx": 0})
	_check(_main.ui.state() == ScreenManager.State.IN_MATCH, "应处于对局态")
	_check(not _main.ui.is_paused(), "刚进局不该是设置层打开")
	_check(not _main.ui.pause_screen.visible, "刚进局设置层应不可见")

	# —— ESC 开合 ——
	_main.ui.intent_toggle_pause()
	_check(_main.ui.is_paused(), "ESC 应打开设置层")
	_check(_main.ui.pause_screen.visible, "设置层应可见")
	_check(_main.ui.state() == ScreenManager.State.IN_MATCH,
		"设置层不暂停对局，状态应仍是 IN_MATCH，得到 %d" % _main.ui.state())
	_check(_main.ui.pause_screen.hint_text().contains("仍在继续"),
		"面板要写清对局没有暂停，得到「%s」" % _main.ui.pause_screen.hint_text())

	_main.ui.intent_toggle_pause()
	_check(not _main.ui.is_paused(), "再按 ESC 应关闭设置层")
	_check(not _main.ui.pause_screen.visible, "关闭后设置层应不可见")

	# 大厅里 ESC 不该打开设置层（没有可返回的战场）
	_main.ui.on_match_ended({"winner_slot": 0, "slots": []})
	_main.ui.intent_back_to_lobby()
	_main.ui.intent_toggle_pause()
	_check(not _main.ui.is_paused(), "大厅里 ESC 不应打开设置层")

	# —— 面板渲染当前偏好 ——
	_main.ui.on_matched({"match_id": "m2", "player_idx": 0})
	_main.ui.intent_toggle_pause()
	var panel: Control = _main.ui.pause_screen
	_check(panel.row_count() == 4, "应有 4 行设置，得到 %d" % panel.row_count())
	_check(panel.value_text("ui_scale") == "100%", "界面缩放应显示 100%%，得到 %s" % panel.value_text("ui_scale"))
	_check(panel.value_text("safe_area_pct") == "5%", "安全区应显示 5%%，得到 %s" % panel.value_text("safe_area_pct"))
	_check(panel.value_text("mouse_sensitivity") == "1.00×",
		"灵敏度应显示 1.00×，得到 %s" % panel.value_text("mouse_sensitivity"))

	# —— 拖滑块 → 偏好 → 消费者 ——
	panel.set_slider_for_test("mouse_sensitivity", 2.0)
	_check(absf(_main.ui.settings().value("mouse_sensitivity") - 2.0) < 0.001,
		"拖动滑块应写进偏好，得到 %f" % _main.ui.settings().value("mouse_sensitivity"))
	_check(absf(_main.look_scale() - 2.0) < 0.001, "main 应拿到新的灵敏度倍率")

	panel.set_slider_for_test("safe_area_pct", 0.08)
	_check(absf(_main.ui.hud._safe_pct - 0.08) < 0.001,
		"HUD 安全区应跟着偏好走，得到 %f" % _main.ui.hud._safe_pct)

	panel.set_slider_for_test("ui_scale", 1.5)
	_check(absf(root.content_scale_factor - 1.5) < 0.001,
		"界面缩放应写进窗口的 content_scale_factor，得到 %f" % root.content_scale_factor)

	panel.set_slider_for_test("hit_flash", 0.0)
	_check(absf(_main.ui.settings().value("hit_flash")) < 0.001, "受击反馈强度应能关到 0")

	# 越界值必须被夹回区间：滑块之外还有存档文件可以被人手改
	_main.ui.intent_set_setting("mouse_sensitivity", 99.0)
	_check(absf(_main.ui.settings().value("mouse_sensitivity") - 3.0) < 0.001,
		"灵敏度应被夹到上限 3.0，得到 %f" % _main.ui.settings().value("mouse_sensitivity"))

	# —— 恢复默认 ——
	_main.ui.intent_reset_settings()
	_check(absf(_main.ui.settings().value("safe_area_pct") - 0.05) < 0.001, "恢复默认应回到 5% 安全区")
	_check(absf(root.content_scale_factor - 1.0) < 0.001, "恢复默认应把界面缩放推回 100%")

	# —— 离开对局时自动收起（否则鼠标会卡在未捕获状态）——
	if not _main.ui.is_paused():
		_main.ui.intent_toggle_pause()
	_check(_main.ui.is_paused(), "进结算前应处于设置层打开状态")
	_main.ui.on_match_ended({"winner_slot": 0, "slots": []})
	_check(_main.ui.state() == ScreenManager.State.RESULT, "应进入结算态")
	_check(not _main.ui.is_paused(), "进入结算应自动收起设置层")
	_check(not _main.ui.pause_screen.visible, "结算层上不该还盖着设置层")

	_done["pause"] = true
	_finish()

## _sever_client_signals 断开真实客户端上的全部接线（main 与界面各接了一份）。
func _sever_client_signals(client: Node) -> void:
	for sig_name in ["frame_received", "matched_received", "connection_changed", "login_result",
			"logic_state_received", "profile_received", "match_cancel_received",
			"match_status_received", "match_ended_received",
			# 进大厅时的询问框也接在客户端上：本地跑着集群时，一次真实的
			# match.pending 应答会弹出询问框，把本用例的界面状态盖掉。
			"pending_match_received", "abandon_match_received"]:
		for conn: Dictionary in client.get_signal_connection_list(sig_name):
			client.disconnect(sig_name, conn["callable"])

func _check(cond: bool, msg: String) -> void:
	if not cond:
		_failures += 1
		printerr("FAIL: " + msg)

func _finish() -> void:
	if not _done.has("pause"):
		_failures += 1
		printerr("FAIL: 用例没跑完（中途抛错了？）")
	if _failures > 0:
		printerr("pause_settings_test: %d 项失败" % _failures)
		quit(1)
	else:
		print("pause_settings_test: OK")
		quit(0)

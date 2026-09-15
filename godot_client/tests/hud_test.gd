extends SceneTree
## 对局内 HUD：用合成帧驱动 main 的真实渲染路径，断言血条 / 对手血条 / 回合进度 /
## 击杀播报 / 伤害数字 / 准星命中反馈 / 安全区 / 整层不吃鼠标事件。
##
## 运行：Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client \
##         --script res://tests/hud_test.gd

const MainScene := preload("res://scenes/main.tscn")
const ScreenManager := preload("res://ui/screen_manager.gd")
const Tokens := preload("res://theme/tokens.gd")

## 属性表要覆盖 main 渲染路径（_refresh_derived）会读的那些 —— 少了 Pos/Body.* 之类，
## main 会在读属性时抛「Nonexistent constructor」。与 game_frame_test.gd 的 SCHEMA 对齐。
const SCHEMA := {"fields": [
	{"id": 1, "name": "Health", "kind": 0},
	{"id": 2, "name": "Player.Idx", "kind": 1},
	{"id": 3, "name": "Player.Kills", "kind": 1},
	{"id": 4, "name": "Player.Deaths", "kind": 1},
	{"id": 5, "name": "Pos", "kind": 5},
	{"id": 6, "name": "Body.Kind", "kind": 1},
	{"id": 7, "name": "Body.Static", "kind": 2},
	{"id": 8, "name": "Facing", "kind": 0},
], "version": 1}

var _failures := 0
var _done := {}
var _main: Node = null

func _initialize() -> void:
	_run()

func _run() -> void:
	_main = MainScene.instantiate()
	root.add_child(_main)
	await process_frame
	await process_frame

	# 进局（HUD 只在 IN_MATCH 可见；这里直接推进界面状态）
	_main.ui.setup(_main.fps_client, _main)
	_main.ui.on_login_result({"ok": true, "username": "u1"})
	_main.ui.intent_start_match()
	_main.ui.on_matched({"match_id": "m1", "player_idx": 0})
	_check(_main.ui.state() == ScreenManager.State.IN_MATCH, "应处于对局态")
	_check(_main.ui.hud.visible, "对局中 HUD 应可见")

	_main._on_frame({"full": true, "step": 1, "schema": SCHEMA, "entities": [
		{"id": 100, "destroy": false, "removed": [], "set": [
			{"id": 1, "f": [60.0]}, {"id": 2, "i": 0}, {"id": 3, "i": 4}, {"id": 4, "i": 1},
			{"id": 5, "f": [0.0, 0.2, 18.0]}, {"id": 8, "f": [0.0]}]},
		{"id": 101, "destroy": false, "removed": [], "set": [
			{"id": 1, "f": [25.0]}, {"id": 2, "i": 1}, {"id": 3, "i": 7}, {"id": 4, "i": 3},
			{"id": 5, "f": [0.0, 0.2, -18.0]}, {"id": 8, "f": [3.14]}]},
	]})
	var hud: Control = _main.ui.hud
	hud.update_from_world(_main._store, 0, 58.0)

	_check(hud.health_text() == "60 / 100", "自己血量应是「60 / 100」，得到 %s" % hud.health_text())
	_check(absf(hud.health_ratio() - 0.6) < 0.01, "自己血条比例应是 0.6，得到 %f" % hud.health_ratio())
	_check(absf(hud.opp_health_ratio() - 0.25) < 0.01,
		"对手血条比例应是 0.25，得到 %f" % hud.opp_health_ratio())

	# 血条按剩余比例换配色：60% 属于「警告」档（琥珀），25% 是「残血」档（红）。
	# 阈值在 tokens.health_color 里，界面只切主题变体 —— 断言值与阈值函数一致。
	var bar := hud.get_node("%HealthBar") as ProgressBar
	_check(bar.theme_type_variation == "HealthBarWarn",
		"60%% 血量应使用 HealthBarWarn 变体，得到 %s" % bar.theme_type_variation)
	var fill := bar.get_theme_stylebox("fill") as StyleBoxFlat
	_check(fill != null and fill.bg_color == Tokens.health_color(0.6),
		"血条填充色应与 tokens.health_color(0.6) 一致")
	# 两条血条必须不同色：余光里分不清「我的血」和「对手的血」是对枪时最贵的错误。
	var opp_bar := hud.get_node("%OppHealthBar") as ProgressBar
	var opp_fill := opp_bar.get_theme_stylebox("fill") as StyleBoxFlat
	_check(opp_fill != null and opp_fill.bg_color != fill.bg_color, "自己与对手的血条不该同色")
	# 血条 value 走补间（skill：step = 0 才能平滑），所以这里断言的是「比例」而不是瞬时值。
	_check(bar.step == 0.0, "血条 step 应为 0，否则补间会卡在整数刻度上")

	# 整层不吃鼠标事件：HUD 只是显示，鼠标要留给战场。
	_check(hud.mouse_filter == Control.MOUSE_FILTER_IGNORE, "HUD 根节点应忽略鼠标事件")
	_check((hud.get_node("%KillFeed") as Control).mouse_filter == Control.MOUSE_FILTER_IGNORE,
		"击杀播报栈应忽略鼠标事件")

	# 安全区：%SafeArea 负责四边留白，面板挂在它内部的 Field 上按锚点贴角
	# （MarginContainer 是容器，直接挂面板会被拉成同一个满格矩形）。
	var self_panel := hud.get_node("%SelfPanel") as Control
	var panel_parent := self_panel.get_parent()
	_check(panel_parent != null and panel_parent.name == "Field"
			and panel_parent.get_parent() != null and panel_parent.get_parent().name == "SafeArea",
		"HUD 面板应挂在 SafeArea/Field 下，实际父节点 %s" % (panel_parent.name if panel_parent != null else "null"))
	# 贴边面板必须靠锚点定位（skill 清单）：左上面板锚在左上，右上面板锚在右上。
	_check(self_panel.anchor_left == 0.0 and self_panel.anchor_right == 0.0,
		"左上面板应锚在左侧，得到 anchor_left=%f" % self_panel.anchor_left)
	_check((hud.get_node("%OppPanel") as Control).anchor_left == 1.0,
		"右上面板应锚在右侧")
	hud.set_safe_pct(0.05)
	# 直接喂一个已知尺寸：无头窗口的实际大小不该决定这条断言的成败。
	hud._apply_safe_area(Vector2(1000, 500))
	var margins: Vector2 = hud.safe_margins()
	_check(absf(margins.x - 50.0) < 0.01 and absf(margins.y - 25.0) < 0.01,
		"5%% 安全区在 1000×500 下应是 50/25，得到 %s" % margins)
	hud.set_safe_pct(0.0)
	_check(hud.safe_margins() == Vector2.ZERO, "安全区 0%% 应完全贴边，得到 %s" % hud.safe_margins())
	hud.set_safe_pct(0.05)

	_check(absf(hud.round_ratio() - 0.4) < 0.01, "回合进度应是 4/10，得到 %f" % hud.round_ratio())
	_check(hud.round_text().begins_with("先到 10 杀"), "回合文案应以「先到 10 杀」开头")
	_check(hud.kill_feed_count() == 0, "本局第一帧是初始快照，不该产生播报，得到 %d" % hud.kill_feed_count())
	_check(hud.damage_number_count() == 0, "本局第一帧不该冒伤害数字，得到 %d" % hud.damage_number_count())

	# 击杀播报：自己 4→6 杀、对手 3→5 杀 → 两条「你击杀了对手」
	_main._on_frame({"full": false, "entities": [
		{"id": 100, "destroy": false, "removed": [], "set": [{"id": 3, "i": 6}]},
		{"id": 101, "destroy": false, "removed": [], "set": [{"id": 4, "i": 5}]},
	]})
	hud.update_from_world(_main._store, 0, 58.0)
	_check(hud.kill_feed_count() == 2, "两杀应产生两条播报，得到 %d" % hud.kill_feed_count())

	# 播报上限：连推 6 次击杀也只保留 4 条
	_main._on_frame({"full": false, "entities": [
		{"id": 100, "destroy": false, "removed": [], "set": [{"id": 3, "i": 12}]},
	]})
	hud.update_from_world(_main._store, 0, 58.0)
	_check(hud.kill_feed_count() <= 4, "播报最多 4 条，得到 %d" % hud.kill_feed_count())

	# 准星命中反馈：默认不亮，notify_hit_landed 之后亮
	_check(not hud.crosshair_hot(), "默认准星不应处于点亮状态")
	hud.notify_hit_landed()
	_check(hud.crosshair_hot(), "命中后准星应点亮")

	# 命中反馈的触发点在 main：对手掉血时它会调 notify_hit_landed；
	# 同一次掉血也让 HUD 冒一个伤害数字（读的是同一份血量，不是第二套命中判定）。
	_main._on_frame({"full": false, "entities": [
		{"id": 101, "destroy": false, "removed": [], "set": [{"id": 1, "f": [5.0]}]},
	]})
	hud.update_from_world(_main._store, 0, 58.0)
	_check(hud.crosshair_hot(), "对手掉血应由 main 触发准星点亮")
	_check(hud.damage_number_count() == 1, "对手掉血应冒一个伤害数字，得到 %d" % hud.damage_number_count())
	_check(absf(_main._last_opp_health - 5.0) < 0.01, "main 应记住对手血量（命中判定用）")

	# 新一局：战绩、播报、伤害数字全部清空（上一局的残留会让玩家以为自己已经领先）
	hud.reset()
	_check(hud.kill_feed_count() == 0 and hud.damage_number_count() == 0, "reset 应清空播报与伤害数字")
	_check(hud.round_ratio() == 0.0, "reset 后回合进度应归零")
	_check(hud.self_bar_value() == 100.0, "reset 后血条应回满")

	_done["hud"] = true
	_finish()

func _check(cond: bool, msg: String) -> void:
	if not cond:
		_failures += 1
		printerr("FAIL: " + msg)

func _finish() -> void:
	if not _done.has("hud"):
		_failures += 1
		printerr("FAIL: 用例没跑完（中途抛错了？）")
	if _failures > 0:
		printerr("hud_test: %d 项失败" % _failures)
		quit(1)
	else:
		print("hud_test: OK")
		quit(0)

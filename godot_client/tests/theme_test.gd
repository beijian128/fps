extends SceneTree
## 主题契约：tactical_theme.tres 必须与 theme/tokens.gd 一致，且满足界面依赖的那些「不可缺席」项。
##
## 运行：Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client \
##         --script res://tests/theme_test.gd
##
## 为什么值得测：主题是**生成物**（theme/build_theme.gd 从 tokens.gd 写出来）。生成物与源一旦
## 漂移，界面会有的地方新配色、有的地方旧配色 —— 这种不一致肉眼很难发现，必须由测试钉住。
## 另一半断言是「控件样式不许缺席」：缺一个，Godot 会静默退回**默认浅色主题**，暗色界面里
## 一眼出戏（滑杆曾经就是这样）。

const Tokens := preload("res://theme/tokens.gd")
const THEME_PATH := "res://theme/tactical_theme.tres"

var _failures := 0
var _done := {}

func _initialize() -> void:
	_test_theme_exists()
	_test_colors_match_tokens()
	_test_radii_and_components()
	_test_fonts()
	_test_interactive_styles()
	for name: String in ["exists", "colors", "radii", "fonts", "interactive"]:
		if not _done.has(name):
			_failures += 1
			printerr("FAIL: 用例 %s 没跑完（中途抛错了？）" % name)
	if _failures > 0:
		printerr("theme_test: %d 项失败" % _failures)
		quit(1)
	else:
		print("theme_test: OK")
		quit(0)

func _check(cond: bool, msg: String) -> void:
	if not cond:
		_failures += 1
		printerr("FAIL: " + msg)

func _theme() -> Theme:
	return load(THEME_PATH) as Theme

func _test_theme_exists() -> void:
	_check(ResourceLoader.exists(THEME_PATH), "主题资源不存在，先跑 theme/build_theme.gd")
	_check(_theme() != null, "主题资源加载失败")
	_done["exists"] = true

func _test_colors_match_tokens() -> void:
	var t := _theme()
	if t == null:
		return
	_check(t.get_color("font_color", "Label") == Tokens.TEXT, "Label 文字色应与 tokens.TEXT 一致")
	_check(t.get_color("font_color", "Button") == Tokens.TEXT, "Button 文字色应与 tokens.TEXT 一致")

	var primary := t.get_stylebox("normal", "PrimaryButton") as StyleBoxFlat
	_check(primary != null and primary.bg_color == Tokens.ACCENT, "主按钮底色应是 tokens.ACCENT")
	_check(t.get_color("font_color", "PrimaryButton") == Tokens.ON_ACCENT,
		"主按钮文字色应是 tokens.ON_ACCENT（琥珀底上必须是深色字，否则对比度不够）")

	# Godot 的 Panel / PanelContainer 用的样式盒项名是 "panel"（不是 "normal"）
	for type in ["Panel", "PanelContainer"]:
		_check(t.has_stylebox("panel", type), "缺 %s 的 panel 样式" % type)
	var card := t.get_stylebox("panel", "CardPanel") as StyleBoxFlat
	_check(card != null and card.bg_color == Tokens.PANEL, "卡片底色应是 tokens.PANEL（比外框亮一层）")
	var bar := t.get_stylebox("panel", "BarPanel") as StyleBoxFlat
	_check(bar != null and bar.bg_color == Tokens.SURFACE, "状态条底色应是 tokens.SURFACE")

	# 语义色变体：胜负行、错误行都靠它们，色值漂了就一眼看出不对。
	_check(t.get_color("font_color", "LabelMuted") == Tokens.TEXT_MUTED, "LabelMuted 应是 tokens.TEXT_MUTED")
	_check(t.get_color("font_color", "LabelDanger") == Tokens.DANGER, "LabelDanger 应是 tokens.DANGER")
	_check(t.get_color("font_color", "HeadlineWin") == Tokens.SUCCESS, "胜利大字应是 tokens.SUCCESS")
	_check(t.get_color("font_color", "HeadlineLose") == Tokens.DANGER, "失败大字应是 tokens.DANGER")
	_done["colors"] = true

func _test_radii_and_components() -> void:
	var t := _theme()
	if t == null:
		return
	# 圆角是这一版设计的一部分：所有样式盒都从 tokens 取，界面里不出现裸数值。
	for pair in [["normal", "Button"], ["panel", "CardPanel"], ["fill", "HealthBar"]]:
		var sb := t.get_stylebox(pair[0], pair[1]) as StyleBoxFlat
		_check(sb != null, "缺 %s/%s 样式" % [pair[1], pair[0]])
		if sb != null:
			_check(sb.corner_radius_top_left == Tokens.RADIUS_MD,
				"%s/%s 圆角应取 tokens.RADIUS_MD" % [pair[1], pair[0]])
	# 血条 / 对手血条 / 回合条三色分离：三条并排时不许同色。
	var self_fill := t.get_stylebox("fill", "HealthBar") as StyleBoxFlat
	var opp_fill := t.get_stylebox("fill", "OppHealthBar") as StyleBoxFlat
	var round_fill := t.get_stylebox("fill", "RoundBar") as StyleBoxFlat
	_check(self_fill.bg_color != opp_fill.bg_color, "自己与对手的血条不该同色")
	_check(round_fill.bg_color == Tokens.ACCENT, "回合进度条应是琥珀（tokens.ACCENT）")
	# 血条三档配色都必须在主题里（界面只切 variation，不现造样式盒）。
	for pair in [["HealthBar", Tokens.SUCCESS], ["HealthBarWarn", Tokens.ACCENT],
			["HealthBarCritical", Tokens.DANGER], ["OppHealthBar", Tokens.DANGER]]:
		var fill := t.get_stylebox("fill", pair[0]) as StyleBoxFlat
		_check(fill != null and fill.bg_color == pair[1], "%s 的填充色不对" % pair[0])
	# 对局内浮层必须半透明，否则挡住战场。
	var hud_panel := t.get_stylebox("panel", "HudPanel") as StyleBoxFlat
	_check(hud_panel != null and hud_panel.bg_color.a < 1.0, "HUD 浮层应是半透明的")
	_done["radii"] = true

func _test_fonts() -> void:
	var t := _theme()
	if t == null:
		return
	var f := t.default_font as SystemFont
	_check(f != null, "默认字体应是 SystemFont（显式回退链，不依赖 Godot 默认字体）")
	_check(f != null and f.font_names.size() >= 3, "正文字体回退链应至少 3 项")
	_check(f != null and f.font_names[0] == "Microsoft YaHei UI", "正文字体首选应是 Microsoft YaHei UI")
	_check(Tokens.mono_font().font_names[0] == "Consolas", "数字字体首选应是 Consolas")
	_check(t.default_font_size == Tokens.FONT_BODY, "默认字号应是 tokens.FONT_BODY")
	# 字号档位：界面只允许用档位变体，不许写裸字号。
	for pair in [
		["LabelCaption", Tokens.FONT_CAPTION],
		["LabelSmall", Tokens.FONT_LABEL],
		["LabelSub", Tokens.FONT_SUB],
		["LabelTitle", Tokens.FONT_TITLE],
		["LabelHero", Tokens.FONT_HERO],
	]:
		_check(t.get_font_size("font_size", pair[0]) == pair[1], "%s 字号应是 tokens.%d" % pair)
	# 等宽族：数字必须走 mono 字体，否则比分 / 时长在跳动时会左右抖。
	for type in ["LabelMono", "LabelMonoSub", "LabelMonoHero"]:
		var mono := t.get_font("font", type) as SystemFont
		_check(mono != null and mono.font_names[0] == "Consolas", "%s 应使用等宽字体" % type)
	_done["fonts"] = true

## _test_interactive_styles 交互控件必须「看得见焦点」与「不缺样式」。
## 焦点描边是键盘 / 手柄玩家的唯一导航线索；样式缺失则退回默认浅色主题。
func _test_interactive_styles() -> void:
	var t := _theme()
	if t == null:
		return
	for type in ["Button", "PrimaryButton", "GhostButton", "NavButton", "NavCard", "StepButton"]:
		for state in ["normal", "hover", "pressed", "disabled"]:
			_check(t.has_stylebox(state, type), "%s 缺 %s 样式" % [type, state])
		var focus := t.get_stylebox("focus", type) as StyleBoxFlat
		_check(focus != null, "%s 的 focus 应是可见描边，而不是 StyleBoxEmpty" % type)
		if focus != null:
			_check(focus.border_width_left >= 2, "%s 的 focus 描边应不少于 2px" % type)
	# 设置面板：滑杆 / 输入框 / 选项卡 / 折叠组。
	for pair in [["slider", "HSlider"], ["grabber", "HSlider"], ["normal", "LineEdit"],
			["focus", "LineEdit"], ["panel", "TabContainer"], ["panel", "FoldableContainer"]]:
		_check(t.has_stylebox(pair[0], pair[1]), "缺 %s 的 %s 样式" % [pair[1], pair[0]])
	# 语义：低血变红、中等变琥珀 —— 阈值在 tokens 里，界面不许自己定。
	_check(Tokens.health_color(1.0) == Tokens.SUCCESS, "满血应是绿色")
	_check(Tokens.health_color(0.5) == Tokens.ACCENT, "半血应是琥珀色")
	_check(Tokens.health_color(0.2) == Tokens.DANGER, "残血应是红色")
	_done["interactive"] = true

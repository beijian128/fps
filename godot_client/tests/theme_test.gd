extends SceneTree
## 主题与 tokens 必须一致：任何「改了 tokens 忘了重跑 build_theme」都在这里失败。
##
## 运行：Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client \
##         --script res://tests/theme_test.gd
##
## 为什么要有这条测试：五个界面全部从 tactical_theme.tres 取样式，而那个文件是**生成物**。
## 生成物与源（tokens.gd）一旦漂移，界面就会有的地方是新配色、有的地方是旧配色 —— 这种
## 不一致肉眼很难发现，必须由测试钉住。

const Tokens := preload("res://theme/tokens.gd")
const THEME_PATH := "res://theme/tactical_theme.tres"

var _failures := 0
var _done := {}

func _initialize() -> void:
	_test_theme_exists()
	_test_colors_match_tokens()
	_test_button_variants()
	_test_fonts()
	for name: String in ["exists", "colors", "variants", "fonts"]:
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
	_check(ResourceLoader.exists(THEME_PATH), "主题资源不存在，先跑 build_theme.gd")
	_check(_theme() != null, "主题资源加载失败")
	_done["exists"] = true

func _test_colors_match_tokens() -> void:
	var t := _theme()
	if t == null:
		return
	_check(t.get_color("font_color", "Label") == Tokens.TEXT, "Label 文字色应与 tokens.TEXT 一致")
	_check(t.get_color("font_color", "Button") == Tokens.TEXT, "Button 文字色应与 tokens.TEXT 一致")
	_check(t.has_stylebox("normal", "PrimaryButton"), "缺 PrimaryButton 的 normal 样式")
	var box := t.get_stylebox("normal", "PrimaryButton") as StyleBoxFlat
	_check(box != null and box.bg_color == Tokens.ACCENT, "主按钮底色应是 tokens.ACCENT")
	# Godot 的 Panel / PanelContainer 用的样式盒项名是 "panel"（不是 "normal"）
	_check(t.has_stylebox("panel", "Panel"), "缺 Panel 样式")
	var panel := t.get_stylebox("panel", "Panel") as StyleBoxFlat
	_check(panel != null and panel.bg_color == Tokens.PANEL, "面板底色应是 tokens.PANEL")
	var pb := t.get_stylebox("fill", "ProgressBar") as StyleBoxFlat
	_check(pb != null and pb.bg_color == Tokens.ACCENT, "进度条填充应是 tokens.ACCENT")
	_done["colors"] = true

func _test_button_variants() -> void:
	var t := _theme()
	if t == null:
		return
	for state in ["normal", "hover", "pressed", "disabled"]:
		_check(t.has_stylebox(state, "PrimaryButton"), "PrimaryButton 缺 %s" % state)
		_check(t.has_stylebox(state, "GhostButton"), "GhostButton 缺 %s" % state)
	_check(t.get_stylebox("normal", "PrimaryButton") is StyleBoxFlat, "按钮样式应是 StyleBoxFlat")
	# 直角是暗色战术风的一部分（不是圆角卡片风）。
	var primary := t.get_stylebox("normal", "PrimaryButton") as StyleBoxFlat
	_check(primary.corner_radius_top_left == 0 and primary.corner_radius_bottom_right == 0,
		"战术风按钮应是直角（corner_radius = 0）")
	_done["variants"] = true

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
	# 三档字号变体都要在
	_check(t.get_font_size("font_size", "LabelSmall") == Tokens.FONT_LABEL, "LabelSmall 字号不对")
	_check(t.get_font_size("font_size", "LabelSub") == Tokens.FONT_SUB, "LabelSub 字号不对")
	_check(t.get_font_size("font_size", "LabelTitle") == Tokens.FONT_TITLE, "LabelTitle 字号不对")
	_done["fonts"] = true

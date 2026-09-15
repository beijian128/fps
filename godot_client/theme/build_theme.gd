extends SceneTree
## 从 theme/tokens.gd 生成 theme/tactical_theme.tres。
##
## 运行：Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client \
##         --script res://theme/build_theme.gd
##
## 生成物必须提交（与 .redis.go 同理：生成的文件也是仓库的一部分）。改配色只改 tokens.gd，
## 然后重跑本脚本 —— `tests/theme_test.gd` 会挡住「改了 tokens 忘了重跑」。
##
## 两类字体的分工：正文字体（body_font）挂在 Theme 默认字体上；等宽字体（mono_font）不挂在
## 默认字体上，而是由界面按需 `add_theme_font_override("font", Tokens.mono_font())` —— 只有
## 数字型的 Label 才需要它。

const Tokens := preload("res://theme/tokens.gd")
const OUT_PATH := "res://theme/tactical_theme.tres"

func _initialize() -> void:
	var theme := Theme.new()
	theme.default_font = Tokens.body_font()
	theme.default_font_size = Tokens.FONT_BODY

	_label(theme)
	_button(theme)
	_panel(theme)
	_progress(theme)
	_line_edit(theme)
	_scrollbar(theme)
	_item_list(theme)
	_tree(theme)

	var err := ResourceSaver.save(theme, OUT_PATH)
	if err != OK:
		printerr("save theme failed: ", err)
		quit(1)
		return
	print("theme written: ", OUT_PATH)
	quit(0)

# ---- 各类控件 ----

func _label(theme: Theme) -> void:
	theme.set_color("font_color", "Label", Tokens.TEXT)
	theme.set_font_size("font_size", "Label", Tokens.FONT_BODY)
	# 三档字号变体：界面用 theme_type_variation = "LabelTitle" 等选用。
	for pair in [
		["LabelSmall", Tokens.FONT_LABEL],
		["LabelSub", Tokens.FONT_SUB],
		["LabelTitle", Tokens.FONT_TITLE],
		["LabelHero", Tokens.FONT_HERO],
	]:
		theme.set_type_variation(pair[0], "Label")
		theme.set_font_size("font_size", pair[0], pair[1])
	# 常用的语义色变体（胜负行、错误行、次要说明）
	for pair in [
		["LabelMuted", "Label", Tokens.TEXT_MUTED],
		["LabelSuccess", "Label", Tokens.SUCCESS],
		["LabelDanger", "Label", Tokens.DANGER],
		["LabelAccent", "Label", Tokens.ACCENT],
	]:
		theme.set_type_variation(pair[0], pair[1])
		theme.set_color("font_color", pair[0], pair[2])
		theme.set_font_size("font_size", pair[0], Tokens.FONT_BODY)

func _button(theme: Theme) -> void:
	# 基础 Button：描边风格（次要动作的默认观感）
	theme.set_stylebox("normal", "Button", _box(Tokens.PANEL, Tokens.BORDER_STRONG, 1))
	theme.set_stylebox("hover", "Button", _box(Tokens.PANEL_ALT, Tokens.ACCENT, 1))
	theme.set_stylebox("pressed", "Button", _box(Tokens.BORDER, Tokens.ACCENT, 1))
	theme.set_stylebox("disabled", "Button", _box(Tokens.PANEL, Tokens.BORDER, 1))
	theme.set_stylebox("focus", "Button", StyleBoxEmpty.new())
	theme.set_color("font_color", "Button", Tokens.TEXT)
	theme.set_color("font_hover_color", "Button", Tokens.ACCENT)
	theme.set_color("font_pressed_color", "Button", Tokens.ACCENT)
	theme.set_color("font_disabled_color", "Button", Tokens.TEXT_DIM)
	theme.set_font_size("font_size", "Button", Tokens.FONT_BODY)

	# PrimaryButton：琥珀底 + 深色字（一个界面里只该有一个）
	theme.set_type_variation("PrimaryButton", "Button")
	theme.set_stylebox("normal", "PrimaryButton", _box(Tokens.ACCENT, Tokens.ACCENT, 0))
	theme.set_stylebox("hover", "PrimaryButton", _box(Tokens.ACCENT.lightened(0.12), Tokens.ACCENT, 0))
	theme.set_stylebox("pressed", "PrimaryButton", _box(Tokens.ACCENT.darkened(0.15), Tokens.ACCENT, 0))
	theme.set_stylebox("disabled", "PrimaryButton", _box(Tokens.BORDER, Tokens.BORDER, 0))
	theme.set_stylebox("focus", "PrimaryButton", StyleBoxEmpty.new())
	theme.set_color("font_color", "PrimaryButton", Tokens.ON_ACCENT)
	theme.set_color("font_hover_color", "PrimaryButton", Tokens.ON_ACCENT)
	theme.set_color("font_pressed_color", "PrimaryButton", Tokens.ON_ACCENT)
	theme.set_color("font_disabled_color", "PrimaryButton", Tokens.TEXT_DIM)
	theme.set_font_size("font_size", "PrimaryButton", Tokens.FONT_BODY)

	# GhostButton：只有描边与浅字（取消、返回这类）
	theme.set_type_variation("GhostButton", "Button")
	theme.set_stylebox("normal", "GhostButton", _box(Color(0, 0, 0, 0), Tokens.BORDER_STRONG, 1))
	theme.set_stylebox("hover", "GhostButton", _box(Color(0, 0, 0, 0), Tokens.ACCENT, 1))
	theme.set_stylebox("pressed", "GhostButton", _box(Tokens.PANEL, Tokens.ACCENT, 1))
	theme.set_stylebox("disabled", "GhostButton", _box(Color(0, 0, 0, 0), Tokens.BORDER, 1))
	theme.set_stylebox("focus", "GhostButton", StyleBoxEmpty.new())
	theme.set_color("font_color", "GhostButton", Tokens.TEXT_MUTED)
	theme.set_color("font_hover_color", "GhostButton", Tokens.ACCENT)
	theme.set_color("font_pressed_color", "GhostButton", Tokens.ACCENT)
	theme.set_color("font_disabled_color", "GhostButton", Tokens.TEXT_DIM)
	theme.set_font_size("font_size", "GhostButton", Tokens.FONT_BODY)

func _panel(theme: Theme) -> void:
	for type in ["Panel", "PanelContainer"]:
		theme.set_stylebox("panel", type, _box(Tokens.PANEL, Tokens.BORDER, 1))
	theme.set_type_variation("CardPanel", "PanelContainer")
	theme.set_stylebox("panel", "CardPanel", _box(Tokens.PANEL, Tokens.BORDER, 1))
	theme.set_type_variation("FramePanel", "PanelContainer")
	theme.set_stylebox("panel", "FramePanel", _box(Tokens.SURFACE, Tokens.BORDER, 1))
	theme.set_type_variation("BarPanel", "PanelContainer")
	theme.set_stylebox("panel", "BarPanel", _box(Tokens.PANEL_ALT, Tokens.BORDER, 1, Tokens.SP_2, Tokens.SP_1))

func _progress(theme: Theme) -> void:
	theme.set_stylebox("background", "ProgressBar", _box(Tokens.PANEL, Tokens.BORDER, 1))
	theme.set_stylebox("fill", "ProgressBar", _box(Tokens.ACCENT, Tokens.ACCENT, 0))
	theme.set_color("font_color", "ProgressBar", Tokens.TEXT)
	theme.set_font_size("font_size", "ProgressBar", Tokens.FONT_LABEL)
	# 血条变体（自伤用红、对手用红；经验条用琥珀）
	theme.set_type_variation("HealthBar", "ProgressBar")
	theme.set_stylebox("fill", "HealthBar", _box(Tokens.DANGER, Tokens.DANGER, 0))

func _line_edit(theme: Theme) -> void:
	theme.set_stylebox("normal", "LineEdit", _box(Tokens.PANEL, Tokens.BORDER_STRONG, 1))
	theme.set_stylebox("focus", "LineEdit", _box(Tokens.PANEL, Tokens.ACCENT, 1))
	theme.set_color("font_color", "LineEdit", Tokens.TEXT)
	theme.set_color("font_placeholder_color", "LineEdit", Tokens.TEXT_DIM)
	theme.set_color("caret_color", "LineEdit", Tokens.ACCENT)
	theme.set_font_size("font_size", "LineEdit", Tokens.FONT_BODY)

func _scrollbar(theme: Theme) -> void:
	for type in ["VScrollBar", "HScrollBar"]:
		theme.set_stylebox("scroll", type, _box(Tokens.PANEL_ALT, Color(0, 0, 0, 0), 0, Tokens.SP_1, Tokens.SP_1))
		theme.set_stylebox("grabber", type, _box(Tokens.BORDER_STRONG, Tokens.BORDER_STRONG, 0))
		theme.set_stylebox("grabber_highlight", type, _box(Tokens.ACCENT, Tokens.ACCENT, 0))
		theme.set_stylebox("grabber_pressed", type, _box(Tokens.ACCENT, Tokens.ACCENT, 0))

func _item_list(theme: Theme) -> void:
	theme.set_stylebox("panel", "ItemList", _box(Tokens.PANEL, Tokens.BORDER, 1))
	theme.set_stylebox("selected", "ItemList", _box(Tokens.BORDER, Tokens.ACCENT, 1))
	theme.set_stylebox("selected_focus", "ItemList", _box(Tokens.BORDER, Tokens.ACCENT, 1))
	theme.set_stylebox("hovered", "ItemList", _box(Tokens.PANEL_ALT, Tokens.BORDER_STRONG, 1))
	theme.set_stylebox("focus", "ItemList", StyleBoxEmpty.new())
	theme.set_color("font_color", "ItemList", Tokens.TEXT)
	theme.set_color("font_selected_color", "ItemList", Tokens.ACCENT)
	theme.set_font_size("font_size", "ItemList", Tokens.FONT_BODY)
	theme.set_constant("v_separation", "ItemList", Tokens.SP_1)

func _tree(theme: Theme) -> void:
	theme.set_stylebox("panel", "Tree", _box(Tokens.PANEL, Tokens.BORDER, 1))
	theme.set_stylebox("selected", "Tree", _box(Tokens.BORDER, Tokens.ACCENT, 1))
	theme.set_stylebox("selected_focus", "Tree", _box(Tokens.BORDER, Tokens.ACCENT, 1))
	theme.set_stylebox("focus", "Tree", StyleBoxEmpty.new())
	theme.set_stylebox("title_button_normal", "Tree", _box(Tokens.PANEL_ALT, Tokens.BORDER, 1))
	theme.set_color("font_color", "Tree", Tokens.TEXT)
	theme.set_color("font_selected_color", "Tree", Tokens.ACCENT)
	theme.set_font_size("font_size", "Tree", Tokens.FONT_BODY)
	theme.set_constant("v_separation", "Tree", Tokens.SP_1)

## _box 是这套主题唯一的样式盒工厂：暗色战术风 = 直角 + 细描边 + 紧凑内边距。
func _box(bg: Color, border: Color, width: int,
		margin_h: int = Tokens.SP_3, margin_v: int = Tokens.SP_2) -> StyleBoxFlat:
	var sb := StyleBoxFlat.new()
	sb.bg_color = bg
	sb.border_color = border
	sb.border_width_left = width
	sb.border_width_top = width
	sb.border_width_right = width
	sb.border_width_bottom = width
	sb.corner_radius_top_left = 0
	sb.corner_radius_top_right = 0
	sb.corner_radius_bottom_left = 0
	sb.corner_radius_bottom_right = 0
	sb.content_margin_left = margin_h
	sb.content_margin_right = margin_h
	sb.content_margin_top = margin_v
	sb.content_margin_bottom = margin_v
	return sb

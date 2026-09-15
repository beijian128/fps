extends SceneTree
## 从 theme/tokens.gd 生成 theme/tactical_theme.tres。
##
## 运行：Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client \
##         --script res://theme/build_theme.gd
##
## 生成物必须提交（与 .redis.go 同理：生成的文件也是仓库的一部分）。改配色 / 圆角 / 间距只改
## tokens.gd，然后重跑本脚本 —— tests/theme_test.gd 会挡住「改了 tokens 忘了重跑」。
##
## 两类字体：正文字体（body_font）挂在 Theme 默认字体上；等宽字体（mono_font）不挂默认，
## 而是给 LabelMono* 这一族变体单独设 font —— 只有数字 / 比分 / 时长才需要等宽。

const Tokens := preload("res://theme/tokens.gd")
const OUT_PATH := "res://theme/tactical_theme.tres"

func _initialize() -> void:
	var theme := Theme.new()
	theme.default_font = Tokens.body_font()
	theme.default_font_size = Tokens.FONT_BODY

	_label(theme)
	_button(theme)
	_input(theme)
	_panel(theme)
	_progress(theme)
	_tabs(theme)
	_foldable(theme)
	_popup(theme)
	_scrollbar(theme)
	_list(theme)
	_misc(theme)

	var err := ResourceSaver.save(theme, OUT_PATH)
	if err != OK:
		printerr("save theme failed: ", err)
		quit(1)
		return
	print("theme written: ", OUT_PATH)
	quit(0)

# ---- Label ----

func _label(theme: Theme) -> void:
	theme.set_color("font_color", "Label", Tokens.TEXT)
	theme.set_font_size("font_size", "Label", Tokens.FONT_BODY)

	# 字号档位：界面用 theme_type_variation = "LabelTitle" 等选用，不写裸字号。
	for pair in [
		["LabelCaption", Tokens.FONT_CAPTION],
		["LabelSmall", Tokens.FONT_LABEL],
		["LabelSub", Tokens.FONT_SUB],
		["LabelTitle", Tokens.FONT_TITLE],
		["LabelHero", Tokens.FONT_HERO],
	]:
		theme.set_type_variation(pair[0], "Label")
		theme.set_font_size("font_size", pair[0], pair[1])

	# 语义色：胜负行、错误行、次要说明、强调行。
	for pair in [
		["LabelMuted", Tokens.TEXT_MUTED],
		["LabelDim", Tokens.TEXT_DIM],
		["LabelSuccess", Tokens.SUCCESS],
		["LabelDanger", Tokens.DANGER],
		["LabelAccent", Tokens.ACCENT],
		["LabelInfo", Tokens.INFO],
	]:
		theme.set_type_variation(pair[0], "Label")
		theme.set_color("font_color", pair[0], pair[1])
		theme.set_font_size("font_size", pair[0], Tokens.FONT_BODY)

	# 等宽数字族：血条读数、K/D、比分、时长、队列秒数。战术感的来源是数字对齐。
	for pair in [
		["LabelMono", Tokens.FONT_BODY],
		["LabelMonoSmall", Tokens.FONT_LABEL],
		["LabelMonoSub", Tokens.FONT_SUB],
		["LabelMonoTitle", Tokens.FONT_TITLE],
		["LabelMonoHero", Tokens.FONT_HERO],
	]:
		theme.set_type_variation(pair[0], "Label")
		theme.set_font("font", pair[0], Tokens.mono_font())
		theme.set_font_size("font_size", pair[0], pair[1])
	theme.set_type_variation("LabelMonoMuted", "LabelMonoSmall")
	theme.set_color("font_color", "LabelMonoMuted", Tokens.TEXT_MUTED)

	# 大标题：Hero 字号 + 等宽 + 字间距（用 outline_size 之外的唯一手段：加粗靠字体本身，
	# SystemFont 不提供 weight 参数，这里靠字号与颜色对比建立层级）。
	theme.set_type_variation("HeadlineWin", "Label")
	theme.set_font("font", "HeadlineWin", Tokens.mono_font())
	theme.set_font_size("font_size", "HeadlineWin", Tokens.FONT_HERO)
	theme.set_color("font_color", "HeadlineWin", Tokens.SUCCESS)
	theme.set_type_variation("HeadlineLose", "Label")
	theme.set_font("font", "HeadlineLose", Tokens.mono_font())
	theme.set_font_size("font_size", "HeadlineLose", Tokens.FONT_HERO)
	theme.set_color("font_color", "HeadlineLose", Tokens.DANGER)

# ---- Button ----

func _button(theme: Theme) -> void:
	# Button：次级动作的默认观感（描边 + 悬停点亮描边）。
	theme.set_stylebox("normal", "Button", _box(Tokens.PANEL, Tokens.BORDER_STRONG, 1,
		Tokens.SP_4, Tokens.SP_3))
	theme.set_stylebox("hover", "Button", _box(Tokens.PANEL_ALT, Tokens.ACCENT, 1,
		Tokens.SP_4, Tokens.SP_3))
	theme.set_stylebox("pressed", "Button", _box(Tokens.BORDER, Tokens.ACCENT, 1,
		Tokens.SP_4, Tokens.SP_3))
	theme.set_stylebox("disabled", "Button", _box(Tokens.SURFACE, Tokens.BORDER, 1,
		Tokens.SP_4, Tokens.SP_3))
	theme.set_stylebox("focus", "Button", _focus_box(Tokens.ACCENT))
	theme.set_color("font_color", "Button", Tokens.TEXT)
	theme.set_color("font_hover_color", "Button", Tokens.ACCENT)
	theme.set_color("font_pressed_color", "Button", Tokens.ACCENT)
	theme.set_color("font_disabled_color", "Button", Tokens.TEXT_DIM)
	theme.set_font_size("font_size", "Button", Tokens.FONT_BODY)

	# PrimaryButton：琥珀底 + 深色字（一个界面里只该有一个）。
	theme.set_type_variation("PrimaryButton", "Button")
	theme.set_stylebox("normal", "PrimaryButton", _box(Tokens.ACCENT, Tokens.ACCENT, 0,
		Tokens.SP_4, Tokens.SP_3))
	theme.set_stylebox("hover", "PrimaryButton", _box(Tokens.ACCENT.lightened(0.12), Tokens.ACCENT, 0,
		Tokens.SP_4, Tokens.SP_3))
	theme.set_stylebox("pressed", "PrimaryButton", _box(Tokens.ACCENT.darkened(0.15), Tokens.ACCENT, 0,
		Tokens.SP_4, Tokens.SP_3))
	theme.set_stylebox("disabled", "PrimaryButton", _box(Tokens.BORDER, Tokens.BORDER, 0,
		Tokens.SP_4, Tokens.SP_3))
	theme.set_stylebox("focus", "PrimaryButton", _focus_box(Tokens.TEXT))
	theme.set_color("font_color", "PrimaryButton", Tokens.ON_ACCENT)
	theme.set_color("font_hover_color", "PrimaryButton", Tokens.ON_ACCENT)
	theme.set_color("font_pressed_color", "PrimaryButton", Tokens.ON_ACCENT)
	theme.set_color("font_disabled_color", "PrimaryButton", Tokens.TEXT_DIM)
	theme.set_font_size("font_size", "PrimaryButton", Tokens.FONT_BODY)

	# GhostButton：只有描边与浅字（取消、返回、导航项）。
	theme.set_type_variation("GhostButton", "Button")
	theme.set_stylebox("normal", "GhostButton", _box(Color(0, 0, 0, 0), Tokens.BORDER_STRONG, 1,
		Tokens.SP_4, Tokens.SP_3))
	theme.set_stylebox("hover", "GhostButton", _box(Color(Tokens.ACCENT, 0.08), Tokens.ACCENT, 1,
		Tokens.SP_4, Tokens.SP_3))
	theme.set_stylebox("pressed", "GhostButton", _box(Tokens.PANEL, Tokens.ACCENT, 1,
		Tokens.SP_4, Tokens.SP_3))
	theme.set_stylebox("disabled", "GhostButton", _box(Color(0, 0, 0, 0), Tokens.BORDER, 1,
		Tokens.SP_4, Tokens.SP_3))
	theme.set_stylebox("focus", "GhostButton", _focus_box(Tokens.ACCENT))
	theme.set_color("font_color", "GhostButton", Tokens.TEXT_MUTED)
	theme.set_color("font_hover_color", "GhostButton", Tokens.ACCENT)
	theme.set_color("font_pressed_color", "GhostButton", Tokens.ACCENT)
	theme.set_color("font_disabled_color", "GhostButton", Tokens.TEXT_DIM)
	theme.set_font_size("font_size", "GhostButton", Tokens.FONT_BODY)

	# NavButton：大厅左侧导航。选中态 = 琥珀左竖条 + 亮字（比「整块变色」更安静，
	# 也避免和主按钮抢注意力）。Danger 变体给「卸下」这类反向动作。
	theme.set_type_variation("NavButton", "GhostButton")
	theme.set_stylebox("normal", "NavButton", _box(Color(0, 0, 0, 0), Color(0, 0, 0, 0), 0,
		Tokens.SP_3, Tokens.SP_3))
	theme.set_stylebox("hover", "NavButton", _box(Color(Tokens.PANEL_ALT, 0.9), Tokens.BORDER, 1,
		Tokens.SP_3, Tokens.SP_3))
	theme.set_stylebox("pressed", "NavButton", _box(Tokens.PANEL_ALT, Tokens.BORDER, 1,
		Tokens.SP_3, Tokens.SP_3))
	theme.set_stylebox("disabled", "NavButton", _box(Color(0, 0, 0, 0), Color(0, 0, 0, 0), 0,
		Tokens.SP_3, Tokens.SP_3))
	theme.set_stylebox("focus", "NavButton", _focus_box(Tokens.ACCENT))
	theme.set_color("font_color", "NavButton", Tokens.TEXT_MUTED)
	theme.set_color("font_hover_color", "NavButton", Tokens.TEXT)
	theme.set_color("font_pressed_color", "NavButton", Tokens.TEXT)
	theme.set_color("font_disabled_color", "NavButton", Tokens.TEXT_DIM)

	# StepButton：卡片里的数量步进器（1–99），方形、紧凑。
	# NavCard：大厅首页的入口大卡（图标 + 标题 + 说明）。做成按钮而不是面板 + 手势，
	# 是为了白拿焦点、键盘确认、悬停/按下三态 —— 自绘的「可点面板」全都要自己实现一遍。
	theme.set_type_variation("NavCard", "Button")
	theme.set_stylebox("normal", "NavCard", _box(Tokens.PANEL, Tokens.BORDER, 1,
		Tokens.SP_5, Tokens.SP_5, Tokens.RADIUS_LG))
	theme.set_stylebox("hover", "NavCard", _box(Tokens.PANEL_ALT, Tokens.ACCENT, 1,
		Tokens.SP_5, Tokens.SP_5, Tokens.RADIUS_LG))
	theme.set_stylebox("pressed", "NavCard", _box(Tokens.BORDER, Tokens.ACCENT, 1,
		Tokens.SP_5, Tokens.SP_5, Tokens.RADIUS_LG))
	theme.set_stylebox("disabled", "NavCard", _box(Tokens.SURFACE, Tokens.BORDER, 1,
		Tokens.SP_5, Tokens.SP_5, Tokens.RADIUS_LG))
	theme.set_stylebox("focus", "NavCard", _focus_box(Tokens.ACCENT))
	theme.set_color("font_color", "NavCard", Tokens.TEXT)
	theme.set_font_size("font_size", "NavCard", Tokens.FONT_SUB)

	theme.set_type_variation("StepButton", "Button")
	for state in ["normal", "hover", "pressed", "disabled", "focus"]:
		var bg := Tokens.PANEL_ALT
		var border := Tokens.BORDER_STRONG
		if state == "hover":
			border = Tokens.ACCENT
		elif state == "pressed":
			bg = Tokens.BORDER
			border = Tokens.ACCENT
		elif state == "disabled":
			bg = Tokens.SURFACE
			border = Tokens.BORDER
		var sb := _box(bg, border, 1, Tokens.SP_2, Tokens.SP_2)
		if state == "focus":
			sb = _focus_box(Tokens.ACCENT)
		theme.set_stylebox(state, "StepButton", sb)
	theme.set_color("font_color", "StepButton", Tokens.TEXT)
	theme.set_color("font_hover_color", "StepButton", Tokens.ACCENT)
	theme.set_color("font_disabled_color", "StepButton", Tokens.TEXT_DIM)
	theme.set_font("font", "StepButton", Tokens.mono_font())
	theme.set_font_size("font_size", "StepButton", Tokens.FONT_SUB)

# ---- 输入控件 ----

func _input(theme: Theme) -> void:
	theme.set_stylebox("normal", "LineEdit", _box(Tokens.BG, Tokens.BORDER_STRONG, 1,
		Tokens.SP_3, Tokens.SP_3))
	theme.set_stylebox("focus", "LineEdit", _box(Tokens.BG, Tokens.ACCENT, 2,
		Tokens.SP_3, Tokens.SP_3))
	theme.set_color("font_color", "LineEdit", Tokens.TEXT)
	theme.set_color("font_placeholder_color", "LineEdit", Tokens.TEXT_DIM)
	theme.set_color("caret_color", "LineEdit", Tokens.ACCENT)
	theme.set_color("selection_color", "LineEdit", Color(Tokens.ACCENT, 0.25))
	theme.set_font_size("font_size", "LineEdit", Tokens.FONT_BODY)

	# 设置面板的滑杆：不写这一段，HSlider 会退回 Godot 默认主题的浅色滑杆，暗底上一眼出戏。
	theme.set_stylebox("slider", "HSlider", _box(Tokens.BG, Tokens.BORDER_STRONG, 1, 0, 3))
	theme.set_stylebox("grabber_area", "HSlider", _box(Tokens.ACCENT, Tokens.ACCENT, 0, 0, 3))
	theme.set_stylebox("grabber_area_highlight", "HSlider",
		_box(Tokens.ACCENT.lightened(0.12), Tokens.ACCENT, 0, 0, 3))
	# 抓手做大一点：它是拖动目标，太小就变成「精确操作」。
	theme.set_stylebox("grabber", "HSlider", _box(Tokens.TEXT, Tokens.ACCENT, 1,
		Tokens.SP_3, Tokens.SP_3))
	theme.set_stylebox("grabber_highlight", "HSlider", _box(Tokens.ACCENT, Tokens.ACCENT, 0,
		Tokens.SP_3, Tokens.SP_3))
	theme.set_stylebox("focus", "HSlider", _focus_box(Tokens.ACCENT))

	for type in ["CheckBox", "CheckButton"]:
		theme.set_color("font_color", type, Tokens.TEXT)
		theme.set_color("font_hover_color", type, Tokens.ACCENT)
		theme.set_color("font_disabled_color", type, Tokens.TEXT_DIM)
		theme.set_font_size("font_size", type, Tokens.FONT_BODY)

	theme.set_stylebox("normal", "OptionButton", _box(Tokens.PANEL, Tokens.BORDER_STRONG, 1,
		Tokens.SP_3, Tokens.SP_2))
	theme.set_stylebox("hover", "OptionButton", _box(Tokens.PANEL_ALT, Tokens.ACCENT, 1,
		Tokens.SP_3, Tokens.SP_2))
	theme.set_stylebox("focus", "OptionButton", _focus_box(Tokens.ACCENT))
	theme.set_color("font_color", "OptionButton", Tokens.TEXT)
	theme.set_font_size("font_size", "OptionButton", Tokens.FONT_BODY)

# ---- 面板 ----

func _panel(theme: Theme) -> void:
	for type in ["Panel", "PanelContainer"]:
		theme.set_stylebox("panel", type, _box(Tokens.SURFACE, Tokens.BORDER, 1,
			Tokens.SP_4, Tokens.SP_3))

	# CardPanel：网格里的一张卡（物品、战绩行）。
	theme.set_type_variation("CardPanel", "PanelContainer")
	theme.set_stylebox("panel", "CardPanel", _box(Tokens.PANEL, Tokens.BORDER, 1,
		Tokens.SP_4, Tokens.SP_4))
	# EquippedCard：已装备的物品卡（琥珀描边）。做成变体而不是在代码里 duplicate + 改色，
	# 是为了让「已装备长什么样」也只存在于主题里。
	theme.set_type_variation("EquippedCard", "CardPanel")
	theme.set_stylebox("panel", "EquippedCard", _box(Tokens.PANEL, Tokens.ACCENT, 2,
		Tokens.SP_4, Tokens.SP_4))

	# BarPanel：顶栏 / 状态条 / 播报条这类「一条」。
	theme.set_type_variation("BarPanel", "PanelContainer")
	theme.set_stylebox("panel", "BarPanel", _box(Tokens.SURFACE, Tokens.BORDER, 1,
		Tokens.SP_4, Tokens.SP_2))

	# HudPanel：对局内的浮层，半透明（不能挡住战场观察）。
	theme.set_type_variation("HudPanel", "PanelContainer")
	theme.set_stylebox("panel", "HudPanel", _box(Color(Tokens.BG, 0.72), Color(Tokens.BORDER, 0.9), 1,
		Tokens.SP_3, Tokens.SP_2))

	# DialogPanel：顶层对话框（登录卡、结算卡、设置层），更亮的描边 + 外阴影。
	var dialog := _box(Tokens.SURFACE, Tokens.BORDER_STRONG, 1, Tokens.SP_5, Tokens.SP_5)
	dialog.shadow_color = Color(0, 0, 0, 0.45)
	dialog.shadow_size = 18
	theme.set_type_variation("DialogPanel", "PanelContainer")
	theme.set_stylebox("panel", "DialogPanel", dialog)

	# SlotPanel：物品图标块 / 头像块这类纯色方块。
	theme.set_type_variation("SlotPanel", "PanelContainer")
	theme.set_stylebox("panel", "SlotPanel", _box(Tokens.BG, Tokens.BORDER_STRONG, 1,
		Tokens.SP_2, Tokens.SP_2))

# ---- 进度条 ----

func _progress(theme: Theme) -> void:
	theme.set_stylebox("background", "ProgressBar", _box(Tokens.BG, Tokens.BORDER, 1, 0, 0))
	theme.set_stylebox("fill", "ProgressBar", _box(Tokens.ACCENT, Tokens.ACCENT, 0, 0, 0))
	theme.set_color("font_color", "ProgressBar", Tokens.TEXT)
	theme.set_font("font", "ProgressBar", Tokens.mono_font())
	theme.set_font_size("font_size", "ProgressBar", Tokens.FONT_LABEL)

	# 自己的血条：底色绿，运行时按剩余比例换成琥珀 / 红（tokens.health_color）。
	theme.set_type_variation("HealthBar", "ProgressBar")
	theme.set_stylebox("fill", "HealthBar", _box(Tokens.SUCCESS, Tokens.SUCCESS, 0, 0, 0))
	# 同一个血条的另外两档：界面按剩余比例切 variation，而不是在代码里现造 StyleBox。
	theme.set_type_variation("HealthBarWarn", "ProgressBar")
	theme.set_stylebox("fill", "HealthBarWarn", _box(Tokens.ACCENT, Tokens.ACCENT, 0, 0, 0))
	theme.set_type_variation("HealthBarCritical", "ProgressBar")
	theme.set_stylebox("fill", "HealthBarCritical", _box(Tokens.DANGER, Tokens.DANGER, 0, 0, 0))

	# 对手血条：固定红，避免「自己的绿」和「对手的绿」在余光里混淆。
	theme.set_type_variation("OppHealthBar", "ProgressBar")
	theme.set_stylebox("fill", "OppHealthBar", _box(Tokens.DANGER, Tokens.DANGER, 0, 0, 0))

	# 回合进度条（先到 10 杀）用琥珀，经验条用蓝 —— 三条条同时出现时靠颜色区分。
	theme.set_type_variation("RoundBar", "ProgressBar")
	theme.set_stylebox("fill", "RoundBar", _box(Tokens.ACCENT, Tokens.ACCENT, 0, 0, 0))
	theme.set_type_variation("XpBar", "ProgressBar")
	theme.set_stylebox("fill", "XpBar", _box(Tokens.INFO, Tokens.INFO, 0, 0, 0))

# ---- TabContainer / TabBar ----

func _tabs(theme: Theme) -> void:
	for type in ["TabContainer", "TabBar"]:
		theme.set_stylebox("tab_selected", type, _box(Tokens.PANEL, Tokens.ACCENT, 0,
			Tokens.SP_4, Tokens.SP_2))
		theme.set_stylebox("tab_unselected", type, _box(Tokens.SURFACE, Tokens.BORDER, 1,
			Tokens.SP_4, Tokens.SP_2))
		theme.set_stylebox("tab_hovered", type, _box(Tokens.PANEL_ALT, Tokens.BORDER_STRONG, 1,
			Tokens.SP_4, Tokens.SP_2))
		theme.set_stylebox("tab_disabled", type, _box(Tokens.SURFACE, Tokens.BORDER, 1,
			Tokens.SP_4, Tokens.SP_2))
		theme.set_stylebox("tab_focus", type, _focus_box(Tokens.ACCENT))
		theme.set_color("font_selected_color", type, Tokens.TEXT)
		theme.set_color("font_unselected_color", type, Tokens.TEXT_MUTED)
		theme.set_color("font_hovered_color", type, Tokens.TEXT)
		theme.set_color("font_disabled_color", type, Tokens.TEXT_DIM)
		theme.set_font_size("font_size", type, Tokens.FONT_BODY)
		theme.set_constant("tab_separation", type, Tokens.SP_1)
		theme.set_constant("side_margin", type, 0)
	theme.set_stylebox("panel", "TabContainer", _box(Tokens.SURFACE, Tokens.BORDER, 1,
		Tokens.SP_4, Tokens.SP_4))

# ---- FoldableContainer（Godot 4.5+）----

func _foldable(theme: Theme) -> void:
	var open := _box(Tokens.PANEL, Tokens.BORDER, 1, Tokens.SP_3, Tokens.SP_3)
	var closed := _box(Tokens.SURFACE, Tokens.BORDER, 1, Tokens.SP_3, Tokens.SP_3)
	var hover := _box(Tokens.PANEL_ALT, Tokens.ACCENT, 1, Tokens.SP_3, Tokens.SP_3)
	theme.set_stylebox("panel", "FoldableContainer", open)
	theme.set_stylebox("title_panel", "FoldableContainer", open)
	theme.set_stylebox("title_hover_panel", "FoldableContainer", hover)
	theme.set_stylebox("title_collapsed_panel", "FoldableContainer", closed)
	theme.set_stylebox("title_collapsed_hover_panel", "FoldableContainer", hover)
	theme.set_stylebox("title_focus", "FoldableContainer", _focus_box(Tokens.ACCENT))
	theme.set_color("title_color", "FoldableContainer", Tokens.TEXT)
	theme.set_color("title_collapsed_color", "FoldableContainer", Tokens.TEXT_MUTED)
	theme.set_color("title_hovered_color", "FoldableContainer", Tokens.ACCENT)
	theme.set_color("title_collapsed_hovered_color", "FoldableContainer", Tokens.ACCENT)
	theme.set_font_size("font_size", "FoldableContainer", Tokens.FONT_BODY)
	theme.set_constant("h_separation", "FoldableContainer", Tokens.SP_2)
	theme.set_constant("extra_h_separation", "FoldableContainer", Tokens.SP_3)
	theme.set_constant("vertical_separation", "FoldableContainer", Tokens.SP_2)

# ---- 弹窗 / 提示 ----

func _popup(theme: Theme) -> void:
	var popup := _box(Tokens.SURFACE, Tokens.BORDER_STRONG, 1, Tokens.SP_2, Tokens.SP_2)
	popup.shadow_color = Color(0, 0, 0, 0.4)
	popup.shadow_size = 10
	for type in ["PopupPanel", "PopupMenu"]:
		theme.set_stylebox("panel", type, popup)
	theme.set_stylebox("hover", "PopupMenu", _box(Tokens.PANEL_ALT, Tokens.BORDER, 0,
		Tokens.SP_2, Tokens.SP_1))
	theme.set_stylebox("separator", "PopupMenu", _box(Tokens.BORDER, Tokens.BORDER, 0, 0, 1))
	theme.set_color("font_color", "PopupMenu", Tokens.TEXT)
	theme.set_color("font_hover_color", "PopupMenu", Tokens.ACCENT)
	theme.set_color("font_disabled_color", "PopupMenu", Tokens.TEXT_DIM)
	theme.set_color("font_separator_color", "PopupMenu", Tokens.TEXT_MUTED)
	theme.set_font_size("font_size", "PopupMenu", Tokens.FONT_BODY)
	theme.set_stylebox("panel", "TooltipPanel", popup)
	theme.set_color("font_color", "TooltipLabel", Tokens.TEXT)
	theme.set_font_size("font_size", "TooltipLabel", Tokens.FONT_LABEL)

# ---- 滚动条 / 分隔线 ----

func _scrollbar(theme: Theme) -> void:
	for type in ["VScrollBar", "HScrollBar"]:
		theme.set_stylebox("scroll", type, _box(Tokens.BG, Color(0, 0, 0, 0), 0,
			Tokens.SP_1, Tokens.SP_1))
		theme.set_stylebox("grabber", type, _box(Tokens.BORDER_STRONG, Tokens.BORDER_STRONG, 0))
		theme.set_stylebox("grabber_highlight", type, _box(Tokens.ACCENT, Tokens.ACCENT, 0))
		theme.set_stylebox("grabber_pressed", type, _box(Tokens.ACCENT, Tokens.ACCENT, 0))

func _misc(theme: Theme) -> void:
	for type in ["HSeparator", "VSeparator"]:
		theme.set_stylebox("separator", type, _box(Tokens.BORDER, Tokens.BORDER, 0, 0, 1))
	theme.set_constant("separation", "HSeparator", Tokens.SP_2)
	theme.set_constant("separation", "VSeparator", Tokens.SP_2)

# ---- ItemList / Tree（调试面板用）----

func _list(theme: Theme) -> void:
	theme.set_stylebox("panel", "ItemList", _box(Tokens.PANEL, Tokens.BORDER, 1,
		Tokens.SP_2, Tokens.SP_2))
	theme.set_stylebox("selected", "ItemList", _box(Tokens.PANEL_ALT, Tokens.ACCENT, 1,
		Tokens.SP_2, Tokens.SP_1))
	theme.set_stylebox("selected_focus", "ItemList", _box(Tokens.PANEL_ALT, Tokens.ACCENT, 1,
		Tokens.SP_2, Tokens.SP_1))
	theme.set_stylebox("hovered", "ItemList", _box(Tokens.PANEL_ALT, Tokens.BORDER_STRONG, 1,
		Tokens.SP_2, Tokens.SP_1))
	theme.set_stylebox("focus", "ItemList", StyleBoxEmpty.new())
	theme.set_color("font_color", "ItemList", Tokens.TEXT)
	theme.set_color("font_selected_color", "ItemList", Tokens.ACCENT)
	theme.set_font_size("font_size", "ItemList", Tokens.FONT_BODY)
	theme.set_constant("v_separation", "ItemList", Tokens.SP_1)

	theme.set_stylebox("panel", "Tree", _box(Tokens.PANEL, Tokens.BORDER, 1,
		Tokens.SP_2, Tokens.SP_2))
	theme.set_stylebox("selected", "Tree", _box(Tokens.PANEL_ALT, Tokens.ACCENT, 1,
		Tokens.SP_2, Tokens.SP_2))
	theme.set_stylebox("selected_focus", "Tree", _box(Tokens.PANEL_ALT, Tokens.ACCENT, 1,
		Tokens.SP_2, Tokens.SP_2))
	theme.set_stylebox("focus", "Tree", StyleBoxEmpty.new())
	theme.set_stylebox("title_button_normal", "Tree", _box(Tokens.SURFACE, Tokens.BORDER, 1,
		Tokens.SP_2, Tokens.SP_1))
	theme.set_color("font_color", "Tree", Tokens.TEXT)
	theme.set_color("font_selected_color", "Tree", Tokens.ACCENT)
	theme.set_font_size("font_size", "Tree", Tokens.FONT_BODY)
	theme.set_constant("v_separation", "Tree", Tokens.SP_1)

# ---- 样式盒工厂 ----

## _focus_box 焦点指示：2px 描边 + 外发光，叠在当前状态样式之上（透明底，不盖掉按钮配色）。
## 焦点样式是键盘与手柄玩家的唯一导航线索 —— 设成 StyleBoxEmpty 等于「按键能走、看不见走到哪」。
func _focus_box(color: Color) -> StyleBoxFlat:
	var sb := StyleBoxFlat.new()
	sb.draw_center = false
	sb.border_color = color
	sb.set_border_width_all(2)
	sb.corner_radius_top_left = Tokens.RADIUS_MD
	sb.corner_radius_top_right = Tokens.RADIUS_MD
	sb.corner_radius_bottom_left = Tokens.RADIUS_MD
	sb.corner_radius_bottom_right = Tokens.RADIUS_MD
	sb.shadow_color = Color(color, 0.35)
	sb.shadow_size = 4
	return sb

## _box 是这套主题唯一的样式盒工厂：小圆角 + 细描边 + 可调内边距。
func _box(bg: Color, border: Color, width: int,
		margin_h: int = Tokens.SP_3, margin_v: int = Tokens.SP_2,
		radius: int = Tokens.RADIUS_MD) -> StyleBoxFlat:
	var sb := StyleBoxFlat.new()
	sb.bg_color = bg
	sb.border_color = border
	sb.border_width_left = width
	sb.border_width_top = width
	sb.border_width_right = width
	sb.border_width_bottom = width
	sb.corner_radius_top_left = radius
	sb.corner_radius_top_right = radius
	sb.corner_radius_bottom_left = radius
	sb.corner_radius_bottom_right = radius
	sb.content_margin_left = margin_h
	sb.content_margin_right = margin_h
	sb.content_margin_top = margin_v
	sb.content_margin_bottom = margin_v
	return sb

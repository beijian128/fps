extends SceneTree
## 一次性生成 ui/ 下全部 .tscn 骨架：
##   Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client \
##     --script res://tools/gen_ui_scenes.gd
##
## 为什么用脚本生成而不是手写 .tscn：场景文件是 ID 化的（每个节点一个 `NodeType_xxxxx` id，
## 引用靠 id 而不是名字），手写几个还能忍、九个界面手写必然出错，而且 diff 完全不可评审。
## 生成后它们就是标准场景文件，可以在 Godot 编辑器里继续调；结构性改动改这里再重跑。
##
## 布局一律走容器（VBox/HBox/Grid/Margin/Center）与锚点，不用固定像素定位 —— 窗口拉伸时
## 才不破版（HUD 的四个贴边面板是唯一例外，它们本来就该贴着屏幕四角）。

const Tokens := preload("res://theme/tokens.gd")
const THEME := preload("res://theme/tactical_theme.tres")

const UI_DIR := "res://ui/"

func _initialize() -> void:
	_save(_build_shell(), "shell")
	_save(_build_login(), "login_screen")
	_save(_build_profile(), "profile_screen")
	_save(_build_shop(), "shop_screen")
	_save(_build_bag(), "bag_screen")
	_save(_build_item_card(), "item_card")
	_save(_build_match_bar(), "match_status_bar")
	_save(_build_result_overlay(), "result_overlay")
	_save(_build_hud(), "hud")
	print("all ui scenes written")
	quit(0)

# ---------------- 各界面 ----------------

func _build_shell() -> Node:
	var root := _screen("Shell", "shell.gd")
	var column := _vbox(root, 0)
	_full(column)

	var top := _panel("TopBar", "BarPanel")
	column.add_child(top)
	var top_row := _hbox(top, Tokens.SP_3)
	top_row.add_child(_label("Brand", "JOLT FPS", "LabelAccent"))
	top_row.add_child(_spacer())
	top_row.add_child(_label("TopText", "未登录", "LabelMuted"))

	var body := _hbox(column, Tokens.SP_2)
	body.size_flags_vertical = Control.SIZE_EXPAND_FILL

	var nav := _panel("NavPanel", "BarPanel")
	nav.custom_minimum_size = Vector2(180, 0)
	body.add_child(nav)
	var nav_box := _vbox(nav, Tokens.SP_1)
	nav_box.add_child(_nav_button("NavProfile", "个人信息", true))
	nav_box.add_child(_nav_button("NavShop", "商城", false))
	nav_box.add_child(_nav_button("NavBag", "背包", false))
	nav_box.add_child(_spacer(true))

	var content_wrap := _margin(body, Tokens.SP_4)
	content_wrap.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	content_wrap.add_child(_unique(_ctl("Content"), "Content", true))

	var status_slot := _unique(_ctl("StatusSlot"), "StatusSlot", false)
	status_slot.custom_minimum_size = Vector2(0, 44)
	column.add_child(status_slot)
	return root

func _build_login() -> Node:
	var root := _screen("LoginScreen", "login_screen.gd")
	var backdrop := ColorRect.new()
	backdrop.name = "Backdrop"
	backdrop.color = Tokens.SCRIM_STRONG
	backdrop.mouse_filter = Control.MOUSE_FILTER_IGNORE
	root.add_child(backdrop)
	_full(backdrop)

	var center := CenterContainer.new()
	center.name = "Center"
	root.add_child(center)
	_full(center)

	var card := _panel("Card", "CardPanel")
	card.custom_minimum_size = Vector2(420, 0)
	center.add_child(card)
	var box := _vbox(card, Tokens.SP_2)

	box.add_child(_label("Title", "JOLT FPS", "LabelTitle"))
	box.add_child(_label("Subtitle", "登录后进入大厅：个人信息 · 商城 · 背包 · 匹配", "LabelMuted"))
	var user := _unique(LineEdit.new(), "User", true)
	user.placeholder_text = "用户名（3-16 位字母/数字/下划线）"
	box.add_child(user)
	var pass_input := _unique(LineEdit.new(), "Pass", true)
	pass_input.placeholder_text = "密码（6-64 位）"
	pass_input.secret = true
	box.add_child(pass_input)
	var error := _unique(_label("Error", "", "LabelDanger"), "Error", true)
	error.autowrap_mode = TextServer.AUTOWRAP_WORD_SMART
	box.add_child(error)
	var actions := _hbox(box, Tokens.SP_2)
	actions.add_child(_button("Login", "登录", "PrimaryButton", true))
	actions.add_child(_button("Register", "注册", "GhostButton", true))
	return root

func _build_profile() -> Node:
	var root := _screen("ProfileScreen", "profile_screen.gd")
	var body := _hbox(root, Tokens.SP_4)
	_full(body)

	var identity := _panel("Identity", "BarPanel")
	identity.custom_minimum_size = Vector2(220, 0)
	body.add_child(identity)
	var id_box := _vbox(identity, Tokens.SP_2)
	var avatar := _unique(ColorRect.new(), "Avatar", true)
	avatar.color = Tokens.PANEL
	avatar.custom_minimum_size = Vector2(64, 64)
	avatar.size_flags_horizontal = Control.SIZE_SHRINK_CENTER
	id_box.add_child(avatar)
	id_box.add_child(_center(_label("Username", "—", ""), true))
	id_box.add_child(_center(_label("Level", "LV.1", "LabelAccent"), true))
	var xp := _unique(ProgressBar.new(), "XP", true)
	xp.show_percentage = false
	xp.custom_minimum_size = Vector2(0, 6)
	id_box.add_child(xp)
	id_box.add_child(_label("XPText", "0 / 200 XP", "LabelMuted"))
	id_box.add_child(_spacer(true))
	id_box.add_child(_label("AccountId", "账号 #—", "LabelMuted"))

	var right := _vbox(body, Tokens.SP_3)
	right.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	var stats := _unique(GridContainer.new(), "Stats", true)
	stats.columns = 3
	stats.add_theme_constant_override("h_separation", Tokens.SP_2)
	stats.add_theme_constant_override("v_separation", Tokens.SP_2)
	for key in ["kills", "deaths", "kd", "matches", "wins", "losses"]:
		stats.add_child(_stat_card(key))
	right.add_child(stats)
	right.add_child(_label("RecordsTitle", "最近 20 场", "LabelSub"))
	var scroll := ScrollContainer.new()
	scroll.name = "RecordsScroll"
	scroll.size_flags_vertical = Control.SIZE_EXPAND_FILL
	right.add_child(scroll)
	var records := _unique(_vbox(scroll, Tokens.SP_1), "Records", true)
	records.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	var empty := _unique(_label("EmptyHint", "还没有对局记录，去打一局吧", "LabelMuted"), "EmptyHint", true)
	empty.visible = false
	right.add_child(empty)
	return root

func _build_shop() -> Node:
	var root := _screen("ShopScreen", "shop_screen.gd")
	var box := _vbox(root, Tokens.SP_3)
	_full(box)
	var header := _hbox(box, Tokens.SP_3)
	header.add_child(_label("Title", "商城", "LabelTitle"))
	header.add_child(_spacer())
	header.add_child(_unique(_label("Coins", "⛁ 0", "LabelAccent"), "Coins", true))
	var scroll := ScrollContainer.new()
	scroll.name = "Scroll"
	scroll.size_flags_vertical = Control.SIZE_EXPAND_FILL
	box.add_child(scroll)
	var grid := _unique(GridContainer.new(), "Grid", true)
	grid.columns = 4
	grid.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	grid.add_theme_constant_override("h_separation", Tokens.SP_3)
	grid.add_theme_constant_override("v_separation", Tokens.SP_3)
	scroll.add_child(grid)
	return root

func _build_bag() -> Node:
	var root := _screen("BagScreen", "bag_screen.gd")
	var box := _vbox(root, Tokens.SP_3)
	_full(box)
	box.add_child(_label("Title", "背包", "LabelTitle"))
	var scroll := ScrollContainer.new()
	scroll.name = "Scroll"
	scroll.size_flags_vertical = Control.SIZE_EXPAND_FILL
	box.add_child(scroll)
	var grid := _unique(GridContainer.new(), "Grid", true)
	grid.columns = 4
	grid.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	grid.add_theme_constant_override("h_separation", Tokens.SP_3)
	grid.add_theme_constant_override("v_separation", Tokens.SP_3)
	scroll.add_child(grid)

	var empty := _unique(_vbox(root, Tokens.SP_3), "EmptyState", true)
	empty.visible = false
	empty.add_child(_label("EmptyText", "背包还是空的，去商城看看", "LabelMuted"))
	var to_shop := _button("ToShop", "去商城", "GhostButton", true)
	to_shop.size_flags_horizontal = Control.SIZE_SHRINK_BEGIN
	empty.add_child(to_shop)
	return root

func _build_item_card() -> Node:
	var root := _screen("ItemCard", "item_card.gd", "PanelContainer")
	var card := root as PanelContainer
	card.custom_minimum_size = Vector2(170, 190)
	var box := _vbox(card, Tokens.SP_1)
	var icon := _unique(ColorRect.new(), "Icon", true)
	icon.color = Tokens.BORDER
	icon.custom_minimum_size = Vector2(0, 52)
	box.add_child(icon)
	box.add_child(_unique(_label("Name", "物品", ""), "Name", true))
	box.add_child(_unique(_label("Info", "—", "LabelMuted"), "Info", true))
	var qty_row := _hbox(box, Tokens.SP_1)
	qty_row.add_child(_button("QtyMinus", "−", "GhostButton", true))
	var qty := _unique(_label("Qty", "1", ""), "Qty", true)
	qty.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
	qty.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	qty_row.add_child(qty)
	qty_row.add_child(_button("QtyPlus", "+", "GhostButton", true))
	box.add_child(_spacer(true))
	box.add_child(_button("Action", "购买", "PrimaryButton", true))
	return root

func _build_match_bar() -> Node:
	var root := _screen("MatchStatusBar", "match_status_bar.gd", "PanelContainer")
	(root as PanelContainer).theme_type_variation = "BarPanel"
	var row := _hbox(root, Tokens.SP_3)
	row.add_child(_unique(_label("LastResult", "", "LabelMuted"), "LastResult", true))
	row.add_child(_spacer())
	row.add_child(_unique(_label("QueueText", "", "LabelAccent"), "QueueText", true))
	row.add_child(_button("StartMatch", "开始匹配", "PrimaryButton", true))
	var cancel := _button("CancelMatch", "取消", "GhostButton", true)
	cancel.visible = false
	row.add_child(cancel)
	return root

func _build_result_overlay() -> Node:
	var root := _screen("ResultOverlay", "result_overlay.gd")
	root.visible = false
	var scrim := ColorRect.new()
	scrim.name = "Scrim"
	scrim.color = Tokens.SCRIM_STRONG
	root.add_child(scrim)
	_full(scrim)
	var center := CenterContainer.new()
	center.name = "Center"
	root.add_child(center)
	_full(center)
	var box := _vbox(center, Tokens.SP_3)
	box.add_child(_unique(_label("Headline", "", "LabelHero"), "Headline", true))
	box.add_child(_unique(_label("ScoreLine", "", "LabelSub"), "ScoreLine", true))
	box.add_child(_unique(_label("Duration", "", "LabelMuted"), "Duration", true))
	var actions := _hbox(box, Tokens.SP_2)
	actions.alignment = BoxContainer.ALIGNMENT_CENTER
	actions.add_child(_button("BackToLobby", "回大厅", "PrimaryButton", true))
	actions.add_child(_button("PlayAgain", "再来一局", "GhostButton", true))
	return root

func _build_hud() -> Node:
	var root := _screen("Hud", "hud.gd")
	root.mouse_filter = Control.MOUSE_FILTER_IGNORE

	# 左下：自己
	var self_panel := _panel("SelfPanel", "BarPanel")
	root.add_child(self_panel)
	_anchor(self_panel, Control.PRESET_BOTTOM_LEFT, 16, -104, 260, -16)
	var self_box := _vbox(self_panel, Tokens.SP_2)
	var self_row := _hbox(self_box, Tokens.SP_2)
	self_row.add_child(_label("SelfTitle", "你", "LabelMuted"))
	self_row.add_child(_spacer())
	self_row.add_child(_unique(_label("SelfKd", "0 / 0", ""), "SelfKd", true))
	var health := _unique(ProgressBar.new(), "HealthBar", true)
	health.show_percentage = false
	health.custom_minimum_size = Vector2(0, 10)
	self_box.add_child(health)
	self_box.add_child(_unique(_label("HealthText", "100", "LabelMuted"), "HealthText", true))

	# 右上：对手
	var opp_panel := _panel("OppPanel", "BarPanel")
	root.add_child(opp_panel)
	_anchor(opp_panel, Control.PRESET_TOP_RIGHT, -260, 16, -16, 104)
	var opp_box := _vbox(opp_panel, Tokens.SP_2)
	var opp_row := _hbox(opp_box, Tokens.SP_2)
	opp_row.add_child(_label("OppTitle", "对手", "LabelMuted"))
	opp_row.add_child(_spacer())
	opp_row.add_child(_unique(_label("OppKd", "0 / 0", ""), "OppKd", true))
	var opp_health := _unique(ProgressBar.new(), "OppHealthBar", true)
	opp_health.show_percentage = false
	opp_health.custom_minimum_size = Vector2(0, 10)
	opp_box.add_child(opp_health)
	opp_box.add_child(_unique(_label("OppHealthText", "100", "LabelMuted"), "OppHealthText", true))

	# 顶部中央：回合进度
	var round_panel := _panel("RoundPanel", "BarPanel")
	root.add_child(round_panel)
	_anchor(round_panel, Control.PRESET_CENTER_TOP, -140, 16, 140, 84)
	var round_box := _vbox(round_panel, Tokens.SP_2)
	round_box.add_child(_center(_unique(_label("RoundText", "先到 10 杀", "LabelMuted"), "RoundText", true), true))
	var round_bar := _unique(ProgressBar.new(), "RoundBar", true)
	round_bar.max_value = 10
	round_bar.show_percentage = false
	round_bar.custom_minimum_size = Vector2(0, 8)
	round_box.add_child(round_bar)

	# 左上：击杀播报
	var feed := _unique(_vbox(root, Tokens.SP_1), "KillFeed", true)
	_anchor(feed, Control.PRESET_TOP_LEFT, 16, 16, 300, 120)

	# 右下角：FPS
	var fps := _unique(_label("FpsLabel", "", "LabelMuted"), "FpsLabel", true)
	root.add_child(fps)
	_anchor(fps, Control.PRESET_BOTTOM_RIGHT, -140, -40, -16, -16)
	return root

func _stat_card(key: String) -> Control:
	var card := _panel("Stat_" + key, "CardPanel")
	var box := _vbox(card, 0)
	box.add_child(_label("Stat_" + key + "_Name", STAT_NAMES.get(key, key), "LabelMuted"))
	var value := _label("Stat_" + key + "_Value", "0", "LabelSub")
	value.add_theme_font_override("font", Tokens.mono_font())
	box.add_child(value)
	return card

const STAT_NAMES := {
	"kills": "击杀", "deaths": "死亡", "kd": "K/D",
	"matches": "场次", "wins": "胜", "losses": "负",
}

# ---------------- 构建小工具 ----------------

func _screen(node_name: String, script_name: String, base_type: String = "Control") -> Node:
	var root: Node = _ctl(base_type)
	root.name = node_name
	root.theme = THEME
	var script_path := "res://ui/" + script_name
	if ResourceLoader.exists(script_path):
		root.set_script(load(script_path))
	if root is Control:
		(root as Control).size_flags_horizontal = Control.SIZE_EXPAND_FILL
		(root as Control).size_flags_vertical = Control.SIZE_EXPAND_FILL
	return root

func _ctl(type_name: String) -> Node:
	match type_name:
		"Control": return Control.new()
		"PanelContainer": return PanelContainer.new()
		_: return Control.new()

func _unique(node: Node, node_name: String, _flag: bool) -> Node:
	node.name = node_name
	node.unique_name_in_owner = true
	return node

func _full(node: Control) -> void:
	node.set_anchors_preset(Control.PRESET_FULL_RECT)
	node.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	node.size_flags_vertical = Control.SIZE_EXPAND_FILL

func _anchor(node: Control, preset: int, left: float, top: float, right: float, bottom: float) -> void:
	node.set_anchors_preset(preset)
	node.offset_left = left
	node.offset_top = top
	node.offset_right = right
	node.offset_bottom = bottom

func _vbox(parent: Node, sep: int) -> VBoxContainer:
	var box := VBoxContainer.new()
	box.add_theme_constant_override("separation", sep)
	parent.add_child(box)
	return box

func _hbox(parent: Node, sep: int) -> HBoxContainer:
	var box := HBoxContainer.new()
	box.add_theme_constant_override("separation", sep)
	parent.add_child(box)
	return box

func _margin(parent: Node, m: int) -> MarginContainer:
	var wrap := MarginContainer.new()
	for side in ["left", "top", "right", "bottom"]:
		wrap.add_theme_constant_override("margin_" + side, m)
	parent.add_child(wrap)
	return wrap

func _panel(node_name: String, variation: String) -> PanelContainer:
	var panel := PanelContainer.new()
	panel.name = node_name
	panel.theme_type_variation = variation
	return panel

func _label(node_name: String, text: String, variation: String) -> Label:
	var label := Label.new()
	label.name = node_name
	label.text = text
	if variation != "":
		label.theme_type_variation = variation
	return label

func _button(node_name: String, text: String, variation: String, unique: bool) -> Button:
	var button := Button.new()
	button.name = node_name
	button.text = text
	if variation != "":
		button.theme_type_variation = variation
	button.unique_name_in_owner = unique
	return button

func _nav_button(node_name: String, text: String, active: bool) -> Button:
	var button := _button(node_name, text, "GhostButton", true)
	button.alignment = HORIZONTAL_ALIGNMENT_LEFT
	if active:
		button.add_theme_color_override("font_color", Tokens.ACCENT)
	return button

func _center(node: Control, expand_h: bool) -> CenterContainer:
	var center := CenterContainer.new()
	if expand_h:
		center.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	center.add_child(node)
	return center

func _spacer(expand_vertical: bool = false) -> Control:
	var spacer := Control.new()
	spacer.name = "Spacer"
	spacer.mouse_filter = Control.MOUSE_FILTER_IGNORE
	if expand_vertical:
		spacer.size_flags_vertical = Control.SIZE_EXPAND_FILL
	else:
		spacer.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	return spacer

func _save(root: Node, file_name: String) -> void:
	var packed := PackedScene.new()
	if packed.pack(root) != OK:
		printerr("pack failed: ", file_name)
		quit(1)
		return
	var path := UI_DIR + file_name + ".tscn"
	if ResourceSaver.save(packed, path) != OK:
		printerr("save failed: ", path)
		quit(1)
		return
	print("scene written: ", path)
	root.free()

extends Control
## 大厅外壳：左侧竖排导航 + 顶栏（用户名/等级/金币）+ 内容插槽 + 底部匹配状态条。
##
## 它自己不发光：数据从 `screen_manager` 读（`profile()` / `logic_state()`），导航点击只是把
## 意图交给 `screen_manager`。页面内容按 `current_page()` 挂进 `%Content`。

const ProfileScene := preload("res://ui/profile_screen.tscn")
const ShopScene := preload("res://ui/shop_screen.tscn")
const BagScene := preload("res://ui/bag_screen.tscn")
const Tokens := preload("res://theme/tokens.gd")

const NAV := {
	"profile": "NavProfile",
	"shop": "NavShop",
	"bag": "NavBag",
}

var _manager = null
var _page_node: Control = null

func _ready() -> void:
	%NavProfile.pressed.connect(func() -> void: _manager.intent_show_page("profile"))
	%NavShop.pressed.connect(func() -> void: _manager.intent_show_page("shop"))
	%NavBag.pressed.connect(func() -> void: _manager.intent_show_page("bag"))

func bind(manager) -> void:
	_manager = manager
	manager.page_changed.connect(_on_page_changed)
	manager.data_changed.connect(_refresh_top)
	manager.state_changed.connect(_on_state_changed)
	_refresh_top()
	_refresh_nav()
	_mount_page()

# ---- 查询（测试用）----

## top_text 顶栏那一行：`用户名 · LV.n · ⛁ 金币`。三项来自三个不同的数据源：
## 用户名（LoginReply）、等级（PlayerProfileReply）、金币（LogicStateReply）。
func top_text() -> String:
	return %TopText.text

## nav_active 当前高亮的导航页（"profile" / "shop" / "bag"）。
func nav_active() -> String:
	for page: String in NAV.keys():
		var button: Button = get_node("%" + NAV[page])
		if button.has_theme_color_override("font_color"):
			return page
	return ""

## page_node_name 当前挂在内容插槽里的页面节点名（没挂时为空串）。
func page_node_name() -> String:
	return "" if _page_node == null else String(_page_node.name)

# ---- 内部 ----

func _on_page_changed(_page: String) -> void:
	_refresh_nav()
	_mount_page()

func _on_state_changed(_state: int) -> void:
	_refresh_top()

func _refresh_top() -> void:
	if _manager == null:
		return
	var profile: Dictionary = _manager.profile()
	var logic: Dictionary = _manager.logic_state()
	var name: String = _manager.username()
	if name.is_empty():
		%TopText.text = "未登录"
		return
	var level := int(profile.get("level", 1))
	var coins := int(logic.get("coins", 0))
	%TopText.text = "%s · LV.%d · ⛁ %d" % [name, level, coins]

func _refresh_nav() -> void:
	if _manager == null:
		return
	var current: String = _manager.current_page()
	for page: String in NAV.keys():
		var button: Button = get_node("%" + NAV[page])
		if page == current:
			button.add_theme_color_override("font_color", Tokens.ACCENT)
		else:
			button.remove_theme_color_override("font_color")

func _mount_page() -> void:
	if _manager == null:
		return
	var page: String = _manager.current_page()
	var wanted := {"profile": ProfileScene, "shop": ShopScene, "bag": BagScene}
	var scene: PackedScene = wanted.get(page, ProfileScene)
	if _page_node != null:
		_page_node.queue_free()
		_page_node = null
	var host: Control = get_node("%Content")
	_page_node = scene.instantiate()
	_page_node.name = String(page).capitalize() + "Screen"
	host.add_child(_page_node)
	_page_node.set_anchors_preset(Control.PRESET_FULL_RECT)
	if _page_node.has_method("bind"):
		_page_node.bind(_manager)
	_feed_page()

## _feed_page 把快照喂给页面（页面自己决定怎么渲染）。
## 用 has_method 兜底：Task 3 时三个页面还是空骨架，Task 4/5 才实现 render。
func _feed_page() -> void:
	if _page_node == null or not _page_node.has_method("render"):
		return
	var page: String = _manager.current_page()
	if page == "profile":
		_page_node.render(_manager.profile())
	else:
		_page_node.render(_manager.logic_state())

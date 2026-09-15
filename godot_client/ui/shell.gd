extends Control
## 大厅首页：顶栏（品牌 + 用户名/等级/金币）+ 三个入口大卡 + 底部匹配状态条。
##
## 大厅只做**入口**：点卡片把意图交给 `screen_manager`，由它把对应子界面整屏铺开
## （子界面不是嵌在这里的一块内容，见 screen_manager.State 与 _apply_visibility）。
## 它自己不发光：数据从 `screen_manager` 读（`profile()` / `logic_state()` / `username()`）。
##
## 键盘 / 手柄：三个入口构成一条焦点链（左右互跳），打开大厅时焦点落在第一张卡上。

const Tokens := preload("res://theme/tokens.gd")

## 入口卡：page id → 卡片节点名。顺序就是焦点链顺序。
const ENTRIES := {
	"profile": "EntryProfile",
	"shop": "EntryShop",
	"bag": "EntryBag",
}

var _manager = null

func _ready() -> void:
	for page: String in ENTRIES.keys():
		var node := get_node("%" + ENTRIES[page]) as Button
		node.pressed.connect(func() -> void: _manager.intent_show_page(page))
	_wire_focus()

## _wire_focus 三张卡横排：左右互跳并首尾相接（手柄上不会走进死胡同）。
func _wire_focus() -> void:
	var cards: Array[Button] = []
	for page: String in ENTRIES.keys():
		cards.append(get_node("%" + ENTRIES[page]))
	for i in cards.size():
		var left: Button = cards[(i - 1 + cards.size()) % cards.size()]
		var right: Button = cards[(i + 1) % cards.size()]
		cards[i].focus_neighbor_left = left.get_path()
		cards[i].focus_neighbor_right = right.get_path()

func bind(manager) -> void:
	_manager = manager
	manager.data_changed.connect(_refresh_top)
	manager.state_changed.connect(_on_state_changed)
	_refresh_top()

# ---- 查询（测试用）----

## top_text 顶栏那一行：`用户名 · LV.n · ⛁ 金币`。三项来自三个不同的数据源：
## 用户名（LoginReply）、等级（PlayerProfileReply）、金币（LogicStateReply）。
func top_text() -> String:
	return %TopText.text

## entry_pages 大厅上可见的入口（顺序稳定，测试与焦点链都按它走）。
func entry_pages() -> Array:
	return ENTRIES.keys()

## entry_visible page 对应的入口卡是否可见（子界面打开时大厅整层会隐藏，这里用于断言）。
func entry_visible(page: String) -> bool:
	var node := get_node_or_null("%" + String(ENTRIES.get(page, ""))) as Button
	return node != null and node.visible

## focus_active_entry 当前持有焦点的入口卡（没焦点时为空串）。
func focus_active_entry() -> String:
	for page: String in ENTRIES.keys():
		if (get_node("%" + ENTRIES[page]) as Button).has_focus():
			return page
	return ""

## focus_first_entry 把焦点放回第一张入口卡。
## 大厅被遮挡时（比如进大厅的询问框压在上面）焦点在大厅之外，框一收起就得还回来 ——
## 留在已经隐藏的按钮上，键盘玩家按确认键会打到一个看不见的控件上。
func focus_first_entry() -> void:
	if is_visible_in_tree():
		(get_node("%" + ENTRIES["profile"]) as Button).grab_focus()

# ---- 内部 ----

func _on_state_changed(_state: int) -> void:
	_refresh_top()
	# 回到大厅时把焦点放回第一张卡：键盘与手柄玩家不用重新找位置。
	if is_visible_in_tree():
		await get_tree().process_frame
		focus_first_entry()

func _refresh_top() -> void:
	if _manager == null:
		return
	var profile: Dictionary = _manager.profile()
	var logic: Dictionary = _manager.logic_state()
	var name: String = String(_manager.username())
	if name.is_empty():
		%TopText.text = "未登录"
		return
	var level := int(profile.get("level", 1))
	var coins := int(logic.get("coins", 0))
	%TopText.text = "%s · LV.%d · ⛁ %d" % [name, level, coins]

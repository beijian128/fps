extends Control
## 背包页：与商城同一套卡片网格，但显示拥有数量、主按钮是装备/卸下；空背包显示空态。

const CardScene := preload("res://ui/item_card.tscn")

var _manager = null
var _cards: Array = []

func _ready() -> void:
	%ToShop.pressed.connect(func() -> void: _manager.intent_show_page("shop"))
	%Back.pressed.connect(func() -> void: _manager.intent_close_page())
	# 打开这一屏时焦点落在「返回大厅」；从空态的「去商城」按钮进来的玩家也不会迷路。
	visibility_changed.connect(_on_visibility_changed)

func _on_visibility_changed() -> void:
	if visible and is_visible_in_tree():
		%Back.grab_focus()

func bind(manager) -> void:
	_manager = manager

## render 只显示**拥有数量 > 0** 的物品（背包不该出现买不起/没买过的东西）。
func render(state: Dictionary) -> void:
	_clear()
	for item: Variant in state.get("items", []):
		var data: Dictionary = item
		if int(data.get("owned_quantity", 0)) <= 0:
			continue
		var card: Control = CardScene.instantiate()
		%Grid.add_child(card)
		card.bind(data, "bag", _manager)
		_cards.append(card)
	%EmptyState.visible = _cards.is_empty()
	%Scroll.visible = not _cards.is_empty()

func cards() -> Array:
	return _cards

func empty_hint_visible() -> bool:
	return %EmptyState.visible

func _clear() -> void:
	for card in _cards:
		card.queue_free()
	_cards = []
	for child in %Grid.get_children():
		%Grid.remove_child(child)

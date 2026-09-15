extends Control
## 商城页：卡片网格（每张卡 = 一个商品），顶部金币，卡片内数量步进器 1–99 + 购买按钮。

const CardScene := preload("res://ui/item_card.tscn")

var _manager = null
var _cards: Array = []

func bind(manager) -> void:
	_manager = manager

## render 用一份 LogicStateReply 刷整页：金币 + 每件商品一张卡。
## 购买/装备成功后服务端返回的就是最新状态，界面直接再渲染一次即可（不做本地乐观更新）。
func render(state: Dictionary) -> void:
	%Coins.text = "⛁ %d" % int(state.get("coins", 0))
	_clear()
	for item: Variant in state.get("items", []):
		var card: Control = CardScene.instantiate()
		%Grid.add_child(card)
		card.bind(item as Dictionary, "shop", _manager)
		_cards.append(card)

## cards 当前渲染出的卡片（测试用）。
func cards() -> Array:
	return _cards

func _clear() -> void:
	for card in _cards:
		card.queue_free()
	_cards = []
	for child in %Grid.get_children():
		%Grid.remove_child(child)

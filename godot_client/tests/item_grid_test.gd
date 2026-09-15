extends SceneTree
## 商城 / 背包卡片网格：卡片数量、步进器边界（1–99）、买不起禁用、装备/卸下文案与空态。
##
## 运行：Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client \
##         --script res://tests/item_grid_test.gd

const Shop := preload("res://ui/shop_screen.tscn")
const Bag := preload("res://ui/bag_screen.tscn")

const STATE := {
	"ok": true, "coins": 100, "equipped_primary_weapon": "pistol",
	"items": [
		{"item_id": "rifle", "display_name": "步枪", "price": 300,
			"equip_slot": "primary_weapon", "owned_quantity": 1},
		{"item_id": "pistol", "display_name": "手枪", "price": 150,
			"equip_slot": "primary_weapon", "owned_quantity": 2},
		{"item_id": "medkit", "display_name": "医疗包", "price": 50,
			"equip_slot": "", "owned_quantity": 5},
	],
}

var _failures := 0
var _done := {}

## FakeManager 只提供卡片需要的那几件东西（快照 + 两个意图）。
class FakeManager:
	var snapshot: Dictionary
	var purchases: Array = []
	var equips: Array = []
	func _init(state: Dictionary) -> void:
		snapshot = state
	func logic_state() -> Dictionary: return snapshot
	func intent_purchase(item_id: String, qty: int) -> void: purchases.append([item_id, qty])
	func intent_equip(item_id: String) -> void: equips.append(item_id)
	func intent_show_page(_page: String) -> void: pass

func _initialize() -> void:
	var manager := FakeManager.new(STATE)

	# ---- 商城 ----
	var shop: Control = Shop.instantiate()
	root.add_child(shop)
	shop.bind(manager)
	shop.render(STATE)
	var shop_cards: Array = shop.cards()
	_check(shop_cards.size() == 3, "商城应有 3 张卡片，得到 %d" % shop_cards.size())
	_check(shop_cards[0].action_disabled(), "金币 100 买不起 300 的步枪，按钮应禁用")
	_check(shop_cards[2].action_disabled() == false, "50 金币的医疗包应买得起")
	_check(shop_cards[1].action_text() == "购买", "商城按钮文案应是「购买」")
	_check(shop_cards[1].quantity() == 1, "步进器默认应是 1")
	shop_cards[1].set_quantity(0)
	_check(shop_cards[1].quantity() == 1, "步进器下界应是 1，得到 %d" % shop_cards[1].quantity())
	shop_cards[1].set_quantity(999)
	_check(shop_cards[1].quantity() == 99, "步进器上界应是 99，得到 %d" % shop_cards[1].quantity())
	# 数量一加就买不起：医疗包 50 × 3 = 150 > 100
	shop_cards[2].set_quantity(3)
	_check(shop_cards[2].action_disabled(), "50×3 超过 100 金币时应禁用")
	shop_cards[2].set_quantity(2)
	_check(shop_cards[2].action_disabled() == false, "50×2 仍在 100 金币内，应可购买")
	# 点购买 → 意图带上 item_id 与当前数量
	shop_cards[2]._on_action()
	_check(manager.purchases.size() == 1 and manager.purchases[0] == ["medkit", 2],
		"购买意图应带 medkit × 2，得到 %s" % str(manager.purchases))

	# ---- 背包 ----
	var bag: Control = Bag.instantiate()
	root.add_child(bag)
	bag.bind(manager)
	bag.render(STATE)
	var bag_cards: Array = bag.cards()
	_check(bag_cards.size() == 3, "背包应显示 3 件拥有数量 > 0 的物品，得到 %d" % bag_cards.size())
	_check(bag_cards[0].action_text() == "装备", "未装备的步枪按钮应是「装备」，得到 %s" % bag_cards[0].action_text())
	_check(bag_cards[1].action_text() == "卸下", "已装备的手枪按钮应是「卸下」，得到 %s" % bag_cards[1].action_text())
	_check(bag_cards[2].action_disabled(), "医疗包不可装备，按钮应禁用")
	bag_cards[0]._on_action()
	_check(manager.equips.size() == 1 and manager.equips[0] == "rifle", "装备意图应是 rifle")
	bag_cards[1]._on_action()
	_check(manager.equips.size() == 2 and manager.equips[1] == "", "点「卸下」应发空 item_id")

	# 只显示拥有的：把一件的拥有数改成 0，它就该从背包里消失
	var trimmed: Dictionary = STATE.duplicate(true)
	trimmed["items"][0]["owned_quantity"] = 0
	bag.render(trimmed)
	_check(bag.cards().size() == 2, "拥有数为 0 的物品不该出现在背包，得到 %d" % bag.cards().size())

	# 空态
	bag.render({"ok": true, "coins": 0, "equipped_primary_weapon": "", "items": []})
	_check(bag.cards().is_empty(), "没有物品时不该有卡片")
	_check(bag.empty_hint_visible(), "没有物品时应显示空态")

	_done["grid"] = true
	_finish()

func _check(cond: bool, msg: String) -> void:
	if not cond:
		_failures += 1
		printerr("FAIL: " + msg)

func _finish() -> void:
	if not _done.has("grid"):
		_failures += 1
		printerr("FAIL: 用例没跑完（中途抛错了？）")
	if _failures > 0:
		printerr("item_grid_test: %d 项失败" % _failures)
		quit(1)
	else:
		print("item_grid_test: OK")
		quit(0)

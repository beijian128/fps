extends PanelContainer
## 单件物品卡片：图标块 + 名称 + 价格或拥有数 + 数量步进器 + 主按钮。
##
## 商城与背包共用：`mode = "shop"` 时按钮是「购买」并显示价格，`mode = "bag"` 时显示拥有数、
## 按钮是「装备 / 卸下」（不可装备的物品按钮禁用）。

const Tokens := preload("res://theme/tokens.gd")

const QTY_MIN := 1
const QTY_MAX := 99

var _item := {}
var _mode := "shop"
var _manager = null
var _quantity := QTY_MIN

func _ready() -> void:
	%QtyMinus.pressed.connect(func() -> void: set_quantity(_quantity - 1))
	%QtyPlus.pressed.connect(func() -> void: set_quantity(_quantity + 1))
	%Action.pressed.connect(_on_action)

func bind(item: Dictionary, mode: String, manager) -> void:
	_item = item
	_mode = mode
	_manager = manager
	_quantity = QTY_MIN
	_refresh()

# ---- 查询（测试用）----

func quantity() -> int: return _quantity
func action_text() -> String: return %Action.text
func action_disabled() -> bool: return %Action.disabled
func item_id() -> String: return String(_item.get("item_id", ""))

## set_quantity 步进器边界：1..99（服务端的 PurchaseMsg 也只接受 1..99）。
func set_quantity(value: int) -> void:
	_quantity = clampi(value, QTY_MIN, QTY_MAX)
	_refresh()

# ---- 内部 ----

func _on_action() -> void:
	if action_disabled():
		return
	if _mode == "shop":
		_manager.intent_purchase(item_id(), _quantity)
	else:
		# 背包里的按钮是「装备 / 卸下」：卸下就是把主武器槽清空（服务端以空 item_id 表示）。
		if is_equipped():
			_manager.intent_equip("")
		else:
			_manager.intent_equip(item_id())

func is_equipped() -> bool:
	if _manager == null:
		return false
	return String(_manager.logic_state().get("equipped_primary_weapon", "")) == item_id()

func _refresh() -> void:
	var display_name := String(_item.get("display_name", "物品"))
	%Name.text = display_name
	%Icon.color = _icon_color(item_id())
	%Qty.text = str(_quantity) if _mode == "shop" else "×%d" % int(_item.get("owned_quantity", 0))
	if _mode == "shop":
		var price := int(_item.get("price", 0))
		%Info.text = "%d 金币" % price
		%Action.text = "购买"
		%QtyMinus.visible = true
		%QtyPlus.visible = true
		var coins := int(_manager.logic_state().get("coins", 0)) if _manager != null else 0
		# 买不起就禁用：与其点下去再吃一个 insufficient_funds，不如入口就拦住。
		%Action.disabled = price * _quantity > coins
		if %Action.disabled:
			%Info.text = "%d 金币（金币不足）" % price
	else:
		%Info.text = "拥有 ×%d" % int(_item.get("owned_quantity", 0))
		%QtyMinus.visible = false
		%QtyPlus.visible = false
		var equippable := String(_item.get("equip_slot", "")) != ""
		%Action.text = "卸下" if is_equipped() else "装备"
		%Action.disabled = not equippable
		%Action.theme_type_variation = "PrimaryButton" if is_equipped() else "GhostButton"
	# 已装备的卡片加一圈琥珀描边（商城与背包都能看出来）
	var box := get_theme_stylebox("panel").duplicate() as StyleBoxFlat
	if box != null:
		box.border_color = Tokens.ACCENT if is_equipped() else Tokens.BORDER
		add_theme_stylebox_override("panel", box)

## _icon_color 用 item_id 取一个稳定色（同一个物品每次打开颜色一致）。
func _icon_color(id: String) -> Color:
	var palette := [Tokens.BORDER_STRONG, Tokens.ACCENT, Tokens.SUCCESS, Tokens.DANGER]
	return palette[absi(id.hash()) % palette.size()]

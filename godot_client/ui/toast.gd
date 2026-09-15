extends Control
## 交互反馈层：右上角的通知栈（「已购买步枪 ×1」「连接断开，正在重连…」…）。
##
## 为什么要有它：一个动作之后**必须**有回音。买完东西、装备、取消匹配、掉线，界面如果什么都不变，
## 玩家只会反复点 —— 这是「不友好」最常见的来源，比任何配色问题都致命。
##
## 规矩：
##   - 只报「玩家刚做的事 / 刚发生在他身上的事」，不报系统内部事件（那是日志的事）。
##   - 每条自带 TTL 与类型色（信息 / 成功 / 警告 / 失败），最多同时 4 条，超了丢最旧的。
##   - 整层 `mouse_filter = IGNORE`：反馈永远不该挡住下一次点击。

const Tokens := preload("res://theme/tokens.gd")

const MAX_VISIBLE := 4
const TTL := 3.6

enum Kind { INFO, SUCCESS, WARN, DANGER }

var _items: Array = []   # [{node: Control, expires: float}]

func _ready() -> void:
	%Timer.timeout.connect(_expire)

## notify 弹一条提示。重复内容不叠加，只把已有的那条续命并闪一下更好 ——
## 连点五次「购买」不该刷出五条一模一样的提示把屏幕占满。
func notify(text: String, kind: Kind = Kind.INFO) -> void:
	for entry: Dictionary in _items:
		if String(entry.get("text", "")) == text:
			entry["expires"] = _now() + TTL
			_pulse(entry["node"] as Control)
			return
	_push(text, kind)

## clear 清空（测试与新局开始时用）。
func clear() -> void:
	for entry: Dictionary in _items:
		(entry["node"] as Node).queue_free()
	_items = []

## count / texts / last_text 供测试与调试读。
func count() -> int: return _items.size()

func texts() -> Array:
	var out: Array = []
	for entry: Dictionary in _items:
		out.append(String(entry["text"]))
	return out

func last_text() -> String:
	return "" if _items.is_empty() else String((_items.back() as Dictionary)["text"])

func _push(text: String, kind: Kind) -> void:
	var panel := PanelContainer.new()
	panel.theme_type_variation = "BarPanel"
	panel.mouse_filter = Control.MOUSE_FILTER_IGNORE
	var label := Label.new()
	label.text = text
	label.theme_type_variation = "LabelMonoSmall"
	label.add_theme_color_override("font_color", _kind_color(kind))
	label.autowrap_mode = TextServer.AUTOWRAP_WORD_SMART
	panel.add_child(label)
	%Stack.add_child(panel)
	panel.modulate = Color(1, 1, 1, 0)
	var tween := create_tween()
	tween.set_trans(Tween.TRANS_QUAD).set_ease(Tween.EASE_OUT)
	tween.tween_property(panel, "modulate:a", 1.0, 0.12)
	_items.append({"node": panel, "expires": _now() + TTL, "text": text})
	while _items.size() > MAX_VISIBLE:
		var oldest: Dictionary = _items.pop_front()
		(oldest["node"] as Node).queue_free()

func _pulse(node: Control) -> void:
	if node == null:
		return
	var tween := create_tween()
	tween.tween_property(node, "modulate", Color(1.35, 1.35, 1.35, 1.0), 0.06)
	tween.tween_property(node, "modulate", Color(1, 1, 1, 1), 0.18)

func _expire() -> void:
	var now := _now()
	while not _items.is_empty() and float((_items[0] as Dictionary)["expires"]) <= now:
		var entry: Dictionary = _items.pop_front()
		(entry["node"] as Node).queue_free()

func _kind_color(kind: Kind) -> Color:
	match kind:
		Kind.SUCCESS: return Tokens.SUCCESS
		Kind.WARN: return Tokens.ACCENT
		Kind.DANGER: return Tokens.DANGER
	return Tokens.TEXT

func _now() -> float:
	return Time.get_ticks_msec() / 1000.0

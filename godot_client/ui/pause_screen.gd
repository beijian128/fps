extends Control
## 对局内设置层（ESC 打开 / ESC 关闭）。
##
## 它**不暂停对局**：服务端 20 Hz 无条件推进，对手还在跑位开枪。所以面板上写的是
## 「对局仍在继续」而不是「已暂停」，按钮写「返回战场」——让玩家以为暂停了、回去发现
## 自己已经被打死，比没有这个面板更糟。
##
## 职责只有两件：把 `manager.settings()` 渲染成滑块 + 把调整发回 `intent_*`。

const Settings := preload("res://ui/settings.gd")
const Tokens := preload("res://theme/tokens.gd")

## 每行：[偏好键, 滑块节点名, 值标签节点名, 标题]
const ROWS := [
	["mouse_sensitivity", "SensitivitySlider", "SensitivityValue", "鼠标灵敏度"],
	["ui_scale", "UiScaleSlider", "UiScaleValue", "界面缩放"],
	["hit_flash", "HitFlashSlider", "HitFlashValue", "受击反馈强度（0 = 关闭全屏红闪）"],
	["safe_area_pct", "SafeAreaSlider", "SafeAreaValue", "HUD 安全区（电视/投影建议 ≥5%）"],
]

var _manager = null

func _ready() -> void:
	%Resume.pressed.connect(func() -> void: _manager.intent_resume_match())
	%ResetDefaults.pressed.connect(func() -> void: _manager.intent_reset_settings())
	%Scrim.color = Tokens.SCRIM_STRONG
	%Resume.focus_neighbor_left = %ResetDefaults.get_path()
	%ResetDefaults.focus_neighbor_right = %Resume.get_path()
	# 拖动滑块只发意图，值回到 manager 里由 refresh() 统一写回（单一真相源）。
	for row: Array in ROWS:
		var key := String(row[0])
		var slider := get_node("%" + String(row[1])) as HSlider
		slider.value_changed.connect(func(v: float) -> void: _manager.intent_set_setting(key, v))
		# 焦点链：滑杆 → 主按钮 → 恢复默认 → 回到第一根滑杆，手柄上不至于走进死胡同。
		_slider_for(key).focus_neighbor_bottom = %Resume.get_path()
	%Resume.focus_neighbor_top = _slider_for("ui_scale").get_path()

func bind(manager) -> void:
	_manager = manager
	manager.settings_changed.connect(refresh)
	manager.pause_changed.connect(_on_pause_changed)
	refresh()

# ---- 查询（测试用）----

func hint_text() -> String: return %Hint.text
func resume_text() -> String: return %Resume.text
func row_count() -> int: return ROWS.size()

func slider_value(key: String) -> float:
	var slider := _slider_for(key)
	return -1.0 if slider == null else slider.value

func value_text(key: String) -> String:
	for row: Array in ROWS:
		if String(row[0]) == key:
			return (get_node("%" + String(row[2])) as Label).text
	return ""

## set_slider_for_test 直接改滑块（模拟玩家拖动）：无头环境没有真实鼠标事件。
func set_slider_for_test(key: String, v: float) -> void:
	var slider := _slider_for(key)
	if slider != null:
		slider.value = v

# ---- 渲染 ----

## refresh 把当前偏好写进滑块。
##
## 写回时**全程静音**（block_signals）：这里不只是「改写 value 会不会发信号」的问题 ——
## 设置 `min_value` / `max_value` 时，Range 会顺手把当前值夹进新区间并**发出 value_changed**。
## 首次绑定时滑块还是 0，而灵敏度最小值是 0.2、界面缩放最小值是 0.8 —— 那两次夹值会把
## 玩家的偏好直接改写成最小值（0.20× / 80%），而且还顺手存进了 settings.cfg。
## 信号只在「人来拖」时才该发。
func refresh() -> void:
	if _manager == null:
		return
	var settings = _manager.settings()
	for row: Array in ROWS:
		var key := String(row[0])
		var slider := get_node("%" + String(row[1])) as HSlider
		var bounds: Dictionary = Settings.RANGES[key]
		slider.set_block_signals(true)
		slider.min_value = float(bounds["min"])
		slider.max_value = float(bounds["max"])
		slider.step = float(bounds["step"])
		var v: float = settings.value(key)
		slider.set_value_no_signal(v)
		slider.set_block_signals(false)
		var label := get_node("%" + String(row[2])) as Label
		# 灵敏度是「倍数」，其余三项是百分比 —— 两者对玩家的读法不同，不做统一。
		label.text = ("%.2f×" % v) if key == "mouse_sensitivity" else ("%d%%" % int(round(v * 100.0)))

func _on_pause_changed(paused: bool) -> void:
	# 打开时对一遍偏好：设置可能是在这一局之外（比如上一局）改的。
	if paused:
		refresh()
		# 焦点落在「返回战场」：ESC 打开设置的人多半只想确认一下就回去。
		await get_tree().process_frame
		if is_visible_in_tree():
			%Resume.grab_focus()

func _slider_for(key: String) -> HSlider:
	for row: Array in ROWS:
		if String(row[0]) == key:
			return get_node_or_null("%" + String(row[1])) as HSlider
	return null

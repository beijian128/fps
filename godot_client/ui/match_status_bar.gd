extends PanelContainer
## 大厅底部状态条：空闲时是「开始匹配」主按钮；匹配中显示「● 搜索中 · 队列 N 人 · 已等待 Ns」
## 并换成「取消」；左侧常驻最近一局的摘要（来自本地记住的上一次 onMatchEnded）。

var _manager = null

func _ready() -> void:
	%StartMatch.pressed.connect(func() -> void: _manager.intent_start_match())
	%CancelMatch.pressed.connect(func() -> void: _manager.intent_cancel_match())

func bind(manager) -> void:
	_manager = manager
	manager.state_changed.connect(_on_state_changed)
	manager.data_changed.connect(refresh)
	refresh()

# ---- 查询（测试用）----

## queue_text 匹配中的状态文案：`● 搜索中 · 队列 N 人 · 已等待 Ns`。
func queue_text() -> String:
	return %QueueText.text

func last_result_text() -> String:
	return %LastResult.text

func start_visible() -> bool:
	return %StartMatch.visible

func cancel_visible() -> bool:
	return %CancelMatch.visible

# ---- 渲染 ----

func refresh() -> void:
	if _manager == null:
		return
	var screen_manager := preload("res://ui/screen_manager.gd")
	var state: int = _manager.state()
	var searching := state == screen_manager.State.MATCHING
	%StartMatch.visible = not searching
	%CancelMatch.visible = searching
	if searching:
		# 队列人数与等待时长来自服务端每秒推送（onMatchStatus）；推送还没到时先显示占位，
		# 免得闪成「队列 0 人」。
		var status: Dictionary = _manager.match_status()
		if status.is_empty():
			%QueueText.text = "● 搜索中…"
		else:
			%QueueText.text = "● 搜索中 · 队列 %d 人 · 已等待 %ds" % [
				int(status.get("queued_players", 0)), int(status.get("waited_seconds", 0))]
	else:
		%QueueText.text = ""
	%LastResult.text = _last_result_text()

func _on_state_changed(_state: int) -> void:
	refresh()

## _last_result_text 上一局的摘要：胜者在自己的槽位就是「胜」，比分读双方击杀数之和。
## 数据来自本地记住的上一次 onMatchEnded（会话内有效，不回服务端查）。
func _last_result_text() -> String:
	var result: Dictionary = _manager.last_result()
	if result.is_empty():
		return ""
	var slots: Array = result.get("slots", [])
	if slots.size() < 2:
		return ""
	var my_slot: int = _manager.my_slot()
	var won := int(result.get("winner_slot", -1)) == my_slot
	var mine := int((slots[my_slot] as Dictionary).get("kills", 0))
	var theirs := int((slots[1 - my_slot] as Dictionary).get("kills", 0))
	return "上一局：%s %d : %d" % ["胜" if won else "负", mine, theirs]

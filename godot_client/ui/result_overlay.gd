extends Control
## 一局结束的全屏结算层：胜负大字 + 比分 + 对手名 + 时长 + 「回大厅 / 再来一局」。
##
## 它出现就意味着服务端实例已经终结（帧流已断），所以它同时承担「停掉接收看门狗」的责任 ——
## 进入 RESULT 状态由 screen_manager 负责，这里只管显示与两个按钮的意图。

const Tokens := preload("res://theme/tokens.gd")

var _manager = null

func _ready() -> void:
	%BackToLobby.pressed.connect(func() -> void: _manager.intent_back_to_lobby())
	%PlayAgain.pressed.connect(func() -> void: _manager.intent_play_again())

func bind(manager) -> void:
	_manager = manager
	manager.state_changed.connect(_on_state_changed)

# ---- 查询（测试用）----

func headline() -> String: return %Headline.text
func score_text() -> String: return %ScoreLine.text
func duration_text() -> String: return %Duration.text

## show_result 用一份 MatchEnded 填结算层。
##
## 比分读**自己槽位**的击杀数与对方槽位的击杀数 —— 两边的 k/d 是一对镜像数字，读错就
## 把自己和对手搞反了。对手名优先取档案里最近一局的名字（MatchEnded 本身不带名字），
## 取不到就显示「对手」。
func show_result(result: Dictionary, my_slot: int) -> void:
	var slots: Array = result.get("slots", [])
	if slots.size() < 2:
		%Headline.text = "本局结束"
		%ScoreLine.text = ""
		%Duration.text = ""
		return
	var won := int(result.get("winner_slot", -1)) == my_slot
	%Headline.text = "胜 利" if won else "失 败"
	%Headline.add_theme_color_override("font_color", Tokens.SUCCESS if won else Tokens.DANGER)
	var mine := int((slots[my_slot] as Dictionary).get("kills", 0))
	var theirs := int((slots[1 - my_slot] as Dictionary).get("kills", 0))
	%ScoreLine.text = "你 %d : %d %s" % [mine, theirs, _opponent_name()]
	%Duration.text = "用时 %d 秒" % int(result.get("duration_seconds", 0))

## _opponent_name 对手名来自档案最近一局（服务端在记账时存进历史）；拿不到就回退。
func _opponent_name() -> String:
	var recent: Array = _manager.profile().get("recent_matches", [])
	if recent.is_empty():
		return "对手"
	var name := String((recent[0] as Dictionary).get("opponent_name", ""))
	return name if name != "" else "对手"

func _on_state_changed(state: int) -> void:
	var screen_manager := preload("res://ui/screen_manager.gd")
	if state == screen_manager.State.RESULT:
		show_result(_manager.last_result(), _manager.my_slot())

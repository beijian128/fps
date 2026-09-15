extends Control
## 进大厅时的询问框：「你有一场没打完的对局 —— 回到对局，还是放弃对局？」
##
## 为什么要有这个框：服务端是权威的，玩家断线/关客户端之后那一局**照常继续**
## （20 Hz 不因为有人掉线而暂停，实例是「世界生命周期 = 实例生命周期」）。
## 所以回到大厅时只有两种正确做法 —— 回去打完，或者明确放弃；替他自动选任何一个
## 都是错的：自动重连会把他瞬间从大厅拽进战场，自动放弃则是拿一个他没做过的决定
## 去改写他的战绩。
##
## 分层：本屏幕只做两件事 —— 渲染 `manager.pending_match_*()` 与发意图
## （`intent_rejoin_match` / `intent_abandon_match` / `intent_dismiss_rejoin_prompt`）。
## 显隐由 `screen_manager` 统一决定（它是唯一状态源），这里不自己切。

const Tokens := preload("res://theme/tokens.gd")

var _manager = null

func _ready() -> void:
	%Reconnect.pressed.connect(func() -> void: _manager.intent_rejoin_match())
	%Abandon.pressed.connect(func() -> void: _manager.intent_abandon_match())
	%Scrim.color = Tokens.SCRIM_STRONG
	# 两个动作左右互跳：手柄上不会走进死胡同，也不会跑到屏幕外的控件上去。
	%Reconnect.focus_neighbor_left = %Abandon.get_path()
	%Abandon.focus_neighbor_right = %Reconnect.get_path()

func bind(manager) -> void:
	_manager = manager

## open 用一份 PendingMatchReply 填内容。显隐由 manager 管，这里只负责「内容 + 焦点」。
func open(result: Dictionary) -> void:
	var match_id := String(result.get("match_id", ""))
	%MatchInfo.text = ("对局 %s · 仍在服务端进行" % match_id) if match_id != "" else "对局进行中"
	# 焦点落在「回到对局」：进大厅的人多半是想接着打，而破坏性的那一个（放弃）不该
	# 是敲一下回车就命中的默认选项。
	%Reconnect.grab_focus()

## close 收起时把焦点交出去（由 manager 转给大厅）。
func close() -> void:
	if %Reconnect.has_focus():
		%Reconnect.release_focus()
	elif %Abandon.has_focus():
		%Abandon.release_focus()

# ---- 查询（测试用）----

func title_text() -> String: return %Title.text
func info_text() -> String: return %MatchInfo.text
func detail_text() -> String: return %Detail.text
func hint_text() -> String: return %Hint.text
func reconnect_text() -> String: return %Reconnect.text
func abandon_text() -> String: return %Abandon.text
func focus_button() -> String:
	if %Reconnect.has_focus():
		return "reconnect"
	if %Abandon.has_focus():
		return "abandon"
	return ""

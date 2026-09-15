extends Control
## 对局内 HUD：准星、自己血条与 K/D、对手血条与 K/D、回合进度（先到 10 杀）、击杀播报。
##
## 数据全部来自同步属性（按属性名从 WorldStore 读），不新增任何上行消息 —— HUD 是纯消费者。
## 命中的判定点只有一个：main 在「对手掉血」时调 notify_hit_landed（HUD 不重新实现一遍）。

const Tokens := preload("res://theme/tokens.gd")

const MAX_HEALTH := 100.0      # 与 sim 的 playerMaxHealth 一致（血条比例用）
const KILL_TARGET := 10        # 与 sim 的 killTarget 一致（回合进度用）
const FEED_MAX := 4            # 播报条最多同时显示几条
const FEED_TTL := 6.0          # 每条播报存活秒数
const HOT_SECONDS := 0.12      # 命中后准星点亮时长

var _health := MAX_HEALTH
var _opp_health := MAX_HEALTH
var _my_kills := 0
var _my_deaths := 0
var _opp_kills := 0
var _opp_deaths := 0
var _hot_until := 0.0
var _feed: Array = []          # [{node: Control, expires: float}]
var _primed := false           # 本局第一帧是「初始快照」而不是事件，不该产生播报

## update_from_world 每渲染帧由 main 调用一次（只在对局中调用）。
## 只读属性、不改世界：血条读自己与对手的 Health，回合进度读自己的击杀数。
func update_from_world(store, my_slot: int, fps: float) -> void:
	var hp := MAX_HEALTH
	var opp_hp := MAX_HEALTH
	for id: Variant in store.entities_with("Player.Idx"):
		var eid := int(id)
		var idx := int(store.attr(eid, "Player.Idx"))
		var kills := int(store.attr(eid, "Player.Kills"))
		var deaths := int(store.attr(eid, "Player.Deaths"))
		var health := float(store.attr(eid, "Health"))
		if idx == my_slot:
			if _primed:
				_emit_feed(_my_kills, kills, _my_deaths, deaths)
			_my_kills = kills
			_my_deaths = deaths
			hp = health
		else:
			_opp_kills = kills
			_opp_deaths = deaths
			opp_hp = health
	_health = hp
	_opp_health = opp_hp
	%HealthBar.value = _health
	%HealthText.text = str(roundi(_health))
	%OppHealthBar.value = _opp_health
	%OppHealthText.text = str(roundi(_opp_health))
	%SelfKd.text = "%d / %d" % [_my_kills, _my_deaths]
	%OppKd.text = "%d / %d" % [_opp_kills, _opp_deaths]
	%RoundBar.max_value = KILL_TARGET
	%RoundBar.value = _my_kills
	%RoundText.text = "先到 %d 杀 · %d : %d" % [KILL_TARGET, _my_kills, _opp_kills]
	%FpsLabel.text = "%d FPS" % roundi(fps)
	_expire_feed()
	_primed = true
	queue_redraw()   # 准星是自绘的：点亮状态可能刚刚过期，每帧重画一次最省心

## reset 在新一局开始时清空：上一局的战绩、播报、准星状态都不该带进新局；
## 同时把 _primed 归零，让新局第一帧只当初始快照（不刷一屏「你击杀了对手」）。
func reset() -> void:
	_primed = false
	_my_kills = 0
	_my_deaths = 0
	_opp_kills = 0
	_opp_deaths = 0
	_health = MAX_HEALTH
	_opp_health = MAX_HEALTH
	_hot_until = 0.0
	for entry: Dictionary in _feed:
		(entry["node"] as Node).queue_free()
	_feed = []

## notify_hit_landed 由 main 在「对手掉血」时调用（唯一的命中判定点），
## 这里只负责把准星点亮一小会儿。
func notify_hit_landed() -> void:
	_hot_until = _now() + HOT_SECONDS

# ---- 查询（测试用）----

func health_ratio() -> float: return _health / MAX_HEALTH
func health_text() -> String: return %HealthText.text
func opp_health_ratio() -> float: return _opp_health / MAX_HEALTH
func round_ratio() -> float: return float(_my_kills) / float(KILL_TARGET)
func kill_feed_count() -> int: return _feed.size()
func crosshair_hot() -> bool: return _now() < _hot_until
func round_text() -> String: return %RoundText.text

# ---- 自绘准星 ----

## 准星画在 HUD 根节点上（它是全屏 Control）：默认一圈细十字，命中时变琥珀并略微张开。
func _draw() -> void:
	var center := size * 0.5
	var hot := crosshair_hot()
	var color := Tokens.ACCENT if hot else Color(Tokens.TEXT, 0.72)
	var gap := 7.0 if hot else 5.0
	var arm := 13.0 if hot else 9.0
	draw_line(center + Vector2(-gap - arm, 0), center + Vector2(-gap, 0), color, 2.0)
	draw_line(center + Vector2(gap, 0), center + Vector2(gap + arm, 0), color, 2.0)
	draw_line(center + Vector2(0, -gap - arm), center + Vector2(0, -gap), color, 2.0)
	draw_line(center + Vector2(0, gap), center + Vector2(0, gap + arm), color, 2.0)
	draw_circle(center, 1.0, color)

# ---- 内部 ----

func _now() -> float:
	return Time.get_ticks_msec() / 1000.0

## _emit_feed 从「击杀/死亡数变化」推进播报：每多一个击杀是一条「你击杀了对手」，
## 每多一次死亡是一条「你被对手击杀」。差值可能大于 1（重连后一帧拿到多步），所以用循环。
func _emit_feed(prev_kills: int, kills: int, prev_deaths: int, deaths: int) -> void:
	if kills > prev_kills:
		for _i in range(kills - prev_kills):
			_push_feed("你击杀了对手", Tokens.SUCCESS)
	if deaths > prev_deaths:
		for _i in range(deaths - prev_deaths):
			_push_feed("你被对手击杀", Tokens.DANGER)

func _push_feed(text: String, color: Color) -> void:
	var panel := PanelContainer.new()
	panel.theme_type_variation = "BarPanel"
	var label := Label.new()
	label.text = text
	label.add_theme_color_override("font_color", color)
	panel.add_child(label)
	%KillFeed.add_child(panel)
	_feed.append({"node": panel, "expires": _now() + FEED_TTL})
	while _feed.size() > FEED_MAX:   # 超上限从最旧的开始丢
		var oldest: Dictionary = _feed.pop_front()
		(oldest["node"] as Node).queue_free()

func _expire_feed() -> void:
	var now := _now()
	while not _feed.is_empty() and float(_feed[0]["expires"]) <= now:
		var entry: Dictionary = _feed.pop_front()
		(entry["node"] as Node).queue_free()

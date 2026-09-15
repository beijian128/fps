extends Control
## 对局内 HUD：准星、自己与对手的血条 / K/D、回合进度（先到 10 杀）、击杀播报、伤害数字。
##
## 数据全部来自同步属性（按属性名从 WorldStore 读），不新增任何上行消息 —— HUD 是纯消费者。
## 命中没有第二个判定点：main 在「对手掉血」时调 notify_hit_landed，这里有基线之后自己也算一次
## 差值用来冒伤害数字（读的是同一份血量，不重新实现一遍命中逻辑）。
##
## 布局：SafeArea（MarginContainer，安全区四边留白）→ Field（全屏 Control，贴边面板按锚点挂这里）
## → 四个角的面板 + 顶部播报栈 + 全屏伤害数字层。面板挂在容器里会被容器拉成同一个矩形，
## 所以「按锚点贴角」必须挂在普通 Control 上。
##
## 可点性：整层 mouse_filter = IGNORE —— HUD 永远不该吃掉战场的鼠标事件。

const Tokens := preload("res://theme/tokens.gd")

const MAX_HEALTH := 100.0      # 与 sim 的 playerMaxHealth 一致（血条比例用）
const KILL_TARGET := 10        # 与 sim 的 killTarget 一致（回合进度用）
const FEED_MAX := 4            # 播报条最多同时显示几条
const FEED_TTL := 6.0          # 每条播报存活秒数
const DAMAGE_TTL := 0.9        # 伤害数字存活秒数
const DAMAGE_MIN := 0.5        # 小于这个掉血量不冒数字（浮点噪声 / 重连补齐）
const BAR_TWEEN := 0.22        # 血条渐变时长（0 = 瞬间跳变）
const HOT_SECONDS := 0.12      # 命中后准星点亮时长

var _health := MAX_HEALTH
var _opp_health := MAX_HEALTH
var _my_kills := 0
var _my_deaths := 0
var _opp_kills := 0
var _opp_deaths := 0
var _hot_until := 0.0
var _feed: Array = []          # [{node: Control, expires: float}]
var _damage: Array = []        # [{node: Control, expires: float}]
var _primed := false           # 本局第一帧是「初始快照」而不是事件，不该产生播报 / 伤害数字
var _safe_pct := 0.0           # 安全区（屏幕短边的百分比），由 screen_manager 按偏好推下来
var _self_tween: Tween = null
var _opp_tween: Tween = null

func _ready() -> void:
	# 尺寸变化（窗口拉伸 / 全屏切换）时重算安全区：拉伸模式下 size 就是设计基准分辨率，
	# 所以百分比换算不需要任何常量表。
	resized.connect(_on_resized)
	%FeedTimer.timeout.connect(_expire_feed)
	_apply_health_style()
	%HealthBar.step = 0.0
	%OppHealthBar.step = 0.0

func _on_resized() -> void:
	_apply_safe_area(size)

## set_safe_pct 设置 HUD 安全区（0–0.20）。默认 5% = 静态 HUD 落在 90% 的「标题安全区」内：
## 电视/投影会裁掉边缘 3%–10%，贴边摆的血条与击杀播报在那些设备上会被切掉。拉 0 就是纯 PC 贴边。
func set_safe_pct(pct: float) -> void:
	_safe_pct = clampf(pct, 0.0, 0.2)
	_apply_safe_area(size)

## safe_margins 当前四边留白（x = 左右，y = 上下），测试与设置界面用。
func safe_margins() -> Vector2:
	var safe := get_node_or_null("%SafeArea") as MarginContainer
	if safe == null:
		return Vector2.ZERO
	return Vector2(
		float(safe.get_theme_constant("margin_left")),
		float(safe.get_theme_constant("margin_top")),
	)

## _apply_safe_area 把安全区百分比换算成四边留白。HUD 的所有面板都挂在 %SafeArea 下面，
## 所以这里改一次，血条 / 播报 / FPS 会一起内缩，不需要各面板各写一套偏移。
func _apply_safe_area(viewport_size: Vector2) -> void:
	var safe := get_node_or_null("%SafeArea") as MarginContainer
	if safe == null:
		return
	var h := int(round(viewport_size.x * _safe_pct))
	var v := int(round(viewport_size.y * _safe_pct))
	for side in ["left", "right"]:
		safe.add_theme_constant_override("margin_" + side, h)
	for side in ["top", "bottom"]:
		safe.add_theme_constant_override("margin_" + side, v)

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

	# 对手掉血 → 冒一个伤害数字。用同一份血量算差值，不引入第二个命中判定点。
	var lost := _opp_health - opp_hp
	if _primed and lost > DAMAGE_MIN:
		_push_damage(lost)

	_set_health(hp)
	_set_opp_health(opp_hp)
	%HealthText.text = _health_text(_health)
	%OppHealthText.text = _health_text(_opp_health)
	%SelfKd.text = "%d / %d" % [_my_kills, _my_deaths]
	%OppKd.text = "%d / %d" % [_opp_kills, _opp_deaths]
	%RoundBar.max_value = KILL_TARGET
	%RoundBar.value = _my_kills
	%RoundText.text = "先到 %d 杀 · %d : %d" % [KILL_TARGET, _my_kills, _opp_kills]
	%FpsLabel.text = "%d FPS" % roundi(fps)
	_expire_feed()
	_expire_damage()
	_primed = true
	queue_redraw()   # 准星是自绘的：点亮状态可能刚刚过期，每帧重画一次最省心

## reset 在新一局开始时清空：上一局的战绩、播报、伤害数字、准星状态都不该带进新局；
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
	if _self_tween != null:
		_self_tween.kill()
		_self_tween = null
	if _opp_tween != null:
		_opp_tween.kill()
		_opp_tween = null
	%HealthBar.value = MAX_HEALTH
	%OppHealthBar.value = MAX_HEALTH
	for entry: Dictionary in _feed:
		(entry["node"] as Node).queue_free()
	_feed = []
	for entry: Dictionary in _damage:
		(entry["node"] as Node).queue_free()
	_damage = []
	_apply_health_style()

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
func damage_number_count() -> int: return _damage.size()
func crosshair_hot() -> bool: return _now() < _hot_until
func round_text() -> String: return %RoundText.text
func self_bar_value() -> float: return %HealthBar.value

# ---- 血条 ----

## _set_health 血条渐变：掉血是「事件」，突然跳一格会让人怀疑刚才那枪到底打没打中；
## 0.22s 的补间既跟得上节奏，又给出可读的变化过程。同一时刻只允许一条补间在跑。
func _set_health(hp: float) -> void:
	_health = clampf(hp, 0.0, MAX_HEALTH)
	_apply_health_style()
	if _primed and BAR_TWEEN > 0.0:
		if _self_tween != null:
			_self_tween.kill()
		_self_tween = create_tween()
		_self_tween.set_trans(Tween.TRANS_QUAD).set_ease(Tween.EASE_OUT)
		_self_tween.tween_property(%HealthBar, "value", _health, BAR_TWEEN)
	else:
		%HealthBar.value = _health

func _set_opp_health(hp: float) -> void:
	_opp_health = clampf(hp, 0.0, MAX_HEALTH)
	if _primed and BAR_TWEEN > 0.0:
		if _opp_tween != null:
			_opp_tween.kill()
		_opp_tween = create_tween()
		_opp_tween.set_trans(Tween.TRANS_QUAD).set_ease(Tween.EASE_OUT)
		_opp_tween.tween_property(%OppHealthBar, "value", _opp_health, BAR_TWEEN)
	else:
		%OppHealthBar.value = _opp_health

## _apply_health_style 按剩余比例切血条配色（绿 → 琥珀 → 红）。
## 阈值在 tokens 里，界面只负责「取色 → 换成对应的主题变体」，不现造样式盒。
func _apply_health_style() -> void:
	var ratio := _health / MAX_HEALTH
	var variation := "HealthBar"
	if ratio <= 0.3:
		variation = "HealthBarCritical"
	elif ratio <= 0.6:
		variation = "HealthBarWarn"
	%HealthBar.theme_type_variation = variation

## _health_text 「当前 / 上限」：数字自带参照，不用靠血条长度估还剩多少。
func _health_text(hp: float) -> String:
	return "%d / %d" % [roundi(hp), roundi(MAX_HEALTH)]

# ---- 自绘准星 ----

## 准星画在 HUD 根节点上（它是全屏 Control）：默认一圈细十字，命中时变琥珀、略微张开，
## 并补上四根斜向命中标记 —— 只靠「颜色变了」在高速对枪时很容易漏看。
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
	if hot:
		for corner: Vector2 in [Vector2(-1, -1), Vector2(1, -1), Vector2(-1, 1), Vector2(1, 1)]:
			var inner := center + corner * 9.0
			draw_line(inner, inner + corner * 5.0, Tokens.ACCENT, 2.0)

# ---- 播报 / 伤害数字 ----

## _emit_feed 从「击杀/死亡数变化」推进播报：每多一个击杀是一条「你击杀了对手」，
## 每多一次死亡是一条「你被对手击杀」。差值可能大于 1（重连后一帧拿到多步），所以用循环。
func _emit_feed(prev_kills: int, kills: int, prev_deaths: int, deaths: int) -> void:
	if kills > prev_kills:
		for _i in range(kills - prev_kills):
			_push_feed("你击杀了对手", Tokens.SUCCESS)
	if deaths > prev_deaths:
		for _i in range(deaths - prev_deaths):
			_push_feed("你被对手击杀", Tokens.DANGER)

## 播报走「通知栈」：最旧的在上、超上限丢最旧、每条自带 TTL。
## 入场用补间淡入 + 右滑，退出直接释放（继续加动画会拖慢快速连杀的节奏）。
func _push_feed(text: String, color: Color) -> void:
	var panel := PanelContainer.new()
	panel.theme_type_variation = "BarPanel"
	panel.mouse_filter = Control.MOUSE_FILTER_IGNORE
	var label := Label.new()
	label.text = text
	label.theme_type_variation = "LabelMonoSmall"
	label.add_theme_color_override("font_color", color)
	panel.add_child(label)
	%KillFeed.add_child(panel)
	panel.modulate = Color(1, 1, 1, 0)
	var tween := create_tween()
	tween.set_trans(Tween.TRANS_QUAD).set_ease(Tween.EASE_OUT)
	tween.tween_property(panel, "modulate:a", 1.0, 0.15)
	_feed.append({"node": panel, "expires": _now() + FEED_TTL})
	while _feed.size() > FEED_MAX:   # 超上限从最旧的开始丢
		var oldest: Dictionary = _feed.pop_front()
		(oldest["node"] as Node).queue_free()

func _expire_feed() -> void:
	var now := _now()
	while not _feed.is_empty() and float(_feed[0]["expires"]) <= now:
		var entry: Dictionary = _feed.pop_front()
		(entry["node"] as Node).queue_free()

## _push_damage 冒一个伤害数字：从准星位置轻微上浮 + 淡出。
## 位置先用准星（屏幕中心）而不是命中点 —— 命中点要从 3D 世界投影过来，而 HUD 拿不到
## 相机；等 main 把投影后的屏幕坐标传进来，这里换成那个坐标即可（接口不用改）。
func _push_damage(amount: float) -> void:
	var label := Label.new()
	label.theme_type_variation = "LabelMonoSmall"
	label.text = "-%d" % roundi(amount)
	label.add_theme_color_override("font_color", Tokens.ACCENT)
	label.mouse_filter = Control.MOUSE_FILTER_IGNORE
	%DamageLayer.add_child(label)
	var center := size * 0.5 + Vector2(18.0, -26.0)
	label.position = center
	label.modulate = Color(1, 1, 1, 0.95)
	var tween := create_tween()
	tween.set_parallel(true)
	tween.tween_property(label, "position", center + Vector2(0, -34), DAMAGE_TTL)
	tween.tween_property(label, "modulate:a", 0.0, DAMAGE_TTL)
	_damage.append({"node": label, "expires": _now() + DAMAGE_TTL})

func _expire_damage() -> void:
	var now := _now()
	while not _damage.is_empty() and float(_damage[0]["expires"]) <= now:
		var entry: Dictionary = _damage.pop_front()
		(entry["node"] as Node).queue_free()

func _now() -> float:
	return Time.get_ticks_msec() / 1000.0

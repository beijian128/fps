extends Control
## 个人信息页：左侧身份栏（头像/等级/经验条/账号）+ 右侧六个统计卡 + 最近 20 场战绩列表。

const Tokens := preload("res://theme/tokens.gd")

const STAT_KEYS := ["kills", "deaths", "kd", "matches", "wins", "losses"]

var _manager = null
var _profile := {}
var _rows: Array = []

func bind(manager) -> void:
	_manager = manager

## render 用一份 PlayerProfileReply 刷新整页。派生值（K/D、胜率）在这里算，
## 服务端只给原始计数。
func render(profile: Dictionary) -> void:
	_profile = profile
	var username: String = "—" if _manager == null else String(_manager.username())
	%Username.text = username
	%Avatar.color = _avatar_color(username)
	%Level.text = level_text()
	%XP.max_value = maxf(1.0, float(profile.get("xp_for_next_level", 200)))
	%XP.value = float(profile.get("xp_into_level", 0))
	%XPText.text = xp_text()
	var account: String = "—" if _manager == null else String(_manager.account_id())
	%AccountId.text = "账号 #%s" % account
	for key in STAT_KEYS:
		# 注意别写成 `"%Stat_%s_Value" % key`：格式串开头的 `%S` 会被当成格式符而报错。
		var label: Label = get_node("%Stat_" + key + "_Value")
		label.text = stat_text(key)
	_build_rows(profile.get("recent_matches", []))

# ---- 查询（测试用）----

func level_text() -> String:
	return "LV.%d" % int(_profile.get("level", 1))

func xp_text() -> String:
	return "%d / %d XP" % [int(_profile.get("xp_into_level", 0)), int(_profile.get("xp_for_next_level", 0))]

## stat_text 六个统计卡的值。K/D 与胜率是**派生**的：服务端只存原始计数，
## 界面自己算，免得为展示逻辑加协议字段。
func stat_text(key: String) -> String:
	var kills := int(_profile.get("kills", 0))
	var deaths := int(_profile.get("deaths", 0))
	var matches := int(_profile.get("matches", 0))
	var wins := int(_profile.get("wins", 0))
	match key:
		"kills": return str(kills)
		"deaths": return str(deaths)
		"matches": return str(matches)
		"wins": return str(wins)
		"losses": return str(int(_profile.get("losses", 0)))
		"kd": return "%.2f" % (float(kills) / float(maxi(deaths, 1)))
		"winrate": return "%d%%" % roundi(100.0 * float(wins) / float(maxi(matches, 1)))
	return ""

## record_rows 战绩列表的渲染数据（每行：结果 / 比分 / 对手 / 时长）。
## 顺序由服务端决定（新的在前，最多 20 条），界面不排序、不截断。
func record_rows() -> Array:
	return _rows

func empty_hint_visible() -> bool:
	return %EmptyHint.visible

# ---- 内部 ----

func _build_rows(records: Array) -> void:
	for child in %Records.get_children():
		%Records.remove_child(child)
		child.queue_free()
	_rows = []
	for record: Variant in records:
		var r: Dictionary = record
		var won := bool(r.get("won", false))
		var kills := int(r.get("kills", 0))
		var opponent_kills := int(r.get("opponent_kills", 0))
		_rows.append({
			"result": "胜" if won else "负",
			"score": "%d : %d" % [kills, opponent_kills],
			"opponent": String(r.get("opponent_name", "")) if String(r.get("opponent_name", "")) != "" else "对手",
			"duration": "%ds" % int(r.get("duration_seconds", 0)),
		})
		%Records.add_child(_row_node(_rows.back(), won))
	%EmptyHint.visible = _rows.is_empty()
	%RecordsTitle.visible = not _rows.is_empty()

func _row_node(row: Dictionary, won: bool) -> Control:
	var panel := PanelContainer.new()
	var box := HBoxContainer.new()
	box.add_theme_constant_override("separation", Tokens.SP_3)
	panel.add_child(box)
	var result := Label.new()
	result.text = String(row["result"])
	result.custom_minimum_size = Vector2(28, 0)
	result.add_theme_color_override("font_color", Tokens.SUCCESS if won else Tokens.DANGER)
	var score := Label.new()
	score.text = String(row["score"])
	score.custom_minimum_size = Vector2(72, 0)
	score.add_theme_font_override("font", Tokens.mono_font())
	var opponent := Label.new()
	opponent.text = String(row["opponent"])
	opponent.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	opponent.add_theme_color_override("font_color", Tokens.TEXT)
	var duration := Label.new()
	duration.text = String(row["duration"])
	duration.add_theme_font_override("font", Tokens.mono_font())
	duration.add_theme_color_override("font_color", Tokens.TEXT_MUTED)
	for node in [result, score, opponent, duration]:
		box.add_child(node)
	return panel

## _avatar_color 头像块按用户名取一个稳定色（同一账号每次打开颜色一致）。
func _avatar_color(username: String) -> Color:
	var palette := [Tokens.ACCENT, Tokens.SUCCESS, Tokens.DANGER, Tokens.BORDER_STRONG]
	return palette[absi(username.hash()) % palette.size()]

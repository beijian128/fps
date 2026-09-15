extends SceneTree
## 个人信息页：用合成 PlayerProfileReply 驱动，断言派生值（K/D、胜率）与战绩列表。
##
## 运行：Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client \
##         --script res://tests/profile_screen_test.gd

const Screen := preload("res://ui/profile_screen.tscn")

var _failures := 0
var _done := {}

func _initialize() -> void:
	var screen: Control = Screen.instantiate()
	root.add_child(screen)
	screen.render({
		"ok": true, "level": 7, "xp": 420, "xp_into_level": 20, "xp_for_next_level": 200,
		"kills": 128, "deaths": 55, "matches": 39, "wins": 25, "losses": 14,
		"recent_matches": [
			{"match_id": "m1", "won": true, "kills": 10, "deaths": 7, "opponent_kills": 7,
				"duration_seconds": 84, "opponent_name": "bob_77", "ended_at": 1},
			{"match_id": "m2", "won": false, "kills": 4, "deaths": 10, "opponent_kills": 10,
				"duration_seconds": 132, "opponent_name": "kate_x", "ended_at": 2},
		],
	})
	_check(screen.stat_text("kills") == "128", "击杀数应原样显示，得到 %s" % screen.stat_text("kills"))
	_check(screen.stat_text("deaths") == "55", "死亡数应原样显示")
	_check(screen.stat_text("matches") == "39", "场次应原样显示")
	_check(screen.stat_text("wins") == "25" and screen.stat_text("losses") == "14", "胜负应原样显示")
	_check(screen.stat_text("kd") == "2.33", "K/D 应是 128/55 保留两位（2.33），得到 %s" % screen.stat_text("kd"))
	_check(screen.stat_text("winrate") == "64%", "胜率应是 25/39 四舍五入（64%%），得到 %s" % screen.stat_text("winrate"))
	_check(screen.level_text() == "LV.7", "等级应带 LV. 前缀，得到 %s" % screen.level_text())
	_check(screen.xp_text() == "20 / 200 XP", "经验应显示级内值与升级所需，得到 %s" % screen.xp_text())

	var rows: Array = screen.record_rows()
	_check(rows.size() == 2, "应渲染 2 条战绩，得到 %d" % rows.size())
	if rows.size() == 2:
		_check(String(rows[0]["result"]) == "胜", "首行应是胜")
		_check(String(rows[0]["score"]) == "10 : 7", "首行比分应是 10 : 7，得到 %s" % rows[0]["score"])
		_check(String(rows[0]["opponent"]) == "bob_77", "首行应显示对手名")
		_check(String(rows[0]["duration"]) == "84s", "首行时长应是 84s")
		_check(String(rows[1]["result"]) == "负", "次行应是负")
	_check(not screen.empty_hint_visible(), "有战绩时不该显示空态")

	# 空态：没有战绩 → 空提示可见、列表为空
	screen.render({"ok": true, "level": 1, "xp": 0, "xp_into_level": 0, "xp_for_next_level": 200,
		"kills": 0, "deaths": 0, "matches": 0, "wins": 0, "losses": 0, "recent_matches": []})
	_check(screen.record_rows().is_empty(), "空态不该有战绩行")
	_check(screen.empty_hint_visible(), "空态应显示提示文案")
	_check(screen.stat_text("kd") == "0.00", "0 死亡时 K/D 应按 1 算（0.00），得到 %s" % screen.stat_text("kd"))
	_check(screen.stat_text("winrate") == "0%", "0 场时胜率应是 0%%")

	# 对手名为空（对方没有账号 Hash）→ 回退成「对手」而不是空字符串
	screen.render({"ok": true, "level": 2, "xp_into_level": 0, "xp_for_next_level": 200,
		"kills": 1, "deaths": 1, "matches": 1, "wins": 1, "losses": 0,
		"recent_matches": [{"match_id": "m3", "won": true, "kills": 10, "deaths": 2,
			"opponent_kills": 2, "duration_seconds": 60, "opponent_name": "", "ended_at": 3}]})
	_check(String(screen.record_rows()[0]["opponent"]) == "对手", "空对手名应回退成「对手」")

	_done["profile"] = true
	_finish()

func _check(cond: bool, msg: String) -> void:
	if not cond:
		_failures += 1
		printerr("FAIL: " + msg)

func _finish() -> void:
	if not _done.has("profile"):
		_failures += 1
		printerr("FAIL: 用例没跑完（中途抛错了？）")
	if _failures > 0:
		printerr("profile_screen_test: %d 项失败" % _failures)
		quit(1)
	else:
		print("profile_screen_test: OK")
		quit(0)

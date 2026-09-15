extends SceneTree
## 新增 route `logic.logic.profile` 的端到端冒烟（需要活集群）：注册一个新账号 →
## 请求个人档案 → 断言应答解得出 ok=true 与零值档案。
##
## 这条链路覆盖的是「gate 把 logic.* 随机路由到 logic 节点 → logic handler 从会话取
## uid → 缺行就地补建 → PlayerProfileReply → 客户端解码」整段。战绩本身（打完一局 +1）
## 由 Go 侧单测覆盖，见 spec §9 的说明。
##
## 运行：godot --headless --path godot_client --script res://tests/profile_smoke.gd

const PASSWORD := "smokepass"

var _client: Node = null
var _username := ""
var _state := {}

func _initialize() -> void:
	for p in ["user://auth_token.txt", "user://last_username.txt"]:
		if FileAccess.file_exists(p):
			DirAccess.remove_absolute(ProjectSettings.globalize_path(p))
	_username = "prof_%d" % (Time.get_ticks_usec() % 100000000)
	_client = load("res://scripts/fps_client.gd").new()
	_client.login_result.connect(_on_login)
	_client.profile_received.connect(_on_profile)
	root.add_child(_client)
	_run()

func _on_login(result: Dictionary) -> void:
	if bool(result.get("ok", false)):
		_state["auth"] = true
		_client.send_profile()   # 认证之后才请求档案（身份来自会话绑定）
		return
	var reason := String(result.get("reason", ""))
	if reason == "no_token" and not _state.has("registering"):
		_state["registering"] = true
		_client.send_register(_username, PASSWORD)
		return
	_state["fail"] = reason

func _on_profile(result: Dictionary) -> void:
	_state["profile"] = result

func _run() -> void:
	var deadline := Time.get_ticks_msec() + 40000
	while not _state.has("profile") and Time.get_ticks_msec() < deadline:
		if _state.has("fail"):
			break
		await process_frame

	if _state.has("fail"):
		printerr("FAIL: 登录失败 reason=%s" % _state["fail"])
		quit(1)
		return
	if not _state.has("profile"):
		printerr("FAIL: 等 PlayerProfileReply 超时（route 没通？）")
		quit(1)
		return
	var p: Dictionary = _state["profile"]
	if bool(p.get("_failed", false)):
		printerr("FAIL: profile 请求失败 reason=%s" % p.get("reason", ""))
		quit(1)
		return
	if not bool(p.get("ok", false)):
		printerr("FAIL: profile 应答 ok=false reason=%s" % p.get("reason", ""))
		quit(1)
		return
	print("PROFILE level=%d xp=%d kills=%d deaths=%d matches=%d wins=%d losses=%d recent=%d"
		% [int(p["level"]), int(p["xp"]), int(p["kills"]), int(p["deaths"]),
			int(p["matches"]), int(p["wins"]), int(p["losses"]), (p["recent_matches"] as Array).size()])
	if int(p["level"]) != 1 or int(p["matches"]) != 0:
		printerr("FAIL: 新账号应是 1 级、0 场，得到 level=%d matches=%d"
			% [int(p["level"]), int(p["matches"])])
		quit(1)
		return
	print("profile_smoke: OK")
	quit(0)

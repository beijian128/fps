extends SceneTree
## 开发用冒烟测试（不进发布包）：
## 用真实的 FpsClient 节点走 pitaya/pomelo 握手 + 注册 + 匹配 + protobuf 解码，
## 验证服务端匹配成功（onMatched）后以 20 Hz 推送 onFrame 帧（4 秒约 80 帧）。
##
## 身份来自登录：match 只认会话绑定的 uid，未登录的 join 会被静默忽略，
## 所以握手之后必须先注册一个账号才能进匹配。
##
## 运行：先起 etcd + nats + redis + gate/account/match/game 四进程，再：
##   godot --headless --path <项目> --script res://tests/ws_smoke.gd

const PASSWORD := "smokepass"

var _client: Node = null
var _steps := {}
var _min_step := 1 << 30
var _max_step := -1
var _matched := false
var _username := ""
var _auth := {}

func _initialize() -> void:
	# 从「本地没有凭证」开始：残留 token 会让客户端 resume 成上一轮的账号，
	# 而那个账号多半还挂着上一轮的对局，帧号不会从头开始。
	for p in ["user://auth_token.txt", "user://last_username.txt"]:
		if FileAccess.file_exists(p):
			DirAccess.remove_absolute(ProjectSettings.globalize_path(p))
	_username = "wssmoke_%d" % (Time.get_ticks_usec() % 100000000)
	_client = load("res://scripts/fps_client.gd").new()
	# 信号在 add_child 之前连上：握手应答可能在下一帧就回来。
	_client.login_result.connect(_on_login)
	_client.frame_received.connect(_on_frame)
	_client.matched_received.connect(_on_matched)
	root.add_child(_client)
	_run()

## _on_login 本地无凭证就注册；认证成功后才发 match.join（绑定之前发会被丢掉）。
func _on_login(result: Dictionary) -> void:
	if bool(result.get("ok", false)):
		_auth["ok"] = true
		_client.send_match_join()
		return
	var reason := String(result.get("reason", ""))
	if reason == "no_token" and not _auth.has("registering"):
		_auth["registering"] = true
		_client.send_register(_username, PASSWORD)
		return
	_auth["fail"] = reason

func _on_matched(_r: Dictionary) -> void:
	_matched = true

func _on_frame(f: Dictionary) -> void:
	var step := int(f.get("step", -1))
	if step >= 0:
		_steps[step] = true
		_min_step = mini(_min_step, step)
		_max_step = maxi(_max_step, step)

func _run() -> void:
	# 先等 WS 连接建立。
	var deadline := Time.get_ticks_msec() + 5000
	while not _client.connected and Time.get_ticks_msec() < deadline:
		await process_frame
	if not _client.connected:
		print("SMOKE ws open failed")
		quit(1)
		return
	print("SMOKE ws open")

	# 等注册/登录应答。
	deadline = Time.get_ticks_msec() + 10000
	while not (_auth.has("ok") or _auth.has("fail")) and Time.get_ticks_msec() < deadline:
		await process_frame
	if _auth.has("fail"):
		print("SMOKE login failed reason=", _auth["fail"])
		quit(1)
		return
	if not _auth.has("ok"):
		print("SMOKE login timed out (no LoginReply)")
		quit(1)
		return
	print("SMOKE logged in user=", _username)

	# 等匹配完成（单人兜底 10s 内会 onMatched）。
	deadline = Time.get_ticks_msec() + 15000
	while not _matched and Time.get_ticks_msec() < deadline:
		if not _client.connected:
			print("SMOKE ws closed before match")
			quit(1)
			return
		await process_frame
	if not _matched:
		print("SMOKE match failed")
		quit(1)
		return
	print("SMOKE matched")

	var t0 := Time.get_ticks_msec()
	while Time.get_ticks_msec() - t0 < 4000:
		if not _client.connected:
			print("SMOKE ws closed mid-run")
			quit(1)
			return
		await process_frame
	print("SMOKE unique_steps=", _steps.size(), " span=", _max_step - _min_step,
		" elapsed_ms=", Time.get_ticks_msec() - t0)
	quit(0)

extends SceneTree
## 开发用冒烟测试（不进发布包）：
## 用真实的 FpsClient 节点走 pitaya/pomelo 握手 + 匹配 + protobuf 解码，验证服务端
## 匹配成功（onMatched）后以 20 Hz 推送 onSnapshot 快照（4 秒约 80 帧）。
##
## 运行：先起 etcd+nats + gate/match/game 三进程，再：
##   godot --headless --path <项目> --script res://tests/ws_smoke.gd

var _client: Node = null
var _steps := {}
var _min_step := 1 << 30
var _max_step := -1
var _matched := false

func _initialize() -> void:
	_client = load("res://scripts/fps_client.gd").new()
	_client.state_received.connect(_on_state)
	_client.matched_received.connect(_on_matched)
	root.add_child(_client)
	_run()

func _on_matched(_r: Dictionary) -> void:
	_matched = true

func _on_state(s: Dictionary) -> void:
	var step := int(s.get("step", -1))
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

	# 等握手 + 匹配完成（单人兜底 10s 内会 onMatched）。
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

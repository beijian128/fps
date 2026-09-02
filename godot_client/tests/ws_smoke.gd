extends SceneTree
## 开发用冒烟测试（不进发布包）：
## 连接 WebSocket 状态流，验证服务端以 20 Hz 主动推送快照（4 秒约 80 帧）。
##
## 运行：godot --headless --path <项目> --script res://tests/ws_smoke.gd

const WS_URL := "ws://localhost:8080/"

var ws := WebSocketPeer.new()

func _initialize() -> void:
	ws.connect_to_url(WS_URL)
	_run()

func _run() -> void:
	var deadline := Time.get_ticks_msec() + 5000
	while ws.get_ready_state() != WebSocketPeer.STATE_OPEN and Time.get_ticks_msec() < deadline:
		ws.poll()
		await process_frame
	if ws.get_ready_state() != WebSocketPeer.STATE_OPEN:
		print("SMOKE ws open failed")
		quit(1)
		return
	print("SMOKE ws open")
	var steps := {}
	var min_step := 1 << 30
	var max_step := -1
	var t0 := Time.get_ticks_msec()
	while Time.get_ticks_msec() - t0 < 4000:
		if ws.get_ready_state() != WebSocketPeer.STATE_OPEN:
			print("SMOKE ws closed mid-run")
			quit(1)
			return
		ws.poll()
		while ws.get_available_packet_count() > 0:
			var pkt: PackedByteArray = ws.get_packet()
			var data: Variant = JSON.parse_string(pkt.get_string_from_utf8())
			if data is Dictionary:
				var step := int(data.get("step", -1))
				if step >= 0:
					steps[step] = true
					min_step = mini(min_step, step)
					max_step = maxi(max_step, step)
		await process_frame
	print("SMOKE unique_steps=", steps.size(), " span=", max_step - min_step,
		" elapsed_ms=", Time.get_ticks_msec() - t0)
	quit(0)

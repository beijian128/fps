extends Node
## 与游戏服务端的 WebSocket 传输层：只负责连接管理、收发与 JSON 编解码，
## 游戏逻辑不感知网络细节。连接断开后自动重连并补推快照。

signal state_received(state: Dictionary)  # 服务端推送的状态快照
signal connection_changed(connected: bool) # 连接状态变化

const WS_URL := "ws://localhost:8080/"
const RETRY_SECS := 1.0

var _ws := WebSocketPeer.new()
var _retry_at := 0.0
var connected := false

func _ready() -> void:
	_ws.connect_to_url(WS_URL)

func _process(_delta: float) -> void:
	_ws.poll()
	match _ws.get_ready_state():
		WebSocketPeer.STATE_OPEN:
			if not connected:
				connected = true
				connection_changed.emit(true)
			while _ws.get_available_packet_count() > 0:
				_handle_packet(_ws.get_packet())
		WebSocketPeer.STATE_CLOSED:
			if connected:
				connected = false
				connection_changed.emit(false)
			var now := Time.get_ticks_msec() / 1000.0
			if now >= _retry_at:
				_retry_at = now + RETRY_SECS
				_ws = WebSocketPeer.new()
				_ws.connect_to_url(WS_URL)

func _handle_packet(pkt: PackedByteArray) -> void:
	var data: Variant = JSON.parse_string(pkt.get_string_from_utf8())
	if data is Dictionary:
		state_received.emit(data)

func send_input(move: Vector2, jump: bool) -> void:
	_send({"type": "input", "move": [move.x, move.y], "jump": jump})

func send_shoot(origin: Vector3, dir: Vector3) -> void:
	_send({"type": "shoot", "origin": [origin.x, origin.y, origin.z], "dir": [dir.x, dir.y, dir.z]})

func send_reset() -> void:
	_send({"type": "reset"})

func _send(msg: Dictionary) -> void:
	if connected:
		_ws.send_text(JSON.stringify(msg))

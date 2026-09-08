extends Node
## 与游戏服务端的 WebSocket 传输层：只负责连接管理、收发与 JSON 编解码，
## 游戏逻辑不感知网络细节。连接断开后自动重连并补推快照。
##
## 活性保障：服务端每 tick（50 ms）都推送快照，因此用"N 秒收不到任何数据"
## 作为接收看门狗——半开连接（对端崩溃不发 FIN、路由丢包、休眠唤醒）不会让
## WebSocketPeer 进入 CLOSED，此时强制重建连接，避免画面永久冻结。

signal state_received(state: Dictionary)  # 服务端推送的状态快照
signal connection_changed(connected: bool) # 连接状态变化

const WS_URL := "ws://localhost:8080/"
const RETRY_SECS := 1.0
const RECV_TIMEOUT := 2.5  # 秒：20 Hz 推送下正常间隔 ~50ms，远小于此阈值

var _ws := WebSocketPeer.new()
var _retry_at := 0.0
var _last_recv := 0.0  # 最近一次收到数据的时间戳（秒）
var connected := false

func _ready() -> void:
	_ws.connect_to_url(WS_URL)

func _process(_delta: float) -> void:
	var now := Time.get_ticks_msec() / 1000.0
	_ws.poll()
	match _ws.get_ready_state():
		WebSocketPeer.STATE_OPEN:
			if not connected:
				connected = true
				_last_recv = now
				connection_changed.emit(true)
			while _ws.get_available_packet_count() > 0:
				_handle_packet(_ws.get_packet())
				_last_recv = now
			if now - _last_recv > RECV_TIMEOUT:
				_force_reconnect()
		WebSocketPeer.STATE_CLOSED:
			if connected:
				connected = false
				connection_changed.emit(false)
			if now >= _retry_at:
				_retry_at = now + RETRY_SECS
				_ws = WebSocketPeer.new()
				_ws.connect_to_url(WS_URL)

## 强制重连：旧 peer 可能停在一个永远不会进入 CLOSED 的半开连接上（对端已死、
## 没有 FIN/RST 回来），继续等待只会冻结画面。直接丢弃旧 peer 并立即重连。
func _force_reconnect() -> void:
	if connected:
		connected = false
		connection_changed.emit(false)
	_ws = WebSocketPeer.new()
	_retry_at = 0.0
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

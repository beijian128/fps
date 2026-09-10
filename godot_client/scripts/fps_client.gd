extends Node
## 与游戏服务端的传输层：只负责连接管理、收发与协议编解码，游戏逻辑不感知网络
## 细节。连接断开后自动重连，重连成功后重新走握手。
##
## 协议：pitaya 的 pomelo 帧格式（二进制）：
##   - 帧头 4 字节：1 字节 type + 3 字节大端长度 + data
##     type = Handshake 0x01 / HandshakeAck 0x02 / Heartbeat 0x03 / Data 0x04
##   - Data 帧内是 message 编码：flag 字节（type<<1）+ 1 字节 route 长度 + route
##     字符串 + payload（Notify 无 id；Push 无 id）
##   - payload 用 **protobuf** 序列化（schema 见 joltgo/game/protos/game.proto）：
##     上行 match.join（JoinMsg，带持久化 token）/ game.cmd（CommandMsg，一帧一条）/
##     game.resync（空，请求全量）；
##     下行 onMatched（MatchResult）/ onFrame（Frame，实体-属性增量）
##   - 握手：连接后发 Handshake{json} → 收 Handshake 响应 → 发 HandshakeAck →
##     发 match.join 进入匹配，收到 onMatched 后进入对局
##   - 心跳：按固定间隔发 Heartbeat 空帧，防止服务端超时踢人
##
## 活性保障：服务端每 tick（50 ms）都推送快照，因此用"N 秒收不到任何数据"作为
## 接收看门狗——半开连接（对端崩溃不发 FIN、路由丢包、休眠唤醒）不会让
## WebSocketPeer 进入 CLOSED，此时强制重建连接，避免画面永久冻结。

signal frame_received(frame: Dictionary)   # 服务端推送的同步帧（增量或全量）
signal matched_received(result: Dictionary)
signal connection_changed(connected: bool)

const WS_URL := "ws://localhost:8080/"
const RETRY_SECS := 1.0
const RECV_TIMEOUT := 2.5    # 秒：20 Hz 推送下正常间隔 ~50ms，远小于此阈值
const HEARTBEAT_EVERY := 10.0 # 秒：服务端心跳超时 30s（2 倍才踢），10s 足够安全

const TYPE_HANDSHAKE := 0x01
const TYPE_HANDSHAKE_ACK := 0x02
const TYPE_HEARTBEAT := 0x03
const TYPE_DATA := 0x04

const MSG_REQUEST := 0x00
const MSG_NOTIFY := 0x01
const MSG_RESPONSE := 0x02
const MSG_PUSH := 0x03

# protobuf wire type
const WIRE_VARINT := 0
const WIRE_FIXED32 := 5
const WIRE_LEN := 2

const TOKEN_PATH := "user://client_id.txt"

var _ws := WebSocketPeer.new()
var _retry_at := 0.0
var _last_recv := 0.0    # 最近一次收到数据的时间戳（秒）
var _last_beat := 0.0    # 最近一次发心跳的时间戳（秒）
var _handshaken := false # 是否已完成握手（连接后置 false，收到握手响应后置 true）
var _matched := false    # 是否已匹配进入对局（匹配前无快照流，看门狗不生效）
var connected := false
var client_token := ""

func _ready() -> void:
	client_token = _load_or_create_token()
	_ws.connect_to_url(WS_URL)

## _load_or_create_token 读取持久化的客户端身份；首次运行生成一个 UUID 并落盘。
## 服务端把它当会话 UID，重连时据此找回原来的对局实例。
func _load_or_create_token() -> String:
	if FileAccess.file_exists(TOKEN_PATH):
		var f := FileAccess.open(TOKEN_PATH, FileAccess.READ)
		if f != null:
			var t := f.get_as_text().strip_edges()
			if t != "":
				return t
	var t := _uuid4()
	var f := FileAccess.open(TOKEN_PATH, FileAccess.WRITE)
	if f != null:
		f.store_string(t)
	return t

## _uuid4 生成一个 RFC 4122 v4 UUID 字符串。
func _uuid4() -> String:
	var b := PackedByteArray()
	for i in 16:
		b.append(randi() & 0xFF)
	b[6] = (b[6] & 0x0F) | 0x40
	b[8] = (b[8] & 0x3F) | 0x80
	var hex := b.hex_encode()
	return "%s-%s-%s-%s-%s" % [
		hex.substr(0, 8), hex.substr(8, 4), hex.substr(12, 4),
		hex.substr(16, 4), hex.substr(20, 12),
	]

func _process(_delta: float) -> void:
	var now := Time.get_ticks_msec() / 1000.0
	_ws.poll()
	match _ws.get_ready_state():
		WebSocketPeer.STATE_OPEN:
			if not connected:
				connected = true
				_last_recv = now
				_handshaken = false
				_send_frame(TYPE_HANDSHAKE, JSON.stringify({
					"sys": {"platform": "godot", "libVersion": "0.1.0", "clientVersion": "0.1.0"},
				}).to_utf8_buffer())
				connection_changed.emit(true)
			while _ws.get_available_packet_count() > 0:
				_handle_frame(_ws.get_packet())
				_last_recv = now
			if _handshaken and now - _last_beat >= HEARTBEAT_EVERY:
				_last_beat = now
				_send_frame(TYPE_HEARTBEAT, PackedByteArray())
			# 接收看门狗只在匹配后（有 20Hz 快照流）生效：匹配等待期间没有快照，
			# 2.5s 无数据是正常的（单人兜底要等 10s）。
			if _matched and now - _last_recv > RECV_TIMEOUT:
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
	_handshaken = false
	_matched = false
	_ws.connect_to_url(WS_URL)

# ---- 上行：业务接口（main.gd 调用） ----

## JoinMsg：token = 字段 1（string）。
func send_match_join() -> void:
	var payload := _tag_len(1, client_token.to_utf8_buffer())
	_send_notify("match.match.join", payload)

## CommandMsg：把一帧的上行命令合并成一条消息发送（帧是最小发送单位）。
func send_command(move: Vector2, yaw: float, jump: bool, shoot: bool,
		origin: Vector3, dir: Vector3, reset: bool) -> void:
	var msg := _packed_floats(1, [move.x, move.y])
	msg.append_array(_field_fixed32(2, yaw))
	if jump:
		msg.append_array(_field_varint(3, 1))
	if shoot:
		msg.append_array(_field_varint(4, 1))
		msg.append_array(_packed_floats(5, [origin.x, origin.y, origin.z]))
		msg.append_array(_packed_floats(6, [dir.x, dir.y, dir.z]))
	if reset:
		msg.append_array(_field_varint(7, 1))
	_send_notify("game.game.cmd", msg)

## 请求服务端下一帧下发全量（full 帧自带 schema）。收到 full 之前忽略一切增量。
func send_resync() -> void:
	_send_notify("game.game.resync", PackedByteArray())

# ---- protobuf 下行解码（服务端 → 客户端） ----

## Frame 解码：{step, full, schema:{fields:[{id,name,kind}], version}, entities:[...]}。
## 每条 EntityDelta 是 {id, destroy, removed:[], set:[{id, f:[], i, b, s}]}，
## 原样交给 WorldStore.apply_frame 解释 —— 协议层不理解属性语义。
func _decode_frame(buf: PackedByteArray) -> Dictionary:
	var d := {"step": 0, "full": false, "schema": {"fields": [], "version": 0}, "entities": []}
	var i := 0
	while i < buf.size():
		var tag: Array = _read_varint(buf, i)
		i = int(tag[1])
		var field: int = int(tag[0]) >> 3
		var wire: int = int(tag[0]) & 0x07
		match wire:
			WIRE_VARINT:
				var r: Array = _read_varint(buf, i)
				i = int(r[1])
				match field:
					1: d["step"] = int(r[0])
					2: d["full"] = int(r[0]) != 0
			WIRE_LEN:
				var rl: Array = _read_varint(buf, i)
				i = int(rl[1])
				var n: int = int(rl[0])
				var sub: PackedByteArray = buf.slice(i, i + n)
				i += n
				match field:
					3: d["entities"].append(_decode_entity_delta(sub))
					4: d["schema"] = _decode_schema(sub)
			_:
				break
	return d

## Schema：fields（字段 1，repeated SchemaField）+ version（字段 2，varint）。
func _decode_schema(buf: PackedByteArray) -> Dictionary:
	var d := {"fields": [], "version": 0}
	var i := 0
	while i < buf.size():
		var tag: Array = _read_varint(buf, i)
		i = int(tag[1])
		var field: int = int(tag[0]) >> 3
		var wire: int = int(tag[0]) & 0x07
		match wire:
			WIRE_VARINT:
				var r: Array = _read_varint(buf, i)
				i = int(r[1])
				if field == 2:
					d["version"] = int(r[0])
			WIRE_LEN:
				var rl: Array = _read_varint(buf, i)
				i = int(rl[1])
				var n: int = int(rl[0])
				var sub: PackedByteArray = buf.slice(i, i + n)
				i += n
				if field == 1:
					d["fields"].append(_decode_schema_field(sub))
			_:
				break
	return d

## SchemaField：id（1，varint）/ name（2，string）/ kind（3，varint）。
func _decode_schema_field(buf: PackedByteArray) -> Dictionary:
	var d := {"id": 0, "name": "", "kind": 0}
	var i := 0
	while i < buf.size():
		var tag: Array = _read_varint(buf, i)
		i = int(tag[1])
		var field: int = int(tag[0]) >> 3
		var wire: int = int(tag[0]) & 0x07
		match wire:
			WIRE_VARINT:
				var r: Array = _read_varint(buf, i)
				i = int(r[1])
				match field:
					1: d["id"] = int(r[0])
					3: d["kind"] = int(r[0])
			WIRE_LEN:
				var rl: Array = _read_varint(buf, i)
				i = int(rl[1])
				var n: int = int(rl[0])
				var sub: PackedByteArray = buf.slice(i, i + n)
				i += n
				if field == 2:
					d["name"] = sub.get_string_from_utf8()
			_:
				break
	return d

## AttrValue：id（1）/ f（2，packed floats）/ i（3，varint）/ b（4，varint）/ s（5，string）。
func _decode_attr_value(buf: PackedByteArray) -> Dictionary:
	var d := {"id": 0, "f": [], "i": 0, "b": false, "s": ""}
	var i := 0
	while i < buf.size():
		var tag: Array = _read_varint(buf, i)
		i = int(tag[1])
		var field: int = int(tag[0]) >> 3
		var wire: int = int(tag[0]) & 0x07
		match wire:
			WIRE_VARINT:
				var r: Array = _read_varint(buf, i)
				i = int(r[1])
				match field:
					1: d["id"] = int(r[0])
					3: d["i"] = int(r[0])
					4: d["b"] = int(r[0]) != 0
			WIRE_FIXED32:
				if field == 2:
					d["f"].append(buf.slice(i, i + 4).to_float32_array()[0])
				i += 4
			WIRE_LEN:
				var rl: Array = _read_varint(buf, i)
				i = int(rl[1])
				var n: int = int(rl[0])
				var sub: PackedByteArray = buf.slice(i, i + n)
				i += n
				match field:
					2: d["f"].append_array(_decode_floats(sub))
					5: d["s"] = sub.get_string_from_utf8()
			_:
				break
	return d

## EntityDelta：id（1）/ destroy（2）/ removed（3，packed varint）/ set（4，repeated AttrValue）。
func _decode_entity_delta(buf: PackedByteArray) -> Dictionary:
	var d := {"id": 0, "destroy": false, "removed": [], "set": []}
	var i := 0
	while i < buf.size():
		var tag: Array = _read_varint(buf, i)
		i = int(tag[1])
		var field: int = int(tag[0]) >> 3
		var wire: int = int(tag[0]) & 0x07
		match wire:
			WIRE_VARINT:
				var r: Array = _read_varint(buf, i)
				i = int(r[1])
				match field:
					1: d["id"] = int(r[0])
					2: d["destroy"] = int(r[0]) != 0
					3: d["removed"].append(int(r[0]))
			WIRE_LEN:
				var rl: Array = _read_varint(buf, i)
				i = int(rl[1])
				var n: int = int(rl[0])
				var sub: PackedByteArray = buf.slice(i, i + n)
				i += n
				match field:
					3: d["removed"].append_array(_decode_varints(sub))
					4: d["set"].append(_decode_attr_value(sub))
			_:
				break
	return d

## packed repeated varint（proto3 对 repeated uint32 的默认编码）。
func _decode_varints(bytes: PackedByteArray) -> Array:
	var out: Array = []
	var i := 0
	while i < bytes.size():
		var r: Array = _read_varint(bytes, i)
		i = int(r[1])
		out.append(int(r[0]))
	return out

## MatchResult 解码：match_id（字段1 string）/ game_server_id（字段2 string）/
## player_idx（字段3 varint）。
func _decode_match_result(buf: PackedByteArray) -> Dictionary:
	var d := {"match_id": "", "game_server_id": "", "player_idx": 0}
	var i := 0
	while i < buf.size():
		var tag: Array = _read_varint(buf, i)
		i = int(tag[1])
		var field: int = int(tag[0]) >> 3
		var wire: int = int(tag[0]) & 0x07
		match wire:
			WIRE_VARINT:
				var r: Array = _read_varint(buf, i)
				i = int(r[1])
				if field == 3:
					d["player_idx"] = int(r[0])
			WIRE_LEN:
				var rl: Array = _read_varint(buf, i)
				i = int(rl[1])
				var n: int = int(rl[0])
				var s := buf.slice(i, i + n).get_string_from_utf8()
				i += n
				match field:
					1: d["match_id"] = s
					2: d["game_server_id"] = s
			_:
				break
	return d

# ---- protobuf 底层原语 ----

## 无符号 varint 解码，返回 [值, 下一个字节下标]。
func _read_varint(buf: PackedByteArray, start: int) -> Array:
	var result := 0
	var shift := 0
	var i := start
	while true:
		var b := buf[i]
		i += 1
		result |= (b & 0x7F) << shift
		if (b & 0x80) == 0:
			break
		shift += 7
	return [result, i]

## 无符号 varint 编码（值均为非负：字段号、长度、id、bool）。
func _varint(v: int) -> PackedByteArray:
	var out := PackedByteArray()
	var x := v
	while true:
		var b := x & 0x7F
		x >>= 7
		if x != 0:
			out.append(b | 0x80)
		else:
			out.append(b)
			break
	return out

## 字段 tag：field<<3 | wire，再 varint。
func _field_varint(field: int, value: int) -> PackedByteArray:
	var out := _varint((field << 3) | WIRE_VARINT)
	out.append_array(_varint(value))
	return out

## 单精度 fixed32 字段（proto float）：字段 tag(FIXED32) + 4 字节小端 float32。
func _field_fixed32(field: int, value: float) -> PackedByteArray:
	var out := _varint((field << 3) | WIRE_FIXED32)
	var f32 := PackedFloat32Array([value])
	out.append_array(f32.to_byte_array())
	return out

## packed repeated float：字段 tag(LEN) + varint 长度 + N×4 字节小端 float32。
func _packed_floats(field: int, floats: Array) -> PackedByteArray:
	var f32 := PackedFloat32Array()
	for f in floats:
		f32.append(float(f))
	var payload := f32.to_byte_array()
	var out := _varint((field << 3) | WIRE_LEN)
	out.append_array(_varint(payload.size()))
	out.append_array(payload)
	return out

## 把一段 packed float 字节还原成普通 Array（main.gd 要求 pos/quat/size 是 Array）。
func _decode_floats(bytes: PackedByteArray) -> Array:
	var f32 := bytes.to_float32_array()
	var out := []
	for f in f32:
		out.append(f)
	return out

## 一个 LEN 型字段（string / bytes / 嵌入消息）：tag + varint 长度 + 内容。
func _tag_len(field: int, payload: PackedByteArray) -> PackedByteArray:
	var out := _varint((field << 3) | WIRE_LEN)
	out.append_array(_varint(payload.size()))
	out.append_array(payload)
	return out

# ---- 帧 / 消息编解码 ----

## 拼一帧：1 字节 type + 3 字节大端长度 + data。
func _send_frame(typ: int, data: PackedByteArray) -> void:
	var out := PackedByteArray()
	out.resize(4)
	out[0] = typ
	var n := data.size()
	out[1] = (n >> 16) & 0xFF
	out[2] = (n >> 8) & 0xFF
	out[3] = n & 0xFF
	out.append_array(data)
	_ws.send(out)

## 发一条 Notify 消息：flag=0x02，1 字节 route 长度 + route + protobuf payload。
func _send_notify(route: String, payload: PackedByteArray) -> void:
	if not (connected and _handshaken):
		return
	var msg := PackedByteArray()
	msg.append(MSG_NOTIFY << 1)
	var rb := route.to_utf8_buffer()
	msg.append(rb.size())
	msg.append_array(rb)
	msg.append_array(payload)
	_send_frame(TYPE_DATA, msg)

## 处理一帧（每个 WebSocket message 恰好是一帧，无需缓冲拆包）。
func _handle_frame(pkt: PackedByteArray) -> void:
	if pkt.size() < 4:
		return
	var typ := pkt[0]
	var n := (pkt[1] << 16) | (pkt[2] << 8) | pkt[3]
	var data := pkt.slice(4)
	if data.size() != n:
		return

	match typ:
		TYPE_HANDSHAKE:
			_on_handshake(data)
		TYPE_HEARTBEAT:
			pass  # 服务端保活帧，忽略
		TYPE_DATA:
			_on_data(data)

func _on_handshake(data: PackedByteArray) -> void:
	var obj: Variant = JSON.parse_string(data.get_string_from_utf8())
	if not (obj is Dictionary):
		return
	_handshaken = true
	_last_beat = Time.get_ticks_msec() / 1000.0
	# HandshakeAck 带 sys/user 空信息即可，服务端只把它置为 Working 状态。
	_send_frame(TYPE_HANDSHAKE_ACK, JSON.stringify({
		"sys": {}, "user": {},
	}).to_utf8_buffer())
	# 进入匹配队列：match 服务配对后推 onMatched。必须带持久化 token —— 服务端把它
	# 当会话 UID，重连时才能走 game.rejoin 找回原来的对局实例。
	send_match_join()

## 解析 Data 帧内的 message：flag 得类型，Push 读 route + protobuf payload。
func _on_data(data: PackedByteArray) -> void:
	if data.size() < 2:
		return
	var flag := data[0]
	var mtype := (flag >> 1) & 0x07
	if mtype != MSG_PUSH:
		return  # 本客户端不期待 response，忽略其它
	var rl := data[1]
	if data.size() < 2 + rl:
		return
	var route := data.slice(2, 2 + rl).get_string_from_utf8()
	var payload := data.slice(2 + rl)
	match route:
		"onMatched":
			_matched = true
			matched_received.emit(_decode_match_result(payload))
		"onFrame":
			frame_received.emit(_decode_frame(payload))

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
##     有本地凭证则发 account.resume，否则等 UI 登录/注册；认证成功后才发
##     match.join 进入匹配，收到 onMatched 后进入对局
##   - 心跳：按固定间隔发 Heartbeat 空帧，防止服务端超时踢人
##
## 活性保障：服务端每 tick（50 ms）都推送同步帧，因此用"N 秒收不到任何数据"作为
## 接收看门狗——半开连接（对端崩溃不发 FIN、路由丢包、休眠唤醒）不会让
## WebSocketPeer 进入 CLOSED，此时强制重建连接，避免画面永久冻结。
## 匹配等待期没有帧流，接收看门狗不生效，另有一个「迟迟收不到 onMatched 就重发
## match.join」的看门狗兜底（见 MATCH_RETRY_SECS）。

signal frame_received(frame: Dictionary)   # 服务端推送的同步帧（增量或全量）
signal matched_received(result: Dictionary)
signal connection_changed(connected: bool)
signal login_result(result: Dictionary)   # LoginReply：{ok, token, username, account_id, reason}

const WS_URL := "ws://localhost:8080/"
const RETRY_SECS := 1.0
const RECV_TIMEOUT := 2.5    # 秒：20 Hz 推送下正常间隔 ~50ms，远小于此阈值
const HEARTBEAT_EVERY := 10.0 # 秒：服务端心跳超时 30s（2 倍才踢），10s 足够安全
const LOGIN_TIMEOUT := 5.0   # 登录类请求的响应超时（秒）
# 匹配等待期的重发间隔（秒）。**必须大于服务端的 10 s 单人兜底超时**（match/match.go
# 的 `timeout`）：重发会刷新排队者在 ZSET 里的入队时间戳，间隔 ≤10 s 的话每次重发都把
# 等待时钟归零，服务端的 PopStale 永远取不到「已等超过 10 s」的人，单人兜底就再也不会
# 触发 —— 一个人玩时反而彻底卡死。别为了「快点重试」把它调小。
const MATCH_RETRY_SECS := 15.0

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

const TOKEN_PATH := "user://auth_token.txt"      # 服务端签发的会话凭证
const USERNAME_PATH := "user://last_username.txt" # 上次登录的用户名，用于预填

var _ws := WebSocketPeer.new()
var _retry_at := 0.0
var _last_recv := 0.0    # 最近一次收到数据的时间戳（秒）
var _last_beat := 0.0    # 最近一次发心跳的时间戳（秒）
var _handshaken := false # 是否已完成握手（连接后置 false，收到握手响应后置 true）
var _matched := false    # 是否已匹配进入对局（匹配前无帧流，看门狗不生效）
# 匹配等待看门狗的到期时刻（秒；0 = 未武装）。武装 = 已发出 match.join 但还没收到
# onMatched。用一个标量而不是 Timer 节点：赋值天然幂等，反复武装也只有一份状态，
# 不可能出现「两个计时器同时在跑」。
var _match_retry_at := 0.0
var connected := false
var client_token := ""
var last_username := ""
var _pending := {}    # mid -> {"route": String, "at": float}，用于响应关联与超时
var _next_mid := 1

func _ready() -> void:
	client_token = _load_token()
	last_username = _load_username()
	_ws.connect_to_url(WS_URL)

## _load_token 读本地持久化的凭证。空串表示没登录过（要显示登录面板）。
##
## 不复用旧的 client_id.txt：那里面是客户端自己生成的 UUID，服务端不认，
## 拿它去 resume 必然失败 —— 不如不认，直接走登录面板。
func _load_token() -> String:
	if not FileAccess.file_exists(TOKEN_PATH):
		return ""
	var f := FileAccess.open(TOKEN_PATH, FileAccess.READ)
	if f == null:
		return ""
	return f.get_as_text().strip_edges()

## _save_token 落盘凭证与用户名。写失败不致命（下次仍要重新登录）。
func _save_token(token: String, username: String) -> void:
	if token != "":
		client_token = token
		var f := FileAccess.open(TOKEN_PATH, FileAccess.WRITE)
		if f != null:
			f.store_string(token)
	if username != "":
		last_username = username
		var g := FileAccess.open(USERNAME_PATH, FileAccess.WRITE)
		if g != null:
			g.store_string(username)

## _clear_token 丢弃已失效的本地凭证。
##
## 服务端明确说 token 无效（过期 / 被顶号 / 已吊销）之后必须清掉：留着的话每次
## 重连、每次重启都会拿同一个死凭证再试一次 —— 界面上的错误提示被反复清空、
## 密码框的焦点被反复抢走，还白烧限流额度。
##
## 只在 token_invalid 时调用。timeout / no_connection / internal / rate_limited
## 都不能清：那些情况下凭证很可能仍然是好的，清掉等于平白把人踢回登录面板。
func _clear_token() -> void:
	client_token = ""
	if FileAccess.file_exists(TOKEN_PATH):
		# 删不掉也没关系（只读、被占用……）：真正起作用的是上面清空的
		# client_token —— 本次运行不会再发它，下次启动最坏是多试一次 resume。
		DirAccess.remove_absolute(ProjectSettings.globalize_path(TOKEN_PATH))

## _load_username 读上次登录的用户名（预填输入框用）。
func _load_username() -> String:
	if not FileAccess.file_exists(USERNAME_PATH):
		return ""
	var f := FileAccess.open(USERNAME_PATH, FileAccess.READ)
	if f == null:
		return ""
	return f.get_as_text().strip_edges()

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
			# 登录类请求超时：响应丢了不能一直转圈，退回登录面板。
			for mid in _pending.keys():
				if now - float(_pending[mid]["at"]) > LOGIN_TIMEOUT:
					_pending.erase(mid)
					login_result.emit({"ok": false, "reason": "timeout"})
			# 接收看门狗只在匹配后（有 20Hz 帧流）生效：匹配等待期间没有帧，
			# 2.5s 无数据是正常的（单人兜底要等 10s）。
			if _matched and now - _last_recv > RECV_TIMEOUT:
				_force_reconnect()
			# 匹配等待看门狗：迟迟收不到 onMatched 就重发 match.join。
			#
			# 服务端有两条路径会把「已经出队」的玩家丢掉：入队本身失败、以及 pushMatched
			# 失败（两处都只打日志，见 match/match.go）。Redis 抖一下或一次 NATS 推送丢
			# 了，玩家就悬在一个自己看不见的对局里，游戏实例 60 s 后空闲回收 —— 而客户端
			# 这边**什么都不会超时**（接收看门狗被 _matched 挡着），只能重启客户端。
			#
			# 重发是安全的：服务端的 tryRejoin 让 match.join 幂等（已在局中的人会被直接
			# 送回原局），而重复 Enqueue 在 ZSET 上只是刷新等待时间。
			if _match_retry_at > 0.0 and not _matched and now >= _match_retry_at:
				send_match_join()   # 内部重新武装，不会叠加出第二个计时器
		WebSocketPeer.STATE_CLOSED:
			# 连接没了就撤掉匹配看门狗：重连后要等 resume/登录成功才重新发 join，
			# 这里不撤的话旧计时器会在新连接上抢跑一条 match.join（那时会话还没绑定，
			# 服务端只会忽略它），而真正的 join 反倒不再武装看门狗。
			_match_retry_at = 0.0
			if connected:
				connected = false
				connection_changed.emit(false)
				_pending = {}
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
	_match_retry_at = 0.0
	_pending = {}
	_ws.connect_to_url(WS_URL)

# ---- 上行：业务接口（main.gd 调用） ----

## JoinMsg：token = 字段 1（string）。
## send_match_join 进入匹配队列。
##
## JoinMsg 现在是**空消息**：身份来自会话绑定（account 服务在登录成功时做的），
## 客户端不再往线上放任何凭证。以前这里会把 token 当字段 1 发出去 —— 服务端会
## 忽略它（未知字段），但那是每次匹配都重发一次有效凭证，且与 proto 定义矛盾。
##
## 发送即武装匹配等待看门狗（见 _process 里的重发分支）。武装点收敛在这一个函数里，
## 调用方（main.gd 的登录成功回调、看门狗自己的重发）都不需要各自记时间。
func send_match_join() -> void:
	_send_notify("match.match.join", PackedByteArray())
	_match_retry_at = Time.get_ticks_msec() / 1000.0 + MATCH_RETRY_SECS

## send_register 注册新账号；结果经 login_result 信号回来。
func send_register(username: String, password: String) -> void:
	_send_login_request("account.account.register", username, password)

## send_login 用已有账号登录。
func send_login(username: String, password: String) -> void:
	_send_login_request("account.account.login", username, password)

func _send_login_request(route: String, username: String, password: String) -> void:
	var payload := PackedByteArray()
	payload.append_array(_tag_len(1, username.to_utf8_buffer()))
	payload.append_array(_tag_len(2, password.to_utf8_buffer()))
	_send_tracked(route, payload)

## send_resume 用本地凭证换回会话。
func send_resume() -> void:
	_send_tracked("account.account.resume", _tag_len(1, client_token.to_utf8_buffer()))

## _send_tracked 发一条需要关联响应的请求（登记 mid 以便超时与配对）。
func _send_tracked(route: String, payload: PackedByteArray) -> void:
	var mid := _next_mid
	_next_mid += 1
	if _next_mid > 0xFFFFFF:
		_next_mid = 1
	if _send_request(mid, route, payload):
		_pending[mid] = {"route": route, "at": Time.get_ticks_msec() / 1000.0}
	else:
		# 连接还没就绪：立刻当作失败，让 UI 退回登录面板而不是干等超时。
		login_result.emit({"ok": false, "reason": "no_connection"})

## CommandMsg：把一帧的上行命令合并成一条消息发送（帧是最小发送单位）。
## 编码拆成 _encode_command 是为了能脱离 WebSocket 单测字段号 —— `reset` 在服务端
## 生成码里叫 Reset_（与生成方法重名），线上字段号仍是 7，是最容易写错的一处。
func send_command(move: Vector2, yaw: float, jump: bool, shoot: bool,
		origin: Vector3, dir: Vector3, reset: bool) -> void:
	_send_notify("game.game.cmd", _encode_command(move, yaw, jump, shoot, origin, dir, reset))

## _encode_command 生成 CommandMsg 的 protobuf 载荷。
## 字段号取自 game/protos/game.proto：move=1 yaw=2 jump=3 shoot=4 origin=5 dir=6 reset=7。
func _encode_command(move: Vector2, yaw: float, jump: bool, shoot: bool,
		origin: Vector3, dir: Vector3, reset: bool) -> PackedByteArray:
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
	return msg

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
## 循环以 `i < buf.size()` 为界：畸形/截断的输入（末字节仍带续位 0x80）只会提前收
## 尾并返回 i == buf.size()，不会越界读。调用方若在意完整性，自行检查末字节的续位。
func _read_varint(buf: PackedByteArray, start: int) -> Array:
	var result := 0
	var shift := 0
	var i := start
	while i < buf.size():
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

## 发一条 Request 消息：flag=0x00(MSG_REQUEST<<1)，mid（LEB128 变长）
## + route 长度 + route + protobuf payload。
##
## 与 Notify 的唯一区别是多了 mid：服务端按 mid 回 Response 帧。
## 登录必须走 Request —— NATS 模式下会话未绑定时服务端 push 不了，
## 「登录失败」这个结果没有别的路能回来。
func _send_request(mid: int, route: String, payload: PackedByteArray) -> bool:
	if not (connected and _handshaken):
		return false
	var msg := PackedByteArray()
	msg.append(MSG_REQUEST << 1)
	msg.append_array(_varint(mid))
	var rb := route.to_utf8_buffer()
	msg.append(rb.size())
	msg.append_array(rb)
	msg.append_array(payload)
	_send_frame(TYPE_DATA, msg)
	return true

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
	# 有本地凭证就先试 resume（用户无感，重连回同一局的体验与之前一致）；
	# 没有就交给 UI 显示登录面板。
	#
	# 关键：没有凭证时**绝不**发 match.join —— 会话未绑定时服务端会忽略它，
	# 客户端会卡在「正在匹配…」而无从排查。
	if client_token != "":
		send_resume()
	else:
		login_result.emit({"ok": false, "reason": "no_token"})

## 解析 Data 帧内的 message：flag 低 3 位得类型（Push / Response 两种），
## 高位 0x20 是 pitaya 的错误标记。
func _on_data(data: PackedByteArray) -> void:
	if data.size() < 2:
		return
	var flag := data[0]
	var mtype := (flag >> 1) & 0x07
	var is_err := (flag & 0x20) != 0
	match mtype:
		MSG_PUSH:
			_on_push(data)
		MSG_RESPONSE:
			_on_response(data, is_err)
		_:
			pass  # 本客户端只发 Notify/Request，其它类型忽略

## Push 帧：route 长度 + route + payload。
func _on_push(data: PackedByteArray) -> void:
	var rl := data[1]
	if data.size() < 2 + rl:
		return
	var route := data.slice(2, 2 + rl).get_string_from_utf8()
	var payload := data.slice(2 + rl)
	match route:
		"onMatched":
			_matched = true
			_match_retry_at = 0.0   # 匹配到了，撤掉等待看门狗
			matched_received.emit(_decode_match_result(payload))
		"onFrame":
			frame_received.emit(_decode_frame(payload))

## Response 帧：mid（LEB128 变长）+ payload —— **没有 route 字段**。
## is_err 为真时 payload 是 pitaya 的错误字符串，不是 LoginReply。
func _on_response(data: PackedByteArray, is_err: bool) -> void:
	if data.size() < 2:
		# 连 mid 的第一个字节都没有：不能读 data[1]。与 _on_push 的
		# `data.size() < 2 + rl` 守卫对称 —— 畸形帧宁可明确失败，也不能让
		# 上层一直等一个永远不来的响应。
		_fail_unreadable_response()
		return
	var r: Array = _read_varint(data, 1)
	var next: int = int(r[1])
	# _read_varint 到缓冲末尾就停。末字节仍带续位 0x80 说明 mid 的 LEB128 没读完，
	# 帧是截断的：mid 不可信、payload 也不完整，同样明确报失败而不是静默丢弃。
	if (data[next - 1] & 0x80) != 0:
		printerr("Response 帧被截断（mid 的 LEB128 不完整）")
		_fail_unreadable_response()
		return
	var mid: int = int(r[0])
	var payload: PackedByteArray = data.slice(next)
	_pending.erase(mid)
	if is_err:
		printerr("登录请求失败（服务端错误）: " + payload.get_string_from_utf8())
		login_result.emit({"ok": false, "reason": "internal"})
		return
	var reply := _decode_login_reply(payload)
	if bool(reply.get("ok", false)):
		# 登录/注册/resume 成功才落盘凭证：失败时服务端不发 token，
		# 写空串会把上一次的有效凭证也抹掉。
		_save_token(String(reply.get("token", "")), String(reply.get("username", "")))
	elif String(reply.get("reason", "")) == "token_invalid":
		# 服务端明确否掉了这个凭证：丢掉，别再拿它反复试（见 _clear_token）。
		_clear_token()
	login_result.emit(reply)

## _fail_unreadable_response 处理「mid 读不出来」的畸形 Response：报一次失败。
##
## 关键是先清空 _pending。mid 不可读，没法精确 erase 对应的那一条，留着它就会在
## LOGIN_TIMEOUT 之后再 emit 一条 timeout —— 用户眼睁睁看着「服务暂时不可用」
## 5 秒后自己变成「服务器无响应，请重试」。整表清空的粒度正好：在途的登录类请求
## 最多只有一条（UI 有 _login_busy 单飞守卫，resume 每次握手只发一次）。
func _fail_unreadable_response() -> void:
	_pending = {}
	login_result.emit({"ok": false, "reason": "internal"})

## LoginReply：ok=1(varint) token=2 username=3 account_id=4 reason=5（均为 string）。
func _decode_login_reply(buf: PackedByteArray) -> Dictionary:
	var d := {"ok": false, "token": "", "username": "", "account_id": "", "reason": ""}
	var i := 0
	while i < buf.size():
		var tag: Array = _read_varint(buf, i)
		i = int(tag[1])
		var field: int = int(tag[0]) >> 3
		var wire: int = int(tag[0]) & 0x07
		if wire == WIRE_VARINT:
			var r: Array = _read_varint(buf, i)
			i = int(r[1])
			if field == 1:
				d["ok"] = int(r[0]) != 0
		elif wire == WIRE_LEN:
			var rl: Array = _read_varint(buf, i)
			i = int(rl[1])
			var n: int = int(rl[0])
			var sub: PackedByteArray = buf.slice(i, i + n)
			i += n
			match field:
				2: d["token"] = sub.get_string_from_utf8()
				3: d["username"] = sub.get_string_from_utf8()
				4: d["account_id"] = sub.get_string_from_utf8()
				5: d["reason"] = sub.get_string_from_utf8()
		else:
			break
	return d

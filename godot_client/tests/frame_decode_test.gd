extends SceneTree
## Frame / Schema 的 protobuf 解码测试：用手工构造的字节串验证字段还原。

const FpsClient := preload("res://scripts/fps_client.gd")

var _failures := 0
var _c: Node
# 跑完的用例标记。GDScript 没有 try/catch：某个测试函数内部一旦抛错（比如解码器
# 挂掉），函数会中途返回、_failures 还是 0，整个用例就会"假绿"—— 所以每个函数
# 末尾打一个完成标记，_init 逐个核对。
var _done := {}

func _init() -> void:
	_c = FpsClient.new()
	_test_schema()
	_test_full_frame_with_schema()
	_test_delta_frame()
	_test_command_encoding()
	_test_profile_decode()
	_test_match_cancel_reply_decode()
	_test_match_status_decode()
	_test_match_ended_decode()
	for name: String in ["schema", "full", "delta", "command", "profile", "cancel", "status", "ended"]:
		if not _done.has(name):
			_failures += 1
			printerr("FAIL: 用例 %s 没跑完（中途抛错了？）" % name)
	if _failures > 0:
		printerr("frame_decode_test: %d 项失败" % _failures)
		quit(1)
	else:
		print("frame_decode_test: OK")
		quit(0)

func _check(cond: bool, msg: String) -> void:
	if not cond:
		_failures += 1
		printerr("FAIL: " + msg)

# ---- protobuf 编码辅助（测试里手写，与服务端生成码对齐） ----

func _tag(field: int, wire: int) -> PackedByteArray:
	return _c._varint((field << 3) | wire)

func _f_varint(field: int, v: int) -> PackedByteArray:
	var out := _tag(field, 0)
	out.append_array(_c._varint(v))
	return out

func _f_fixed32(field: int, v: float) -> PackedByteArray:
	var out := _tag(field, 5)
	out.append_array(PackedFloat32Array([v]).to_byte_array())
	return out

func _f_floats(field: int, vals: Array) -> PackedByteArray:
	var f32 := PackedFloat32Array()
	for v in vals:
		f32.append(float(v))
	var payload := f32.to_byte_array()
	var out := _tag(field, 2)
	out.append_array(_c._varint(payload.size()))
	out.append_array(payload)
	return out

func _f_bytes(field: int, payload: PackedByteArray) -> PackedByteArray:
	var out := _tag(field, 2)
	out.append_array(_c._varint(payload.size()))
	out.append_array(payload)
	return out

## packed repeated varint（proto3 对 repeated uint32 的默认编码）。
func _f_varints_packed(field: int, vals: Array) -> PackedByteArray:
	var payload := PackedByteArray()
	for v in vals:
		payload.append_array(_c._varint(int(v)))
	var out := _tag(field, 2)
	out.append_array(_c._varint(payload.size()))
	out.append_array(payload)
	return out

# ---- 用例 ----

func _schema_field(id: int, name: String, kind: int) -> PackedByteArray:
	var m := _f_varint(1, id)
	m.append_array(_f_bytes(2, name.to_utf8_buffer()))
	m.append_array(_f_varint(3, kind))
	return m

func _test_schema() -> void:
	var schema := PackedByteArray()
	schema.append_array(_f_bytes(1, _schema_field(1, "Pos", 5)))
	schema.append_array(_f_bytes(1, _schema_field(2, "Health", 0)))
	schema.append_array(_f_varint(2, 12345))

	var frame := _f_varint(1, 7)          # step
	frame.append_array(_f_varint(2, 1))   # full = true
	frame.append_array(_f_bytes(4, schema))

	var d: Dictionary = _c._decode_frame(frame)
	_check(int(d["step"]) == 7, "step 应解出 7，得到 %s" % d["step"])
	_check(bool(d["full"]) == true, "full 应为 true")
	var fields: Array = d["schema"]["fields"]
	_check(fields.size() == 2, "schema 应有 2 个字段，得到 %d" % fields.size())
	_check(String(fields[0]["name"]) == "Pos" and int(fields[0]["kind"]) == 5, "第一个字段应是 Pos/Vec3")
	_check(int(d["schema"]["version"]) == 12345, "schema version 应解出 12345")
	_done["schema"] = true

func _test_full_frame_with_schema() -> void:
	var av := _f_varint(1, 1)                       # id = 1 (Pos)
	av.append_array(_f_floats(2, [1.0, 2.0, 3.0]))  # f
	var av2 := _f_varint(1, 3)                      # id = 3 (Enemy)
	av2.append_array(_f_varint(4, 1))               # b = true

	var ed := _f_varint(1, 42)                      # entity id = 42
	ed.append_array(_f_bytes(4, av))                # set
	ed.append_array(_f_bytes(4, av2))

	var frame := _f_varint(1, 3)
	frame.append_array(_f_varint(2, 1))
	frame.append_array(_f_bytes(3, ed))

	var d: Dictionary = _c._decode_frame(frame)
	_check(d["entities"].size() == 1, "应有 1 个实体条目")
	var e: Dictionary = d["entities"][0]
	_check(int(e["id"]) == 42 and not bool(e["destroy"]), "实体 42 不应是 destroy")
	_check(e["set"].size() == 2, "应有 2 条 set")
	_check((e["set"][0]["f"] as Array).size() == 3, "Pos 应有 3 个分量")
	_check(float((e["set"][0]["f"] as Array)[2]) == 3.0, "Pos.z 应为 3")
	_check(bool(e["set"][1]["b"]) == true, "Enemy 应为 true")
	_done["full"] = true

func _test_delta_frame() -> void:
	var ed := _f_varint(1, 5)
	ed.append_array(_f_varint(2, 1))                # destroy
	ed.append_array(_f_varint(3, 2))                # removed: [2]

	var ed2 := _f_varint(1, 6)
	# 实体 6 用 **packed** 形式 —— 服务端 proto3 对 repeated uint32 默认就是 packed，
	# 上面实体 5 的非 packed 形式只是兼容分支，真正跑在线上的是这一条。
	ed2.append_array(_f_varints_packed(3, [4, 5]))  # removed: [4, 5]

	var frame := _f_varint(1, 9)
	frame.append_array(_f_bytes(3, ed))
	frame.append_array(_f_bytes(3, ed2))

	var d: Dictionary = _c._decode_frame(frame)
	_check(bool(d["full"]) == false, "默认应是增量帧")
	_check(bool(d["entities"][0]["destroy"]) == true, "实体 5 应是 destroy")
	_check((d["entities"][1]["removed"] as Array) == [4, 5], "实体 6 的 packed removed 应为 [4,5]")
	_done["delta"] = true

# 上行字段号必须与 CommandMsg 一致。字段 7（曾是 reset）已随场景重置一起删除并在
# proto 里 reserved，客户端不得再发它。
func _test_command_encoding() -> void:
	var buf: PackedByteArray = _c._encode_command(
		Vector2(1.0, 2.0), 0.5, true, true, Vector3(3, 4, 5), Vector3(0, 0, 1))

	var seen := {}
	var i := 0
	while i < buf.size():
		var t: Array = _c._read_varint(buf, i)
		i = int(t[1])
		var field: int = int(t[0]) >> 3
		var wire: int = int(t[0]) & 0x07
		seen[field] = wire
		match wire:
			_c.WIRE_VARINT:
				var r: Array = _c._read_varint(buf, i)
				i = int(r[1])
			_c.WIRE_FIXED32:
				i += 4
			_c.WIRE_LEN:
				var rl: Array = _c._read_varint(buf, i)
				i = int(rl[1]) + int(rl[0])
			_:
				break

	_check(seen.get(1, -1) == _c.WIRE_LEN, "move 应是字段 1（packed float）")
	_check(seen.get(2, -1) == _c.WIRE_FIXED32, "yaw 应是字段 2（fixed32）")
	_check(seen.get(3, -1) == _c.WIRE_VARINT, "jump 应是字段 3")
	_check(seen.get(4, -1) == _c.WIRE_VARINT, "shoot 应是字段 4")
	_check(seen.get(5, -1) == _c.WIRE_LEN, "origin 应是字段 5")
	_check(seen.get(6, -1) == _c.WIRE_LEN, "dir 应是字段 6")
	_check(not seen.has(7), "字段 7 已 reserved，客户端不得再发")
	_done["command"] = true

# ---- 新增：个人档案 / 取消匹配 / 匹配状态 / 本局结束 ----

func _test_profile_decode() -> void:
	var record := PackedByteArray()
	record.append_array(_f_bytes(1, "m1".to_utf8_buffer()))
	record.append_array(_f_varint(2, 1))          # won
	record.append_array(_f_varint(3, 10))         # kills
	record.append_array(_f_varint(4, 7))          # deaths
	record.append_array(_f_varint(5, 7))          # opponent_kills
	record.append_array(_f_varint(6, 84))         # duration_seconds
	record.append_array(_f_bytes(7, "bob".to_utf8_buffer()))
	record.append_array(_f_varint(8, 1737000000)) # ended_at

	var buf := PackedByteArray()
	buf.append_array(_f_varint(1, 1))    # ok
	buf.append_array(_f_varint(3, 3))    # level
	buf.append_array(_f_varint(4, 420))  # xp
	buf.append_array(_f_varint(5, 20))   # xp_into_level
	buf.append_array(_f_varint(6, 200))  # xp_for_next_level
	buf.append_array(_f_varint(7, 30))   # kills
	buf.append_array(_f_varint(8, 12))   # deaths
	buf.append_array(_f_varint(9, 4))    # matches
	buf.append_array(_f_varint(10, 3))   # wins
	buf.append_array(_f_varint(11, 1))   # losses
	buf.append_array(_f_bytes(12, record))

	var d: Dictionary = _c._decode_profile(buf)
	_check(bool(d.get("ok", false)), "profile 应解码为 ok=true")
	_check(int(d.get("level", 0)) == 3, "level 应是 3")
	_check(int(d.get("xp", 0)) == 420, "xp 应是 420")
	_check(int(d.get("xp_into_level", 0)) == 20, "xp_into_level 应是 20")
	_check(int(d.get("xp_for_next_level", 0)) == 200, "xp_for_next_level 应是 200")
	_check(int(d.get("kills", 0)) == 30 && int(d.get("deaths", 0)) == 12, "累计 k/d 应解出")
	_check(int(d.get("matches", 0)) == 4, "场次应是 4")
	_check(int(d.get("wins", 0)) == 3 && int(d.get("losses", 0)) == 1, "胜负场次应解出")
	var recent: Array = d.get("recent_matches", [])
	_check(recent.size() == 1, "应解出 1 条历史")
	if recent.size() == 1:
		_check(String(recent[0]["match_id"]) == "m1", "历史 match_id 应是 m1")
		_check(bool(recent[0]["won"]), "历史 won 应是 true")
		_check(int(recent[0]["kills"]) == 10 && int(recent[0]["deaths"]) == 7, "历史 k/d 应是 10/7")
		_check(int(recent[0]["opponent_kills"]) == 7, "历史 opponent_kills 应是 7")
		_check(int(recent[0]["duration_seconds"]) == 84, "历史时长应是 84")
		_check(String(recent[0]["opponent_name"]) == "bob", "历史 opponent_name 应是 bob")
		_check(int(recent[0]["ended_at"]) == 1737000000, "历史 ended_at 应是 1737000000")
	_check(bool(d.get("_malformed", false)) == false, "合法载荷不应判为畸形")
	_done["profile"] = true

func _test_match_cancel_reply_decode() -> void:
	var buf := PackedByteArray()
	buf.append_array(_f_bytes(2, "not_queued".to_utf8_buffer()))
	var d: Dictionary = _c._decode_match_cancel_reply(buf)
	_check(bool(d.get("ok", true)) == false, "缺省 ok 应是 false")
	_check(String(d.get("reason", "")) == "not_queued", "reason 应解出")

	var ok_buf := PackedByteArray()
	ok_buf.append_array(_f_varint(1, 1))
	ok_buf.append_array(_f_bytes(2, "cancelled".to_utf8_buffer()))
	var d2: Dictionary = _c._decode_match_cancel_reply(ok_buf)
	_check(bool(d2.get("ok", false)), "ok=1 应解成 true")
	_check(String(d2.get("reason", "")) == "cancelled", "reason 应是 cancelled")
	_done["cancel"] = true

func _test_match_status_decode() -> void:
	var buf := PackedByteArray()
	buf.append_array(_f_varint(1, 3))   # queued_players
	buf.append_array(_f_varint(2, 12))  # waited_seconds
	var d: Dictionary = _c._decode_match_status(buf)
	_check(int(d.get("queued_players", 0)) == 3, "queued_players 应是 3")
	_check(int(d.get("waited_seconds", 0)) == 12, "waited_seconds 应是 12")
	_done["status"] = true

func _test_match_ended_decode() -> void:
	var slot0 := PackedByteArray()
	slot0.append_array(_f_bytes(1, "7".to_utf8_buffer()))
	slot0.append_array(_f_varint(2, 3))
	slot0.append_array(_f_varint(3, 10))
	var slot1 := PackedByteArray()
	slot1.append_array(_f_bytes(1, "8".to_utf8_buffer()))
	slot1.append_array(_f_varint(2, 10))
	slot1.append_array(_f_varint(3, 3))

	var buf := PackedByteArray()
	buf.append_array(_f_bytes(1, "m1".to_utf8_buffer()))
	buf.append_array(_f_varint(2, 1))
	buf.append_array(_f_bytes(3, slot0))
	buf.append_array(_f_bytes(3, slot1))
	buf.append_array(_f_varint(4, 84))

	var d: Dictionary = _c._decode_match_ended(buf)
	_check(String(d.get("match_id", "")) == "m1", "match_id 应是 m1")
	_check(int(d.get("winner_slot", -1)) == 1, "winner_slot 应是 1")
	_check(int(d.get("duration_seconds", 0)) == 84, "duration_seconds 应是 84")
	var slots: Array = d.get("slots", [])
	_check(slots.size() == 2, "应解出 2 个槽位")
	if slots.size() == 2:
		_check(String(slots[0]["uid"]) == "7" && int(slots[0]["kills"]) == 3, "槽位 0 数据不对")
		_check(int(slots[0]["deaths"]) == 10, "槽位 0 的 deaths 应是 10")
		_check(String(slots[1]["uid"]) == "8" && int(slots[1]["deaths"]) == 3, "槽位 1 数据不对")
	_done["ended"] = true

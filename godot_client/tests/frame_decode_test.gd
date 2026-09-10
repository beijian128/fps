extends SceneTree
## Frame / Schema 的 protobuf 解码测试：用手工构造的字节串验证字段还原。

const FpsClient := preload("res://scripts/fps_client.gd")

var _failures := 0
var _c: Node

func _init() -> void:
	_c = FpsClient.new()
	_test_schema()
	_test_full_frame_with_schema()
	_test_delta_frame()
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

func _test_delta_frame() -> void:
	var ed := _f_varint(1, 5)
	ed.append_array(_f_varint(2, 1))                # destroy
	ed.append_array(_f_varint(3, 2))                # removed: [2]

	var ed2 := _f_varint(1, 6)
	ed2.append_array(_f_varint(3, 4))               # removed: [4]
	ed2.append_array(_f_varint(3, 5))               # removed: [4, 5]

	var frame := _f_varint(1, 9)
	frame.append_array(_f_bytes(3, ed))
	frame.append_array(_f_bytes(3, ed2))

	var d: Dictionary = _c._decode_frame(frame)
	_check(bool(d["full"]) == false, "默认应是增量帧")
	_check(bool(d["entities"][0]["destroy"]) == true, "实体 5 应是 destroy")
	_check((d["entities"][1]["removed"] as Array) == [4, 5], "实体 6 的 removed 应为 [4,5]")

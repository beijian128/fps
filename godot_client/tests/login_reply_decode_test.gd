extends SceneTree
## Response 帧与 LoginReply 的解码测试。
##
## 这是本项目第一次走 pitaya 的 Request/Response 路径：此前只发 Notify、只收
## Push。契约与 Push 不同 —— Response 帧是 flag + mid(LEB128) + payload，
## 没有 route 字段。这里用手工构造的字节串把两种帧都钉住。

const FpsClient := preload("res://scripts/fps_client.gd")

var _failures := 0
var _c: Node
# GDScript 没有 try/catch：某个用例内部抛错会让函数中途返回、_failures 还是 0，
# 整个用例就"假绿"了。所以每个函数末尾打完成标记，_init 逐个核对。
var _done := {}

func _init() -> void:
	_c = FpsClient.new()
	_test_login_reply_ok()
	_test_login_reply_failure()
	_test_login_reply_empty()
	_test_response_frame_layout()
	_test_error_mask_flag()
	for name: String in ["reply_ok", "reply_fail", "reply_empty", "frame_layout", "error_mask"]:
		if not _done.has(name):
			_failures += 1
			printerr("FAIL: 用例 %s 没跑完（中途抛错了？）" % name)
	if _failures > 0:
		printerr("login_reply_decode_test: %d 项失败" % _failures)
		quit(1)
	else:
		print("login_reply_decode_test: OK")
		quit(0)

func _check(cond: bool, msg: String) -> void:
	if not cond:
		_failures += 1
		printerr("FAIL: " + msg)

# ---- protobuf 编码辅助（测试里手写，与服务端生成码对齐） ----

func _tag(field: int, wire: int) -> PackedByteArray:
	return _c._varint((field << 3) | wire)

func _f_varint(field: int, v: int) -> PackedByteArray:
	var out := _tag(field, _c.WIRE_VARINT)
	out.append_array(_c._varint(v))
	return out

func _f_str(field: int, s: String) -> PackedByteArray:
	var body := s.to_utf8_buffer()
	var out := _tag(field, _c.WIRE_LEN)
	out.append_array(_c._varint(body.size()))
	out.append_array(body)
	return out

# ---- 用例 ----

func _test_login_reply_ok() -> void:
	var buf := PackedByteArray()
	buf.append_array(_f_varint(1, 1))          # ok = true
	buf.append_array(_f_str(2, "tok123"))      # token
	buf.append_array(_f_str(3, "Alice"))       # username
	buf.append_array(_f_str(4, "7"))           # account_id
	var d: Dictionary = _c._decode_login_reply(buf)
	_check(bool(d["ok"]), "ok 应为 true")
	_check(d["token"] == "tok123", "token 应为 tok123，得到 %s" % d["token"])
	_check(d["username"] == "Alice", "username 应保留原始大小写，得到 %s" % d["username"])
	_check(d["account_id"] == "7", "account_id 应为 7，得到 %s" % d["account_id"])
	_check(d["reason"] == "", "成功时 reason 应为空，得到 %s" % d["reason"])
	_done["reply_ok"] = true

func _test_login_reply_failure() -> void:
	# ok 字段缺省 = false（proto3），失败应答只有 reason。
	var buf := _f_str(5, "bad_credentials")
	var d: Dictionary = _c._decode_login_reply(buf)
	_check(not bool(d["ok"]), "缺省 ok 应为 false")
	_check(d["reason"] == "bad_credentials", "reason 应解出，得到 %s" % d["reason"])
	_check(d["token"] == "", "失败时 token 应为空")
	_done["reply_fail"] = true

func _test_login_reply_empty() -> void:
	var d: Dictionary = _c._decode_login_reply(PackedByteArray())
	_check(not bool(d["ok"]), "空载荷应是失败态")
	_check(d["token"] == "" and d["username"] == "" and d["reason"] == "", "空载荷各字段应为空串")
	_done["reply_empty"] = true

## Response 帧的布局：flag(0x04) + mid(LEB128) + payload —— 没有 route。
func _test_response_frame_layout() -> void:
	var payload := _f_varint(1, 1)
	var frame := PackedByteArray()
	frame.append(_c.MSG_RESPONSE << 1)
	frame.append_array(_c._varint(300))   # 需要 2 字节 LEB128（300 > 127）
	frame.append_array(payload)

	# 用与服务端编码器同样的方式还原。
	var r: Array = _c._read_varint(frame, 1)
	_check(int(r[0]) == 300, "mid 应 LEB128 还原成 300，得到 %d" % int(r[0]))
	var rest: PackedByteArray = frame.slice(int(r[1]))
	_check(rest.size() == payload.size(), "mid 之后应恰好是 payload")
	_check((frame[0] >> 1) & 0x07 == _c.MSG_RESPONSE, "flag 低 3 位应是 Response")

	# 关键契约：Response 帧没有 route 字段。payload 的第一个字节是 protobuf tag
	# （字段 1 varint = 0x08），不是 route 长度 —— 若误按 Push 解析，会把它当成
	# 长度为 8 的 route 读走 8 个字节，静默解出垃圾。
	_check(rest[0] == 0x08, "payload 首字节应是 protobuf tag 0x08，得到 %d" % rest[0])
	_done["frame_layout"] = true

## errorMask(0x20)：pitaya 层错误会置位，此时 payload 是错误字符串而非 LoginReply。
func _test_error_mask_flag() -> void:
	# 显式标注类型：_c 是 Node，成员常量取出来是 Variant，:= 推不出类型。
	var flag: int = (_c.MSG_RESPONSE << 1) | 0x20
	_check((flag & 0x20) != 0, "errorMask 应被识别")
	_check((flag >> 1) & 0x07 == _c.MSG_RESPONSE, "置了 errorMask 也仍是 Response 类型")
	_done["error_mask"] = true

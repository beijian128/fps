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
# 帧类用例走真实入口 _on_data，结果只能从 login_result 信号里接。
var _last_login := {}

func _on_login(result: Dictionary) -> void:
	_last_login = result

func _init() -> void:
	_c = FpsClient.new()
	_c.login_result.connect(_on_login)
	_test_login_reply_ok()
	_test_login_reply_failure()
	_test_login_reply_empty()
	_test_response_frame_layout()
	_test_error_mask_flag()
	_test_truncated_response_frame()
	for name: String in ["reply_ok", "reply_fail", "reply_empty", "frame_layout", "error_mask", "truncated"]:
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

## Response 帧的布局：flag + mid(LEB128) + payload —— **没有 route 字段**。
##
## 必须走 _on_data 而不是在测试里自己再解析一遍：自己解析等于断言自己的实现，
## 生产代码把 Response 当成 Push 解析（读 data[1] 当 route 长度）也照样绿。
## 这里把构造好的帧喂进真实的入口，断言经 login_result 信号出来的结果。
func _test_response_frame_layout() -> void:
	_last_login = {}
	var payload := _f_varint(1, 1)          # LoginReply{ok:true}
	payload.append_array(_f_str(3, "Alice"))
	var frame := PackedByteArray()
	frame.append(_c.MSG_RESPONSE << 1)
	frame.append_array(_c._varint(300))     # 2 字节 LEB128 的 mid
	frame.append_array(payload)

	_c._on_data(frame)

	var got: Dictionary = _last_login
	_check(got.get("ok", false) == true, "响应应解出 ok=true，得到 %s" % str(got))
	_check(got.get("username", "") == "Alice", "username 应解出 Alice，得到 %s" % str(got))
	_done["frame_layout"] = true

## errorMask(0x20) 置位时 payload 是 pitaya 的错误字符串，不是 LoginReply ——
## 绝不能当成一次成功登录（ok 不能变成 true）。
func _test_error_mask_flag() -> void:
	_last_login = {}
	var frame := PackedByteArray()
	frame.append((_c.MSG_RESPONSE << 1) | 0x20)
	frame.append_array(_c._varint(7))
	frame.append_array("boom".to_utf8_buffer())

	_c._on_data(frame)

	var got: Dictionary = _last_login
	_check(got.get("ok", true) == false, "错误帧绝不能报成登录成功，得到 %s" % str(got))
	_check(got.get("reason", "") == "internal", "错误帧应回 internal，得到 %s" % str(got))
	_done["error_mask"] = true

## 畸形帧（mid 被截断）不能读越界，也不能让调用方一直等 —— 必须明确报失败。
func _test_truncated_response_frame() -> void:
	_last_login = {}
	var frame := PackedByteArray()
	frame.append(_c.MSG_RESPONSE << 1)
	frame.append(0x80)   # LEB128 续位，后面没有字节了

	_c._on_data(frame)

	var got: Dictionary = _last_login
	_check(got.get("ok", true) == false, "截断帧应明确报失败，得到 %s" % str(got))
	_done["truncated"] = true

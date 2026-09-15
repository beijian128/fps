extends SceneTree
## 「有没有没打完的局」两条请求的 Response 解码与认领测试。
##
## 这两条都是 Request/Response（客户端要一个明确结论：有没有存量对局 / 放弃成没成），
## 走的是与 login / cancel 同一条路：Response 帧**没有 route 字段**，靠客户端自己记的
## mid 认领。字段号写错、认领错 route、或把错误帧当成成功，都会静默走错分支 ——
## 所以这里用手工字节串把 wire 布局钉住。
##
## 运行：Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client \
##         --script res://tests/pending_match_decode_test.gd

const FpsClient := preload("res://scripts/fps_client.gd")

var _failures := 0
var _done := {}
var _c: Node
var _last_pending := {}
var _last_abandon := {}

func _on_pending(result: Dictionary) -> void:
	_last_pending = result

func _on_abandon(result: Dictionary) -> void:
	_last_abandon = result

func _init() -> void:
	_c = FpsClient.new()
	_c.pending_match_received.connect(_on_pending)
	_c.abandon_match_received.connect(_on_abandon)
	_test_pending_found()
	_test_pending_not_found()
	_test_pending_truncated()
	_test_abandon_released()
	_test_abandon_failure()
	_test_abandon_error_frame()
	for name: String in ["pending_found", "pending_none", "pending_trunc",
			"abandon_released", "abandon_fail", "abandon_err"]:
		if not _done.has(name):
			_failures += 1
			printerr("FAIL: 用例 %s 没跑完（中途抛错了？）" % name)
	if _failures > 0:
		printerr("pending_match_decode_test: %d 项失败" % _failures)
		quit(1)
	else:
		print("pending_match_decode_test: OK")
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

## _response 拼一条 Response 帧（flag + mid + payload），并把 mid 登记成刚发出去的请求。
func _response(mid: int, route: String, payload: PackedByteArray) -> void:
	_c._pending[mid] = {"route": route, "at": 0.0}
	var frame := PackedByteArray()
	frame.append(_c.MSG_RESPONSE << 1)
	frame.append_array(_c._varint(mid))
	frame.append_array(payload)
	_c._on_data(frame)

# ---- 用例 ----

## PendingMatchReply{found:true, match_id:"m1"}：进大厅时要靠它弹询问框。
func _test_pending_found() -> void:
	_last_pending = {}
	var payload := _f_varint(1, 1)
	payload.append_array(_f_str(2, "m1"))

	_response(11, "match.match.pending", payload)

	_check(_last_pending.get("found", false) == true, "found 应解出 true，得到 %s" % str(_last_pending))
	_check(_last_pending.get("match_id", "") == "m1",
		"match_id 应解出 m1，得到 %s" % str(_last_pending))
	_done["pending_found"] = true

## 没有存量对局：proto3 的默认值不上线，这里就是一条空 payload。
func _test_pending_not_found() -> void:
	_last_pending = {}

	_response(12, "match.match.pending", PackedByteArray())

	_check(_last_pending.get("found", true) == false, "空 payload 应解出 found=false，得到 %s" % str(_last_pending))
	_check(not _last_pending.has("_malformed"), "空 payload 不是畸形消息")
	_done["pending_none"] = true

## 截断的字节串必须被标成畸形，宁可少弹一次框也不能拿垃圾去渲染。
func _test_pending_truncated() -> void:
	_last_pending = {}
	var payload := _f_varint(1, 1)
	payload.append(0x12)  # 字段 2 的 tag，但长度字节被截断

	_response(13, "match.match.pending", payload)

	_check(_last_pending.get("found", true) == false,
		"畸形应答应退回 found=false（客户端不弹框），得到 %s" % str(_last_pending))
	_done["pending_trunc"] = true

## AbandonMatchReply{ok:true, reason:"released"}。
func _test_abandon_released() -> void:
	_last_abandon = {}
	var payload := _f_varint(1, 1)
	payload.append_array(_f_str(2, "released"))

	_response(21, "match.match.abandon", payload)

	_check(_last_abandon.get("ok", false) == true, "ok 应解出 true，得到 %s" % str(_last_abandon))
	_check(_last_abandon.get("reason", "") == "released", "reason 应是 released，得到 %s" % str(_last_abandon))
	_done["abandon_released"] = true

## 失败应答（reason=not_found / internal）：ok=false + 原因码原样上桌，界面据此
## 决定「收起框」还是「保留框并提示重试」。
func _test_abandon_failure() -> void:
	_last_abandon = {}
	var payload := _f_str(2, "not_found")

	_response(22, "match.match.abandon", payload)

	_check(_last_abandon.get("ok", true) == false, "ok 应解出 false，得到 %s" % str(_last_abandon))
	_check(_last_abandon.get("reason", "") == "not_found", "reason 应是 not_found，得到 %s" % str(_last_abandon))
	_done["abandon_fail"] = true

## errorMask(0x20) 置位时 payload 是 pitaya 的错误串 —— 绝不能当成放弃成功。
func _test_abandon_error_frame() -> void:
	_last_abandon = {}
	_c._pending[23] = {"route": "match.match.abandon", "at": 0.0}
	var frame := PackedByteArray()
	frame.append((_c.MSG_RESPONSE << 1) | 0x20)
	frame.append_array(_c._varint(23))
	frame.append_array("boom".to_utf8_buffer())

	_c._on_data(frame)

	_check(_last_abandon.get("ok", true) == false, "错误帧绝不能报成放弃成功，得到 %s" % str(_last_abandon))
	_check(_last_abandon.get("reason", "") == "internal", "错误帧应回 internal，得到 %s" % str(_last_abandon))
	_done["abandon_err"] = true

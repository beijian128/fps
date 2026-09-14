extends SceneTree

const FpsClient := preload("res://scripts/fps_client.gd")

var _failures := 0
var _c: Node
var _last_logic := {}
var _done := {}

func _on_logic(result: Dictionary) -> void:
    _last_logic = result

func _init() -> void:
    _c = FpsClient.new()
    _c.logic_state_received.connect(_on_logic)
    _test_state_response()
    _test_logic_error_mask()
    _test_purchase_encoding()
    _test_equip_encoding()
    for name: String in ["state", "error", "purchase", "equip"]:
        if not _done.has(name):
            _failures += 1
            printerr("FAIL: 用例 %s 没跑完" % name)
    if _failures > 0:
        printerr("logic_state_decode_test: %d 项失败" % _failures)
        quit(1)
    else:
        print("logic_state_decode_test: OK")
        quit(0)

func _check(cond: bool, msg: String) -> void:
    if not cond:
        _failures += 1
        printerr("FAIL: " + msg)

func _tag(field: int, wire: int) -> PackedByteArray:
    return _c._varint((field << 3) | wire)

func _f_varint(field: int, value: int) -> PackedByteArray:
    var out := _tag(field, _c.WIRE_VARINT)
    out.append_array(_c._varint(value))
    return out

func _f_str(field: int, value: String) -> PackedByteArray:
    var body := value.to_utf8_buffer()
    var out := _tag(field, _c.WIRE_LEN)
    out.append_array(_c._varint(body.size()))
    out.append_array(body)
    return out

func _f_bytes(field: int, body: PackedByteArray) -> PackedByteArray:
    var out := _tag(field, _c.WIRE_LEN)
    out.append_array(_c._varint(body.size()))
    out.append_array(body)
    return out

func _item(item_id: String, name: String, price: int, slot: String, qty: int) -> PackedByteArray:
    var out := _f_str(1, item_id)
    out.append_array(_f_str(2, name))
    out.append_array(_f_varint(3, price))
    out.append_array(_f_str(4, slot))
    out.append_array(_f_varint(5, qty))
    return out

func _test_state_response() -> void:
    var payload := _f_varint(1, 1)
    payload.append_array(_f_varint(3, 400))
    payload.append_array(_f_bytes(4, _item("rifle", "步枪", 300, "primary_weapon", 2)))
    payload.append_array(_f_bytes(4, _item("medkit", "医疗包", 50, "", 5)))
    payload.append_array(_f_str(5, "rifle"))

    _c._pending[77] = {"route": "logic.logic.state", "at": 0.0}
    var frame := PackedByteArray()
    frame.append(_c.MSG_RESPONSE << 1)
    frame.append_array(_c._varint(77))
    frame.append_array(payload)
    _c._on_data(frame)

    _check(bool(_last_logic.get("ok", false)), "ok 应为 true")
    _check(int(_last_logic.get("coins", 0)) == 400, "coins 应为 400")
    _check(String(_last_logic.get("equipped_primary_weapon", "")) == "rifle", "装备应为 rifle")
    _check((_last_logic.get("items", []) as Array).size() == 2, "应有 2 个商品项")
    _check(int(_last_logic["items"][0]["owned_quantity"]) == 2, "rifle 数量应为 2")
    _done["state"] = true

func _test_logic_error_mask() -> void:
    _last_logic = {}
    _c._pending[78] = {"route": "logic.logic.purchase", "at": 0.0}
    var frame := PackedByteArray()
    frame.append((_c.MSG_RESPONSE << 1) | 0x20)
    frame.append_array(_c._varint(78))
    frame.append_array("boom".to_utf8_buffer())
    _c._on_data(frame)
    _check(not bool(_last_logic.get("ok", true)), "errorMask 应回 ok=false")
    _check(String(_last_logic.get("reason", "")) == "internal", "errorMask 应回 internal")
    _done["error"] = true

func _test_purchase_encoding() -> void:
    var encoded: PackedByteArray = _c._encode_purchase("rifle", 2)
    var seen := {}
    var i := 0
    while i < encoded.size():
        var tag: Array = _c._read_varint(encoded, i)
        i = int(tag[1])
        var field := int(tag[0]) >> 3
        var wire := int(tag[0]) & 7
        seen[field] = wire
        if wire == _c.WIRE_LEN:
            var length: Array = _c._read_varint(encoded, i)
            i = int(length[1]) + int(length[0])
        else:
            var value: Array = _c._read_varint(encoded, i)
            i = int(value[1])
    _check(seen.get(1, -1) == _c.WIRE_LEN, "item_id 应为字段 1")
    _check(seen.get(2, -1) == _c.WIRE_VARINT, "quantity 应为字段 2")
    _done["purchase"] = true

func _test_equip_encoding() -> void:
    var encoded: PackedByteArray = _c._encode_equip("")
    _check(encoded.size() == 2, "空 item_id 仍应编码字段 1 的空字符串")
    _done["equip"] = true
extends SceneTree

const MainScene := preload("res://scenes/main.tscn")

var _failures := 0

func _init() -> void:
    var main: Node = MainScene.instantiate()
    root.add_child(main)
    main._logic_state = {
        "ok": true,
        "coins": 400,
        "equipped_primary_weapon": "rifle",
        "items": [
            {"item_id": "rifle", "display_name": "步枪", "price": 300,
             "equip_slot": "primary_weapon", "owned_quantity": 2},
            {"item_id": "medkit", "display_name": "医疗包", "price": 50,
             "equip_slot": "", "owned_quantity": 5},
        ],
    }
    main._refresh_logic_panel()
    _check(main._logic_coins.text.contains("400"), "金币应显示 400")
    _check(main._logic_items_box.get_child_count() == 2, "应显示两个商品行")
    _check(main._logic_status.text == "", "成功状态不应显示错误")

    main._on_logic_state({"ok": false, "reason": "insufficient_funds"})
    _check(main._logic_status.text == "金币不足", "应显示余额不足")
    _check(main._logic_coins.text.contains("400"), "失败不能污染已有金币状态")
    main.queue_free()
    if _failures > 0:
        printerr("logic_panel_test: %d 项失败" % _failures)
        quit(1)
    else:
        print("logic_panel_test: OK")
        quit(0)

func _check(cond: bool, msg: String) -> void:
    if not cond:
        _failures += 1
        printerr("FAIL: " + msg)

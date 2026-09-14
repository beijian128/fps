extends SceneTree

const MainScene := preload("res://scenes/main.tscn")

var _failures := 0

class FakeClient:
    extends Node

    var state_requests := 0
    var match_requests := 0
    var last_username := ""
    var client_token := ""

    func send_logic_state() -> void:
        state_requests += 1

    func send_match_join() -> void:
        match_requests += 1

func _init() -> void:
    var main: Node = MainScene.instantiate()
    var fake := FakeClient.new()
    main.fps_client = fake
    main.add_child(fake)
    main.conn_label = Label.new()
    main.add_child(main.conn_label)
    root.add_child(main)
    main._build_logic_panel()
    main._login_panel = CanvasLayer.new()
    main.add_child(main._login_panel)
    main._login_error = Label.new()
    main._login_panel.add_child(main._login_error)
    main._login_panel.visible = true

    _check(not main._logic_panel.visible, "商城面板初始应隐藏")
    _check(not main._logic_authenticated, "初始状态不应视为已认证")

    main._login_panel.visible = false
    main._logic_panel.visible = false
    main._logic_busy = false
    main._logic_status.text = ""
    fake.state_requests = 0
    var unauth_key := InputEventKey.new()
    unauth_key.keycode = KEY_B
    unauth_key.pressed = true
    main._unhandled_input(unauth_key)
    _check(not main._logic_panel.visible, "恢复登录未完成时 B 不能打开商城")
    _check(not main._logic_busy, "未认证的 B 操作不能设置请求中")
    _check(main._logic_status.text == "", "未认证的 B 操作不能显示加载状态")
    _check(fake.state_requests == 0, "未认证的 B 操作不能请求状态")

    main._logic_panel.visible = false
    main._logic_busy = false
    main._logic_status.text = ""
    fake.state_requests = 0
    main._toggle_logic_panel()
    _check(not main._logic_panel.visible, "未认证时直接切换也不能打开商城")
    _check(not main._logic_busy, "未认证的直接切换不能设置请求中")
    _check(main._logic_status.text == "", "未认证的直接切换不能显示加载状态")
    _check(fake.state_requests == 0, "未认证的直接切换不能请求状态")

    main._logic_panel.visible = false
    main._logic_busy = false
    main._logic_status.text = ""
    fake.state_requests = 0
    main._on_login_result({"ok": true})
    _check(main._logic_authenticated, "登录成功后才应放行商城")
    _check(fake.state_requests == 0, "登录成功不应自动请求商城状态")
    _check(fake.match_requests == 1, "登录成功仍应发起匹配")

    main._login_panel.visible = true
    fake.state_requests = 0
    var key_b := InputEventKey.new()
    key_b.keycode = KEY_B
    key_b.pressed = true
    main._unhandled_input(key_b)
    _check(not main._logic_panel.visible, "登录面板可见时 B 不能打开商城")
    _check(fake.state_requests == 0, "登录面板可见时不能请求商城状态")

    main._login_panel.visible = false
    main._logic_authenticated = true
    main._logic_panel.visible = false
    main._logic_busy = false
    main._logic_status.text = ""
    fake.state_requests = 0
    main._toggle_logic_panel()
    _check(main._logic_panel.visible, "商城打开后应可见")
    _check(fake.state_requests == 1, "打开商城应请求一次状态")
    _check(main._logic_busy, "打开商城后应处于请求中")

    main._logic_authenticated = true
    main._logic_panel.visible = false
    main._logic_busy = true
    main._logic_status.text = "加载中…"
    fake.state_requests = 0
    main._toggle_logic_panel()
    _check(main._logic_panel.visible, "请求在途时仍应打开商城面板")
    _check(fake.state_requests == 0, "请求在途时不能重复请求状态")
    _check(main._logic_busy, "重复打开不能提前清除请求中状态")

    main._logic_authenticated = true
    main._logic_panel.visible = true
    main._logic_busy = true
    main._logic_status.text = "加载中…"
    main._toggle_logic_panel()
    _check(not main._logic_panel.visible, "再次切换应关闭商城面板")
    _check(main._logic_busy, "关闭面板不能清除在途请求")
    _check(main._logic_status.text == "加载中…", "关闭面板不能清除请求状态文本")

    main._on_connection(false)
    _check(not main._logic_busy, "断线应清除在途商城请求")
    _check(main._logic_status.text == "", "断线应清除商城状态文本")
    _check(not main._logic_authenticated, "断线应撤销商城认证门")
    _check(not main._logic_panel.visible, "断线应隐藏商城面板")

    main._logic_state = {
        "ok": true,
        "coins": 400,
        "equipped_primary_weapon": "rifle",
        "items": [
            {"item_id": "rifle", "display_name": "步枪", "price": 300,
             "equip_slot": "primary_weapon", "owned_quantity": 2},
        ],
    }
    main._logic_busy = true
    main._on_logic_state({
        "ok": true,
        "coins": 500,
        "equipped_primary_weapon": "shotgun",
        "items": [
            {"item_id": "shotgun", "display_name": "霰弹枪", "price": 450,
             "equip_slot": "primary_weapon", "owned_quantity": 1},
        ],
    })
    _check(int(main._logic_state.get("coins", 0)) == 500, "成功响应应替换完整状态")
    _check(String(main._logic_state.get("equipped_primary_weapon", "")) == "shotgun",
            "成功响应应替换装备状态")
    _check(main._logic_coins.text.contains("500"), "成功响应应刷新金币显示")
    _check(not main._logic_busy, "成功响应应清除请求中状态")

    var before: Dictionary = main._logic_state.duplicate(true) as Dictionary
    main._logic_busy = true
    main._on_logic_state({"ok": false, "reason": "insufficient_funds"})
    _check(int(main._logic_state.get("coins", 0)) == int(before.get("coins", -1)),
            "失败响应不能污染金币状态")
    _check(String(main._logic_state.get("equipped_primary_weapon", "")) ==
            String(before.get("equipped_primary_weapon", "")),
            "失败响应不能污染装备状态")
    _check((main._logic_state.get("items", []) as Array).size() ==
            (before.get("items", []) as Array).size(),
            "失败响应不能污染商品状态")
    _check(main._logic_status.text == "金币不足", "失败响应应显示余额不足")
    _check(not main._logic_busy, "失败响应应清除请求中状态")

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

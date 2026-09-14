extends SceneTree

const PASSWORD := "smokepass"
const TIMEOUT_MS := 20000

var _failures := 0
var _client: Node
var _phase := 0
var _username := ""

func _initialize() -> void:
    for p in ["user://auth_token.txt", "user://last_username.txt"]:
        if FileAccess.file_exists(p):
            DirAccess.remove_absolute(ProjectSettings.globalize_path(p))
    _username = "logicsmoke_%d" % (Time.get_ticks_usec() % 100000000)
    _client = load("res://scripts/fps_client.gd").new()
    _client.login_result.connect(_on_login)
    _client.logic_state_received.connect(_on_logic_state)
    root.add_child(_client)
    _run()

func _check(cond: bool, msg: String) -> void:
    if not cond:
        _failures += 1
        printerr("FAIL: " + msg)

func _on_login(result: Dictionary) -> void:
    if bool(result.get("ok", false)):
        _client.send_logic_state()
        return
    if String(result.get("reason", "")) == "no_token":
        _client.send_register(_username, PASSWORD)
        return
    _check(false, "登录失败: %s" % result)

func _on_logic_state(result: Dictionary) -> void:
    if not bool(result.get("ok", false)):
        _check(false, "Logic 状态失败: %s" % result.get("reason", ""))
        return
    var coins := int(result.get("coins", -1))
    var items: Array = result.get("items", [])
    if _phase == 0:
        _check(coins == 1000, "初始金币应为 1000，得到 %d" % coins)
        _phase = 1
        _client.send_purchase("rifle", 1)
    elif _phase == 1:
        _check(coins == 700, "购买步枪后金币应为 700，得到 %d" % coins)
        _check(_quantity(items, "rifle") == 1, "步枪数量应为 1")
        _phase = 2
        _client.send_equip("rifle")
    elif _phase == 2:
        _check(String(result.get("equipped_primary_weapon", "")) == "rifle", "步枪应已装备")
        _phase = 3

func _quantity(items: Array, item_id: String) -> int:
    for item in items:
        if String(item.get("item_id", "")) == item_id:
            return int(item.get("owned_quantity", 0))
    return 0

func _run() -> void:
    var deadline := Time.get_ticks_msec() + TIMEOUT_MS
    while Time.get_ticks_msec() < deadline and _phase < 3:
        await process_frame
    if _phase != 3:
        _check(false, "smoke 未完成，phase=%d" % _phase)
    if _failures > 0:
        printerr("logic_smoke: %d 项失败" % _failures)
        quit(1)
    else:
        print("logic_smoke: OK")
        quit(0)

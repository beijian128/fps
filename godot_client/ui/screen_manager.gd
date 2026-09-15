extends CanvasLayer
## 客户端界面的唯一状态源。
##
## 规则（整个 ui/ 目录都遵守）：
##   - 状态只在这里改：谁都不许绕过它去改「现在处于哪一步」。屏幕、状态条、结算层、
##     HUD 全部只做两件事 —— 渲染 `state()` 与发意图（`intent_*`）。
##   - 信号接线也在这里，而不是在 main.gd：测试要注入假 FpsClient，而
##     `@onready var fps_client = $FpsClient` 会在 _ready 时把测试提前塞进去的假客户端
##     覆盖掉；接线放在 setup() 里，测试就能在 _ready 之后重新绑定。
##   - 协议细节（Request/Response、token、重连）在 fps_client.gd；世界渲染与输入在
##     main.gd。这里只认识「事件」与「意图」。

enum State { BOOT, LOGIN, LOBBY, MATCHING, IN_MATCH, RESULT }

const LoginScene := preload("res://ui/login_screen.tscn")
const ShellScene := preload("res://ui/shell.tscn")
const MatchBarScene := preload("res://ui/match_status_bar.tscn")
const ResultScene := preload("res://ui/result_overlay.tscn")
const HudScene := preload("res://ui/hud.tscn")

signal state_changed(state: State)
signal page_changed(page: String)
signal data_changed()

var login_screen: Control = null
var shell: Control = null
var match_bar: Control = null
var result_overlay: Control = null
var hud: Control = null

var _state := State.BOOT
var _page := "profile"
var _client: Node = null
var _main: Node = null
var _logic := {}
var _profile := {}
var _last_result := {}
var _status := {}

## setup 绑定客户端并接上信号。可重复调用（测试会换成假客户端再调一次）。
func setup(client: Node, main: Node) -> void:
	_client = client
	_main = main
	_connect_if_needed(client.login_result, on_login_result)
	_connect_if_needed(client.logic_state_received, on_logic_state)
	_connect_if_needed(client.profile_received, on_profile)
	_connect_if_needed(client.match_status_received, on_match_status)
	_connect_if_needed(client.matched_received, on_matched)
	_connect_if_needed(client.match_ended_received, on_match_ended)
	_connect_if_needed(client.match_cancel_received, on_cancel_reply)
	_connect_if_needed(client.connection_changed, on_connection_changed)
	if login_screen == null:
		_build_screens()
	# 有本地凭证时客户端会自动 resume，先不显示登录页（否则会闪一下）；
	# 没有凭证就直接进登录页。
	_set_state(State.BOOT if String(client.client_token) != "" else State.LOGIN)

# ---- 查询（屏幕与测试都从这里读）----

func state() -> State: return _state
func current_page() -> String: return _page
func logic_state() -> Dictionary: return _logic
func profile() -> Dictionary: return _profile
func last_result() -> Dictionary: return _last_result
func match_status() -> Dictionary: return _status

# ---- 意图（屏幕只调这些）----

func intent_register(username: String, password: String) -> void:
	_client.send_register(username, password)

func intent_login(username: String, password: String) -> void:
	_client.send_login(username, password)

func intent_show_page(page: String) -> void:
	if _page == page:
		return
	_page = page
	page_changed.emit(page)

func intent_start_match() -> void:
	_status = {}
	_set_state(State.MATCHING)
	_client.send_match_join()

func intent_cancel_match() -> void:
	# 结果由服务端应答决定（cancelled / already_matched / not_queued），这里不猜。
	_client.send_cancel_match()

func intent_purchase(item_id: String, qty: int) -> void:
	_client.send_purchase(item_id, qty)

func intent_equip(item_id: String) -> void:
	_client.send_equip(item_id)

func intent_back_to_lobby() -> void:
	_set_state(State.LOBBY)
	_client.send_profile()   # 刚打完一局，档案（场次/战绩）变了

func intent_play_again() -> void:
	_status = {}
	_set_state(State.MATCHING)
	_client.send_match_join()

func intent_resync() -> void:
	_client.send_resync()

# ---- 客户端事件 ----

func on_login_result(result: Dictionary) -> void:
	if bool(result.get("ok", false)):
		_set_state(State.LOBBY)
		_client.send_logic_state()
		_client.send_profile()
		return
	# no_token 不是错误：本地没有凭证而已，安静显示登录页。
	_set_state(State.LOGIN)
	if login_screen != null and String(result.get("reason", "")) != "no_token":
		login_screen.show_failure(String(result.get("reason", "")))

func on_logic_state(result: Dictionary) -> void:
	if bool(result.get("ok", false)):
		_logic = result
	else:
		_logic = {}
	data_changed.emit()

func on_profile(result: Dictionary) -> void:
	if bool(result.get("ok", false)):
		_profile = result
		data_changed.emit()

func on_match_status(status: Dictionary) -> void:
	_status = status
	data_changed.emit()

func on_matched(_result: Dictionary) -> void:
	_set_state(State.IN_MATCH)

func on_match_ended(result: Dictionary) -> void:
	_last_result = result
	_set_state(State.RESULT)

func on_cancel_reply(result: Dictionary) -> void:
	var reason := String(result.get("reason", ""))
	if reason == "cancelled" or reason == "not_queued":
		_set_state(State.LOBBY)
	# already_matched：什么都不做 —— 服务端马上会推 onMatched。

func on_connection_changed(_connected: bool) -> void:
	data_changed.emit()

# ---- 内部 ----

func _connect_if_needed(sig: Signal, callable: Callable) -> void:
	if not sig.is_connected(callable):
		sig.connect(callable)

func _build_screens() -> void:
	login_screen = LoginScene.instantiate()
	add_child(login_screen)
	shell = ShellScene.instantiate()
	add_child(shell)
	result_overlay = ResultScene.instantiate()
	add_child(result_overlay)
	hud = HudScene.instantiate()
	add_child(hud)
	# 匹配状态条长在大厅外壳里（它是外壳底部那一行）。
	match_bar = MatchBarScene.instantiate()
	var slot: Node = shell.get_node_or_null("%StatusSlot")
	if slot != null:
		slot.add_child(match_bar)

func _set_state(next: State) -> void:
	if _state == next:
		return
	_state = next
	_apply_visibility()
	state_changed.emit(next)

func _apply_visibility() -> void:
	if login_screen != null:
		login_screen.visible = _state == State.LOGIN
	if shell != null:
		shell.visible = _state == State.LOBBY or _state == State.MATCHING
	if result_overlay != null:
		result_overlay.visible = _state == State.RESULT
	if hud != null:
		hud.visible = _state == State.IN_MATCH

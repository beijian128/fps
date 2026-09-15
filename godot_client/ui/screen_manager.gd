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
const BackdropScene := preload("res://ui/backdrop.tscn")
const ProfileScene := preload("res://ui/profile_screen.tscn")
const ShopScene := preload("res://ui/shop_screen.tscn")
const BagScene := preload("res://ui/bag_screen.tscn")
const MatchBarScene := preload("res://ui/match_status_bar.tscn")
const ResultScene := preload("res://ui/result_overlay.tscn")
const HudScene := preload("res://ui/hud.tscn")
const PauseScene := preload("res://ui/pause_screen.tscn")
const RejoinPromptScene := preload("res://ui/rejoin_prompt.tscn")
const ToastScene := preload("res://ui/toast.tscn")
const Settings := preload("res://ui/settings.gd")
const Toast := preload("res://ui/toast.gd")

signal state_changed(state: State)
signal page_changed(page: String)
signal data_changed()
## settings_changed 本机偏好变了（设置面板改的）；pause_changed 设置层开合，
## main 靠它同步鼠标捕获；后两个是「某一类数据到了」，页面据此重渲染（见 shell.gd）。
signal settings_changed()
signal pause_changed(paused: bool)
signal logic_state_changed()
signal profile_changed()

var login_screen: Control = null
var backdrop: Control = null
var shell: Control = null
var profile_screen: Control = null
var shop_screen: Control = null
var bag_screen: Control = null
var match_bar: Control = null
var result_overlay: Control = null
var hud: Control = null
var pause_screen: Control = null
## rejoin_prompt 进大厅时的询问框（「回到对局 / 放弃对局」）。它是**大厅之上的浮层**，
## 与 pause_screen 之于对局同一性质：不进 State 枚举，只由 _rejoin_prompt_open 控制。
var rejoin_prompt: Control = null
var toast: Control = null

var _state := State.BOOT
## _page 当前打开的子界面（`""` = 大厅首页）。大厅是入口、子界面整屏铺开，
## 所以它同时决定「显示大厅还是显示某个子界面」，不再是一块内嵌内容。
var _page := ""
var _client: Node = null
var _main: Node = null
var _settings = Settings.new()
var _paused := false
var _logic := {}
var _profile := {}
var _last_result := {}
var _status := {}
var _username := ""
var _account_id := ""
var _my_slot := 0
## _pending_match 最近一次 match.pending 的应答（found / match_id）。它是「有没有没打完
## 的局」的唯一真相源：询问框显不显示、显示哪个对局，都从这里读。
var _pending_match := {}
var _rejoin_prompt_open := false
## _pending 记「刚发出去、还没看到结果」的动作，等下一份状态回来时才能给出确定的回音
## （「正在购买…」→「已购买」）。没有它就只能乐观地提前说成功，或者干脆什么都不说。
var _pending := {}
var _was_connected := true

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
	_connect_if_needed(client.pending_match_received, on_pending_match)
	_connect_if_needed(client.abandon_match_received, on_abandon_reply)
	_connect_if_needed(client.connection_changed, on_connection_changed)
	if login_screen == null:
		_build_screens()
	# 有本地凭证时客户端会自动 resume，先不显示登录页（否则会闪一下）；
	# 没有凭证就直接进登录页。
	_set_state(State.BOOT if String(client.client_token) != "" else State.LOGIN)
	# 偏好要在界面建好之后立刻生效一次：界面缩放（窗口）与安全区（HUD）都挂在它上面。
	apply_settings()

# ---- 查询（屏幕与测试都从这里读）----

func state() -> State: return _state
func current_page() -> String: return _page
## page_node_name 当前子界面的根节点名（大厅首页时为空串）。测试用。
func page_node_name() -> String:
	var page := _page_node()
	return "" if page == null else String(page.name)
## page_visible 某个子界面当前是否可见（整屏铺开的语义：它与大厅互斥）。
func page_visible(page: String) -> bool:
	var node := _page_node(page)
	return node != null and node.visible
func logic_state() -> Dictionary: return _logic
func profile() -> Dictionary: return _profile
func last_result() -> Dictionary: return _last_result
func match_status() -> Dictionary: return _status
## username 顶栏要显示用户名，而档案回复里没有它（自己的用户名只在 LoginReply 里，
## 避免两个数据源）—— 登录成功时记在这里。
func username() -> String: return _username
## account_id 同理来自 LoginReply（十进制字符串）。
func account_id() -> String: return _account_id
## my_slot 本机在局内的槽位（0/1），来自 onMatched —— 结算层靠它判断自己是胜是负、
## 该读哪一侧的比分。
func my_slot() -> int: return _my_slot
## has_pending_match 服务端当前还留着我的一场没打完的局（来自 match.pending）。
func has_pending_match() -> bool: return bool(_pending_match.get("found", false))
## pending_match_id 那场对局的 id（没有时为空串）。
func pending_match_id() -> String: return String(_pending_match.get("match_id", ""))
## rejoin_prompt_visible 询问框是否正开着。
func rejoin_prompt_visible() -> bool: return _rejoin_prompt_open
## settings 本机偏好（灵敏度 / 界面缩放 / 受击反馈强度 / 安全区）。
## 界面只读它，改一律走 intent_set_setting —— 否则「谁改的」就散落在各个屏幕里了。
func settings(): return _settings

## notify 弹一条交互反馈（买到了 / 已装备 / 掉线…）。界面也能调它，但只该报「玩家的动作结果」，
## 不要拿它当日志用 —— 没人看的信息会稀释真正重要的那条。
func notify(text: String, kind: int = Toast.Kind.INFO) -> void:
	if toast != null:
		toast.notify(text, kind)
## is_paused 对局内的设置层是否打开。注意它**不暂停对局**：服务端 20Hz 照常推进。
func is_paused() -> bool: return _paused

# ---- 意图（屏幕只调这些）----

func intent_register(username: String, password: String) -> void:
	_client.send_register(username, password)

func intent_login(username: String, password: String) -> void:
	_client.send_login(username, password)

func intent_show_page(page: String) -> void:
	if _page == page:
		return
	_page = page
	_apply_visibility()
	_feed_pages()
	page_changed.emit(page)

## intent_close_page 子界面上的「返回大厅」：回到入口页，而不是把子界面留在背后。
func intent_close_page() -> void:
	intent_show_page("")

func intent_start_match() -> void:
	_status = {}
	_set_state(State.MATCHING)
	notify("已进入匹配队列（凑满 2 人开局，可随时取消）", Toast.Kind.INFO)
	_client.send_match_join()

func intent_cancel_match() -> void:
	# 结果由服务端应答决定（cancelled / already_matched / not_queued），这里不猜。
	_client.send_cancel_match()

## intent_rejoin_match 询问框上的「回到对局」：走的就是点「开始匹配」那条路
## （服务端的 match.join 有回局分支：命中存量实例就把人送回原局、原槽位），
## 只是文案不同 —— 玩家点的是「回到对局」，这里就不该说「已进入匹配队列」。
func intent_rejoin_match() -> void:
	_pending_match = {}
	_close_rejoin_prompt()
	_status = {}
	_set_state(State.MATCHING)
	notify("正在回到原来的对局…", Toast.Kind.INFO)
	_client.send_match_join()

## intent_abandon_match 询问框上的「放弃对局」：服务端只把本玩家从对局里释放出来
## （不再收帧、不再参与结算），对手那一局照常打完。结果由应答决定，这里不猜。
func intent_abandon_match() -> void:
	notify("正在放弃对局…", Toast.Kind.INFO)
	_client.send_abandon_match()

## intent_dismiss_rejoin_prompt 稍后再决定（ESC）：收起询问框、留在大厅。
##
## 对局仍在服务端继续（没有放弃），之后点「开始匹配」会通过回局分支回到它 ——
## 所以这里必须说清楚「怎么再回来」，否则玩家会以为这个框关掉就把对局丢了。
func intent_dismiss_rejoin_prompt() -> void:
	_close_rejoin_prompt()
	notify("稍后决定：点「开始匹配」仍会回到这场对局", Toast.Kind.INFO)

func intent_purchase(item_id: String, qty: int) -> void:
	var item := _find_item(item_id)
	_pending = {"kind": "purchase", "item_id": item_id, "qty": qty,
		"owned_before": int(item.get("owned_quantity", 0)),
		"coins_before": int(_logic.get("coins", 0))}
	notify("正在购买 %s ×%d…" % [_item_name(item_id), qty], Toast.Kind.INFO)
	_client.send_purchase(item_id, qty)

func intent_equip(item_id: String) -> void:
	_pending = {"kind": "equip", "item_id": item_id}
	if item_id == "":
		notify("正在卸下主武器…", Toast.Kind.INFO)
	else:
		notify("正在装备 %s…" % _item_name(item_id), Toast.Kind.INFO)
	_client.send_equip(item_id)

func intent_back_to_lobby() -> void:
	_page = ""
	_set_state(State.LOBBY)
	_client.send_profile()   # 刚打完一局，档案（场次/战绩）变了

func intent_play_again() -> void:
	_status = {}
	_set_state(State.MATCHING)
	_client.send_match_join()

func intent_resync() -> void:
	_client.send_resync()

## intent_toggle_pause ESC 的行为：只在对局中有意义（大厅/结算里没有可「返回」的战场）。
func intent_toggle_pause() -> void:
	if _state != State.IN_MATCH:
		return
	_set_paused(not _paused)

## intent_escape ESC 的统一入口：对局内开合设置层，子界面里返回大厅。
## 同一个键只做一件事、取决于「现在在哪」—— 玩家不必记两套快捷键，
## 也不会出现「在大厅按 ESC 什么反应都没有」这种「按键坏了」的错觉。
func intent_escape() -> void:
	# 询问框在最上层：ESC 先收它（= 稍后再决定，**不是**放弃对局 —— 破坏性动作不该
	# 有一个「顺手按到」的键）。
	if _rejoin_prompt_open:
		intent_dismiss_rejoin_prompt()
		return
	if _state == State.IN_MATCH:
		_set_paused(not _paused)
		return
	if _page != "":
		intent_close_page()

func intent_resume_match() -> void:
	_set_paused(false)

func intent_set_setting(key: String, value: float) -> void:
	_settings.set_value(key, value)
	apply_settings()

func intent_reset_settings() -> void:
	_settings.reset()
	apply_settings()

## apply_settings 把偏好推到消费者那里。只有两项需要「推」：
##   - 界面缩放 → 窗口的 content_scale_factor（拉伸模式下缩放的是 CanvasItem，3D 不受影响）
##   - 安全区 → HUD
## 灵敏度与受击反馈强度不在这里推：它们是 main 每次用到时现读的瞬时值（读一次比存两份更省心）。
func apply_settings() -> void:
	var win := get_window()
	if win != null:
		win.content_scale_factor = _settings.value("ui_scale")
	if hud != null:
		hud.set_safe_pct(_settings.value("safe_area_pct"))
	settings_changed.emit()

func _set_paused(paused: bool) -> void:
	if _paused == paused:
		return
	_paused = paused
	_apply_visibility()
	pause_changed.emit(paused)

# ---- 客户端事件 ----

func on_login_result(result: Dictionary) -> void:
	if bool(result.get("ok", false)):
		_username = String(result.get("username", ""))
		_account_id = String(result.get("account_id", ""))
		_page = ""          # 登录后落在入口页，不直接钻进某个子界面
		_set_state(State.LOBBY)
		_client.send_logic_state()
		_client.send_profile()
		# 进大厅的第一件事：问一句「我这个账号有没有没打完的局」。有就弹询问框
		# （回到对局 / 放弃对局），没有就什么都不做 —— 这个查询没有任何副作用。
		_client.send_pending_match()
		return
	# no_token 不是错误：本地没有凭证而已，安静显示登录页。
	_set_state(State.LOGIN)
	if login_screen == null:
		return
	var reason := String(result.get("reason", ""))
	if reason == "no_token":
		login_screen.show_silently()
	else:
		login_screen.show_failure(reason)

func on_logic_state(result: Dictionary) -> void:
	if bool(result.get("ok", false)):
		_logic = result
	else:
		_logic = {}
	data_changed.emit()
	logic_state_changed.emit()
	_feed_pages()
	# 商城状态回来了 → 上一次「正在购买/正在装备」可以给结论了（见 _resolve_pending）。
	_resolve_pending()

func on_profile(result: Dictionary) -> void:
	if bool(result.get("ok", false)):
		_profile = result
		data_changed.emit()
		profile_changed.emit()
		_feed_pages()

func on_match_status(status: Dictionary) -> void:
	_status = status
	data_changed.emit()

func on_matched(result: Dictionary) -> void:
	_my_slot = int(result.get("player_idx", 0))
	# 已经进局了：进大厅时那份「有没有没打完的局」的快照过期了（旧局或者新局，总之
	# 现在人在局里），留着它会在下次回大厅时显示一个对不上的对局 id。
	_pending_match = {}
	if hud != null:
		hud.reset()   # 新一局：战绩、播报、准星状态都从头来
	_set_state(State.IN_MATCH)
	notify("已匹配，进入对局", Toast.Kind.SUCCESS)

func on_match_ended(result: Dictionary) -> void:
	_last_result = result
	_set_state(State.RESULT)

func on_cancel_reply(result: Dictionary) -> void:
	var reason := String(result.get("reason", ""))
	if reason == "cancelled" or reason == "not_queued":
		_set_state(State.LOBBY)
		notify("已取消匹配" if reason == "cancelled" else "你不在队列中", Toast.Kind.INFO)
	# already_matched：什么都不做 —— 服务端马上会推 onMatched。

## on_pending_match 「我有没有没打完的局」的应答。
##
## 只有**在大厅里**才弹框：这条应答可能比玩家的手慢（比如他登录后直接点了「开始匹配」，
## 那时 onMatched 可能已经先到了），对局中再糊一层询问框只会挡住战场。
func on_pending_match(result: Dictionary) -> void:
	_pending_match = result
	if not bool(result.get("found", false)):
		_close_rejoin_prompt()
		data_changed.emit()
		return
	if _state != State.LOBBY:
		return
	_open_rejoin_prompt()
	data_changed.emit()

## on_abandon_reply 「放弃对局」的应答。
##
## ok=true：他已经被服务端从那一局里释放出来了（不再收帧、不再参与结算），询问框收起。
## not_found：那一局已经不存在了（刚好打完 / 已经释放过）—— 目标达成，照常收起。
## 其余（unauthenticated / internal）：**保留询问框**并给失败提示，玩家可以重试；
## 绝不能在这里假装成功（他会以为对局已经放弃，实际还挂在服务端）。
func on_abandon_reply(result: Dictionary) -> void:
	if bool(result.get("ok", false)):
		_pending_match = {}
		_close_rejoin_prompt()
		notify("已放弃对局：本局不再计入你的战绩", Toast.Kind.SUCCESS)
		data_changed.emit()
		return
	var reason := String(result.get("reason", ""))
	if reason == "not_found":
		_pending_match = {}
		_close_rejoin_prompt()
		notify("那场对局已经结束了", Toast.Kind.INFO)
	elif reason == "unauthenticated":
		notify("放弃失败：登录状态已失效，请重新登录", Toast.Kind.DANGER)
	else:
		notify("放弃失败：服务暂时不可用，请重试", Toast.Kind.DANGER)
	data_changed.emit()

func on_connection_changed(connected: bool) -> void:
	data_changed.emit()
	# 掉线是这个游戏里最需要「有回音」的状态：界面不动不代表没事，玩家会以为是自己点错了。
	if not connected and _was_connected:
		notify("连接断开，正在自动重连…", Toast.Kind.WARN)
	elif connected and not _was_connected:
		notify("已重新连接", Toast.Kind.SUCCESS)
	_was_connected = connected

# ---- 内部 ----

func _connect_if_needed(sig: Signal, callable: Callable) -> void:
	if not sig.is_connected(callable):
		sig.connect(callable)

func _build_screens() -> void:
	# 背景层第一个加：CanvasLayer 里后加的画在上面，背景必须最先生成。
	backdrop = BackdropScene.instantiate()
	add_child(backdrop)
	login_screen = LoginScene.instantiate()
	add_child(login_screen)
	shell = ShellScene.instantiate()
	add_child(shell)
	# 子界面：每个都独占一整屏，由 _apply_visibility 切换，彼此不认识对方。
	profile_screen = ProfileScene.instantiate()
	add_child(profile_screen)
	shop_screen = ShopScene.instantiate()
	add_child(shop_screen)
	bag_screen = BagScene.instantiate()
	add_child(bag_screen)
	result_overlay = ResultScene.instantiate()
	add_child(result_overlay)
	hud = HudScene.instantiate()
	add_child(hud)
	pause_screen = PauseScene.instantiate()
	add_child(pause_screen)
	# 进大厅的询问框：在大厅之上、提示之下（CanvasLayer 里后加的画在上面）。
	rejoin_prompt = RejoinPromptScene.instantiate()
	add_child(rejoin_prompt)
	# 反馈层最后加：CanvasLayer 里后加的画在上面，提示必须压在所有界面之上。
	toast = ToastScene.instantiate()
	add_child(toast)
	# 匹配状态条长在大厅外壳里（它是外壳底部那一行）。
	match_bar = MatchBarScene.instantiate()
	var slot: Node = shell.get_node_or_null("%StatusSlot")
	if slot != null:
		slot.add_child(match_bar)
	# 屏幕自己不认识彼此，仅持有 manager 的引用（渲染 + 发意图）。
	login_screen.bind(self)
	shell.bind(self)
	profile_screen.bind(self)
	shop_screen.bind(self)
	bag_screen.bind(self)
	match_bar.bind(self)
	result_overlay.bind(self)
	pause_screen.bind(self)
	rejoin_prompt.bind(self)
	_feed_pages()

func _set_state(next: State) -> void:
	if _state == next:
		return
	_state = next
	# 离开对局时收起设置层：否则鼠标会留在「未捕获」状态 —— 结算层上点不动按钮，
	# 或者回到大厅后忽然被锁住鼠标，两种都是卡死体验。
	if next != State.IN_MATCH:
		_set_paused(false)
	# 询问框只属于大厅/匹配前的时刻：一旦进了对局或结算，它必须消失（否则它会盖在
	# 战场上，而且那个「放弃对局」的按钮按下去时人已经在别的局里了）。
	if next != State.LOBBY and next != State.MATCHING:
		_close_rejoin_prompt()
	_apply_visibility()
	_feed_pages()
	state_changed.emit(next)

func _apply_visibility() -> void:
	# 大厅与子界面互斥：`_page` 为空时显示大厅入口，否则显示那一个子界面。
	var in_lobby_state := _state == State.LOBBY or _state == State.MATCHING
	if login_screen != null:
		login_screen.visible = _state == State.LOGIN
	if shell != null:
		shell.visible = in_lobby_state and _page == ""
	for page: String in ["profile", "shop", "bag"]:
		var node := _page_node(page)
		if node != null:
			node.visible = in_lobby_state and _page == page
	if backdrop != null:
		# 背景只给大厅与子界面看：登录页与结算层各有自己的遮罩，对局里是 3D 战场。
		backdrop.visible = in_lobby_state
	if result_overlay != null:
		result_overlay.visible = _state == State.RESULT
	if hud != null:
		hud.visible = _state == State.IN_MATCH
	if pause_screen != null:
		pause_screen.visible = _paused and _state == State.IN_MATCH
	if rejoin_prompt != null:
		# 只在入口页上弹：它问的是「要不要回到上一局」，与正在看的子界面无关，
		# 而子界面是整屏铺开的，两者叠在一起只会互相遮挡。
		rejoin_prompt.visible = _rejoin_prompt_open and in_lobby_state and _page == ""

# ---- 子界面 ----

## _open_rejoin_prompt 弹「你有一场没打完的局」询问框。
func _open_rejoin_prompt() -> void:
	_rejoin_prompt_open = true
	_apply_visibility()
	if rejoin_prompt != null:
		# 每次都用最新快照重填（已经在开着的框也一样）：掉线重连后 resume 会再问一次
		# match.pending，不能把上一轮的对局 id 留在框里。
		rejoin_prompt.open(_pending_match)

## _close_rejoin_prompt 收起询问框，并把焦点还给大厅 —— 焦点留在已经隐藏的按钮上，
## 键盘玩家按确认键会打到一个看不见的控件上。
func _close_rejoin_prompt() -> void:
	if not _rejoin_prompt_open:
		return
	_rejoin_prompt_open = false
	_apply_visibility()
	if rejoin_prompt != null:
		rejoin_prompt.close()
	if shell != null and shell.visible:
		shell.focus_first_entry()

## _page_node 取子界面节点。传空串取当前页（大厅首页时返回 null）。
func _page_node(page: String = "") -> Control:
	var key := _page if page == "" else page
	match key:
		"profile": return profile_screen
		"shop": return shop_screen
		"bag": return bag_screen
	return null

## _feed_pages 把快照喂给子界面（页面自己决定怎么渲染）。
## 三个页面吃的是两份数据：profile 吃 PlayerProfileReply，shop/bag 吃 LogicStateReply。
func _feed_pages() -> void:
	var key := _page
	if key == "":
		return
	var node := _page_node(key)
	if node == null or not node.has_method("render"):
		return
	if key == "profile":
		node.render(_profile)
	else:
		node.render(_logic)

## _resolve_pending 拿刚回来的状态给上一次动作一个确定的回音。
## 服务端没有「购买成功」这种应答消息（回的就是最新 state），所以判断依据是状态本身的变化：
## 拥有数增加 = 买到了；装备槽变成它 = 装上了。判不出来就明说失败，不假装成功。
func _resolve_pending() -> void:
	if _pending.is_empty():
		return
	var kind := String(_pending.get("kind", ""))
	var item_id := String(_pending.get("item_id", ""))
	if kind == "purchase":
		var item := _find_item(item_id)
		var owned := int(item.get("owned_quantity", 0))
		var bought := owned - int(_pending.get("owned_before", 0))
		var spent := int(_pending.get("coins_before", 0)) - int(_logic.get("coins", 0))
		if bought > 0:
			notify("已购买 %s ×%d（-%d 金币）" % [_item_name(item_id), bought, maxi(spent, 0)],
				Toast.Kind.SUCCESS)
		else:
			notify("购买失败：金币不足或服务端未接受", Toast.Kind.DANGER)
	elif kind == "equip":
		var equipped := String(_logic.get("equipped_primary_weapon", ""))
		if equipped == item_id:
			notify("已装备 %s" % _item_name(item_id) if item_id != "" else "已卸下主武器",
				Toast.Kind.SUCCESS)
		else:
			notify("装备失败：该物品不可装备或服务端未接受", Toast.Kind.DANGER)
	_pending = {}

## _find_item 从最近一份商城状态里找一件物品（界面文案要显示名与价格，光有 id 不够）。
func _find_item(item_id: String) -> Dictionary:
	for item: Variant in _logic.get("items", []):
		if String((item as Dictionary).get("item_id", "")) == item_id:
			return item
	return {}

func _item_name(item_id: String) -> String:
	var item := _find_item(item_id)
	return String(item.get("display_name", item_id))

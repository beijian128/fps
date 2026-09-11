extends SceneTree
## 端到端冒烟：注册 → 收到 LoginReply(token) → 断线 → resume → 进入对局（onMatched）。
##
## 需要活集群（etcd + nats + redis + gate/account/match/game 四进程）。
## 这是唯一验证「登录 → 匹配 → 对局」整条链路的测试，改动 account 服务、
## gate 的会话归属、match 的开局链路后必跑。
##
## 运行：
##   godot --headless --path <项目> --script res://tests/login_smoke.gd
##
## 不会假通过：GDScript 没有 try/catch，中途抛错的协程会静默停住，而「没有报错」
## 看起来就跟通过一样。所以每一步都往 _done 里打完成标记，收尾时逐个核对 ——
## 缺任何一步都判失败，哪怕一条 FAIL 都没打印出来。

const TIMEOUT_MS := 40000
const PASSWORD := "smokepass"

var _failures := 0
var _c: Node = null
var _done := {}
var _username := ""
var _phase := 0        # 0=等注册 1=等 resume 2=已登录，等 onMatched
var _token_first := "" # 注册时签发的凭证
var _token_resumed := ""

func _initialize() -> void:
	# 每次跑都从「本地没有凭证」开始。残留的 token 会让客户端在握手后直接 resume
	# 成上一轮的账号，注册这一步被整个跳过 —— 而它正是本测试的第一环。
	_clear_saved_credentials()
	# 新用户名避开与上一轮账号撞名（name_taken）。
	_username = "smoke_%d" % (Time.get_ticks_usec() % 100000000)
	_c = load("res://scripts/fps_client.gd").new()
	_c.login_result.connect(_on_login)
	_c.matched_received.connect(_on_matched)
	root.add_child(_c)
	_run()

func _clear_saved_credentials() -> void:
	for p in ["user://auth_token.txt", "user://last_username.txt"]:
		if FileAccess.file_exists(p):
			DirAccess.remove_absolute(ProjectSettings.globalize_path(p))

func _check(cond: bool, msg: String) -> void:
	if not cond:
		_failures += 1
		printerr("FAIL: " + msg)

func _fail(msg: String) -> void:
	_check(false, msg)

## _on_login 处理 register / resume 的统一应答（LoginReply）。
func _on_login(result: Dictionary) -> void:
	var ok := bool(result.get("ok", false))
	var reason := String(result.get("reason", ""))

	if not ok:
		# 握手后本地无凭证：客户端按约定发 no_token 让上层显示登录面板，
		# 这里代替 UI 直接注册一个新账号。
		if reason == "no_token" and _phase == 0:
			_done["prompted"] = true
			_c.send_register(_username, PASSWORD)
			return
		_fail("登录失败 reason=%s（phase=%d）" % [reason, _phase])
		_done["aborted"] = true
		return

	var token := String(result.get("token", ""))
	if _phase == 0:
		_done["registered"] = true
		_check(token != "", "注册应答必须带 token")
		_check(String(result.get("username", "")) == _username,
			"注册应答应回显用户名，得到 %s" % result.get("username", ""))
		_check(_c.client_token == token, "客户端应把 token 存进 client_token")
		_token_first = token
		_phase = 1
		# 模拟断线重连：丢掉当前连接重连，客户端会自己带着本地凭证走 resume。
		_c._force_reconnect()
	elif _phase == 1:
		_done["resumed"] = true
		_check(token != "", "resume 应答必须带 token")
		# 凭证在**每一次未绑定会话上的认证成功**时轮换（account.finishLogin →
		# store.IssueToken 写新 token 并删旧 token），resume 也不例外。所以这里
		# 期望的是「换了一份」，不是「还是原来那份」—— 旧 token 此刻已经作废，
		# 这正是单会话/顶号强制的权威手段。若哪天这条断言变成相等，说明轮换失效了。
		_check(token != _token_first, "resume 应轮换凭证，却拿回了同一个 token")
		_check(_c.client_token == token, "客户端应把轮换后的 token 存下来")
		_token_resumed = token
		_phase = 2
		# 到这里会话才真正绑定了账号。match.join 必须在绑定之后发：
		# 未绑定的 join 会被 match 静默忽略（见 match.Component.Join）。
		_c.send_match_join()
	else:
		_fail("phase=%d 时不应再收到成功的 LoginReply" % _phase)

func _on_matched(result: Dictionary) -> void:
	_done["matched"] = true
	_check(String(result.get("match_id", "")) != "", "onMatched 应带 match_id")
	_check(int(result.get("player_idx", -1)) >= 0, "onMatched 应带 player_idx")

func _run() -> void:
	var deadline := Time.get_ticks_msec() + TIMEOUT_MS
	while Time.get_ticks_msec() < deadline:
		if _done.has("aborted"):
			break
		if _phase >= 2 and _done.has("matched"):
			break
		await process_frame
	_finish()

func _finish() -> void:
	# 逐个核对完成标记：中途抛错/静默卡住的话这些标记就缺，不会假通过。
	_check(_done.has("prompted"), "握手后本地无凭证时应收到 no_token")
	_check(_done.has("registered"), "应完成注册并拿到 token")
	_check(_done.has("resumed"), "断线重连后应 resume 成功")
	_check(_done.has("matched"), "resume 之后应收到 onMatched（进入对局）")
	_check(not _done.has("aborted"), "链路中途失败（见上方 FAIL）")

	if _failures > 0:
		printerr("login_smoke: %d 项失败（user=%s phase=%d）" % [_failures, _username, _phase])
		quit(1)
	else:
		print("login_smoke: OK (user=%s rotated=%s)"
			% [_username, str(_token_first != _token_resumed)])
		quit(0)

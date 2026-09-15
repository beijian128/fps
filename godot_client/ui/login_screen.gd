extends Control
## 登录页：用户名/密码 + 登录/注册 + 错误行。
##
## 只做三件事：把服务端的 reason 翻成中文、请求在途时禁用按钮（单飞）、把意图交给
## `screen_manager`。协议细节（Request/Response、token 落盘）全在 fps_client 里。

## 服务端 reason → 玩家能看懂的中文。没有映射的 reason 一律显示「服务暂时不可用」，
## 免得把内部错误码漏到界面上。
const REASON_TEXT := {
	"bad_credentials": "用户名或密码错误",
	"name_taken": "该用户名已被注册",
	"bad_username": "用户名需 3-16 位字母/数字/下划线",
	"bad_password": "密码需 6-64 位",
	"rate_limited": "操作过于频繁，请稍后再试",
	"timeout": "服务器无响应，请重试",
	"no_connection": "未连接到服务器",
	"internal": "服务暂时不可用",
}

var _manager: Node = null
var _busy := false

func _ready() -> void:
	%Login.pressed.connect(func() -> void: submit(false))
	%Register.pressed.connect(func() -> void: submit(true))
	%Pass.text_submitted.connect(func(_text: String) -> void: submit(false))
	_clear_form()

## bind 由一个 manager 调用（screen_manager 建好场景后立刻绑定）。
func bind(manager: Node) -> void:
	_manager = manager
	manager.state_changed.connect(_on_state_changed)

# ---- 查询（测试与 shell 用）----

func is_busy() -> bool: return _busy
func error_text() -> String: return %Error.text
func username_text() -> String: return %User.text

## 测试用的输入注入：无头环境下没有真实键盘，直接设 LineEdit 文本即可。
func set_username_for_test(value: String) -> void:
	%User.text = value

func set_password_for_test(value: String) -> void:
	%Pass.text = value

# ---- 意图 ----

## submit 提交登录/注册。请求在途时忽略重复点击（单飞），空字段直接给提示。
func submit(register: bool) -> void:
	if _busy:
		return
	var user: String = (%User as LineEdit).text.strip_edges()
	var password: String = (%Pass as LineEdit).text
	if user.is_empty() or password.is_empty():
		_show_error("请填写用户名和密码")
		return
	_busy = true
	_show_error("")   # 清掉上一次的红色错误行
	_set_buttons_disabled(true)
	if register:
		_manager.intent_register(user, password)
	else:
		_manager.intent_login(user, password)

## show_failure 由 screen_manager 在登录失败时调用（reason 是服务端原因码）。
func show_failure(reason: String) -> void:
	_busy = false
	_set_buttons_disabled(false)
	_show_error(REASON_TEXT.get(reason, "服务暂时不可用"))

## show_silently 用于「本地没有凭证」这种不是错误的情况：安静地停在登录页。
func show_silently() -> void:
	_busy = false
	_set_buttons_disabled(false)
	_show_error("")

# ---- 内部 ----

func _on_state_changed(state: int) -> void:
	# 离开登录页时把上一轮的输入与错误清干净（下一个账号不该看到上一个人的用户名）。
	var screen_manager := preload("res://ui/screen_manager.gd")
	if state != screen_manager.State.LOGIN:
		_clear_form()

func _clear_form() -> void:
	_busy = false
	_set_buttons_disabled(false)
	_show_error("")
	if %Pass != null:
		%Pass.text = ""

func _show_error(text: String) -> void:
	%Error.text = text

func _set_buttons_disabled(disabled: bool) -> void:
	%Login.disabled = disabled
	%Register.disabled = disabled

extends RefCounted
## 冒烟测试用的「第二个客户端」：单人兜底删除后，匹配必须凑满两个真实玩家才会开局，
## 所以每个冒烟测试都要再起一个真实 FpsClient 并让它注册 + 入队。
##
## 用法（在 SceneTree 脚本里）：
##   var peer := PairHelper.new(root, "smokeb")
##   ... 等 peer.registered / peer.joined ...
##   ... 再等双方 matched ...
##
## 关于凭证文件：Godot 的 user:// 是每项目一个目录，两个客户端共用 auth_token.txt，
## 后注册的那个会把文件覆盖成自己的 token。这不影响本进程（两边的 client_token 注册后
## 就常驻内存，重连用它而不是重新读盘），但下一次跑测试前必须清掉该文件 —— 三个冒烟
## 测试本来就在开头清。

const FpsClient := preload("res://scripts/fps_client.gd")
const PASSWORD := "smokepass"

var client: Node = null
var username := ""
var registered := false
var joined := false
var matched := false
var match_result := {}
var failed_reason := ""

func _init(parent: Node, prefix: String) -> void:
	# 用户名必须是 3–16 位字母/数字/下划线（account 服务的校验），所以这里把前缀截断，
	# 保证「前缀_随机后缀」永远不会超过 16 位。
	var suffix := str(Time.get_ticks_usec() % 100000)
	var head := prefix
	var room := 16 - 1 - suffix.length()
	if head.length() > room:
		head = head.substr(0, room)
	username = "%s_%s" % [head, suffix]
	# 关键：先清掉磁盘上的凭证再建客户端。否则新客户端的 _ready 会读到**第一个客户端
	# 刚写入的 token**、直接 resume 成同一个账号 —— 那是顶号，会把先登录的那个踢下线
	# （现象就是「peer 刚注册，primary 的 WS 立刻断开」）。两个客户端的 token 注册后都
	# 常驻内存，清盘不影响它们自己的后续重连。
	var token_path := "user://auth_token.txt"
	if FileAccess.file_exists(token_path):
		DirAccess.remove_absolute(ProjectSettings.globalize_path(token_path))
	client = FpsClient.new()
	# 信号在 add_child 之前连上：握手应答可能在下一帧就回来。
	client.login_result.connect(_on_login)
	client.matched_received.connect(_on_matched)
	parent.add_child(client)

func playing() -> bool:
	return failed_reason == ""

func _on_login(result: Dictionary) -> void:
	if bool(result.get("ok", false)):
		registered = true
		client.send_match_join()
		joined = true
		return
	var reason := String(result.get("reason", ""))
	if reason == "no_token" and not registered and failed_reason == "":
		client.send_register(username, PASSWORD)
		return
	failed_reason = reason

func _on_matched(result: Dictionary) -> void:
	matched = true
	match_result = result

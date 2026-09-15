extends SceneTree
## 对局结束（onMatchEnded）必须让客户端进入「已结束」状态：停掉接收看门狗、清空本地
## 世界，且**不触发重连、不自动重新入队**。
##
## 这是去掉自动重开之后最容易被忽略的一条：服务端实例一终结，帧流就断；若客户端仍认为
## 自己在对局里，2.5 秒的接收看门狗会把「本局结束」误判成掉线，于是重连、resume、重新
## 入队 —— 玩家看到的是「打完了 → 闪一下重连 → 又排上队」。
##
## 运行：godot --headless --path godot_client --script res://tests/match_ended_test.gd

const MainScene := preload("res://scenes/main.tscn")

var _failures := 0
var _done := {}

func _initialize() -> void:
	_run()

func _run() -> void:
	var main: Node = MainScene.instantiate()
	root.add_child(main)
	# _initialize 阶段 root 还没进树，_ready 不会触发（HUD 节点全是 null）；等两帧让
	# 场景真正入树、_ready 跑完（与 game_frame_test.gd / reconnect_cleanup_test.gd 同一套路）。
	await process_frame
	await process_frame

	# 用场景里的**真实** FpsClient：这样连「main.gd 有没有把信号接上」一起测到。
	# （不能用替换 fps_client 的假客户端 —— @onready 会在 _ready 时把它覆盖回 $FpsClient。）
	var client: Node = main.fps_client
	_check(client != null, "场景里应有 FpsClient 节点")

	client.connection_changed.emit(true)
	# 进入对局：匹配成功 → 客户端会请求 full 帧（离线时 send_resync 是空操作）。
	client.matched_received.emit({"match_id": "m1", "game_server_id": "g1", "player_idx": 0})
	_check(main._matched, "onMatched 之后应处于对局态")
	_check(not main.conn_label.visible, "onMatched 之后应把「正在匹配」提示收起来")

	# 造一点本地世界状态，验证结算时会清掉。直接塞渲染节点即可 ——
	# 合成整帧属于 game_frame_test.gd 的职责（它驱动 _on_frame）。
	main._entities[100] = Node3D.new()
	main.add_child(main._entities[100])
	_done["seeded"] = true

	client.match_ended_received.emit({
		"match_id": "m1",
		"winner_slot": 0,
		"duration_seconds": 84,
		"slots": [
			{"uid": "7", "kills": 10, "deaths": 3},
			{"uid": "8", "kills": 3, "deaths": 10},
		],
	})
	_done["ended"] = true

	_check(not main._matched, "收到 onMatchEnded 后必须离开对局态（否则看门狗会误判断线）")
	_check(main._entities.is_empty(), "结算应清空本地实体缓存")
	_check(main.conn_label.visible, "结算应给出可见的临时提示（正式界面在 B 子项目）")
	_check(int(main._my_player_idx) == 0, "结算不应改动本客户端的槽位")
	# 匹配等待看门狗必须处于「未武装」状态：结算不是重新排队。
	_check(client._match_retry_at == 0.0, "结算不得武装匹配等待看门狗（否则会自己重新入队）")

	if not _done.has("ended"):
		_failures += 1
		printerr("FAIL: 用例没跑完（中途抛错了？）")
	if _failures > 0:
		printerr("match_ended_test: %d 项失败" % _failures)
		quit(1)
	else:
		print("match_ended_test: OK")
		quit(0)

func _check(cond: bool, msg: String) -> void:
	if not cond:
		_failures += 1
		printerr("FAIL: " + msg)

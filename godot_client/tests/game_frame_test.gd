extends SceneTree
## main.gd 渲染路径回归：加载真实场景、手动喂合成帧，断言节点的新增/更新/销毁，
## 以及两条时序要求 —— 本地/远端插值、以及「一帧内最后一次写必须是插值结果」。
## 服务端缺席也无妨：这里只驱动 _on_frame，不碰 WebSocket。
##
## 注意：GDScript 没有 try/catch，测试函数中途抛错会被吞掉、整个用例"假绿"，
## 所以沿用 frame_decode_test.gd 的完成标记模式。

const SCHEMA := [
	{"id": 1, "name": "Pos", "kind": 5},
	{"id": 2, "name": "Health", "kind": 0},
	{"id": 3, "name": "Projectile", "kind": 2},
	{"id": 4, "name": "Body.Mat", "kind": 1},
	{"id": 5, "name": "Body.Kind", "kind": 1},
	{"id": 6, "name": "Body.Size", "kind": 5},
	{"id": 7, "name": "Body.Static", "kind": 2},
	{"id": 8, "name": "Body.Active", "kind": 2},
	{"id": 9, "name": "Rot", "kind": 6},
	{"id": 10, "name": "Game.Winner", "kind": 1},
	{"id": 11, "name": "Player.Idx", "kind": 1},
	{"id": 12, "name": "Player.Kills", "kind": 1},
	{"id": 13, "name": "Player.Deaths", "kind": 1},
	{"id": 14, "name": "Facing", "kind": 0},
]

# 用例完成标记。GDScript 无 try/catch：某个测试函数中途抛错会静默返回、_failures 仍为 0，
# 整个用例就"假绿"—— 每个函数末尾打标记，这里逐个核对。
var _failures := 0
var _done := {}
var _main: Node = null

func _initialize() -> void:
	_run()

func _run() -> void:
	_main = load("res://scenes/main.tscn").instantiate()
	root.add_child(_main)
	# _initialize 阶段 root 还没进树，_ready 不会触发（HUD 节点全是 null）；等两帧让
	# 场景真正入树、_ready 跑完（与 reconnect_cleanup_test.gd 同一套路）。
	await process_frame
	await process_frame
	_main._on_matched({"player_idx": 0})
	_test_full_frame_creates_bodies()
	_test_delta_updates_and_destroys()
	_test_last_write_in_frame_is_interpolated()
	_test_local_player_and_remote_yaw_interpolated()
	_test_render_interpolated_is_idempotent()
	_test_hud_reads_pvp_attrs()
	_test_destroy_for_unknown_id_is_safe()
	for name: String in ["full", "delta", "interp_node", "interp_player", "idempotent", "coin", "unknown"]:
		if not _done.has(name):
			_failures += 1
			printerr("FAIL: 用例 %s 没跑完（中途抛错了？）" % name)
	if _failures > 0:
		printerr("game_frame_test: %d 项失败" % _failures)
		quit(1)
	else:
		print("game_frame_test: OK")
		quit(0)

func _check(cond: bool, msg: String) -> void:
	if not cond:
		_failures += 1
		printerr("FAIL: " + msg)

func _attr(id: int, d: Dictionary) -> Dictionary:
	var out := {"id": id}
	out.merge(d)
	return out

## 一个可渲染刚体的最小属性集（Body.Kind/Size/Static/Active/Mat + Pos/Rot）。
func _body_attrs(kind: int, size: Array, pos: Vector3, mat: int = 1) -> Array:
	return [
		_attr(5, {"i": kind}),                        # Body.Kind
		_attr(6, {"f": size}),                        # Body.Size
		_attr(7, {"b": true}),                        # Body.Static
		_attr(8, {"b": true}),                        # Body.Active
		_attr(4, {"i": mat}),                         # Body.Mat
		_attr(1, {"f": [pos.x, pos.y, pos.z]}),       # Pos
		_attr(9, {"f": [0.0, 0.0, 0.0, 1.0]}),        # Rot
	]

## 玩家实体属性：刚体属性之外再带 Player.Idx / Health / Facing / Player.Kills /
## Player.Deaths（服务端声明并初始化时就会下发这五项）。
## 必须带全刚体属性 —— _refresh_derived 会把它们当刚体渲染，_build_body_dict 会逐项取用。
func _player_attrs(idx: int, pos: Vector3, facing: float,
		hp := 100.0, kills := 0, deaths := 0) -> Array:
	var out := _body_attrs(0, [0.4, 1.8, 0.4], pos, 0)
	out.append(_attr(11, {"i": idx}))                 # Player.Idx
	out.append(_attr(2, {"f": [hp]}))                 # Health
	out.append(_attr(14, {"f": [facing]}))            # Facing
	out.append(_attr(12, {"i": kills}))               # Player.Kills
	out.append(_attr(13, {"i": deaths}))              # Player.Deaths
	return out

## 一帧只含一个刚体（id 100）。
func _test_full_frame_creates_bodies() -> void:
	_main._on_frame({"full": true, "step": 1, "schema": {"fields": SCHEMA, "version": 1}, "entities": [
		{"id": 100, "set": _body_attrs(0, [0.5, 0.5, 0.5], Vector3(1, 2, 3))},
	]})
	_check(_main._entities.has(100), "全量帧应在场景里建出实体 100 的节点")
	_check((_main._entities[100].global_position - Vector3(1, 2, 3)).length() < 0.001,
		"节点应落在 Pos 上")
	_done["full"] = true

## 增量帧：移动 + 销毁。
func _test_delta_updates_and_destroys() -> void:
	_main._on_frame({"full": false, "entities": [
		{"id": 100, "set": [_attr(1, {"f": [4.0, 5.0, 6.0]})]},
	]})
	_check((_main._body_xform[100]["pos"] - Vector3(4, 5, 6)).length() < 0.001,
		"增量帧应更新刚体变换缓存")

	_main._on_frame({"full": false, "entities": [
		{"id": 100, "destroy": true},
	]})
	_check(not _main._entities.has(100), "destroy 后渲染节点必须被删掉")
	_done["delta"] = true

## 钉住修正 #3：Godot 先跑父节点 _process（内含 _render_interpolated）再跑子节点
## FpsClient._process，后者同步 emit frame_received → _on_frame → _reconcile_scene 把
## **原始**变换写进节点。若 _on_frame 收尾不再插值一次，本渲染帧画的就永远是 raw 值，
## 下一帧才被拉回去 —— 每来一帧抖一下。
func _test_last_write_in_frame_is_interpolated() -> void:
	var a := Vector3(0, 0, 0)
	var b := Vector3(20, 0, 0)
	_main._on_frame({"full": false, "entities": [
		{"id": 101, "set": _body_attrs(0, [0.5, 0.5, 0.5], a)},
	]})
	# 再推一帧把 101 瞬移到 B：_on_frame 内 prev=A、cur=B，_reconcile_scene 先把节点写成 B，
	# 收尾 _render_interpolated 用刚到达的 alpha(≈0) 把节点拉回 ≈A。
	_main._on_frame({"full": false, "entities": [
		{"id": 101, "set": [_attr(1, {"f": [b.x, b.y, b.z]})]},
	]})
	var node: Node3D = _main._entities.get(101)
	_check(node != null, "实体 101 的渲染节点应存在")
	if node != null:
		var d_a := (node.global_position - a).length()
		var d_b := (node.global_position - b).length()
		_check(d_b > 0.5,
			"帧内最后一次写不应是原始缓存值 %s；实际 %s" % [b, node.global_position])
		_check(d_a < d_b,
			"帧内最后一次写应是插值结果（更接近 prev %s 而非 cur %s）；实际 %s"
			% [a, b, node.global_position])
	_done["interp_node"] = true

## 钉住修正 #2：本地玩家位置与远端朝向必须插值。
## 之前 _player_pos 收到帧就直接赋 raw，第一人称相机与第三人称 avatar 按 20 Hz 跳步
## （走 0.4 m/步、跑 0.7 m/步）；_remote_yaw 同理 —— 远端 avatar 台阶式旋转，
## 180° 附近还会因角度回绕翻一下。
func _test_local_player_and_remote_yaw_interpolated() -> void:
	var local_a := Vector3(0, 0.2, 18)
	var local_b := Vector3(0, 0.2, 10)
	var remote_a := Vector3(0, 0.2, -18)
	var remote_b := Vector3(0, 0.2, -10)
	_main._on_frame({"full": false, "entities": [
		{"id": 300, "set": _player_attrs(0, local_a, 0.0)},
		{"id": 301, "set": _player_attrs(1, remote_a, 0.0)},
	]})
	# 第二帧：本地瞬移到 B；远端瞬移到 B 且 Facing 从 0 转到 PI（正好 180°）。
	_main._on_frame({"full": false, "entities": [
		{"id": 300, "set": [_attr(1, {"f": [local_b.x, local_b.y, local_b.z]})]},
		{"id": 301, "set": [
			_attr(1, {"f": [remote_b.x, remote_b.y, remote_b.z]}),
			_attr(14, {"f": [PI]}),
		]},
	]})
	var d_local_a: float = (_main._player_pos - local_a).length()
	var d_local_b: float = (_main._player_pos - local_b).length()
	_check(d_local_a < d_local_b,
		"本地玩家位置应是插值结果（更接近上一帧 %s 而非 raw %s）；实际 %s"
		% [local_a, local_b, _main._player_pos])
	# 远端朝向必须走 lerp_angle：raw 赋值会让它从 0 直接硬跳到 PI。
	# 断言的真正意图是「更接近上一帧 0 而不是 raw 新值 PI」，故容忍度取 PI/2（两者
	# 的中点）。原来写 < 0.1 只有约 1 ms 余量 —— _frame_time 是整数毫秒、alpha 只能以
	# 0.02 步进量化，机器一忙就会 flaky。
	var remote_yaw: float = _main._remote_yaw
	var yaw_err: float = absf(angle_difference(0.0, remote_yaw))
	_check(yaw_err < PI * 0.5,
		"远端朝向应是插值结果（更接近上一帧 0，而不是 raw 值 PI）；实际 %s" % remote_yaw)
	_done["interp_player"] = true

## 钉住修正 #2 的 target/display 拆分（幂等）：_render_interpolated 一帧内会被调两次
## （_process 一次、_on_frame 收尾一次）。若像就地 lerp（`_player_pos = _player_pos.lerp(target)`）
## 那样让显示值同时充当插值输入，第二次调用会拿「结果」再插一次，位置继续漂移。
## 其它断言都在 alpha≈0 跑 —— 就地 lerp 在那种情形下也照样通过，只有本条能区分两种实现。
func _test_render_interpolated_is_idempotent() -> void:
	# 先造出 prev≠target 的插值中状态：一帧落在 18，下一帧落在 10。
	# 显式给 _player_pos 赋值，避免依赖前序用例留下的显示值。
	_main._player_pos = Vector3(0, 0.2, 18)
	_main._on_frame({"full": false, "entities": [
		{"id": 300, "set": [_attr(1, {"f": [0.0, 0.2, 18.0]})]},
	]})
	_main._on_frame({"full": false, "entities": [
		{"id": 300, "set": [_attr(1, {"f": [0.0, 0.2, 10.0]})]},
	]})
	# 把 _frame_time 回拨半个 TICK（TICK=0.05 → 0.025），使 alpha≈0.5。
	# 只有 alpha 明显落在 0/1 之间，二次插值造成的漂移才看得出来。
	_main._frame_time = Time.get_ticks_msec() / 1000.0 - 0.025
	_main._render_interpolated()
	var first: Vector3 = _main._player_pos
	_main._render_interpolated()
	var second: Vector3 = _main._player_pos
	# 幂等：第二次调用必须与第一次完全一致（显示值不能又当插值输入）。
	_check(first.is_equal_approx(second),
		"连续两次 _render_interpolated 必须幂等（target/display 拆分）；第一次 %s 第二次 %s"
		% [first, second])
	# 反向保护：alpha≈0.5 时结果确实落在 prev 与 target 之间，证明上面不是恒等式。
	_check(first.distance_to(Vector3(0, 0.2, 10.0)) > 0.1,
		"alpha≈0.5 时玩家位置应离开 raw target (0,0.2,10)；实际 %s" % first)
	_done["idempotent"] = true

## HUD 读数全部走属性名：本机战绩挂在玩家实体上（Player.Kills / Player.Deaths），
## 对局结果是全局单例上的 Game.Winner。这几条属性名写错不会报错、只会显示成恒定值，
## 所以在这里钉住。
func _test_hud_reads_pvp_attrs() -> void:
	# 300 是本机（player_idx 0）、301 是对手；400 是全局单例。
	_main._on_frame({"full": false, "entities": [
		{"id": 300, "set": _player_attrs(0, Vector3(0, 0.2, 18), 0.0, 66.0, 4, 2)},
		{"id": 301, "set": _player_attrs(1, Vector3(0, 0.2, -18), PI, 32.0)},
		{"id": 400, "set": [_attr(10, {"i": -1})]},   # Game.Winner
	]})
	_check(_main._hud_kills == 4, "HUD 击杀数应取本机 Player.Kills，得到 %d" % _main._hud_kills)
	_check(_main._hud_deaths == 2, "HUD 死亡数应取本机 Player.Deaths，得到 %d" % _main._hud_deaths)
	_check(absf(_main._hud_opp_health - 32.0) < 0.01,
		"HUD 对手血量应取另一名玩家的 Health，得到 %s" % _main._hud_opp_health)
	_check(absf(_main.health_bar.value - 66.0) < 0.01,
		"血条应取本机 Health，得到 %s" % _main.health_bar.value)
	_check(_main._hud_winner == -1, "winner=-1 表示进行中，得到 %d" % _main._hud_winner)
	_check(_main._match_over_text() == "", "进行中不应有结果文案")

	# 分出胜负：0 号获胜 → 本机（0 号）看到 YOU WIN。
	_main._on_frame({"full": false, "entities": [_attr_entity(400, _attr(10, {"i": 0}))]})
	_check(_main._hud_winner == 0, "HUD 应读到大局已定，得到 %d" % _main._hud_winner)
	_check(_main._match_over_text() == "YOU WIN\n",
		"本机获胜应显示 YOU WIN，得到 %s" % _main._match_over_text())
	# 战绩属性不该让玩家实体多长出一个刚体节点（玩家由 Avatar 渲染，
	# _refresh_derived 只把带 Body.Kind 的实体算作刚体）。
	var player_nodes := 0
	for id: Variant in _main._entities:
		if int(id) == 300 or int(id) == 301:
			player_nodes += 1
	_check(player_nodes == 2, "玩家实体应各有一个刚体渲染节点，得到 %d" % player_nodes)
	_done["coin"] = true


## _attr_entity 包一个只带单个属性的实体增量。
func _attr_entity(id: int, av: Dictionary) -> Dictionary:
	return {"id": id, "set": [av]}

## 服务端可能对客户端从未见过的 id 发 destroy（比如客户端刚 resync 完）。
## 真正的断言是「没抛错」—— 由上面的 _done 完成标记保证（抛错就到不了这一行）。
func _test_destroy_for_unknown_id_is_safe() -> void:
	_main._on_frame({"full": false, "entities": [
		{"id": 999, "destroy": true},
	]})
	_done["unknown"] = true

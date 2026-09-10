extends SceneTree
## world_store.gd 的语义测试：full 覆盖、removed、destroy、按名字取值。

const WorldStore := preload("res://scripts/world_store.gd")

const SCHEMA := [
	{"id": 1, "name": "Pos", "kind": 5},        # Vec3
	{"id": 2, "name": "Health", "kind": 0},     # F32
	{"id": 3, "name": "Enemy", "kind": 2},      # Bool
	{"id": 4, "name": "Body.Mat", "kind": 1},   # I32
]

var _failures := 0

func _init() -> void:
	_test_full_replaces_everything()
	_test_set_creates_and_updates()
	_test_removed_and_destroy()
	_test_entities_with()
	if _failures > 0:
		printerr("world_store_test: %d 项失败" % _failures)
		quit(1)
	else:
		print("world_store_test: OK")
		quit(0)

func _check(cond: bool, msg: String) -> void:
	if not cond:
		_failures += 1
		printerr("FAIL: " + msg)

func _fresh() -> WorldStore:
	var ws: WorldStore = WorldStore.new()
	ws.apply_schema(SCHEMA)
	return ws

func _test_full_replaces_everything() -> void:
	var ws := _fresh()
	ws.apply_frame({"full": false, "entities": [
		{"id": 7, "set": [{"id": 2, "f": [50.0]}]},
	]})
	_check(ws.attr(7, "Health") == 50.0, "增量应写入 Health")

	# full 帧是权威的：先清空再整体覆盖
	ws.apply_frame({"full": true, "entities": [
		{"id": 9, "set": [{"id": 2, "f": [10.0]}]},
	]})
	_check(not ws.has_attr(7, "Health"), "full 帧应清掉之前累积的实体")
	_check(ws.attr(9, "Health") == 10.0, "full 帧应写入新实体")

func _test_set_creates_and_updates() -> void:
	var ws := _fresh()
	ws.apply_frame({"full": false, "entities": [
		{"id": 3, "set": [
			{"id": 1, "f": [1.0, 2.0, 3.0]},
			{"id": 3, "b": true},
			{"id": 4, "i": 9},
		]},
	]})
	var pos: Array = ws.attr(3, "Pos")
	_check(pos.size() == 3 and pos[2] == 3.0, "向量属性应还原成 3 元数组")
	_check(ws.attr(3, "Enemy") == true, "bool 属性")
	_check(ws.attr(3, "Body.Mat") == 9, "int 属性")
	_check(ws.entities_with("Enemy") == [3], "entities_with 应找到该实体")

	# 再推一帧只改 Health：Pos 应保持不变
	ws.apply_frame({"full": false, "entities": [
		{"id": 3, "set": [{"id": 2, "f": [77.0]}]},
	]})
	_check(ws.attr(3, "Health") == 77.0, "新属性应写入")
	_check((ws.attr(3, "Pos") as Array)[0] == 1.0, "未提及的属性不应被清掉")

func _test_removed_and_destroy() -> void:
	var ws := _fresh()
	ws.apply_frame({"full": false, "entities": [
		{"id": 3, "set": [{"id": 3, "b": true}, {"id": 2, "f": [5.0]}]},
		{"id": 4, "set": [{"id": 2, "f": [5.0]}]},
	]})
	var res: Dictionary = ws.apply_frame({"full": false, "entities": [
		{"id": 3, "removed": [3]},
		{"id": 4, "destroy": true},
	]})
	_check(not ws.has_attr(3, "Enemy"), "removed 应删掉该属性")
	_check(ws.attr(3, "Health") == 5.0, "removed 不应影响其他属性")
	_check(not ws.has_attr(4, "Health"), "destroy 应删掉整个实体")
	_check(not ws.entity_ids().has(4), "destroy 后实体不应还在列表里")

	var destroyed: Array = res["destroyed"]
	_check(destroyed.size() == 1, "应只报告 1 个销毁事件，得到 %d" % destroyed.size())
	_check(int((destroyed[0] as Dictionary)["id"]) == 4, "销毁的应是实体 4")
	_check((destroyed[0] as Dictionary)["attrs"].has("Health"),
		"销毁事件必须带上消失前的属性，渲染层靠它判断消失的是弹丸还是金币")

func _test_entities_with() -> void:
	var ws := _fresh()
	ws.apply_frame({"full": true, "entities": [
		{"id": 1, "set": [{"id": 3, "b": true}]},
		{"id": 2, "set": [{"id": 3, "b": true}]},
		{"id": 3, "set": [{"id": 2, "f": [1.0]}]},
	]})
	var found := ws.entities_with("Enemy")
	found.sort()
	_check(found == [1, 2], "entities_with 应返回全部带该属性的实体")

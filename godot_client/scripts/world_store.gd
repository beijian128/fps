extends RefCounted
## 客户端本地世界状态：实体 ID → { 属性名: 值 }，外加服务端下发的属性表（schema）。
##
## 服务端推的是增量（只含本帧变化的属性），这里负责累积成完整世界。full 帧是
## 权威的：先整体清空再应用，因此重连 / 首次进入只要收到 full 帧就必然与
## 服务端一致，不需要额外的对齐逻辑。
##
## 属性只按「名字」取用，不认识的新属性照常存进来、只是不渲染 —— 这正是
## 服务端新增同步属性不需要改动客户端的原因。

# 与 Go 侧 replication.Kind 取值一一对应
const KIND_F32 := 0
const KIND_I32 := 1
const KIND_BOOL := 2
const KIND_STR := 3
const KIND_VEC2 := 4
const KIND_VEC3 := 5
const KIND_VEC4 := 6

var schema_version := 0

var _entities := {}   # int -> Dictionary（属性名 -> 值；向量是 Array[float]）
var _names := {}      # int(属性 ID) -> String(属性名)
var _kinds := {}      # int(属性 ID) -> int(kind)

## apply_schema 应用 full 帧里携带的属性表。服务端每次下发 full 帧都会带一份，
## 重复应用是幂等的。
func apply_schema(fields: Array) -> void:
	_names.clear()
	_kinds.clear()
	for f: Variant in fields:
		var d: Dictionary = f
		var id := int(d.get("id", 0))
		var name := String(d.get("name", ""))
		if id <= 0 or name == "":
			continue
		_names[id] = name
		_kinds[id] = int(d.get("kind", KIND_F32))

## apply_frame 应用一帧同步消息。返回 {"destroyed": [{"id": int, "attrs": {...}}]} ——
## 带着消失前的属性，渲染层据此判断消失的是弹丸（爆闪）还是金币（拾取音）。
func apply_frame(frame: Dictionary) -> Dictionary:
	if bool(frame.get("full", false)):
		_entities.clear()

	var destroyed: Array = []
	for e: Variant in frame.get("entities", []):
		var ed: Dictionary = e
		var id := int(ed.get("id", 0))
		if id == 0:
			continue
		var store: Dictionary = _entities.get(id, {})
		if bool(ed.get("destroy", false)):
			# 先把消失前的属性快照带出去，再丢掉本地副本。
			destroyed.append({"id": id, "attrs": store})
			store = {}
			_entities.erase(id)
		# 注意：destroy 之后**不能** continue。服务端在同一帧里销毁并重建（场景重置后
		# 刚体 id 会从头复用）时，同一个 EntityDelta 里既有 destroy 也有新实体的 set；
		# 丢掉 set 客户端就再也收不到重建，世界会一直空着。
		for cid: Variant in ed.get("removed", []):
			var rname: String = _names.get(int(cid), "")
			if rname != "":
				store.erase(rname)
		for av: Variant in ed.get("set", []):
			var d: Dictionary = av
			var cid := int(d.get("id", 0))
			var name: String = _names.get(cid, "")
			if name == "":
				continue # 未知属性：前后端 schema 不一致，忽略
			store[name] = _decode_value(int(_kinds.get(cid, KIND_F32)), d)
		# 只写回真正有内容的条目：全是未知属性的 delta 或空 delta 不该在 store 里留下
		# 一个空实体，否则 entity_ids() 会报出幻影 id、渲染层会建空节点。
		if store.is_empty():
			_entities.erase(id)
		else:
			_entities[id] = store
	return {"destroyed": destroyed}

## attr 返回实体的属性值；实体或属性不存在时返回 null。
func attr(entity_id: int, name: String) -> Variant:
	var store: Dictionary = _entities.get(entity_id, {})
	return store.get(name, null)

func has_attr(entity_id: int, name: String) -> bool:
	var store: Dictionary = _entities.get(entity_id, {})
	return store.has(name)

func entity_ids() -> Array:
	return _entities.keys()

## entities_with 返回带指定属性的全部实体 ID（标记类属性如 "Enemy" / "Player.Idx"
## 用它做查询）。
func entities_with(name: String) -> Array:
	var out: Array = []
	for id: Variant in _entities:
		if (_entities[id] as Dictionary).has(name):
			out.append(id)
	return out

func clear() -> void:
	_entities.clear()

func _decode_value(kind: int, d: Dictionary) -> Variant:
	match kind:
		KIND_F32:
			var f: Array = d.get("f", [])
			return float(f[0]) if f.size() > 0 else 0.0
		KIND_I32:
			return int(d.get("i", 0))
		KIND_BOOL:
			return bool(d.get("b", false))
		KIND_STR:
			return String(d.get("s", ""))
		KIND_VEC2, KIND_VEC3, KIND_VEC4:
			var f: Array = d.get("f", [])
			var n := 2
			if kind == KIND_VEC3:
				n = 3
			elif kind == KIND_VEC4:
				n = 4
			var out: Array = []
			for i in n:
				out.append(float(f[i]) if i < f.size() else 0.0)
			return out
	return null

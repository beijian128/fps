extends RefCounted
## 客户端本机偏好：鼠标灵敏度 / 界面缩放 / 受击反馈强度 / HUD 安全区。
##
## 与 `theme/tokens.gd` 的分工：tokens 是**所有玩家共享的视觉真相**（改它要重新生成主题），
## 这里每一项都是**每个玩家各自的本机开关**（只影响自己，存 `user://settings.cfg`）。
## 两者混在一起，会让「我把字号调大了」变成「所有玩家的界面都变了」。
##
## 谁改谁负责应用：界面只调 `set_value`，由 `screen_manager.apply_settings()` 一次性推给
## 窗口（界面缩放）与 HUD（安全区）；灵敏度与受击强度由 `main.gd` 每次用到时自己读。

const PATH := "user://settings.cfg"
const SECTION := "prefs"

## 每个可调项的区间与步长：设置面板的滑块直接读它，避免「面板」与「偏好」两处各写一套边界。
const RANGES := {
	"mouse_sensitivity": {"min": 0.2, "max": 3.0, "step": 0.05},
	"ui_scale": {"min": 0.8, "max": 1.5, "step": 0.05},
	"hit_flash": {"min": 0.0, "max": 1.0, "step": 0.1},
	"safe_area_pct": {"min": 0.0, "max": 0.10, "step": 0.005},
}

## 默认值。
## - 安全区 5%：静态 HUD 落在 90% 的「标题安全区」内。电视/投影会裁掉边缘 3%–10%，
##   贴着 16 px（≈1.5%）的血条与击杀播报在那些设备上会被切掉；纯 PC 贴边想拉回 0 就在设置里拉。
## - 受击反馈默认拉满（与旧行为一致）：光敏 / 晕动玩家可以把整层全屏红闪关掉，
##   命中音与准星反馈照常，信息不会因此丢失。
const DEFAULTS := {
	"mouse_sensitivity": 1.0,
	"ui_scale": 1.0,
	"hit_flash": 1.0,
	"safe_area_pct": 0.05,
}

var _values := {}
var _persist := true

## persist = false 供测试使用：既不读也不写 `user://settings.cfg`，避免测试污染玩家的真实偏好。
func _init(persist: bool = true) -> void:
	_persist = persist
	_values = DEFAULTS.duplicate()
	if persist:
		_load()

## keys 全部可调项（顺序稳定，设置面板按它排队）。
func keys() -> Array:
	return DEFAULTS.keys()

func value(key: String) -> float:
	return float(_values.get(key, DEFAULTS.get(key, 0.0)))

func set_value(key: String, v: float) -> void:
	if not DEFAULTS.has(key):
		return
	_values[key] = _clamp(key, v)
	if _persist:
		_save()

func reset() -> void:
	_values = DEFAULTS.duplicate()
	if _persist:
		_save()

# ---- 内部 ----

func _clamp(key: String, v: float) -> float:
	var r: Dictionary = RANGES.get(key, {"min": 0.0, "max": 1.0})
	return clampf(v, float(r["min"]), float(r["max"]))

func _save() -> void:
	var cfg := ConfigFile.new()
	for key: String in _values:
		cfg.set_value(SECTION, key, _values[key])
	cfg.save(PATH)

## _load 只认自己认识的键：文件里多出来的键（旧版本遗留）忽略，缺失的键用默认值补 ——
## 这样「加了新偏好」不会让老玩家的整份设置失效。
func _load() -> void:
	var cfg := ConfigFile.new()
	if cfg.load(PATH) != OK:
		return
	for key: String in DEFAULTS:
		if cfg.has_section_key(SECTION, key):
			_values[key] = _clamp(key, float(cfg.get_value(SECTION, key)))

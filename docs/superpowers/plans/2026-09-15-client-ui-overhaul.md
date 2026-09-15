# 客户端界面改造 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 Godot 客户端从「代码堆出来的三个面板」改造成企业级五界面客户端：暗色战术风主题、大厅外壳、个人信息/商城/背包三页、匹配状态条、结算覆盖层、对局内 HUD，服务端零改动。

**Architecture:** 新增 `theme/`（颜色字号唯一真相 + 生成的 Theme 资源）与 `ui/`（`screen_manager` 为唯一状态源，各屏幕只渲染 + 发意图）；`.tscn` 由 `tools/gen_ui_scenes.gd` 一次性生成；`main.gd` 瘦身成输入/相机/渲染 + 把 `FpsClient` 信号派发给 UI。

**Tech Stack:** Godot 4.7.2（GL Compatibility）、GDScript、`.tscn` + `.tres`、headless 脚本测试（`SceneTree` + 完成标记）。

**Spec:** `docs/superpowers/specs/2026-09-15-client-ui-overhaul-design.md`

**视觉真源**：`.superpowers/brainstorm/ui-1/content/` 下的四个 HTML 是这次设计时**用户逐屏确认过**的稿子（`visual-style.html` 暗色战术风、`lobby-shell.html` 左侧竖排导航、`profile-layout.html` 乙方案、`shop-bag-layout.html` 甲方案、`overlay-policy.html` 丙方案）。布局有疑问时以它们为准（浏览器直接打开即可）。

## Global Constraints

- **服务端零改动**：`git diff --stat` 里 `joltgo/` 必须为空；不新增 route、不改 proto、不改 `fps_client.gd` 的协议层（只调用已有方法、监听已有信号）。
- **颜色/字号/间距只能来自 `ui/../theme/tokens.gd`**：界面脚本里禁止出现十六进制颜色字面量或裸字号（HUD 的纯绘制除外，它也必须通过 tokens 取值）。
- **不许固定像素定位**：界面用锚点/容器布局（`set_anchors_preset` + `Container` + `size_flags_*`）；禁止用 `offset_left/top/right/bottom` 摆固定位置（HUD 与遮罩层的贴边定位除外）。
- **场景由脚本生成**：不手写 `.tscn` 的 `[node]`/`[ext_resource]` 块；改结构就改 `tools/gen_ui_scenes.gd` 后重跑生成。
- **提交规则**：直接提交到 `main`（不开分支、不走 PR），信息 `<type>: <subject>`，AI 提交结尾加 `Co-Authored-By: Codex <noreply@openai.com>`。
- **测试命令**（每任务结束都要跑）：
  ```bash
  Godot_..._console.exe --headless --path godot_client --script res://tests/<name>.gd
  ```
  全量无头回归：`world_store_test` / `frame_decode_test` / `game_frame_test` / `reconnect_cleanup_test` / `login_reply_decode_test` / `logic_state_decode_test` / `logic_panel_test` / `match_ended_test` + 本轮新增的 5 个。
- **服务端契约不变量**（客户端必须照它写，别自己发明语义）：
  - 一局 = 一次匹配；判出胜负即终结实例，**不会自动重开**，重新匹配必须由玩家发起。
  - `onMatchEnded` 是「本局结束」的权威信号（收到即离开对局态）。
  - `match.cancel` 三态：`cancelled`（ok=true）、`already_matched`、`not_queued`（后两者 ok=false）。
  - 等级由服务端从 XP 现算，客户端只展示 `level` / `xp_into_level` / `xp_for_next_level`。
  - 商城/背包的所有物品种类由 `LogicStateReply.items` 给出；购买/装备的响应本身就是最新状态。

---

## File Structure

| 文件 | 职责 | 新建/修改 |
|---|---|---|
| `godot_client/theme/tokens.gd` | 颜色 / 字号 / 间距的唯一真相 | 新建 |
| `godot_client/theme/build_theme.gd` | 从 tokens 生成 Theme 资源 | 新建 |
| `godot_client/theme/tactical_theme.tres` | 主题资源（生成物，提交） | 新建 |
| `godot_client/ui/screen_manager.gd` | 屏幕栈 + 状态机（唯一状态源） | 新建 |
| `godot_client/ui/login_screen.gd` / `shell.gd` / `profile_screen.gd` / `shop_screen.gd` / `bag_screen.gd` / `item_card.gd` / `match_status_bar.gd` / `result_overlay.gd` / `hud.gd` | 各界面逻辑 | 新建 |
| `godot_client/ui/*.tscn` | 上述界面的场景（生成） | 新建 |
| `godot_client/tools/gen_ui_scenes.gd` | 生成全部 UI 场景 | 新建 |
| `godot_client/scenes/main.tscn` | 增加 UI 根节点 | 修改 |
| `godot_client/scripts/main.gd` | 瘦身：输入/相机/渲染 + 事件转发 | 修改 |
| `godot_client/project.godot` | 打开窗口拉伸 | 修改 |
| `godot_client/tests/*.gd` | 新增 5 个、改造 2 个 | 新建/修改 |
| `godot_client/README.md` / `AGENTS.md` / `docs/ARCHITECTURE.md` / `docs/DEVELOPMENT.md` | 文档 | 修改 |

---

### Task 1: 主题与字体基础

**Files:**
- Create: `godot_client/theme/tokens.gd`
- Create: `godot_client/theme/build_theme.gd`
- Create（生成）: `godot_client/theme/tactical_theme.tres`
- Modify: `godot_client/project.godot`
- Test: `godot_client/tests/theme_test.gd`

**Interfaces:**
- Consumes: 无
- Produces:
  - `tokens.gd` 的颜色常量 `BG / SURFACE / PANEL / PANEL_ALT / BORDER / BORDER_STRONG / TEXT / TEXT_MUTED / TEXT_DIM / ACCENT / ON_ACCENT / SUCCESS / DANGER / SCRIM / SCRIM_STRONG`
  - 字号 `FONT_LABEL=12 / FONT_BODY=14 / FONT_SUB=18 / FONT_TITLE=24 / FONT_HERO=34`
  - 间距 `SP_1=4 / SP_2=8 / SP_3=12 / SP_4=16 / SP_5=24`
  - `tokens.gd` 的 `static func body_font() -> SystemFont`、`static func mono_font() -> SystemFont`
  - 主题资源路径 `res://theme/tactical_theme.tres`，两个按钮变体：`PrimaryButton` / `GhostButton`

- [ ] **Step 1: 写 `theme/tokens.gd`**

```gdscript
extends RefCounted
## 视觉 token 的唯一真相：颜色、字号、间距、字体。
##
## 界面脚本一律从这里取值，不许写死十六进制颜色或裸字号 —— 这是五个界面能保持一致的
## 唯一保证（改一处配色不会漏掉某个界面）。`theme/build_theme.gd` 会把这里的值写进
## `tactical_theme.tres`，`theme_test.gd` 会校验两者一致。

# ---- 颜色（暗色战术风）----
const BG := Color("0f1114")
const SURFACE := Color("14161a")
const PANEL := Color("1b1e23")
const PANEL_ALT := Color("171a1f")
const BORDER := Color("2a2e35")
const BORDER_STRONG := Color("3a4048")
const TEXT := Color("e8e6e3")
const TEXT_MUTED := Color("7d8590")
const TEXT_DIM := Color("4a5058")
const ACCENT := Color("ffa028")
const ON_ACCENT := Color("14161a")
const SUCCESS := Color("6ee7a8")
const DANGER := Color("ff6b6b")
const SCRIM := Color(0, 0, 0, 0.62)
const SCRIM_STRONG := Color(0.03, 0.035, 0.043, 0.86)

# ---- 字号档位 ----
const FONT_LABEL := 12
const FONT_BODY := 14
const FONT_SUB := 18
const FONT_TITLE := 24
const FONT_HERO := 34

# ---- 间距刻度 ----
const SP_1 := 4
const SP_2 := 8
const SP_3 := 12
const SP_4 := 16
const SP_5 := 24

## body_font 返回正文字体：显式声明的系统字体回退链（不往仓库塞字体文件）。
static func body_font() -> SystemFont:
	var f := SystemFont.new()
	f.font_names = PackedStringArray([
		"Microsoft YaHei UI", "Noto Sans CJK SC", "PingFang SC", "Sans-Serif",
	])
	return f

## mono_font 返回数字/等宽字体：统计数字、比分、时长、队列秒数都用它，战术感的一半来自这里。
static func mono_font() -> SystemFont:
	var f := SystemFont.new()
	f.font_names = PackedStringArray(["Consolas", "DejaVu Sans Mono", "Monospace"])
	return f
```

- [ ] **Step 2: 写 `theme/build_theme.gd`（生成主题）**

```gdscript
extends SceneTree
## 从 tokens.gd 生成 tactical_theme.tres。改配色/字号只改 tokens，然后重跑本脚本：
##   godot --headless --path godot_client --script res://tools/../theme/build_theme.gd
## 生成物必须提交（与 .redis.go 同一个道理：生成的文件也是仓库的一部分）。

const Tokens := preload("res://theme/tokens.gd")
const OUT_PATH := "res://theme/tactical_theme.tres"

func _initialize() -> void:
	var theme := Theme.new()
	theme.default_font = Tokens.body_font()
	theme.default_font_size = Tokens.FONT_BODY

	# Label：默认正文，另给两档变体
	theme.set_color("font_color", "Label", Tokens.TEXT)
	theme.set_font_size("font_size", "Label", Tokens.FONT_BODY)
	# 三档字号变体：LabelLabel(12) / LabelSub(18) / LabelTitle(24)，界面用 theme_type_variation 选
	for pair in [["LabelSmall", Tokens.FONT_LABEL], ["LabelSub", Tokens.FONT_SUB], ["LabelTitle", Tokens.FONT_TITLE]]:
		theme.set_type_variation(pair[0], "Label")
		theme.set_font_size("font_size", pair[0], pair[1])
	# ...（面板/按钮/进度条/输入框见实现步骤：每类控件都从 tokens 取色）
	# Button 主变体
	var primary := StyleBoxFlat.new()
	primary.bg_color = Tokens.ACCENT
	primary.corner_radius_* = 0            # 战术风：直角
	primary.content_margin_* = Tokens.SP_2
	theme.set_stylebox("normal", "PrimaryButton", primary)
	# ... hover/pressed/disabled 各一份（hover 提亮 10%，disabled 用 TEXT_DIM 底）

	var err := ResourceSaver.save(theme, OUT_PATH)
	if err != OK:
		printerr("save theme failed: ", err)
		quit(1)
		return
	print("theme written: ", OUT_PATH)
	quit(0)
```

> 完整实现要求：`Button`（含 `PrimaryButton` / `GhostButton` 两个变体，各自 normal/hover/pressed/disabled 四个状态）、`Panel`、`PanelContainer`、`ProgressBar`（背景 `PANEL`、填充 `ACCENT`、圆角 0、高度 6）、`LineEdit`（背景 `PANEL`、边框 `BORDER_STRONG`、聚焦时边框 `ACCENT`）、`ScrollContainer` 的 `VScrollBar`（背景 `PANEL_ALT`、抓头 `BORDER_STRONG`）、`ItemList` / `Tree`（行背景 `PANEL`、选中行 `BORDER_STRONG`、文字 `TEXT`）。所有数值都从 `Tokens` 取。

- [ ] **Step 3: 生成主题资源**

Run: `Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://theme/build_theme.gd`
Expected: 打印 `theme written: res://theme/tactical_theme.tres`，文件出现在 `godot_client/theme/`。

- [ ] **Step 4: 打开窗口拉伸**

`godot_client/project.godot` 的 `[display]` 段加：

```ini
window/stretch/mode="canvas_items"
window/stretch/aspect="expand"
```

- [ ] **Step 5: 写失败的测试 `tests/theme_test.gd`**

```gdscript
extends SceneTree
## 主题与 tokens 必须一致：任何"改了 tokens 忘了重跑 build_theme"都会在这里失败。

const Tokens := preload("res://theme/tokens.gd")
const THEME_PATH := "res://theme/tactical_theme.tres"

var _failures := 0
var _done := {}

func _initialize() -> void:
	_test_theme_exists()
	_test_colors_match_tokens()
	_test_button_variants()
	_test_fonts()
	for name: String in ["exists", "colors", "variants", "fonts"]:
		if not _done.has(name):
			_failures += 1
			printerr("FAIL: 用例 %s 没跑完（中途抛错了？）" % name)
	if _failures > 0:
		printerr("theme_test: %d 项失败" % _failures)
		quit(1)
	else:
		print("theme_test: OK")
		quit(0)

func _check(cond: bool, msg: String) -> void:
	if not cond:
		_failures += 1
		printerr("FAIL: " + msg)

func _theme() -> Theme:
	return load(THEME_PATH) as Theme

func _test_theme_exists() -> void:
	_check(ResourceLoader.exists(THEME_PATH), "主题资源不存在，先跑 build_theme.gd")
	_check(_theme() != null, "主题资源加载失败")
	_done["exists"] = true

func _test_colors_match_tokens() -> void:
	var t := _theme()
	if t == null:
		return
	_check(t.get_color("font_color", "Label") == Tokens.TEXT, "Label 文字色应与 tokens.TEXT 一致")
	_check(t.get_color("font_color", "Button") == Tokens.TEXT, "Button 文字色应与 tokens.TEXT 一致")
	_check(t.has_stylebox("normal", "PrimaryButton"), "缺 PrimaryButton 的 normal 样式")
	var box := t.get_stylebox("normal", "PrimaryButton") as StyleBoxFlat
	_check(box != null and box.bg_color == Tokens.ACCENT, "主按钮底色应是 tokens.ACCENT")
	_check(t.has_stylebox("normal", "Panel"), "缺 Panel 样式")
	_done["colors"] = true

func _test_button_variants() -> void:
	var t := _theme()
	if t == null:
		return
	for state in ["normal", "hover", "pressed", "disabled"]:
		_check(t.has_stylebox(state, "PrimaryButton"), "PrimaryButton 缺 %s" % state)
		_check(t.has_stylebox(state, "GhostButton"), "GhostButton 缺 %s" % state)
	_done["variants"] = true

func _test_fonts() -> void:
	var t := _theme()
	if t == null:
		return
	var f := t.default_font as SystemFont
	_check(f != null, "默认字体应是 SystemFont（显式回退链，不依赖 Godot 默认字体）")
	_check(f != null and f.font_names.size() >= 3, "正文字体回退链应至少 3 项")
	_check(f != null and f.font_names[0] == "Microsoft YaHei UI", "正文字体首选应是 Microsoft YaHei UI")
	_check(Tokens.mono_font().font_names[0] == "Consolas", "数字字体首选应是 Consolas")
	_check(t.default_font_size == Tokens.FONT_BODY, "默认字号应是 tokens.FONT_BODY")
	_done["fonts"] = true
```

- [ ] **Step 6: 跑测试确认通过**

Run: `Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/theme_test.gd`
Expected: `theme_test: OK`

- [ ] **Step 7: 提交**

```bash
git add godot_client/theme godot_client/project.godot godot_client/tests/theme_test.gd
git commit -m "feat: 客户端主题与字体 token" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 2: UI 场景生成器 + 屏幕管理状态机

**Files:**
- Create: `godot_client/tools/gen_ui_scenes.gd`
- Create（生成）: `godot_client/ui/*.tscn`
- Create: `godot_client/ui/screen_manager.gd`
- Modify: `godot_client/scenes/main.tscn`（加 UI 根节点）
- Modify: `godot_client/scripts/main.gd`（挂载 screen_manager、转发信号）
- Test: `godot_client/tests/screen_flow_test.gd`

**Interfaces:**
- Consumes: Task 1 的 `res://theme/tactical_theme.tres`；`fps_client.gd` 的既有信号
- Produces（后续任务全部依赖这些名字）：
  - 场景：`res://ui/shell.tscn`、`login_screen.tscn`、`profile_screen.tscn`、`shop_screen.tscn`、`bag_screen.tscn`、`item_card.tscn`、`match_status_bar.tscn`、`result_overlay.tscn`、`hud.tscn`
  - `screen_manager.gd` 的公开 API：
    - 状态枚举 `enum State { BOOT, LOGIN, LOBBY, MATCHING, IN_MATCH, RESULT }`
    - `func setup(client: Node, main: Node) -> void` —— **信号接线由本函数负责**（见 Step 6 的说明）
    - `func state() -> State`
    - `func current_page() -> String`（`"profile" / "shop" / "bag"`）
    - 意图：`func intent_login(username: String, password: String) -> void`、`func intent_register(...) -> void`、`func intent_show_page(page: String) -> void`、`func intent_start_match() -> void`、`func intent_cancel_match() -> void`、`func intent_purchase(item_id: String, qty: int) -> void`、`func intent_equip(item_id: String) -> void`、`func intent_back_to_lobby() -> void`、`func intent_play_again() -> void`
    - 数据快照：`func logic_state() -> Dictionary`、`func profile() -> Dictionary`、`func last_result() -> Dictionary`
    - 子界面句柄（测试与 main 都靠它们取控件）：`login_screen`、`shell`、`match_bar`、`result_overlay`、`hud`

- [ ] **Step 1: 写场景生成器 `tools/gen_ui_scenes.gd`**

要点（完整实现按 spec §7 的布局逐屏搭节点树）：

**同时创建每个界面的骨架脚本**（`ui/*.gd`，各自只有 `extends <控件类型>` + 一段职责注释，函数留到对应任务里实现）：生成的场景要在根节点挂上脚本，后续任务才有地方写 `render()` / `bind()`；脚本不存在时生成器会跳过挂载，届时就只能手改场景，所以这一步必须一起做。

```gdscript
extends SceneTree
## 一次性生成 ui/ 下所有 .tscn 骨架。
##   godot --headless --path godot_client --script res://tools/gen_ui_scenes.gd
## 生成后可以继续在编辑器里改；结构性改动请改这里再重跑（不要手写 .tscn 的 node 块）。
##
## 每个场景的形状（详细布局见 spec §7 与 .superpowers/brainstorm/ui-1/content/*.html）：
##   shell.tscn            CanvasLayer > (左导航 VBox: 3 个 Button) + (顶栏 HBox) + (内容插槽 MarginContainer) + (底部 match_status_bar)
##   login_screen.tscn     PanelContainer（居中）> VBox: 标题 / 用户名 / 密码 / 错误 Label / HBox[登录, 注册]
##   profile_screen.tscn   HBox: [身份栏 VBox（头像块/用户名/LV/经验条/账号）] + [右侧 VBox（统计 GridContainer 3 列 + 战绩 ScrollContainer>VBox）]
##   item_card.tscn        PanelContainer > VBox: 图标块 / 名称 / 价格或拥有数 / 步进器 HBox / 主按钮
##   shop_screen.tscn      VBox: 标题行 + GridContainer（4 列，装 item_card）
##   bag_screen.tscn       同 shop，加一个空态 VBox（默认隐藏）
##   match_status_bar.tscn HBox: 最近一局 Label + 搜索状态 Label + 开始匹配 Button + 取消 Button
##   result_overlay.tscn   CanvasLayer > ColorRect(scrim) + CenterContainer > VBox: 结果大字/比分行/时长/HBox[回大厅, 再来一局]
##   hud.tscn              CanvasLayer > 准星（自绘 Control）+ 左下血条与 K/D + 右上对手血条与 K/D + 顶部回合进度 + 左上播报 VBox

func _initialize() -> void:
	_save(_build_shell(), "res://ui/shell.tscn")
	# ... 其余场景同理
	quit(0)

func _save(root: Node, path: String) -> void:
	var packed := PackedScene.new()
	if packed.pack(root) != OK:
		printerr("pack failed: ", path)
		quit(1)
		return
	if ResourceSaver.save(packed, path) != OK:
		printerr("save failed: ", path)
		quit(1)
		return
	print("scene written: ", path)
	root.free()
```

每个场景都必须：`theme = preload("res://theme/tactical_theme.tres")`、用 `Container`/锚点而不是固定 offset、给需要被代码访问的节点设 `unique_name_in_owner = true` 且命名稳定（`NavProfile` / `NavShop` / `NavBag` / `TopUser` / `TopCoins` / `Content` / `StartMatch` / `CancelMatch` / `QueueStatus` / `LastResult` / `Crosshair` / `HealthBar` / `OppHealthBar` / `KillFeed` / `RoundBar` …）。

- [ ] **Step 2: 生成场景并确认能加载**

Run: `Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tools/gen_ui_scenes.gd`
Expected: 9 行 `scene written: ...`；随后 `godot --headless --path godot_client --check-only` 无报错。

- [ ] **Step 3: 写失败的测试 `tests/screen_flow_test.gd`**

```gdscript
extends SceneTree
## 屏幕状态机：登录后进大厅（**不自动匹配**），匹配/取消/进局/结算各自落到正确状态。
## 用假 FpsClient 驱动，不碰 WebSocket。

const MainScene := preload("res://scenes/main.tscn")
const ScreenManager := preload("res://ui/screen_manager.gd")   # 枚举用它比较，别走实例

var _failures := 0
var _done := {}
var _main: Node = null

class FakeClient:
	extends Node
	signal frame_received(frame: Dictionary)
	signal matched_received(result: Dictionary)
	signal connection_changed(connected: bool)
	signal login_result(result: Dictionary)
	signal logic_state_received(result: Dictionary)
	signal profile_received(result: Dictionary)
	signal match_cancel_received(result: Dictionary)
	signal match_status_received(result: Dictionary)
	signal match_ended_received(result: Dictionary)

	var client_token := ""
	var calls: Array[String] = []
	var last_args := {}

	func send_register(u: String, p: String) -> void:
		calls.append("register")
		last_args["register"] = [u, p]

	func send_login(u: String, p: String) -> void:
		calls.append("login")

	func send_resume() -> void:
		calls.append("resume")

	func send_logic_state() -> void:
		calls.append("logic_state")

	func send_profile() -> void:
		calls.append("profile")

	func send_purchase(item_id: String, qty: int) -> void:
		calls.append("purchase")
		last_args["purchase"] = [item_id, qty]

	func send_equip(item_id: String) -> void:
		calls.append("equip")
		last_args["equip"] = item_id

	func send_match_join() -> void:
		calls.append("join")

	func send_cancel_match() -> void:
		calls.append("cancel")

	func send_resync() -> void:
		calls.append("resync")

	func send_command(_m: Vector2, _y: float, _j: bool, _s: bool, _o: Vector3, _d: Vector3) -> void:
		pass

func _initialize() -> void:
	_run()

func _run() -> void:
	_main = MainScene.instantiate()
	root.add_child(_main)
	await process_frame
	await process_frame
	# ... 断言序列（见 Step 4 的用例清单）
	_done["flow"] = true
	_finish()

func _finish() -> void:
	if not _done.has("flow"):
		_failures += 1
		printerr("FAIL: 用例没跑完（中途抛错了？）")
	if _failures > 0:
		printerr("screen_flow_test: %d 项失败" % _failures)
		quit(1)
	else:
		print("screen_flow_test: OK")
		quit(0)

func _check(cond: bool, msg: String) -> void:
	if not cond:
		_failures += 1
		printerr("FAIL: " + msg)
```

- [ ] **Step 4: 跑测试确认失败**

Run: `... --script res://tests/screen_flow_test.gd`
Expected: 编译/断言失败（`screen_manager` 还不存在，`main` 上也没有 UI 根节点）。

- [ ] **Step 5: 实现 `ui/screen_manager.gd`**

```gdscript
extends Node
## 客户端界面的唯一状态源。
##
## 谁都不许绕过它改状态：屏幕（shell/各页面/状态条/结算层）只负责渲染 `state()` 与发意图
## （intent_*），main.gd 只负责把 FpsClient 的信号喂给 `on_client_*`。这样"客户端现在处于
## 哪一步"永远只有一个答案，不会被三四份布尔量拼出来。

enum State { BOOT, LOGIN, LOBBY, MATCHING, IN_MATCH, RESULT }

const ShellScene := preload("res://ui/shell.tscn")
const LoginScene := preload("res://ui/login_screen.tscn")
const ProfileScene := preload("res://ui/profile_screen.tscn")
const ShopScene := preload("res://ui/shop_screen.tscn")
const BagScene := preload("res://ui/bag_screen.tscn")
const ResultScene := preload("res://ui/result_overlay.tscn")

var _state := State.BOOT
var _page := "profile"
var _client: Node = null
var _main: Node = null
var _logic := {}
var _profile := {}
var _last_result := {}

signal state_changed(state: State)
signal page_changed(page: String)
signal data_changed()

func setup(client: Node, main: Node) -> void:
	_client = client
	_main = main

func state() -> State: return _state
func current_page() -> String: return _page
func logic_state() -> Dictionary: return _logic
func profile() -> Dictionary: return _profile
func last_result() -> Dictionary: return _last_result

# ---- 意图（界面只调这些）----
func intent_register(u: String, p: String) -> void: _client.send_register(u, p)
func intent_login(u: String, p: String) -> void: _client.send_login(u, p)
func intent_show_page(page: String) -> void:
	_page = page
	page_changed.emit(page)
func intent_start_match() -> void:
	_set_state(State.MATCHING)
	_client.send_match_join()
func intent_cancel_match() -> void:
	_client.send_cancel_match()          # 结果由 on_cancel_reply 决定，别在这里猜
func intent_purchase(item_id: String, qty: int) -> void: _client.send_purchase(item_id, qty)
func intent_equip(item_id: String) -> void: _client.send_equip(item_id)
func intent_back_to_lobby() -> void:
	_set_state(State.LOBBY)
	_client.send_profile()               # 刚打完一局，战绩变了
func intent_play_again() -> void:
	_set_state(State.MATCHING)
	_client.send_match_join()

# ---- 客户端事件（main.gd 转发）----
func on_login_result(result: Dictionary) -> void:
	if bool(result.get("ok", false)):
		_set_state(State.LOBBY)
		_client.send_logic_state()
		_client.send_profile()
	elif String(result.get("reason", "")) != "no_token":
		_set_state(State.LOGIN)
func on_logic_state(result: Dictionary) -> void:
	if bool(result.get("ok", false)):
		_logic = result
		data_changed.emit()
func on_profile(result: Dictionary) -> void:
	if bool(result.get("ok", false)):
		_profile = result
		data_changed.emit()
func on_match_status(status: Dictionary) -> void: data_changed.emit()
func on_matched(_result: Dictionary) -> void: _set_state(State.IN_MATCH)
func on_match_ended(result: Dictionary) -> void:
	_last_result = result
	_set_state(State.RESULT)
func on_cancel_reply(result: Dictionary) -> void:
	var reason := String(result.get("reason", ""))
	if reason == "cancelled" or reason == "not_queued":
		_set_state(State.LOBBY)
	# already_matched：什么都不做，onMatched 马上到
func on_connection_changed(connected: bool) -> void: data_changed.emit()

func _set_state(next: State) -> void:
	if _state == next:
		return
	_state = next
	state_changed.emit(next)
```

- [ ] **Step 6: 接线 `main.tscn` 与 `main.gd`**

`main.tscn` 的 `Main` 下加一个 `UI`（`CanvasLayer`）节点，并把 `screen_manager.gd` 挂上去（`unique_name_in_owner`）。

**接线放在 `screen_manager.setup()` 里，不放在 `main.gd` 里** —— 这条不是风格问题：测试要注入假 `FpsClient`，而 `@onready var fps_client = $FpsClient` 会在 `_ready` 时把测试提前塞进去的假客户端覆盖掉（A 阶段在 `match_ended_test` 上踩过这个坑）。接线在 `screen_manager` 里，测试就可以在 `_ready` 之后调一次 `main.ui.setup(fake, main)` 完成重新绑定。

`screen_manager.setup()` 里：

```gdscript
func setup(client: Node, main: Node) -> void:
	_client = client
	_main = main
	client.login_result.connect(on_login_result)
	client.logic_state_received.connect(on_logic_state)
	client.profile_received.connect(on_profile)
	client.match_status_received.connect(on_match_status)
	client.matched_received.connect(on_matched)
	client.match_ended_received.connect(on_match_ended)
	client.match_cancel_received.connect(on_cancel_reply)
```

`main.gd` 的 `_ready` 里只保留渲染相关与转发：

```gdscript
	fps_client.frame_received.connect(_on_frame)
	fps_client.matched_received.connect(_on_matched)          # 渲染职责仍在 main
	fps_client.connection_changed.connect(_on_connection)
	ui.setup(fps_client, self)
	ui.state_changed.connect(_on_ui_state)
```

其中 `_on_matched` 保留现有的「清空世界 + `send_resync`」逻辑（渲染职责）。**不再**由 `_on_matched` 调 `ui.on_matched`：`screen_manager` 自己接 `matched_received`，两条路互不依赖（渲染与状态机各自订阅同一个信号）。

- [ ] **Step 7: 补完 `screen_flow_test.gd` 的断言序列**

断言清单（顺序执行）：

0. 测试开头先注入假客户端：`var fake := FakeClient.new(); _main.add_child(fake); _main.ui.setup(fake, _main)`（理由见 Task 2 Step 6）。
1. 初始 → `ui.state() == ScreenManager.State.LOGIN`（`fake.client_token == ""`）。
2. `ui.intent_register("u1","p1")` → 假客户端 `calls` 末尾是 `register`。
3. `fake.login_result.emit({"ok": true, "username": "u1"})` → 状态变 `ScreenManager.State.LOBBY`；`calls` 里出现 `logic_state` 与 `profile`；**`calls` 里没有 `join`**（登录不自动匹配）。
4. `ui.intent_start_match()` → 状态 `MATCHING` 且 `calls` 末尾 `join`。
5. `fake.match_status_received.emit({"queued_players": 3, "waited_seconds": 12})` → 底部状态条显示「3 人」与「12s」。
6. `ui.intent_cancel_match()` → `calls` 末尾 `cancel`；`fake.match_cancel_received.emit({"ok": true, "reason": "cancelled"})` → 状态回 `LOBBY`。
7. `ui.intent_start_match()` 后再 `fake.matched_received.emit({"match_id":"m1","player_idx":0})` → 状态 `ScreenManager.State.IN_MATCH`，大厅隐藏、HUD 可见。
8. `fake.match_ended_received.emit({... winner_slot=0, slots, duration_seconds})` → 状态 `ScreenManager.State.RESULT`，结算层可见；再 `ui.intent_back_to_lobby()` → 状态 `ScreenManager.State.LOBBY` 且 `calls` 里多了一次 `profile`。

- [ ] **Step 8: 跑测试确认通过**

Run: `... --script res://tests/screen_flow_test.gd`
Expected: `screen_flow_test: OK`

- [ ] **Step 9: 提交**

```bash
git add godot_client/ui godot_client/tools godot_client/scenes/main.tscn godot_client/scripts/main.gd godot_client/tests/screen_flow_test.gd
git commit -m "feat: 客户端屏幕状态机与 UI 场景骨架" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 3: 登录页与大厅外壳

**Files:**
- Create: `godot_client/ui/login_screen.gd`、`godot_client/ui/shell.gd`
- Modify: `godot_client/tests/screen_flow_test.gd`（补登录页与顶栏断言）

**Interfaces:**
- Consumes: Task 2 的 `screen_manager`（`state()` / `intent_*` / `logic_state()` / `profile()`）
- Produces: `login_screen.gd` 的 `func bind(manager: Node) -> void`；`shell.gd` 的 `func bind(manager: Node) -> void`（两者都在 `_ready` 时按 `state` 渲染，并订阅 `state_changed` / `page_changed` / `data_changed`）

- [ ] **Step 1: 写失败的断言**

在 `screen_flow_test.gd` 的断言序列里补：

```gdscript
	# 登录页：错误行显示服务端 reason 的中文文案；请求在途时按钮禁用（单飞）
	ui.intent_login("u1", "p1")
	_check(_main.ui.login_screen.is_busy(), "登录请求在途时登录页应处于 busy（按钮禁用）")
	client.login_result.emit({"ok": false, "reason": "bad_credentials"})
	_check(_main.ui.login_screen.error_text() == "用户名或密码错误", "错误文案应翻译 reason")

	# 顶栏：用户名 / 等级 / 金币 都来自各自的数据源
	client.login_result.emit({"ok": true, "username": "u1"})
	client.profile_received.emit({"ok": true, "level": 7, "xp": 420, "xp_into_level": 20, "xp_for_next_level": 200})
	client.logic_state_received.emit({"ok": true, "coins": 1000, "items": [], "equipped_primary_weapon": ""})
	_check(_main.ui.shell.top_text() == "u1 · LV.7 · ⛁ 1000", "顶栏应显示用户名/等级/金币，得到 %s" % _main.ui.shell.top_text())
```

- [ ] **Step 2: 跑测试确认失败**

Expected: `ui.login_screen` / `ui.shell` 不存在或断言失败。

- [ ] **Step 3: 实现 `ui/login_screen.gd`**

```gdscript
extends PanelContainer
## 登录页：只渲染 + 发意图。协议细节（Request/Response、错误码）全在 fps_client 与
## screen_manager 里，这里只把 reason 翻成中文。

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

var _busy := false
var _error := ""
var _manager: Node = null

func bind(manager: Node) -> void:
	_manager = manager

func error_text() -> String: return _error
func is_busy() -> bool: return _busy

func submit(register: bool) -> void:
	if _busy:
		return
	var u: String = %User.text.strip_edges()
	var p: String = %Pass.text
	if u.is_empty() or p.is_empty():
		_show_error("请填写用户名和密码")
		return
	_busy = true
	_error = ""
	if register:
		_manager.intent_register(u, p)
	else:
		_manager.intent_login(u, p)

func show_failure(reason: String) -> void:
	_busy = false
	_show_error(REASON_TEXT.get(reason, ""))

func _show_error(text: String) -> void:
	_error = text
	%Error.text = text
```

同时把生成器里 `login_screen.tscn` 的节点名固定为 `User` / `Pass` / `Error` / `Login` / `Register`（`unique_name_in_owner`），并连上 `pressed` 与 `Pass.text_submitted`。

- [ ] **Step 4: 实现 `ui/shell.gd`**

要点：

- 三个导航按钮：点 `个人信息/商城/背包` → `_manager.intent_show_page("profile"|"shop"|"bag")`；`page_changed` 时重画高亮（当前页 `ACCENT` + 左边框，其余 `TEXT_MUTED`）。
- 顶栏：`top_text()` 返回 `"%s · LV.%d · ⛁ %d"`，数据取 `_manager.profile()` 与 `_manager.logic_state()`；`data_changed` 时刷新。
- 内容插槽：按 `current_page()` 实例化 `profile_screen` / `shop_screen` / `bag_screen`（只保留一个子节点，切换时 `queue_free` 旧的）。
- 底部状态条：由 `match_status_bar` 子节点负责（Task 6），这里只把它挂进场景。

- [ ] **Step 5: 跑测试确认通过**

Run: `... --script res://tests/screen_flow_test.gd`
Expected: `screen_flow_test: OK`

- [ ] **Step 6: 提交**

```bash
git add godot_client/ui godot_client/tools godot_client/tests/screen_flow_test.gd
git commit -m "feat: 登录页与大厅外壳" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 4: 个人信息页

**Files:**
- Create: `godot_client/ui/profile_screen.gd`
- Test: `godot_client/tests/profile_screen_test.gd`

**Interfaces:**
- Consumes: `screen_manager.profile()`；`tokens.gd`
- Produces: `profile_screen.gd` 的 `func render(profile: Dictionary) -> void`、`func stat_text(key: String) -> String`、`func record_rows() -> Array`、`func level_text() -> String`、`func xp_text() -> String`、`func empty_hint_visible() -> bool`（测试读这些）

- [ ] **Step 1: 写失败的测试 `tests/profile_screen_test.gd`**

```gdscript
extends SceneTree
## 个人信息页：用合成 PlayerProfileReply 驱动，断言派生值与列表。

const Screen := preload("res://ui/profile_screen.tscn")

var _failures := 0
var _done := {}

func _initialize() -> void:
	var screen: Control = Screen.instantiate()
	root.add_child(screen)
	screen.render({
		"ok": true, "level": 7, "xp": 420, "xp_into_level": 20, "xp_for_next_level": 200,
		"kills": 128, "deaths": 55, "matches": 39, "wins": 25, "losses": 14,
		"recent_matches": [
			{"match_id": "m1", "won": true, "kills": 10, "deaths": 7, "opponent_kills": 7,
				"duration_seconds": 84, "opponent_name": "bob_77", "ended_at": 1},
			{"match_id": "m2", "won": false, "kills": 4, "deaths": 10, "opponent_kills": 10,
				"duration_seconds": 132, "opponent_name": "kate_x", "ended_at": 2},
		],
	})
	_check(screen.stat_text("kills") == "128", "击杀数应原样显示")
	_check(screen.stat_text("kd") == "2.33", "K/D 应是击杀/死亡保留两位小数（128/55）")
	_check(screen.stat_text("winrate") == "64%", "胜率应是 25/39 四舍五入（64%）")
	_check(screen.level_text() == "LV.7", "等级应带 LV. 前缀")
	_check(screen.xp_text() == "20 / 200 XP", "经验应显示级内经验与升级所需")
	var rows: Array = screen.record_rows()
	_check(rows.size() == 2, "应渲染 2 条战绩，得到 %d" % rows.size())
	_check(String(rows[0]["result"]) == "胜" and String(rows[0]["score"]) == "10 : 7", "首行应是胜 10:7")
	_check(String(rows[0]["opponent"]) == "bob_77" and String(rows[0]["duration"]) == "84s", "首行对手与时长不对")

	# 空态
	screen.render({"ok": true, "level": 1, "xp": 0, "xp_into_level": 0, "xp_for_next_level": 200,
		"kills": 0, "deaths": 0, "matches": 0, "wins": 0, "losses": 0, "recent_matches": []})
	_check(screen.record_rows().is_empty(), "空态不应有战绩行")
	_check(screen.empty_hint_visible(), "空态应显示提示文案")
	_done["profile"] = true
	_finish()

func _check(cond: bool, msg: String) -> void:
	if not cond:
		_failures += 1
		printerr("FAIL: " + msg)

func _finish() -> void:
	if not _done.has("profile"):
		_failures += 1
		printerr("FAIL: 用例没跑完（中途抛错了？）")
	if _failures > 0:
		printerr("profile_screen_test: %d 项失败" % _failures)
		quit(1)
	else:
		print("profile_screen_test: OK")
		quit(0)
```

- [ ] **Step 2: 跑测试确认失败**

Expected: `profile_screen.gd` 没有 `render` / `stat_text` 等。

- [ ] **Step 3: 实现 `ui/profile_screen.gd`**

要点：
- `render(profile)` 保存快照，再刷新：身份栏（用户名从 `screen_manager` 传入或 `profile` 里带 `username`；头像块用 `Tokens.ACCENT` 稳定色 + 用户名首字）、`LV.n`、经验条（`max_value = xp_for_next_level`，`value = xp_into_level`）、`账号 #<account_id>`。
- `stat_text(key)`：`kills/deaths/matches/wins/losses` 直接取；`kd` = `kills / max(deaths,1)` 保留两位；`winrate` = `round(wins * 100.0 / max(matches,1))` 加 `%`。
- `record_rows()` 返回渲染用的行数组（每行 `{result, score, opponent, duration}`），同时把行实例化进列表；空列表时显示 `empty_hint`。
- 数字一律用 `Tokens.mono_font()`（统计卡与列表数值）。

- [ ] **Step 4: 跑测试确认通过**

Expected: `profile_screen_test: OK`

- [ ] **Step 5: 提交**

```bash
git add godot_client/ui/profile_screen.gd godot_client/tests/profile_screen_test.gd
git commit -m "feat: 个人信息页" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 5: 商城与背包（共用物品卡）

**Files:**
- Create: `godot_client/ui/item_card.gd`、`godot_client/ui/shop_screen.gd`、`godot_client/ui/bag_screen.gd`
- Test: `godot_client/tests/item_grid_test.gd`
- Modify: `godot_client/tests/logic_panel_test.gd`（改造为驱动 `shop_screen` / `bag_screen`）

**Interfaces:**
- Consumes: `screen_manager.logic_state()` / `intent_purchase` / `intent_equip`；`tokens.gd`
- Produces:
  - `item_card.gd`：`func bind(item: Dictionary, mode: String, manager: Node) -> void`（`mode` = `"shop"` / `"bag"`）、`func quantity() -> int`、`func set_quantity(v: int) -> void`（内部 `clamp(v, 1, 99)`）、`func action_disabled() -> bool`、`func action_text() -> String`
  - `shop_screen.gd` / `bag_screen.gd`：`func render(state: Dictionary) -> void`、`func cards() -> Array`、`func empty_hint_visible() -> bool`

- [ ] **Step 1: 写失败的测试 `tests/item_grid_test.gd`**

```gdscript
extends SceneTree
## 商城/背包卡片：数量步进器边界、金币不足禁用、装备按钮条件与文案。

const Shop := preload("res://ui/shop_screen.tscn")
const Bag := preload("res://ui/bag_screen.tscn")

var _failures := 0
var _done := {}

func _initialize() -> void:
	var shop: Control = Shop.instantiate()
	root.add_child(shop)
	shop.render({
		"ok": true, "coins": 100, "equipped_primary_weapon": "pistol",
		"items": [
			{"item_id": "rifle", "display_name": "步枪", "price": 300, "equip_slot": "primary_weapon", "owned_quantity": 0},
			{"item_id": "pistol", "display_name": "手枪", "price": 150, "equip_slot": "primary_weapon", "owned_quantity": 2},
			{"item_id": "medkit", "display_name": "医疗包", "price": 50, "equip_slot": "", "owned_quantity": 5},
		],
	})
	var cards: Array = shop.cards()
	_check(cards.size() == 3, "商城应有 3 张卡片")
	_check(cards[0].action_disabled(), "金币 100 买不起 300 的步枪，按钮应禁用")
	_check(cards[1].action_text() == "购买", "未装备的手枪按钮应是购买")
	_check(cards[1].quantity() == 1, "步进器默认值应是 1")
	cards[1].set_quantity(0)
	_check(cards[1].quantity() == 1, "步进器下界是 1")
	cards[1].set_quantity(999)
	_check(cards[1].quantity() == 99, "步进器上界是 99")

	var bag: Control = Bag.instantiate()
	root.add_child(bag)
	bag.render({
		"ok": true, "coins": 100, "equipped_primary_weapon": "pistol",
		"items": [
			{"item_id": "rifle", "display_name": "步枪", "price": 300, "equip_slot": "primary_weapon", "owned_quantity": 1},
			{"item_id": "pistol", "display_name": "手枪", "price": 150, "equip_slot": "primary_weapon", "owned_quantity": 2},
			{"item_id": "medkit", "display_name": "医疗包", "price": 50, "equip_slot": "", "owned_quantity": 5},
		],
	})
	var bag_cards: Array = bag.cards()
	_check(bag_cards.size() == 3, "背包应只显示拥有数量 > 0 的物品")
	_check(bag_cards[0].action_text() == "装备", "未装备的步枪按钮应是装备")
	_check(bag_cards[1].action_text() == "卸下", "已装备的手枪按钮应是卸下")
	_check(bag_cards[2].action_disabled(), "医疗包不可装备，按钮应禁用")

	bag.render({"ok": true, "coins": 0, "equipped_primary_weapon": "", "items": []})
	_check(bag.cards().is_empty() and bag.empty_hint_visible(), "无物品时应显示空态")
	_done["grid"] = true
	_finish()

func _check(cond: bool, msg: String) -> void:
	if not cond:
		_failures += 1
		printerr("FAIL: " + msg)

func _finish() -> void:
	if not _done.has("grid"):
		_failures += 1
		printerr("FAIL: 用例没跑完（中途抛错了？）")
	if _failures > 0:
		printerr("item_grid_test: %d 项失败" % _failures)
		quit(1)
	else:
		print("item_grid_test: OK")
		quit(0)
```

- [ ] **Step 2: 跑测试确认失败**

- [ ] **Step 3: 实现 `ui/item_card.gd`**

要点：
- `bind(item, mode, manager)`：图标块按 `item_id` 稳定取色（`hash(item_id) % 调色板长度`，调色板从 tokens 派生）、名称、`mode == "shop"` 显示价格与步进器、`"bag"` 显示「拥有 ×n」。
- `set_quantity(v)`：`clamp(v, 1, 99)`。
- `action_disabled()`：商城看 `price * quantity > coins`；背包看 `equip_slot.is_empty()`。
- `action_text()`：商城恒为「购买」；背包按 `equipped_primary_weapon == item_id` 返回「卸下」/「装备」。
- 按下按钮 → `manager.intent_purchase(item_id, quantity())` 或 `manager.intent_equip(item_id)`；已装备的卡片加琥珀描边。

- [ ] **Step 4: 实现 `shop_screen.gd` / `bag_screen.gd`**

要点：`render(state)` 清空网格 → 过滤（背包只留 `owned_quantity > 0`）→ 实例化 `item_card.tscn` → `bind` → 加入 `GridContainer`；`shop` 顶部显示金币，`bag` 在空列表时显示空态 + 「去商城」按钮（调用 `manager.intent_show_page("shop")`）。

- [ ] **Step 5: 改造 `tests/logic_panel_test.gd`**

保留原测试的**语义**，改驱动方式：用假 `FpsClient` 实例化 `shop_screen` / `bag_screen`，断言「未登录时 `render` 不被调用（`screen_manager` 没数据时不发 state 请求）」「`screen_manager` 在途时不重复请求」「卡片按钮按下后 `send_purchase` / `send_equip` 收到正确参数」。

- [ ] **Step 6: 跑测试确认通过**

Run: `... --script res://tests/item_grid_test.gd` 与 `... --script res://tests/logic_panel_test.gd`
Expected: 两个都 OK。

- [ ] **Step 7: 提交**

```bash
git add godot_client/ui godot_client/tests
git commit -m "feat: 商城与背包卡片网格" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 6: 匹配状态条与结算覆盖层

**Files:**
- Create: `godot_client/ui/match_status_bar.gd`、`godot_client/ui/result_overlay.gd`
- Modify: `godot_client/tests/match_ended_test.gd`（改为断言结算覆盖层）

**Interfaces:**
- Consumes: `screen_manager`（`state()` / `intent_start_match` / `intent_cancel_match` / `intent_back_to_lobby` / `intent_play_again` / `last_result()`）
- Produces: `match_status_bar.gd` 的 `func render(state: Dictionary) -> void`、`func queue_text() -> String`；`result_overlay.gd` 的 `func show_result(result: Dictionary, my_slot: int) -> void`、`func headline() -> String`、`func score_text() -> String`、`func duration_text() -> String`

- [ ] **Step 1: 写失败的断言**

`match_ended_test.gd` 改造后的核心断言（同样先注入假客户端：`main.ui.setup(fake, main)`；文件顶部补 `const ScreenManager := preload("res://ui/screen_manager.gd")` 用于比较枚举）：

```gdscript
	# 进局 → 结算：覆盖层出现、大厅隐藏、不自动重新入队
	client.matched_received.emit({"match_id": "m1", "player_idx": 0})
	_check(main.ui.state() == ScreenManager.State.IN_MATCH, "onMatched 后应处于对局态")
	client.match_ended_received.emit({
		"match_id": "m1", "winner_slot": 0, "duration_seconds": 84,
		"slots": [{"uid": "7", "kills": 10, "deaths": 3}, {"uid": "8", "kills": 3, "deaths": 10}],
	})
	_check(main.ui.state() == ScreenManager.State.RESULT, "onMatchEnded 后应进入结算态")
	_check(main.ui.result_overlay.visible, "结算覆盖层应可见")
	_check(main.ui.result_overlay.headline() == "胜 利", "胜者槽位是自己时应显示胜利")
	_check(main.ui.result_overlay.score_text() == "你 10 : 3 对手", "比分应读自己槽位（10:3）")
	_check(main.ui.result_overlay.duration_text() == "用时 84 秒", "时长文案不对")
	_check(not client.calls.has("join"), "结算不得自动重新入队")
```

另加两条状态条断言：

```gdscript
	client.match_status_received.emit({"queued_players": 3, "waited_seconds": 12})
	_check(main.ui.match_bar.queue_text() == "● 搜索中 · 队列 3 人 · 已等待 12s", "队列文案不对")
```

- [ ] **Step 2: 跑测试确认失败**

- [ ] **Step 3: 实现 `ui/match_status_bar.gd`**

要点：空闲显示「开始匹配」主按钮 + 最近一局摘要（`screen_manager.last_result()` 有值时显示 `上一局：胜 10:7`，否则隐藏）；`MATCHING` 时状态文案用 `● 搜索中 · 队列 %d 人 · 已等待 %ds`（数据来自 `screen_manager` 保存的最近一条 `onMatchStatus`）并把主按钮换成「取消」。

- [ ] **Step 4: 实现 `ui/result_overlay.gd`**

要点：`show_result(result, my_slot)`：`headline()` 按 `winner_slot == my_slot` 返回 `"胜 利"` / `"失 败"`（颜色 `SUCCESS` / `DANGER`）；比分读**自己槽位**的 kills 与对手槽位的 kills，`score_text()` 返回 `"你 %d : %d %s"`（对手名在 A 阶段没进 `MatchEnded`，所以这里显示「对手」—— 对手名来自 `profile.recent_matches` 的第一条，若能拿到就显示它，否则回退「对手」）；`duration_text()` = `"用时 %d 秒"`。两个按钮接 `intent_back_to_lobby` / `intent_play_again`。

- [ ] **Step 5: 跑测试确认通过**

Run: `... --script res://tests/match_ended_test.gd` 与 `... --script res://tests/screen_flow_test.gd`
Expected: 都 OK。

- [ ] **Step 6: 提交**

```bash
git add godot_client/ui godot_client/tests/match_ended_test.gd
git commit -m "feat: 匹配状态条与结算覆盖层" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 7: 对局内 HUD 重做

**Files:**
- Create: `godot_client/ui/hud.gd`
- Test: `godot_client/tests/hud_test.gd`
- Modify: `godot_client/scripts/main.gd`（把旧的 `_build_hud` / `_update_hud` 换成 HUD 接口）

**Interfaces:**
- Consumes: `world_store.gd` 的属性读取（`_store.attr(id, "名字")`）；`tokens.gd`
- Produces: `hud.gd` 的 `func update_from_world(store: Node, my_slot: int, fps: float) -> void`、`func notify_hit_landed() -> void`、`func health_ratio() -> float`、`func health_text() -> String`、`func opp_health_ratio() -> float`、`func round_ratio() -> float`、`func kill_feed_count() -> int`、`func crosshair_hot() -> bool`

- [ ] **Step 1: 写失败的测试 `tests/hud_test.gd`**

```gdscript
extends SceneTree
## HUD：用合成帧驱动 store，断言血条/对手血条/回合进度/击杀播报/准星。

const MainScene := preload("res://scenes/main.tscn")

var _failures := 0
var _done := {}

func _initialize() -> void:
	_run()

func _run() -> void:
	var main: Node = MainScene.instantiate()
	root.add_child(main)
	await process_frame
	await process_frame
	main._on_frame({"full": true, "step": 1, "schema": {"fields": [
		{"id": 1, "name": "Health", "kind": 0},
		{"id": 2, "name": "Player.Idx", "kind": 1},
		{"id": 3, "name": "Player.Kills", "kind": 1},
		{"id": 4, "name": "Player.Deaths", "kind": 1},
	], "version": 1}, "entities": [
		{"id": 100, "destroy": false, "removed": [], "set": [
			{"id": 1, "f": [60.0]}, {"id": 2, "i": 0}, {"id": 3, "i": 4}, {"id": 4, "i": 1}]},
		{"id": 101, "destroy": false, "removed": [], "set": [
			{"id": 1, "f": [25.0]}, {"id": 2, "i": 1}, {"id": 3, "i": 7}, {"id": 4, "i": 3}]},
	], })
	main._on_matched({"match_id": "m1", "player_idx": 0})
	await process_frame
	var hud: Control = main.ui.hud
	_check(hud.health_text() == "60", "自己血量应是 60，得到 %s" % hud.health_text())
	_check(abs(hud.health_ratio() - 0.6) < 0.01, "自己血条比例应是 0.6")
	_check(abs(hud.opp_health_ratio() - 0.25) < 0.01, "对手血条比例应是 0.25")
	_check(abs(hud.round_ratio() - 0.4) < 0.01, "回合进度应是自己 4/10 = 0.4")
	_check(hud.kill_feed_count() >= 1, "对手死亡数从 0 变 3、自己击杀 0 变 4，应产生播报")
	_done["hud"] = true
	_finish()

func _check(cond: bool, msg: String) -> void:
	if not cond:
		_failures += 1
		printerr("FAIL: " + msg)

func _finish() -> void:
	if not _done.has("hud"):
		_failures += 1
		printerr("FAIL: 用例没跑完（中途抛错了？）")
	if _failures > 0:
		printerr("hud_test: %d 项失败" % _failures)
		quit(1)
	else:
		print("hud_test: OK")
		quit(0)
```

- [ ] **Step 2: 跑测试确认失败**

- [ ] **Step 3: 实现 `ui/hud.gd`**

要点：
- `update_from_world(store, my_slot, fps)` 每渲染帧调用：读自己的 `Health`、双方 `Player.Kills/Deaths`，更新血条（`ProgressBar`）、数值、回合进度（`Kills / 10`）与右上对手栏。
- 击杀播报：与上一帧比较 `Player.Kills` / `Player.Deaths`，新增差值时插一条（`你击杀了对手` / `你被对手击杀`），最多 4 条、6 秒后消失（用 `Time` 记时，不用 `Timer` 节点）。
- 准星：`Control._draw()` 画十字；`crosshair_hot` 在「对手 Health 下降」的 0.12 秒内为真（沿用 main 现有的 `_last_opp_health` 检测，把它移到 HUD 或由 main 转发一个 `hit_landed` 信号都行——**选后者**：`main` 已经检测了血量下降并播了命中音，HUD 订阅同一个信号最省事）。
- 顶部/底部所有贴边控件用锚点，不写固定 offset（`PRESET_TOP_LEFT` / `PRESET_TOP_RIGHT` / `PRESET_BOTTOM_LEFT` / `PRESET_CENTER`）。

- [ ] **Step 4: 接线 `main.gd`**

- 删掉 `_build_hud()` 与 `_update_hud()`（以及 `health_bar` / `stat_label` 字段），改为每帧 `ui.hud.update_from_world(_store, _my_player_idx, _fps_ema)`。
- 保留命中反馈的**音效**（`sfx.play("hit")`），但准星状态交给 HUD；`main` 在检测到对手血量下降时 `ui.hud.notify_hit_landed()`。
- `ui.hud` 只在 `IN_MATCH` 状态可见（由 `screen_manager` 控制）。

- [ ] **Step 5: 跑测试确认通过**

Run: `... --script res://tests/hud_test.gd`，随后跑 `game_frame_test` 与 `screen_flow_test` 回归。
Expected: 三个都 OK。

- [ ] **Step 6: 提交**

```bash
git add godot_client/ui godot_client/scripts/main.gd godot_client/tests/hud_test.gd
git commit -m "feat: 对局内 HUD 重做" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 8: main.gd 瘦身与全量回归

**Files:**
- Modify: `godot_client/scripts/main.gd`（删旧 UI 代码）
- Modify: `godot_client/scenes/main.tscn`（如仍有遗留节点）

**Interfaces:**
- Consumes: 前 7 个任务的全部产物
- Produces: 无新增；**移除** `_build_login_panel` / `_show_login_panel` / `_submit*` / `_on_login_result` / `_build_logic_panel` / `_toggle_logic_panel` / `_refresh_logic_panel` / `_set_logic_items` / `_on_logic_state` / `_build_hud` / `_update_hud` / `conn_label` / `_login_*` / `_logic_*` 字段

- [ ] **Step 1: 删除旧 UI 代码**

逐项删除（`rg` 确认没有残留引用）：

```bash
rg -n "_build_login_panel|_build_logic_panel|_toggle_logic_panel|_refresh_logic_panel|_set_logic_items|_on_logic_state|_build_hud|_update_hud|conn_label|_login_panel|_login_user|_login_pass|_login_error|_login_busy|_logic_panel|_logic_coins|_logic_status|_logic_items_box|_logic_busy|_logic_authenticated|_logic_state" godot_client/scripts/main.gd
```

Expected: 逐条清空；`main.gd` 只剩输入/相机/渲染/音效/事件转发（目标 500 行以内）。

- [ ] **Step 2: 跑全部无头测试**

```bash
for t in world_store_test frame_decode_test game_frame_test reconnect_cleanup_test login_reply_decode_test logic_state_decode_test logic_panel_test match_ended_test theme_test screen_flow_test profile_screen_test item_grid_test hud_test; do
  echo "== $t"; Godot_..._console.exe --headless --path godot_client --script res://tests/$t.gd 2>&1 | tail -2
done
```

Expected: 13 个全绿（`theme_test` 需要先跑过 Task 1 的生成脚本）。

- [ ] **Step 3: 提交**

```bash
git add godot_client/scripts/main.gd godot_client/scenes/main.tscn
git commit -m "refactor: main.gd 交出界面职责" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 9: 文档同步

**Files:**
- Modify: `godot_client/README.md`（重写界面章节）
- Modify: `AGENTS.md`（§2 目录树、§5 客户端约定、§6 测试命令、§7 runbook）
- Modify: `docs/ARCHITECTURE.md`（客户端分层）
- Modify: `docs/DEVELOPMENT.md`（客户端改动条目）

- [ ] **Step 1: `godot_client/README.md`**

重写为：目录结构（`theme/` / `ui/` / `tools/` / `scripts/` / `tests/`）、界面状态机图、主题与字体方案、场景生成方式、13 个无头测试与 4 个冒烟的清单与用途。

- [ ] **Step 2: `AGENTS.md`**

- §2 目录树：`godot_client/` 下补 `theme/`、`ui/`、`tools/` 三行。
- §5 新增三条客户端约定：颜色/字号/间距只从 `theme/tokens.gd` 取；界面不许用固定像素定位；`.tscn` 由 `tools/gen_ui_scenes.gd` 生成，不手写节点块。
- §6 客户端测试清单补 5 个新测试。
- §7 新增 runbook：「改客户端界面 → 只动 `godot_client/`（`ui/` + `theme/`），跑 `theme_test` + `screen_flow_test` + 受影响屏幕的测试；改主题先改 `tokens.gd` 再跑 `build_theme.gd` 重生成 `tactical_theme.tres`」。

- [ ] **Step 3: `docs/ARCHITECTURE.md` / `docs/DEVELOPMENT.md`**

补客户端分层：`screen_manager` 是唯一状态源，UI 只渲染 + 发意图，`main.gd` 只做输入/相机/渲染与事件转发；`fps_client.gd` 是唯一的协议层。

- [ ] **Step 4: 提交**

```bash
git add godot_client/README.md AGENTS.md docs/ARCHITECTURE.md docs/DEVELOPMENT.md
git commit -m "docs: 同步客户端界面结构" -m "Co-Authored-By: Codex <noreply@openai.com>"
```

---

### Task 10: 端到端人工验证

**Files:** 无（验证任务）

- [ ] **Step 1: 起集群与两个客户端**

```bash
cd joltgo/deploy && powershell -File start-all.ps1          # 集群（redis-data 不清空）
Godot_..._win64.exe --path godot_client                     # 客户端 A
$env:APPDATA="$PWD\.client2"; Godot_..._win64.exe --path godot_client   # 客户端 B（独立 APPDATA）
```

- [ ] **Step 2: 走完整闭环**（spec §12 第 2 条）

登录 → 大厅（确认**没有**自动匹配）→ 个人信息页看等级/统计/战绩 → 商城买一件（金币变化）→ 背包里装备/卸下 → 开始匹配（底部条出现「队列 N 人 · 已等待 Ns」）→ 取消一次 → 再匹配成功 → 进局检查 HUD（双方血条、K/D、回合进度、准星、击杀播报）→ 打满 10 杀 → 结算层（胜负/比分/时长）→ 回大厅 → 个人信息页战绩 +1。

- [ ] **Step 3: 拉伸与字体**

把窗口从 1280×720 拉到约 1600×900 与 1000×700：界面不重叠、不越界；中文与数字都正常显示（无方框）。

- [ ] **Step 4: 冒烟回归**

```bash
Godot_..._console.exe --headless --path godot_client --script res://tests/login_smoke.gd
Godot_..._console.exe --headless --path godot_client --script res://tests/ws_smoke.gd
Godot_..._console.exe --headless --path godot_client --script res://tests/rejoin_smoke.gd
Godot_..._console.exe --headless --path godot_client --script res://tests/profile_smoke.gd
```

Expected: 四个都通过（B 没改协议，理应继续绿）。

- [ ] **Step 5: 记录结果**

把四步的实际结果（通过/发现的问题）追加到本计划的「验证记录」小节，并提交文档改动（如有）。

---

## 完成标准

按 spec §12 验收：

1. 无头测试 13 个全绿（改造 2 个 + 新增 5 个 + 原有 6 个）。
2. 两个客户端走通完整闭环（登录 → 大厅 → 三个页面 → 匹配/取消 → 对局 HUD → 结算 → 回大厅看战绩 +1）。
3. 窗口拉伸不破版。
4. 中文与数字用显式字体渲染。
5. `git diff --stat` 里 `joltgo/` 为空；4 个冒烟继续通过。

## 验证记录

（Task 10 完成后在此记录实际结果）

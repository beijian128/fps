# 客户端界面改造（五界面 + 大厅 + 对局 HUD）设计

- 日期：2026-09-15
- 状态：设计已确认，待实现
- 关系：客户端界面改造的 **B 子项目**。A 子项目（服务端能力与数据契约）已完成并合并，见 `docs/superpowers/specs/2026-09-15-match-lifecycle-and-player-stats-design.md`。
- 形式：Godot 4.7 原生 `.tscn` 场景 + `.tres` 主题资源（不是继续在 `main.gd` 里用代码堆 UI）。

## 1. 背景与目标

客户端的界面现在是「能在代码里拼出来」的水平：

- 全部 UI 由 `main.gd`（1230 行）用代码创建，控件位置大量依赖固定像素 `offset_*`；窗口固定 1280×720，拉伸会破版。
- 只有三块界面：登录面板（`_build_login_panel`）、商城与背包面板（`_build_logic_panel`，B 键呼出）、HUD（`_build_hud`，左上几行 Label + 血条 + 准星）。没有大厅、没有个人信息页、匹配期只有一行 `conn_label` 文字。
- 项目里**没有任何字体与主题资源**，中文靠 Godot 默认字体往系统字体回退，字形不可控。
- 登录成功就直接进匹配队列，玩家没有「先看看自己的战绩/商城再决定打不打」的余地。

本子项目要把客户端界面做到「企业级 demo」的水准：**五个界面 + 一个大厅外壳 + 一套对局内 HUD**，视觉统一、可拉伸、状态清晰，并且**服务端一行不改**（A 已经把所需数据备齐）。

## 2. 非目标

- 任何服务端改动（协议、路由、持久化、sim 属性）。B 是纯客户端。
- 动效与音效增强（转场动画、按钮悬停动效、UI 音效、结算特效）：留给后续单独一轮（会引入「程序化音效 vs 音频资源」的新取舍）。
- 设置页、排行榜、好友/组队、聊天。
- HUD 的伤害飘字（需要服务端额外下发伤害事件，属于协议改动）。
- 触屏/手柄导航与可访问性（键盘 + 鼠标）。
- 国际化（界面文案仍是中文硬编码）。

## 3. 已确认决策（视觉与交互）

1. **视觉基调 = A「暗色战术风」**：近黑底、琥珀强调色（`#ffa028`）、硬边描边、等宽数字、大写小字标签。
2. **导航模型 = A「大厅为中心」**：登录 → 大厅（左侧竖排导航：个人信息 / 商城 / 背包 + 底部「开始匹配」）→ 匹配中 → 对局 → 结算 → 回大厅。**登录成功不再自动匹配**。
3. **个人信息页 = 乙**：左侧固定身份栏（头像占位、用户名、等级、经验条、账号信息）+ 右侧六个统计卡 + 下方「最近 20 场」列表。
4. **商城与背包 = 甲「卡片网格」**：一物一卡（图标、名称、价格或拥有数、主按钮），「已装备 / 已拥有 ×n」做角标；两页共用同一个卡片场景。
5. **覆盖层策略 = 丙「混合」**：匹配中**就地**显示（大厅底部状态条：搜索中 · 队列 N 人 · 已等待 Ns + 取消），**结算用全屏覆盖层**（胜负、比分、对手名、时长 + 回大厅 / 再来一局）。
6. **字体 = 显式系统字体 + 回退链**（`Microsoft YaHei UI` → `Noto Sans CJK SC` → `PingFang SC`；数字与数字型标签用 `Consolas` → `DejaVu Sans Mono` → `monospace`）。不往仓库塞字体文件。
7. **范围 = 五个界面 + 大厅 + 匹配状态 + 结算 + 对局内 HUD 重做**（不含动效/音效增强）。
8. 购买数量用卡内 **1–99 步进器**（默认 1）。装备状态在商城与背包两处都能看到、也都能切换。

## 4. 目录与职责

```
godot_client/
├── theme/
│   ├── tokens.gd              # ★ 颜色/字号/间距的唯一真相（纯常量 + 少量 helper）
│   ├── build_theme.gd         # 从 tokens 生成 tactical_theme.tres（可重复执行）
│   └── tactical_theme.tres    # 生成的 Theme 资源（提交）
├── ui/
│   ├── screen_manager.gd      # ★ 屏幕栈 + 状态机（唯一的状态源）
│   ├── shell.tscn / shell.gd  # 大厅外壳：左导航 + 顶栏 + 内容插槽 + 底部状态条
│   ├── login_screen.tscn / .gd
│   ├── profile_screen.tscn / .gd
│   ├── shop_screen.tscn / .gd
│   ├── bag_screen.tscn / .gd
│   ├── item_card.tscn / .gd   # 商城与背包共用
│   ├── match_status_bar.tscn / .gd
│   ├── result_overlay.tscn / .gd
│   └── hud.tscn / hud.gd      # 对局内 HUD
├── tools/gen_ui_scenes.gd     # 一次性生成上面所有 .tscn 骨架（提交，便于重建）
├── scenes/main.tscn           # Main(Node3D) + FpsClient + Sfx + UI 根节点（调整）
└── scripts/main.gd            # 瘦身：输入/相机 + 世界渲染 + 把 FpsClient 事件派发给 UI
```

职责边界：

- `screen_manager.gd` 持有状态机与当前屏幕，**是唯一的状态源**；它不认识 `sim` 属性、不认识 WebSocket。
- 各个 `*_screen.gd` 只做「渲染 + 发意图」：向 `screen_manager` 发 `intent`（登录/购买/装备/开始匹配/取消/回大厅/再来一局），不自己发协议请求。
- `main.gd` 只做三件事：输入与相机、世界渲染与插值、把 `FpsClient` 的信号转成 `screen_manager` / `hud` 的输入。**UI 逻辑不再长在 main.gd 里**。

## 5. 状态机与数据流

```
boot ──(本地有凭证 → resume / 没有 → 显示登录页)──> login ──(LoginReply.ok)──> lobby
lobby ──(导航: 个人信息/商城/背包)──> lobby(profile|shop|bag)
lobby ──(开始匹配)──> matching ──(onMatched)──> in_match ──(onMatchEnded)──> result ──> lobby
matching ──(取消 → cancelled)──> lobby
任意状态 ──(connection_changed=false)──> 断线提示（保留当前页面，重连后自动 resume）
```

数据来源全部是 A 已有的协议，**没有新增 wire 改动**：

| 界面/元素 | 数据来源 |
|---|---|
| 顶栏（用户名/等级/金币） | `LoginReply.username`、`PlayerProfileReply.level`、`LogicStateReply.coins` |
| 个人信息页 | `PlayerProfileReply`（level / xp / xp_into_level / xp_for_next_level / kills / deaths / matches / wins / losses / recent_matches[≤20]） |
| 商城页 | `LogicStateReply.items`（item_id / display_name / price / equip_slot / owned_quantity）+ `coins` |
| 背包页 | 同上（`owned_quantity > 0` 的物品）+ `equipped_primary_weapon` |
| 匹配状态条 | `onMatchStatus{queued_players, waited_seconds}`、`MatchCancelReply` |
| 结算覆盖层 | `onMatchEnded{match_id, winner_slot, slots[], duration_seconds}` |
| HUD | 同步属性：`Health`、`Player.Idx`、`Player.Kills`、`Player.Deaths`、`Game.Winner`（按属性名读，见 `world_store.gd`） |

刷新时机：

- 登录成功（`LoginReply.ok`）→ 拉一次 `logic.state` 与 `profile`（顶栏与个人信息页立刻有数据）。
- 打开商城/背包页 → 用当前快照渲染；**不每次请求**（避免面板切换打 RPC）。
- 购买/装备成功 → 用响应里的 `LogicStateReply` 覆盖本地快照（服务端返回的就是最新状态）。
- 从结算层回大厅 → 再拉一次 `profile`（这一局刚记进档案）。

## 6. 主题与字体

`theme/tokens.gd` 是唯一真相（纯常量，任何界面都不许写死颜色/字号）：

```gdscript
# 颜色
const BG          := Color("0f1114")  # 页面最深底色
const SURFACE     := Color("14161a")  # 面板外框
const PANEL       := Color("1b1e23")  # 卡片/行
const PANEL_ALT   := Color("171a1f")  # 侧栏/顶栏
const BORDER      := Color("2a2e35")
const BORDER_STRONG := Color("3a4048")
const TEXT        := Color("e8e6e3")
const TEXT_MUTED  := Color("7d8590")
const TEXT_DIM    := Color("4a5058")
const ACCENT      := Color("ffa028")
const ON_ACCENT   := Color("14161a")
const SUCCESS     := Color("6ee7a8")
const DANGER      := Color("ff6b6b")
const SCRIM       := Color(0, 0, 0, 0.62)     # 面板外遮罩
const SCRIM_STRONG := Color(0.03, 0.035, 0.043, 0.86)  # 结算层遮罩

# 字号档位与间距刻度
const FONT_LABEL := 12
const FONT_BODY  := 14
const FONT_SUB   := 18
const FONT_TITLE := 24
const FONT_HERO  := 34
const SP_1 := 4
const SP_2 := 8
const SP_3 := 12
const SP_4 := 16
const SP_5 := 24
```

字体（`build_theme.gd` 构造 `SystemFont` 并写进 Theme，不引入字体文件）：

- 正文字体：`SystemFont{font_names=["Microsoft YaHei UI","Noto Sans CJK SC","PingFang SC","Sans-Serif"]}`。
- 数字/等宽：`SystemFont{font_names=["Consolas","DejaVu Sans Mono","Monospace"]}`，用于统计数字、比分、时长、队列秒数。

`tactical_theme.tres` 覆盖：`Label`（三档）、`Button`（常态/悬停/按下/禁用 + 主按钮变体）、`Panel`/`PanelContainer`、`ProgressBar`（经验条与血条共用）、`LineEdit`、`ItemList`/`Tree`（战绩表）、`ScrollContainer` 滚动条。主按钮（琥珀底 + 深色字）与次按钮（描边）作为 Theme 的两种变体，界面通过 `theme_type_variation` 使用。

缩放：`project.godot` 打开窗口拉伸（`display/window/stretch/mode="canvas_items"`、`aspect="expand"`），基准仍是 1280×720；所有界面用锚点/容器布局，**不许出现固定 `offset_*` 定位**（HUD 与遮罩层除外，它们按锚点贴边）。

## 7. 各界面设计

### 7.1 登录页（`login_screen`）

- 布局：居中的窄面板（约 380×260），标题 `JOLT FPS` + 一行副标题。
- 控件：用户名、密码、错误/提示行（红色 `DANGER`，成功走状态条）、「登录」「注册」两个按钮（主/次）。
- 行为沿用现有语义：密码框回车 = 登录；请求在途时按钮禁用（单飞）；`no_token` 不报错；本地有凭证时**整个页面不出现**（自动 resume，保持现在的「回头客看不到登录页」体验）。

### 7.2 大厅外壳（`shell`）

- 左侧竖排导航：`个人信息 / 商城 / 背包`，当前页用琥珀左边框 + 琥珀文字高亮（其余 `TEXT_MUTED`）。
- 顶栏：左 `JOLT FPS`（琥珀、字距放宽），右 `用户名 · LV.n · ⛁ 金币`。
- 中部内容插槽：由 `screen_manager` 挂载当前页面（profile / shop / bag）。
- 底部状态条（`match_status_bar`）：空闲时是主按钮「开始匹配」；匹配中变成 `● 搜索中 · 队列 N 人 · 已等待 Ns` + 次按钮「取消」；最近一局摘要（`上一局：胜 10:7`）显示在左侧 —— 这条摘要来自**本地记住的上一次 `onMatchEnded`**（会话内有效，不回服务端查）。

### 7.3 个人信息页（`profile_screen`）

- 左侧身份栏（固定宽约 220）：头像占位块（程序化色块 + 首字母）、用户名、`LV.n`、经验条（`xp_into_level / xp_for_next_level`，数值以「420 / 700 XP」显示）、账号信息（`账号 #<account_id>`）。
- 右侧：六个统计卡两行三列 —— 击杀 / 死亡 / K/D（派生，保留两位小数）/ 场次 / 胜 / 负，另加胜率（派生百分比）。
- 下方「最近 20 场」列表：列 = 结果（胜/负，`SUCCESS`/`DANGER` 色）、比分（我方:对方）、对手名、时长（秒）。空态文案「还没有对局记录，去打一局吧」。
- 列表可滚动（超过约 8 行时）。

### 7.4 商城页（`shop_screen`）

- 卡片网格（每行 4 张，随宽度自适应）：图标块（按 `item_id` 稳定取色的程序化色块 + 名称首字）、显示名、价格、拥有数量角标（`×n`，为 0 不显示）、底部一行：数量步进器（`- [1] +`，范围 1–99）+ 主按钮「购买」。
- 已装备的卡片加琥珀描边并显示「已装备」角标。
- 金币不足时按钮禁用并在卡片上给出原因（`金币不足`）；购买成功后用返回的 `LogicStateReply` 刷新整页（金币与拥有数一起变）。
- 顶部一行：`商城` 标题 + 右侧 `⛁ <coins>`。

### 7.5 背包页（`bag_screen`）

- 与商城同一个卡片网格，但：右侧数字显示「拥有 ×n」而不是价格；主按钮是「装备」/「卸下」（仅 `equip_slot` 非空的物品可装备；装备中的显示「卸下」）。
- 无物品时空态：「背包还是空的，去商城看看」+ 一个跳转商城的次按钮。

### 7.6 匹配状态（`match_status_bar`）

- 空闲：主按钮「开始匹配」（点击后立刻切到搜索中，不等服务端回话）。
- 搜索中：`● 搜索中 · 队列 N 人 · 已等待 Ns`（消费 `onMatchStatus` 推送）+ 次按钮「取消」。
- 取消：发出 `match.cancel` 后按三态处理 —— `cancelled` → 回空闲；`already_matched` → 什么都不做（`onMatched` 马上到）；`not_queued` → 回空闲（`onMatched` 也可能马上到，到时照常进局）。
- 收到 `onMatched` → 状态切到 `in_match`，隐藏大厅与状态条，显示 HUD。

### 7.7 结算覆盖层（`result_overlay`）

- 全屏遮罩（`SCRIM_STRONG`）+ 居中内容：结果大字（`胜 利` / `失 败`，按 `winner_slot == 我的槽位` 判定，`SUCCESS`/`DANGER`）、比分行 `你 10 : 7 对手名`、`用时 84 秒`。
- 两个按钮：**回大厅**（主）/ **再来一局**（次，等于立刻重新匹配）。
- 覆盖层出现期间屏蔽游戏输入（与现有「登录面板出现时暂停输入」同一机制）。

### 7.8 对局内 HUD（`hud`）

- 准星：屏幕中心细十字（默认 `TEXT` 半透明）；命中对手时短暂变琥珀并轻微扩张（0.12 秒）。**命中的判定沿用现有的本地检测**（对手 `Health` 下降，见 `main.gd` 现在的 `_last_opp_health` / `_flash_hit`），**不新增协议**。
- 左下：本人血条（`ProgressBar` + 数值）+ 本人 K/D（`Player.Kills` / `Player.Deaths`）。
- 右上：对手血条与对手 K/D（同一套属性，槽位相反）。
- 顶部中央：回合进度 —— 双方击杀数与「先到 10 杀」的进度条（`Kills / 10`）。
- 左上：击杀播报条（本局内的击杀/死亡事件，由 `Player.Kills` / `Player.Deaths` 的本地变化推出；最多同时显示 4 条，6 秒后淡出）。
- 右下角小字：FPS 与（若有）网络延迟；`Game.Winner >= 0` 时不再由 HUD 显示结果（结算覆盖层负责）。

## 8. 与既有代码和测试的关系

- `main.gd` 保留：输入与相机（`_input` / `_unhandled_input` / `_apply_look` / `_apply_camera_mode` / `_update_camera`）、世界渲染与插值（`_on_frame` / `_render_interpolated` / `_reconcile_scene` / `_body_*`）、音效与命中反馈、`_wish_velocity` 等与网络无关的部分。
- `main.gd` 迁出：登录面板、商城/背包面板、HUD 构建与更新、`_update_hud` 里的文本拼装、所有 `conn_label` 文案。
- `fps_client.gd` **不动协议层**：B 只调用已有的 `send_register` / `send_login` / `send_resume` / `send_logic_state` / `send_purchase` / `send_equip` / `send_profile` / `send_match_join` / `send_cancel_match` / `send_resync` / `send_command`，并监听已有信号。
- 对局内仍然只走 `game.game.cmd` 一条上行命令（帧是最小发送单位），HUD 不新增任何上行。

## 9. 场景生成

`tools/gen_ui_scenes.gd` 用代码构造每个界面的节点树并 `ResourceSaver.save` 成 `.tscn`（一次性运行：`godot --headless --path godot_client --script res://tools/gen_ui_scenes.gd`）。生成后即为标准场景文件，可继续在编辑器里调整；脚本随仓库提交，便于在结构性改动后重建。**不手写 `.tscn` 里的 ID 块**。

## 10. 测试策略

继续沿用 headless 脚本测试（`SceneTree` + 完成标记，不依赖真实渲染与服务端）：

| 测试 | 类型 | 覆盖 |
|---|---|---|
| `theme_test.gd` | 新增 | `tactical_theme.tres` 与 `tokens.gd` 一致（颜色/字号抽样断言）、主按钮/次按钮变体存在、SystemFont 回退链非空 |
| `screen_flow_test.gd` | 新增 | 状态机推进：login → lobby → matching → in_match → result → lobby；登录成功**不**自动发 `match.join`；取消三态各自回到正确状态 |
| `profile_screen_test.gd` | 新增 | 用合成 `PlayerProfileReply` 驱动：等级/经验条比例、六个统计卡数值、K/D 与胜率派生、战绩行数与内容、空态 |
| `item_grid_test.gd` | 新增 | 用合成 `LogicStateReply` 驱动：卡片数量、购买按钮在金币不足时禁用、数量步进器边界 1 与 99、装备/卸下按钮的启用条件与文案 |
| `hud_test.gd` | 新增 | 用合成帧驱动：血条值与数值、双方 K/D、回合进度比例、命中时准星状态、击杀播报条数与上限 |
| `logic_panel_test.gd` | 改造 | 原「商城面板」断言迁移到 `shop_screen` / `bag_screen`：用一个假 `FpsClient` 驱动 `screen_manager`，断言「未登录时不请求 state」「请求在途不重复请求」「点购买 / 装备后按预期参数调用 `send_purchase` / `send_equip`」 |
| `match_ended_test.gd` | 改造 | 改为断言「`onMatchEnded` → 结算覆盖层出现且大厅隐藏」，并保留「不自动重新入队」的核心断言 |
| `game_frame_test.gd` | 保留 | 渲染路径不回归（仍驱动 `main._on_frame`） |
| 其余 5 个无头测试 | 保留 | `world_store` / `frame_decode` / `reconnect_cleanup` / `login_reply_decode` / `logic_state_decode` |
| 冒烟 4 个 | 保留 | `login_smoke` / `ws_smoke` / `rejoin_smoke` / `profile_smoke` 继续走真实集群（B 不改协议，理应继续绿） |

**不做**：像素级截图对比（Godot headless 不渲染，且会引入基线维护成本）。

## 11. 文档更新

- `godot_client/README.md`：整章重写（界面结构、目录、状态机、主题/字体、测试清单）。
- `AGENTS.md`：§2 目录树补 `theme/`、`ui/`、`tools/`；§5 增加客户端约定（颜色/字号只从 `tokens.gd` 取、不写死像素定位、场景由生成脚本产出）；§6 更新客户端测试命令（新增 5 个）；§7 增加「改客户端界面」的 runbook。
- `docs/ARCHITECTURE.md` 客户端章节：补界面分层（`screen_manager` 为唯一状态源、UI 只发意图）。
- `docs/DEVELOPMENT.md`：客户端「改界面」条目指向新的目录与测试。

## 12. 验收标准

1. 客户端无头测试全绿：原有 8 个（其中 2 个改造）+ 新增 5 个。
2. 两个客户端人工走通完整闭环：登录 → **大厅（不自动匹配）** → 个人信息页看到等级/统计/战绩 → 商城买一件、背包里装备/卸下 → 开始匹配（底部条显示人数与秒数）→ 取消一次 → 再匹配成功 → 进局看到新 HUD（双方血条与 K/D、回合进度、准星、击杀播报）→ 打满一局 → 结算层显示胜负/比分/对手/时长 → 回大厅 → 个人信息页战绩 +1。
3. 窗口从 1280×720 拉伸到任意比例，界面不破版（控件不重叠、不跑出边界）。
4. 中文与数字都用显式字体渲染（不依赖 Godot 默认字体），观感与设计稿一致。
5. `git diff --stat` 里 **`joltgo/` 为空**（纯客户端改动），且 4 个冒烟测试继续通过。

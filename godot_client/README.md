# Jolt FPS Demo — Godot 客户端（godot_client/）

基于 [Godot 4.7](https://godotengine.org) 的 PVP 对枪 FPS 客户端，服务端是唯一权威。

- 第一人称视角：点击画面进入（鼠标捕获）、ESC 释放；**V 键切换第一/第三人称**
- 鼠标瞄准、WASD 移动、Space 跳跃、Shift 奔跑
- **PVP 对枪**：两名玩家互相射击，击杀数先到 10 者获胜，被击杀后立即在己方出生点
  满血复活（全部是服务端规则）
- **卡通模型（全部程序化生成，无外部美术资源）**：
  - 第一人称：持枪 viewmodel（枪口闪光 + 后坐 + 卡通手套）
  - 第三人称：玩家人形 Avatar（大头/棒球帽/圆身体/短腿/双肩包），远端玩家换色区分
- 服务端以 **20 Hz** 固定 tick 推进模拟，状态变化通过 WebSocket 主动推送**实体-属性
  增量帧**（只含本帧变化的 `(实体, 属性, 终值)`）；客户端按属性名累积成本地世界
- 客户端 **60 Hz** 渲染：对运动刚体 / 玩家位置做**影子跟随插值**（渲染时刻滞后
  约一个 tick，在「上一帧 → 本帧」之间 lerp / slerp），20 Hz 数据在 60 Hz 屏幕上保持平滑
 - 客户端每渲染帧（约 60 Hz）上报**一条**合并命令（输入 + 射击，route `game.cmd`）
- **账号登录 → 大厅 → 匹配**（详见下文「界面结构」）：启动后先登录（用户名 + 密码），
  进大厅后由玩家自己点「开始匹配」——**登录不会自动进队列**；本地存有凭证时自动 resume、
  玩家看不到登录页
- **断线重连回到同一对局**：重连后先用本地凭证 `account.resume` 登回同一账号，服务端推
  `onMatched`（同一 match_id / 槽位），客户端请求 `game.resync` 拿一份 full 帧整体重建
- 受击反馈：全屏红闪 + 音效；打中对手有命中音、准星短暂点亮
- **对局内 HUD**：准星（命中反馈）、自己与对手的血条和 K/D、回合进度（先到 10 杀）、
  击杀播报条、FPS
- **一局结束**：收到 `onMatchEnded` 即离开对局态（停掉 2.5s 接收看门狗，否则会被误判成
  掉线）、清空本地世界，弹出**全屏结算层**（胜负 / 比分 / 对手 / 时长 + 回大厅 / 再来一局）

## 运行

1. 先启动服务端（见仓库根目录 README 的「快速开始」），监听 `ws://localhost:8080/`
2. 用 Godot 打开 `godot_client/project.godot` 后按 F5，
   或命令行直接运行：

   ```bash
   Godot_v4.7.2-stable_win64.exe --path godot_client
   ```

3. 首次运行会看到**登录页**：填用户名 + 密码，点「注册」建号或「登录」进已有账号
   （密码框里按回车 = 点登录）。之后再启动会自动用本地凭证登录，登录页不再出现
4. 登录后进**大厅**：左侧切「个人信息 / 商城 / 背包」，底部点「开始匹配」进队列
   （搜索中会显示队列人数与已等待秒数，可随时取消）
5. 对局中由**点击画面**锁定鼠标；ESC 释放鼠标（大厅与结算层里鼠标始终可见）

> 服务端地址硬编码在 `scripts/fps_client.gd` 的 `WS_URL`，改端口时同步修改。

## 登录与凭证

服务端不再接受客户端自报身份，进匹配前必须先登录（`account.account.*`，
Request/Response，详见 [docs/API.md](../docs/API.md)）。

- **登录面板**（`main.gd` 的 `_build_login_panel`）：用户名 LineEdit（3–16 位
  字母/数字/下划线，长度上限 16）+ 密码 LineEdit（`secret = true`，6–64 位）+
  「登录」「注册」两个按钮 + 一行红色错误提示。密码框上按**回车**直接提交登录。
  用户名框会预填上次登录用过的名字。
- **面板什么时候出现**：只在**本地没有凭证**时显示。有凭证就直接静默 resume ——
  回头客看不到任何提示。无条件先显示再隐藏不仅会闪一下，还会开出一个竞态窗口
  （resume 在飞的同时玩家点了注册/登录，两个请求打架）。
- **凭证落盘**：
  - `user://auth_token.txt` —— 服务端签发的 token（base64url，43 字符，7 天有效、
    每次 resume 续期）。只在登录/注册/resume **成功**时写入；失败时服务端不发 token，
    写空串会把上一次的有效凭证也抹掉。服务端明确回 `token_invalid` 时删除它，
    免得客户端拿死凭证反复重试。
  - `user://last_username.txt` —— 上次登录的用户名，仅用于预填输入框，不参与鉴权。
  - **不复用旧的 `user://client_id.txt`**：那是旧协议里客户端自己生成的 UUID，语义是
    「我说我是谁」；新的 token 是「服务端认证过我是谁」。同一个文件装两种语义只会让
    「升级后老客户端拿旧 UUID 当 token 发过来」这种情况变成一个说不清的失败。
    旧文件留在原地不管，反正没人读它。
- **顶号**：一个账号只有一个活凭证。别处用同一账号登录后，本机的 token 立即作废，
  当前连接会被踢，重连时 resume 失败 → 停在登录面板并提示「登录已过期，请重新登录」。
- **同机开两个客户端**要用两套 `user://`（`APPDATA` 环境变量分开）**并且**注册两个账号
  —— 只分开目录不够，同一账号登两次就是顶号。

## 代码组织

```text
godot_client/
├── scenes/main.tscn      # 主场景：Main + FpsClient + Sfx + UI（挂 screen_manager）
├── theme/                # 视觉 token（tokens.gd）+ 主题生成器（build_theme.gd）+ tactical_theme.tres（生成物）
├── ui/                   # 界面层：screen_manager 是唯一状态源；各屏幕只渲染 + 发意图
│   ├── screen_manager.gd # 状态机（boot/login/lobby/matching/in_match/result）+ 意图 API + 数据快照
│   ├── shell.tscn/.gd    # 大厅外壳：左导航 + 顶栏 + 内容插槽 + 底部状态条
│   ├── login_screen      # 登录 / 注册
│   ├── profile_screen    # 个人信息：身份栏 + 六个统计卡 + 最近 20 场
│   ├── shop_screen / bag_screen / item_card  # 商城与背包共用卡片
│   ├── match_status_bar  # 底部：开始匹配 / 搜索中（队列人数与等待时长）/ 取消
│   ├── result_overlay    # 全屏结算：胜负 / 比分 / 对手 / 时长 + 回大厅 / 再来一局
│   └── hud               # 对局内：准星 / 双方血条与 K/D / 回合进度 / 击杀播报
├── tools/gen_ui_scenes.gd # 生成上面所有 .tscn（改结构改这里再重跑）
├── scripts/
│   ├── main.gd           # 输入/相机(第一/第三人称)/按属性名查询与插值/持枪/玩法反馈（Node3D）
│   ├── world_store.gd    # 本地世界状态：实体-属性增量累积成完整世界（按名字取值）
│   ├── fps_client.gd     # WebSocket 传输层：连接/重连/收发（Node）
│   ├── body_entity.gd    # 每个服务端刚体一个渲染节点（场景材质配色/简单体）
│   └── sfx.gd            # 程序化音效（Node）
└── tests/                # 无头回归/冒烟测试（.gd；见 AGENTS.md §6）
```

## 界面结构

**状态机**（`ui/screen_manager.gd` 是唯一状态源，界面只渲染它、只向它发意图）：

```text
boot ──(无本地凭证)──> login ──(LoginReply.ok)──> lobby
lobby ──(导航: 个人信息/商城/背包)──> lobby(页面)
lobby ──(开始匹配)──> matching ──(onMatched)──> in_match ──(onMatchEnded)──> result ──> lobby
matching ──(取消 → cancelled/not_queued)──> lobby
```

- **登录成功后不会自动匹配**：必须在大厅点「开始匹配」。新玩家因此先看到自己的战绩与商店，
  而不是被直接丢进队列。
- `matching` 期间大厅仍然可用（可以翻战绩、逛商城），底部状态条显示
  `队列 N 人 · 已等待 Ns`（数据来自服务端每秒推送的 `onMatchStatus`）。
- `in_match` 显示 HUD；`result` 显示全屏结算层，两个按钮是「回大厅」与「再来一局」
  （后者 = 立刻重新匹配）。
- 断线时状态收回 `lobby`（有凭证会自动 resume），本地世界由 `main.gd` 清空。

**主题与字体**：所有颜色/字号/间距都来自 `theme/tokens.gd`（暗色战术风：近黑底 + 琥珀强调
`#ffa028` + 直角细描边 + 等宽数字）。`theme/build_theme.gd` 把 tokens 写进
`tactical_theme.tres`（生成物，改 tokens 后重跑生成；`tests/theme_test.gd` 会挡住漂移）。
字体不打包进仓库，而是显式声明的系统字体回退链（`Microsoft YaHei UI` → `Noto Sans CJK SC`
→ `PingFang SC`，数字用 `Consolas`）——不再依赖 Godot 默认字体对中文的兜底。

**场景怎么来的**：`ui/*.tscn` 由 `tools/gen_ui_scenes.gd` 生成：

```bash
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client \
  --script res://tools/gen_ui_scenes.gd
```

生成后它们就是标准场景文件，可以在编辑器里继续调；**结构性改动请改生成器再重跑**，不要手写
`.tscn` 的节点块（`PackedScene.pack()` 不替你设 `owner`，漏了节点就会整棵丢掉）。

- `FpsClient` 与渲染层**通过信号解耦**：`frame_received(frame)` 发同步帧、
  `matched_received(result)` 报匹配结果、`login_result(result)` 报登录/注册/resume 结果、
  `connection_changed(connected)` 报连接变化
  （断线后自动每秒重连、匹配后 2.5s 接收看门狗）
- `WorldStore` 按需累积：实体 ID → `{ 属性名: 值 }`，只按**名字**取值，不认识的属性
  照常存下、只是不渲染；full 帧先清空再整体覆盖，因此「首次进入 / 重连」在应用层无区别
- 场景与灯光由代码构建（`main.gd`）；**界面**由 `ui/` 下的场景 + 脚本组成（见上节），
  `main.gd` 不再持有任何界面控件，只把 `FpsClient` 的渲染相关事件转给世界与 HUD
- 网格/材质管理在 `BodyEntity`：只在形状/标志/材质签名变化时重建（`Body.Kind` /
  `Body.Size` / `Body.Static` / `Projectile` / `Body.Mat`），位置/旋转每帧写入
- 场景刚体按属性 `Body.Mat` 的材质号配色（`body_entity.gd` 的 `MATS` 表与
  `sim/map.go` 的 `Material` 编号一一对应，**只能追加**）；弹丸由标记属性
  `Projectile` 优先决定外观。客户端遇到不认识的材质号会
  退回默认配色（静态钢灰/动态木色）
- **玩家不走刚体渲染**：本地玩家是相机 + 人形 Avatar，远端玩家是另一个换色 Avatar，
  都按属性 `Player.Idx` 认人；服务端的**玩家命中盒**没有 `Body` 组件、不参与同步，
  客户端根本看不到它
- 玩法反馈（受击红闪、命中音、准星点亮）在 `main.gd`：它比对相邻两帧的 `Health` 得出
  「谁掉血了」，再调 `ui.hud.notify_hit_landed()`；**第一帧只做初始化**，否则初始快照会被
  当成一次命中。HUD 的纯显示（血条、K/D、回合进度、击杀播报）在 `ui/hud.gd` 里各自读属性
- 插值在 `main.gd`：每个刚体节点自带**上一帧变换**，渲染时 alpha = 距本帧到达时间 /
  0.05s 在「上一帧 → 本帧」之间 lerp/slerp；玩家位置与远端朝向同样 prev→target 插值
  （显示值与服务端下发值分开存，避免同一帧被插值两次）
- 实体消失由帧里的 `destroy` / `removed` 显式下发（不再靠快照 diff 推断），销毁事件
  还带着消失前的属性快照，渲染层据此给弹丸做爆闪与命中音
- 新生成 / 移除的刚体不参与插值：按最新帧创建或删除（弹丸消失有爆闪特效）
- 输入 60 Hz 上报（每渲染帧一条合并命令），跳跃在按下瞬间排队（边沿触发）；
  释放鼠标时补一条静止输入，避免服务端沿用旧速度
- 音效全部程序化生成（`sfx.gd` 合成 16-bit WAV），无音频文件
- 坐标约定：属性 `Pos` 是刚体质心（玩家为脚底），`Rot` 是 `[x,y,z,w]`，直接映射
  Godot `Quaternion`；胶囊总高 = 半高 × 2 + 半径 × 2

## 自动化测试

`tests/` 下的测试用 `_console.exe` 变体无头运行（**无服务端**，可单独跑）：

```bash
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/world_store_test.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/frame_decode_test.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/login_reply_decode_test.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/game_frame_test.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/reconnect_cleanup_test.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/logic_state_decode_test.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/logic_panel_test.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/match_ended_test.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/theme_test.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/screen_flow_test.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/profile_screen_test.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/item_grid_test.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/hud_test.gd
```

- `world_store_test.gd`：世界存储语义（full / removed / destroy / 未知属性）
- `frame_decode_test.gd`：`Frame` / `Schema` protobuf 解码
- `login_reply_decode_test.gd`：Response 帧解码 —— LEB128 mid、**无 route**、
  `LoginReply` 字段，以及 errorMask 置位时不能当业务消息解
- `game_frame_test.gd`：渲染路径 + 玩法反馈的血量判定（喂合成帧，不碰 WebSocket）
- `reconnect_cleanup_test.gd`：断线清理本地世界与插值状态
- `logic_state_decode_test.gd` / `logic_panel_test.gd`：商城状态解码；界面的请求时机与意图参数
- `match_ended_test.gd`：本局结束后进结算态、清空世界、**不自动重新入队**
- `theme_test.gd`：`tactical_theme.tres` 与 `tokens.gd` 一致（改 tokens 忘记重跑生成时会红）
- `screen_flow_test.gd`：屏幕状态机全流程（登录不自动匹配 / 匹配 / 取消三态 / 进局 / 结算 / 回大厅）
- `profile_screen_test.gd`：个人信息页（等级/经验条/统计派生 K/D 与胜率/战绩列表/空态）
- `item_grid_test.gd`：商城与背包卡片（数量 1–99、买不起禁用、装备/卸下文案、空态）
- `hud_test.gd`：对局内 HUD（双方血条与 K/D、回合进度、击杀播报上限、准星命中反馈）

下面是**冒烟测试，需要活集群**（etcd + NATS + redis + gate/account/logic/match/game 五进程），
其中 `login_smoke` / `ws_smoke` / `rejoin_smoke` 都需要**两个客户端真配对**（单人兜底已删除）：

```bash
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/login_smoke.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/ws_smoke.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/rejoin_smoke.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/logic_smoke.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/profile_smoke.gd
```

- `login_smoke.gd`：注册（随机用户名）→ 收到 `LoginReply` 带 token → 断线 → `resume`
  → 进入对局（`onMatched`）。它是唯一端到端验证「登录 → 匹配 → 对局」整条链路的测试，
  改 account 服务 / gate 的会话归属 / match 的开局链路后必跑。开跑前会清掉本地凭证
  —— 残留 token 会让客户端握手后直接 resume 成上一轮的账号，把「注册」这一环整个跳过
- `ws_smoke.gd`：登录后匹配 + 统计 4 秒内的增量帧推送速率。预期输出
  `SMOKE unique_steps=81 span=80 elapsed_ms=4000` 左右（4 秒 × 20 Hz），且无 `SCRIPT ERROR`
- `rejoin_smoke.gd`：登录后断线重连回到**同一 match_id + 同一 player_idx**，且重连后收到
  一份 full 帧。它是新协议下**唯一**端到端验证「重连回同一局」的测试，改匹配 / 回局 /
  resync 链路后必跑；断言在载荷解析失败时会显式判失败（不会假通过）

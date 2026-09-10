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
- 客户端每渲染帧（约 60 Hz）上报**一条**合并命令（输入 + 射击 + 重置，route `game.cmd`）
- **断线重连回到同一对局**：token 持久化在 `user://client_id.txt`，重连后服务端推
  `onMatched`（同一 match_id / 槽位），客户端请求 `game.resync` 拿一份 full 帧整体重建
- 受击反馈：全屏红闪 + 音效；打中对手有命中音
- HUD：击杀数、死亡数、血量、对手血量、对局结果、FPS、血条、准星；右上角 Reset 重开

## 运行

1. 先启动服务端（见仓库根目录 README 的「快速开始」），监听 `ws://localhost:8080/`
2. 用 Godot 打开 `godot_client/project.godot` 后按 F5，
   或命令行直接运行：

   ```bash
   Godot_v4.7.2-stable_win64.exe --path godot_client
   ```

3. 点击画面进入游戏并锁定鼠标；ESC 释放鼠标后可点 Reset 或再次进入

> 服务端地址硬编码在 `scripts/fps_client.gd` 的 `WS_URL`，改端口时同步修改。

## 代码组织

```text
godot_client/
├── scenes/main.tscn      # 主场景：Main + FpsClient + Sfx
├── scripts/
│   ├── main.gd           # 输入/相机(第一/第三人称)/按属性名查询与插值/持枪/HUD/音效（Node3D）
│   ├── world_store.gd    # 本地世界状态：实体-属性增量累积成完整世界（按名字取值）
│   ├── fps_client.gd     # WebSocket 传输层：连接/重连/收发（Node）
│   ├── body_entity.gd    # 每个服务端刚体一个渲染节点（场景材质配色/简单体）
│   └── sfx.gd            # 程序化音效（Node）
└── tests/                # 无头回归/冒烟测试（.gd；见 AGENTS.md §6）
```

- `FpsClient` 与渲染层**通过信号解耦**：`frame_received(frame)` 发同步帧、
  `matched_received(result)` 报匹配结果、`connection_changed(connected)` 报连接变化
  （断线后自动每秒重连、匹配后 2.5s 接收看门狗）
- `WorldStore` 按需累积：实体 ID → `{ 属性名: 值 }`，只按**名字**取值，不认识的属性
  照常存下、只是不渲染；full 帧先清空再整体覆盖，因此「首次进入 / 重连」在应用层无区别
- 场景、灯光、HUD 全部由代码构建（`main.gd`），没有外部资源依赖
- 网格/材质管理在 `BodyEntity`：只在形状/标志/材质签名变化时重建（`Body.Kind` /
  `Body.Size` / `Body.Static` / `Projectile` / `Body.Mat`），位置/旋转每帧写入
- 场景刚体按属性 `Body.Mat` 的材质号配色（`body_entity.gd` 的 `MATS` 表与
  `sim/map.go` 的 `Material` 编号一一对应，**只能追加**）；弹丸由标记属性
  `Projectile` 优先决定外观。客户端遇到不认识的材质号会
  退回默认配色（静态钢灰/动态木色）
- **玩家不走刚体渲染**：本地玩家是相机 + 人形 Avatar，远端玩家是另一个换色 Avatar，
  都按属性 `Player.Idx` 认人；服务端的**玩家命中盒**没有 `Body` 组件、不参与同步，
  客户端根本看不到它
- HUD 读数全部按属性名：`Player.Kills` / `Player.Deaths` / `Health` 取自各自的玩家实体，
  对局结果取自全局单例上的 `Game.Winner`（-1 = 进行中）
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
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/game_frame_test.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/reconnect_cleanup_test.gd
```

- `world_store_test.gd`：世界存储语义（full / removed / destroy / 未知属性）
- `frame_decode_test.gd`：`Frame` / `Schema` protobuf 解码
- `game_frame_test.gd`：渲染路径 + HUD 属性名（喂合成帧，不碰 WebSocket）
- `reconnect_cleanup_test.gd`：断线清理本地世界与插值状态

下面两个是**冒烟测试，需要活集群**（etcd + NATS + gate/match/game 三进程）：

```bash
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/ws_smoke.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/rejoin_smoke.gd
```

- `ws_smoke.gd`：匹配 + 统计 4 秒内的增量帧推送速率。预期输出
  `SMOKE unique_steps=81 span=80 elapsed_ms=4000` 左右（4 秒 × 20 Hz），且无 `SCRIPT ERROR`
- `rejoin_smoke.gd`：断线后重连回到**同一 match_id + 同一 player_idx**，且重连后收到
  一份 full 帧。它是新协议下**唯一**端到端验证「重连回同一局」的测试，改匹配 / 回局 /
  resync 链路后必跑；断言在载荷解析失败时会显式判失败（不会假通过）

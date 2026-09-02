# Jolt FPS Demo — Godot 客户端（godot_client/）

基于 [Godot 4.7](https://godotengine.org) 的 PVE 打怪 FPS 客户端，服务端是唯一权威。

- 第一人称视角：点击画面进入（鼠标捕获）、ESC 释放；**V 键切换第一/第三人称**
- 鼠标瞄准、WASD 移动、Space 跳跃、Shift 奔跑
- **PVE 打怪**：怪物按波刷新、清波自动刷下一波（服务端规则）；低难度
- **卡通资源**：金币走近拾取（自转+浮动动画）、击杀掉落；HUD 显示 WAVE / GOLD
- **卡通模型（全部程序化生成，无外部美术资源）**：
  - 第一人称：持枪 viewmodel（枪口闪光 + 后坐 + 卡通手套）
  - 第三人称：玩家人形 Avatar（大头/棒球帽/圆身体/短腿/双肩包）
  - 怪物：圆滚滚大眼小角短腿怪，跑动弹跳
  - 金币：金色双盘（内环发光）
- 服务端以 **20 Hz** 固定 tick 推进模拟，状态变化通过 WebSocket 主动推送
- 客户端 **60 Hz** 渲染：对运动刚体 / 玩家位置做**影子跟随插值**（渲染时刻滞后
  约一个 tick，在最近两帧快照之间 lerp / slerp），20 Hz 数据在 60 Hz 屏幕上保持平滑
- 客户端每渲染帧（约 60 Hz）上报输入；射击、重置也走同一条 WebSocket
- 受击反馈：全屏红闪 + 音效；拾取金币"叮"声
- HUD：分数、波次、金币、目标数、敌人数、FPS、血条、准星；右上角 Reset 重开

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
│   ├── main.gd           # 输入/相机(第一/第三人称)/插值渲染/持枪/HUD/金币/音效（Node3D）
│   ├── fps_client.gd     # WebSocket 传输层：连接/重连/收发（Node）
│   ├── body_entity.gd    # 每个服务端刚体一个渲染节点（卡通怪物/简单体）
│   └── sfx.gd            # 程序化音效（Node）
└── tests/ws_smoke.gd     # 无头冒烟测试（20 Hz 推送速率）
```

- `FpsClient` 与渲染层**通过信号解耦**：`state_received(state)` 发快照、
  `connection_changed(connected)` 报连接变化（断线后自动每秒重连）
- 场景、灯光、HUD 全部由代码构建（`main.gd`），没有外部资源依赖
- 网格/材质管理在 `BodyEntity`：只在快照中形状签名变化时重建，位置/旋转每帧写入
- 快照插值在 `main.gd`：双缓冲 + alpha = 距新快照到达时间 / 0.05s；
  同一 tick 重复推送只保留首次到达时间，tick 跳号（重置/重连）时清空缓冲
- 新生成 / 移除的刚体不参与插值：按最新快照创建或删除（弹丸消失有爆闪特效）
- 输入 60 Hz 上报（每渲染帧一条），跳跃在按下瞬间排队（边沿触发）；
  释放鼠标时补一条静止输入，避免服务端沿用旧速度
- 音效全部程序化生成（`sfx.gd` 合成 16-bit WAV），无音频文件
- 坐标约定：API 的 `pos` 是刚体质心（玩家为脚底），四元数 `[x,y,z,w]`
  直接映射 Godot `Quaternion`；胶囊总高 = 半高 × 2 + 半径 × 2

## 自动化测试

`tests/ws_smoke.gd` 是无头冒烟测试：连接 WebSocket 统计 4 秒内的状态推送，
验证服务端以接近 20 Hz 主动推送（需要服务端已启动）：

```bash
Godot_v4.7.2-stable_win64_console.exe --headless \
  --path godot_client --script res://tests/ws_smoke.gd
```

预期输出 `SMOKE unique_steps=81 span=80 elapsed_ms=4000` 左右（4 秒 × 20 Hz），
且没有任何 `SCRIPT ERROR`。

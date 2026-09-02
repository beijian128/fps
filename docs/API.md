# 网络协议

服务端只提供一个 WebSocket 端点：`ws://localhost:8080/`，这是游戏唯一的通信通道。
消息均为 JSON 文本帧（单帧，不分片）。

## 服务端 → 客户端：状态快照

服务端以 **20 Hz** 固定节奏推进模拟，每个 tick 结束后把完整状态广播给所有客户端；
射击、重置等即时操作也会立即补推一帧。

```jsonc
{
  "bodies": [ /* 见 bodyInfo */ ],
  "resources": [ /* 见 resourceInfo */ ],
  "player": { "pos": [x, y, z], "health": 100.0 },
  "step": 123,
  "score": 4,
  "wave": 2,
  "gold": 3
}
```

`player.pos` 是脚底位置（客户端眼高 = y + 1.6）。`step` 是模拟 tick 计数（20 Hz）；
`score` 是击碎靶球/击杀怪物得分；`wave` 是当前波次；`gold` 是拾取金币数。

### resourceInfo

```jsonc
{
  "id": 12,             // 资源 id
  "pos": [x, y, z],     // 悬浮位置
  "kind": 0             // 0 = 金币
}
```

金币是纯逻辑对象（不在物理世界，不参与碰撞），玩家走近自动拾取（**水平距离
1.0 m 判定**，金币悬浮高度差不计）；击杀怪物会在死亡位置掉落金币（场上上限 10 枚）。

### bodyInfo

```jsonc
{
  "id": 1,             // 服务端分配的刚体 id
  "type": 0,           // 0 = box, 1 = sphere, 2 = enemy capsule
  "static": false,     // 是否静态
  "target": false,     // 是否可破坏靶球
  "enemy": false,      // 是否敌人
  "projectile": false, // 是否弹丸
  "pos": [x, y, z],    // 质心位置
  "quat": [x, y, z, w],// 旋转四元数
  "size": [a, b, c],   // box 半边长；sphere 半径在 [0]；capsule 半径在 [0]、半高在 [1]
  "health": 3.0,       // 敌人血量（其他刚体为 0）
  "active": true       // 是否仍在模拟（未休眠）
}
```

## 客户端 → 服务端：上行消息

`type` 缺省时视为 `input`。

### input —— 上报输入（约 60 Hz）

```jsonc
{ "type": "input", "move": [wx, wz], "jump": false }
```

- `move`：世界空间水平期望速度（m/s），由客户端按相机朝向算出
- `jump`：**边沿触发**——服务端仅在收到后的下一个 tick 消费，客户端只需在按键
  按下瞬间置 true 一次

### shoot —— 发射弹丸

```jsonc
{ "type": "shoot", "origin": [x, y, z], "dir": [x, y, z] }
```

`dir` 会被服务端归一化（零向量忽略）。服务端立即补推一帧快照。

### reset —— 重建场景

```jsonc
{ "type": "reset" }
```

销毁并重建整个场景（地板、墙、箱子、靶球、敌人、金币），重置分数、血量、步数、
波次、输入状态，并立即补推一帧。

## 波次规则（PVE）

- 第 1 波 3 只怪物，之后每波 +1，最多 6 只；清空当前波 2 秒后刷下一波
- 低难度：怪物追击速度 2.2 m/s，贴身伤害 8/s（1.4 m 内），3 发子弹击杀一只
- 怪物不可推动玩家（玩家不可被挤开，见包装层 `CharacterImmovableListener`）

# HTTP API

服务端监听 `http://localhost:8080`。除静态资源外，所有接口返回 JSON。

## 通用状态结构 `state`

```jsonc
{
  "bodies": [ /* 见 bodyInfo */ ],
  "player": { "pos": [x, y, z], "health": 100.0 },
  "step": 123,
  "score": 4
}
```

`player.pos` 是脚底位置（眼高 = y + 1.6）。`step` 是累计步进次数，`score` 是服务端计分。

## bodyInfo

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

## 接口

### GET /api/state

返回当前完整状态，不推进模拟。

响应：`state`

### POST /api/step

推进模拟并返回最新状态。

请求：

```jsonc
{
  "frames": 1,        // 推进多少个 1/60 秒子步，范围 1..240
  "move": [wx, wz],   // 世界空间水平期望速度（m/s），由前端按朝向算出
  "jump": false       // 是否跳跃（仅首个子步生效）
}
```

响应：`state`

### POST /api/shoot

发射一枚弹丸。

请求：

```jsonc
{
  "origin": [x, y, z], // 相机位置
  "dir": [x, y, z]     // 相机朝向（服务端会归一化）
}
```

响应：

```jsonc
{
  "projectileID": 35,  // 新弹丸 id，0 表示失败
  "state": { /* state */ }
}
```

### POST /api/add

往场景里加一个刚体（调试用）。

请求：

```jsonc
{
  "kind": "box",       // "box" 或 "sphere"
  "static": false,     // 仅 box 有效
  "pos": [x, y, z],
  "vel": [x, y, z],
  "size": [hx, hy, hz],// box 半边长
  "radius": 0.5        // sphere 半径
}
```

响应：`state`

### POST /api/reset

销毁并重建整个场景（地板、墙、箱子、靶球、敌人），重置分数、血量和步数。

请求：无

响应：`state`

### POST /api/gravity

设置重力。

请求：

```jsonc
{ "g": [0, -9.81, 0] }
```

响应：`state`

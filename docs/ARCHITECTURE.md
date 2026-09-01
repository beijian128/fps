# 架构说明

## 分层

```text
┌──────────────────────────────────────────────┐
│  浏览器（Three.js，joltgo/web/）              │
│  渲染 / 第一人称相机 / HUD / 音效 / 输入      │
└──────────────────────┬───────────────────────┘
                       │ HTTP JSON（fetch）
┌──────────────────────▼───────────────────────┐
│  Go 服务（joltgo/main.go）                    │
│  游戏循环 / 敌人 AI / 弹丸命中处理 / 血量     │
└──────────────────────┬───────────────────────┘
                       │ cgo
┌──────────────────────▼───────────────────────┐
│  C 包装层（joltgo/wrapper/jolt_c.{h,cpp}）    │
│  extern "C"，只暴露 POD 类型                  │
└──────────────────────┬───────────────────────┘
                       │ C++ API
┌──────────────────────▼───────────────────────┐
│  Jolt Physics（libJolt.a → libjolt_c.dll）    │
└──────────────────────────────────────────────┘
```

## 为什么用 cgo + C ABI

cgo 只能直接调用 C ABI，不能调用 C++ 类/重载/模板。因此所有 Jolt C++ 调用都被
封装成 `extern "C"` 的普通函数，参数和返回值只用：

- 整数 / 浮点数
- 定长数组（`float pos[3]` 等）
- 不透明指针（`JoltWorld *`）

禁止跨边界传递 `std::string`、`std::vector`、C++ 对象、引用或异常。

## 数据流

游戏状态由服务端持有，前端是「输入 + 展示」的瘦客户端：

1. 前端每 1/60 秒调用一次 `/api/step`，带上玩家输入（世界空间水平速度 + 是否跳跃）
2. 服务端在一个互斥锁内：更新角色控制器 → 更新敌人 AI → 步进物理 → 处理弹丸命中 → 结算伤害
3. 服务端返回完整状态快照（所有刚体 + 玩家位置/血量 + 分数）
4. 前端根据快照同步 Three.js 场景、相机位置和 HUD

这种「服务端权威」的设计让所有物理/游戏逻辑只存在于一处，前端换框架不影响逻辑。

## 核心机制

### 玩家角色控制器

- 使用 Jolt `CharacterVirtual`，形状是「胶囊 + 向上平移」：
  - 胶囊半径 0.4 m，圆柱半高 0.5 m，总高 1.8 m
  - 用 `RotatedTranslatedShape` 上移 0.9 m，使形状底部位于脚底（`GetPosition()` 即脚底位置）
- 每步由服务端调用 `jolt_update_character`：
  - 水平速度直接来自前端输入（走 8 m/s，跑 14 m/s）
  - 垂直速度：着地时归零、跳跃设 8 m/s、再叠加重力积分
  - `ExtendedUpdate` 负责碰撞、贴地、上台阶
- 前端只保留偏航/俯仰（鼠标视角），相机位置 = 服务端返回的脚底位置 + 1.6 m 眼高

### 弹丸

- 前端射击 → 服务端 `jolt_fire_projectile` 生成一枚小球：
  - 半径 0.08 m，初速 60 m/s
  - `EMotionQuality::LinearCast`，高速下不会穿透薄墙
  - `SetUserData` 标记为弹丸
- 命中判定依赖 `PhysicsSystem` 的 `ContactListener`：
  - `OnContactAdded` 在碰撞发生时（工作线程内）把「弹丸 BodyID + 目标 BodyID」写入受互斥锁保护的队列
  - 每步结束后服务端 `jolt_poll_projectile_hits` 排空队列
- 命中后处理：命中靶球 → 销毁并加分；命中敌人 → 扣血，血尽销毁并重生；命中其他 → 移除弹丸
- 弹丸最多存活 180 步（3 秒），超时自动移除，防止飞向天空永不落地

### 敌人 AI

- 敌人是动态胶囊刚体（半径 0.35 m，半高 0.5 m，初始 3 点血）
- 每步 `updateEnemies` 把敌人的水平速度设为「指向玩家的单位向量 × 3 m/s」，实现追击
- 与玩家距离小于 1.4 m 时每步扣 0.25 点血；玩家血量归零则传送回出生点并回满
- 敌人死亡后 `spawnEnemy` 在距玩家 10 m 以外随机位置重生，保持场上 3 个敌人

## 关键设计决策与坑

### Jolt 必须和包装层用同一套编译参数

Jolt 的 Release 构建定义 `NDEBUG`、`JPH_DEBUG_RENDERER`、`JPH_PROFILE_ENABLED`
以及 CPU 指令集宏（`JPH_USE_AVX2` 等）。如果包装层自己手写编译命令、漏掉这些宏，
会出现两类问题：

- 缺 `NDEBUG` → `JPH_DEBUG` 被开启 → 引用 `JPH::AssertFailed` 但库内没定义 → 链接失败
- 指令集宏不一致 → 内联函数 ABI 不一致 → 链接或运行期错误

因此 `joltgo/CMakeLists.txt` 用 `add_subdirectory` 把 Jolt 作为子项目引入，
编译宏和指令集标志自动继承，避免手工维护。

### DLL 静态链接 C++ 运行库

`libjolt_c.dll` 编译时加了 `-static`，把 libstdc++ / libgcc / winpthread 静态链入，
所以运行时只需 `libjolt_c.dll` 一个文件，不需要 MSYS2 的 DLL 伴生。

### UCRT64 与 MINGW64 不能混用

项目统一使用 UCRT64 工具链（`C:\msys64\ucrt64\bin`）。`build.ps1` 会在编译包装层时
把 PATH 切到 `/ucrt64/bin`，并在 `go build` 时显式设置 `CC`/`CXX` 指向 UCRT64 编译器。
混用 MINGW64 会因 C 运行时不同（ucrtbase vs msvcrt）导致链接或运行异常。

### 服务端互斥锁

Go 服务用一把全局 `sync.Mutex` 串行化所有会改动物理世界/游戏状态的请求，
Jolt 的 `ContactListener` 在 C++ 侧另有自己的互斥锁保护命中队列。
当前规模下足够，未来若追求性能可改为读写锁或快照式返回。

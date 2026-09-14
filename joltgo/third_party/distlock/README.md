# distlock

基于 Redis 的 Go 分布式锁，从零实现，无 Redisson 等第三方锁库依赖（仅依赖 go-redis 客户端）。

## 特性

- **原子加锁/释放**：加锁、释放、续期全部通过 Lua 脚本在 Redis 端原子执行，无检查-写入竞态
- **可重入**：同一实例重复加锁为计数，需同样次数解锁
- **看门狗自动续期**：持有期间每 TTL/3 续期一次，业务耗时远超 TTL 也不会丢锁；持有者崩溃后锁按 TTL 自动过期，不会死锁
- **安全释放**：释放脚本校验持有者 token，绝不误删他人的锁
- **锁丢失感知**：续期发现锁丢失时触发 `OnLost` 回调（最多一次），可用于告警/降级
- **阻塞获取**：`Lock(ctx)` 支持轮询重试、超时与取消
- **fencing token 支持**：可通过 `WithToken` 注入自增令牌，配合数据端校验抵御"锁已过期但旧持有者仍在写"的极端场景

## 快速开始

```go
package main

import (
	"context"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
	"distlock"
)

func main() {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})

	lock := distlock.New(client, "order:10086:lock",
		distlock.WithTTL(30*time.Second),      // 默认 30s
		distlock.WithOnLost(func() {           // 锁丢失告警（可选）
			log.Println("WARN: lock lost")
		}),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := lock.Lock(ctx); err != nil { // 阻塞直到获取成功或超时
		log.Fatalf("acquire failed: %v", err)
	}
	defer lock.Unlock(context.Background())

	// 业务逻辑，耗时可以超过 TTL，看门狗会自动续期
	time.Sleep(time.Minute)
}
```

非阻塞获取：

```go
ok, err := lock.TryLock(ctx)
if err != nil {
	return err
}
if !ok {
	return errors.New("资源被占用")
}
defer lock.Unlock(ctx)
```

## API

| 方法/选项 | 说明 |
|---|---|
| `New(client, key, opts...)` | 创建锁实例，每个实例持有唯一 token |
| `TryLock(ctx)` | 尝试获取一次，立即返回 `(bool, error)` |
| `Lock(ctx)` | 阻塞获取，直到成功或 ctx 取消/超时 |
| `Unlock(ctx)` | 释放锁；未持有时返回 `ErrNotOwned` |
| `Token()` | 持有者标识，用于日志与 fencing token |
| `IsHeld()` / `IsLost()` | 本地视角的持有/丢失状态 |
| `WithTTL(d)` | 锁过期时间（默认 30s），决定持有者崩溃后锁多久自动释放 |
| `WithRetryInterval(d)` | Lock 轮询间隔（默认 50ms） |
| `WithWatchdog(bool)` | 自动续期开关（默认开启） |
| `WithOnLost(fn)` | 锁丢失回调，最多触发一次 |
| `WithToken(s)` | 覆盖随机 token，可注入自增 fencing token |

## 实现原理

锁在 Redis 中存储为 hash：field 为持有者 token，value 为持有深度。

- **加锁**（Lua 原子）：key 不存在 → `HSET token 1` + `PEXPIRE`；field 为本 token → `HINCRBY +1`（可重入）+ 刷新 TTL；否则失败
- **释放**（Lua 原子）：`HINCRBY -1`，计数归零才 `DEL`；只操作自己的 field，天然防误删
- **续期**（Lua 原子）：仅当 field 仍为本 token 时刷新 TTL，锁丢失时返回失败

看门狗 goroutine 与业务 goroutine 通过互斥锁和停止通道协作；解锁/丢失时看门狗停止，不会残留"复活"已释放的锁。

## 设计取舍

- **不用 Redlock**：Redlock 的互斥性依赖时钟同步、网络延迟有界等假设，存在著名争议（Kleppmann vs antirez）。单节点 + 主从场景，看门狗 + fencing token 是工程上更实用的组合。
- **锁丢失的最终兜底是业务**：锁只是减少并发冲突的手段。资金类场景应配合幂等、版本号或 fencing token 校验，不要依赖锁本身保证强一致。
- **TTL 选择**：看门狗开启时 TTL 决定"持有者宕机后锁多久自动释放"，通常 10~60s；关闭看门狗时业务耗时必须小于 TTL。

## 测试

```bash
go test -race ./...   # 单元测试（miniredis 内存模拟，无需真实 Redis）
REDIS_ADDR=127.0.0.1:6379 go test -run TestIntegration ./...  # 集成测试（需真实 Redis）
```

## 目录结构

```
lock.go            锁核心：Lock/TryLock/Unlock、看门狗、状态管理
scripts.go         Lua 脚本与执行封装
options.go         配置选项
lock_test.go       单元测试（互斥、可重入、看门狗、超时、并发竞争）
integration_test.go 集成测试（设置 REDIS_ADDR 后运行）
```

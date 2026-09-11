// Package kv 是 Redis 连接的唯一入口：只负责按地址建客户端并探活，
// 不含任何业务键名。各业务包（account / online / match / gate）自己定义键空间。
package kv

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// DefaultAddr 是本地默认地址（与 deploy/redis-server.exe 的监听端口一致）。
const DefaultAddr = "localhost:6379"

// Open 按地址建 Redis 客户端并 Ping 一次。尽早探活是为了让「地址配错」
// 在进程启动时就暴露，而不是等到第一个玩家登录才报错。
func Open(ctx context.Context, addr string) (*redis.Client, error) {
	if addr == "" {
		addr = DefaultAddr
	}
	c := redis.NewClient(&redis.Options{
		Addr: addr,
		// 不开这个开关的话 go-redis 会在读写 socket 前把 ctx 换成 Background
		// （baseClient.context()，redis.go:641），调用方设的 deadline 只能拦住
		// 连接池等待与重试间隔，拦不住一次挂死的读 —— 那条路只受 ReadTimeout(3s)
		// 约束。gate 的会话归属读写跑在登录/断连的关键路径上，必须能靠 ctx 掐断
		// （见 gate.onlineTimeout）。
		ContextTimeoutEnabled: true,
	})
	if err := c.Ping(ctx).Err(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("kv: ping %s: %w", addr, err)
	}
	return c, nil
}

// Package kv 是 Redis 连接的唯一入口：只负责按地址建客户端并探活，
// 不含任何业务键名。各业务包（account / online / match / gate）自己定义键空间。
package kv

import (
	"context"
	"fmt"
	"time"

	redigo "github.com/gomodule/redigo/redis"
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

// OpenRedigo 建一个 redigo 连接池。protoc-gen-redis 生成的代码以 redigo.Conn
// 为接口，因此持久化仓储复用该池；普通业务仍使用上面的 go-redis 客户端。
func OpenRedigo(ctx context.Context, addr string) (*redigo.Pool, error) {
	if addr == "" {
		addr = DefaultAddr
	}
	pool := &redigo.Pool{
		MaxIdle:   4,
		MaxActive: 16,
		Wait:      true,
		Dial: func() (redigo.Conn, error) {
			return redigo.Dial("tcp", addr,
				redigo.DialConnectTimeout(3*time.Second),
				redigo.DialReadTimeout(3*time.Second),
				redigo.DialWriteTimeout(3*time.Second),
			)
		},
		TestOnBorrow: func(c redigo.Conn, lastUsed time.Time) error {
			if time.Since(lastUsed) < time.Minute {
				return nil
			}
			_, err := c.Do("PING")
			return err
		},
	}
	conn, err := pool.GetContext(ctx)
	if err != nil {
		_ = pool.Close()
		return nil, fmt.Errorf("kv: redigo ping get %s: %w", addr, err)
	}
	_, pingErr := conn.Do("PING")
	_ = conn.Close()
	if pingErr != nil {
		_ = pool.Close()
		return nil, fmt.Errorf("kv: redigo ping %s: %w", addr, pingErr)
	}
	return pool, nil
}

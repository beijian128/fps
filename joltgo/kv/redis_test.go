package kv

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

// ContextTimeoutEnabled 必须开着，否则调用方设的 deadline 是假的：
// go-redis 在读写 socket 前会把 ctx 换成 Background（baseClient.context()），
// 一次挂死的读只受 ReadTimeout(3s) 约束，掐不断。gate 的会话归属读写跑在
// 登录/断连关键路径上，靠的就是 ctx deadline（见 gate.onlineTimeout）。
func TestOpenRespectsContextDeadlines(t *testing.T) {
	mr := miniredis.RunT(t)
	c, err := Open(context.Background(), mr.Addr())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	if !c.Options().ContextTimeoutEnabled {
		t.Fatal("ContextTimeoutEnabled 必须为 true，否则调用方的 ctx deadline 掐不断 Redis 读写")
	}
}

// 地址配错时 Open 必须当场报错，而不是把问题拖到第一个玩家登录。
func TestOpenFailsFastOnBadAddr(t *testing.T) {
	mr := miniredis.RunT(t)
	addr := mr.Addr()
	mr.Close() // 端口上已经没人在听了

	if _, err := Open(context.Background(), addr); err == nil {
		t.Fatal("连不上时 Open 应返回错误")
	}
}

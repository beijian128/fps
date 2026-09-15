package match

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"joltgo/bot"
	"joltgo/game/protos"
	"joltgo/online"
)

// newBotsTestComponent 造一个带真队列与真密钥的 match 组件。
//
// app 传 joinTestApp：它的 GetServersByType 恒返回错误，正好让 handler 收尾那次
// tryMatch 变成无害的空转 —— 本文件只验证「入队与入口检查」，配对行为由
// bot_pairing_test.go 用带 game 节点的 startMatchTestApp 单独覆盖。
func newBotsTestComponent(t *testing.T, sess *joinTestSession) (*Component, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	app := &joinTestApp{}
	if sess != nil {
		app.sess = sess
	}
	return New(app, NewQueue(rdb), online.NewStore(rdb), "s3cret"), mr
}

func TestQueueBotsAddsRequestedBots(t *testing.T) {
	c, _ := newBotsTestComponent(t, nil)
	ctx := context.Background()

	reply, err := c.AddBots(ctx, &protos.AddBotsMsg{Count: 2, AdminKey: "s3cret"})
	if err != nil {
		t.Fatalf("QueueBots 报错: %v", err)
	}
	if !reply.Ok || reply.Enqueued != 2 {
		t.Fatalf("reply=%+v，期望 ok 且入队 2 个", reply)
	}
	if got := c.queue.queueSize(ctx, t); got != 2 {
		t.Fatalf("队列 %d 人，期望 2", got)
	}
}

func TestQueueBotsRejectsClientCall(t *testing.T) {
	// 客户端经 gate 转发来的调用带会话；管理入口必须拒绝，且**不动队列**。
	c, _ := newBotsTestComponent(t, &joinTestSession{uid: "7"})
	ctx := context.Background()

	reply, err := c.AddBots(ctx, &protos.AddBotsMsg{Count: 2, AdminKey: "s3cret"})
	if err != nil {
		t.Fatalf("拒绝路径不应返回 error: %v", err)
	}
	if reply.Ok || reply.Reason != "forbidden" {
		t.Fatalf("reply=%+v，期望 forbidden", reply)
	}
	if got := c.queue.queueSize(ctx, t); got != 0 {
		t.Fatalf("被拒绝的调用不该动队列，实际 %d 人", got)
	}
}

func TestQueueBotsRejectsWrongKey(t *testing.T) {
	c, _ := newBotsTestComponent(t, nil)
	ctx := context.Background()

	reply, err := c.AddBots(ctx, &protos.AddBotsMsg{Count: 2, AdminKey: "nope"})
	if err != nil {
		t.Fatalf("拒绝路径不应返回 error: %v", err)
	}
	if reply.Ok || reply.Reason != "forbidden" {
		t.Fatalf("reply=%+v，期望 forbidden", reply)
	}
	if got := c.queue.queueSize(ctx, t); got != 0 {
		t.Fatalf("被拒绝的调用不该动队列，实际 %d 人", got)
	}
}

func TestQueueBotsClampsCount(t *testing.T) {
	c, _ := newBotsTestComponent(t, nil)
	ctx := context.Background()

	reply, err := c.AddBots(ctx, &protos.AddBotsMsg{Count: maxBotsPerRequest + 100, AdminKey: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	if !reply.Ok || reply.Enqueued != maxBotsPerRequest {
		t.Fatalf("reply=%+v，期望截到 %d", reply, maxBotsPerRequest)
	}
	if got := c.queue.queueSize(ctx, t); got != maxBotsPerRequest {
		t.Fatalf("队列 %d 人，期望 %d", got, maxBotsPerRequest)
	}
}

func TestQueueBotsWithZeroCountDoesNothing(t *testing.T) {
	c, _ := newBotsTestComponent(t, nil)
	ctx := context.Background()

	reply, err := c.AddBots(ctx, &protos.AddBotsMsg{Count: 0, AdminKey: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	if !reply.Ok || reply.Enqueued != 0 {
		t.Fatalf("reply=%+v，期望 ok 且 0", reply)
	}
	if got := c.queue.queueSize(ctx, t); got != 0 {
		t.Fatalf("队列 %d 人，期望 0", got)
	}
}

func TestQueueBotsGeneratesUniqueBotUIDs(t *testing.T) {
	c, _ := newBotsTestComponent(t, nil)
	ctx := context.Background()

	if _, err := c.AddBots(ctx, &protos.AddBotsMsg{Count: 4, AdminKey: "s3cret"}); err != nil {
		t.Fatal(err)
	}
	members, err := c.queue.rdb.ZRange(ctx, queueKey, 0, -1).Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 4 {
		t.Fatalf("应有 4 个成员，得到 %d（uid 重复会被 ZSET 去重）", len(members))
	}
	seen := map[string]bool{}
	for _, uid := range members {
		if !bot.Is(uid) {
			t.Fatalf("机器人 uid %q 必须带 bot 前缀", uid)
		}
		if seen[uid] {
			t.Fatalf("uid %q 重复", uid)
		}
		seen[uid] = true
	}
}

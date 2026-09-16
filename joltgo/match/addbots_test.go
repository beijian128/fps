package match

import (
	"context"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/topfreegames/pitaya/v3/pkg/component"
	"joltgo/bot"
	"joltgo/game/protos"
	"joltgo/online"
)

// newBotsTestComponent 造一个带真队列的 match 组件。
//
// app 传 joinTestApp：它的 GetServersByType 恒返回错误，正好让 handler 收尾那次
// tryMatch 变成无害的空转 —— 本文件只验证「入队与参数边界」，配对行为由
// bot_pairing_test.go 用带 game 节点的 startMatchTestApp 单独覆盖。
func newBotsTestComponent(t *testing.T) (*Component, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return New(&joinTestApp{}, NewQueue(rdb), online.NewStore(rdb)), mr
}

func TestQueueBotsAddsRequestedBots(t *testing.T) {
	c, _ := newBotsTestComponent(t)
	ctx := context.Background()

	reply, err := c.AddBots(ctx, &protos.AddBotsMsg{Count: 2})
	if err != nil {
		t.Fatalf("AddBots 报错: %v", err)
	}
	if !reply.Ok || reply.Enqueued != 2 {
		t.Fatalf("reply=%+v，期望 ok 且入队 2 个", reply)
	}
	if got := c.queue.queueSize(ctx, t); got != 2 {
		t.Fatalf("队列 %d 人，期望 2", got)
	}
}

func TestQueueBotsClampsCount(t *testing.T) {
	c, _ := newBotsTestComponent(t)
	ctx := context.Background()

	reply, err := c.AddBots(ctx, &protos.AddBotsMsg{Count: maxBotsPerRequest + 100})
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
	c, _ := newBotsTestComponent(t)
	ctx := context.Background()

	reply, err := c.AddBots(ctx, &protos.AddBotsMsg{Count: 0})
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
	c, _ := newBotsTestComponent(t)
	ctx := context.Background()

	if _, err := c.AddBots(ctx, &protos.AddBotsMsg{Count: 4}); err != nil {
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

// TestAddBotsIsBothHandlerAndRemote 把「pitaya 的收录规则」这件事钉在测试里。
//
// 为什么要钉：这个签名（ctx + 指针入参、指针 + error 返回）同时满足 isHandlerMethod
// 与 isRemoteMethod，所以 AddBots **必然**既进 remotes 表（gm 用 RPCTo 调它）又进
// handlers 表（客户端经 gate 转发时查的就是这张表）。拆组件躲不掉 —— ExtractHandler
// 没有任何排除机制。
//
// 这条断言的作用是「让事实显式」：如果将来 pitaya 升级后行为变了（比如不再双重收录，
// 或加了排除机制），这条测试会红，提醒我们重新评估 gate 白名单是否还是必需的。
func TestAddBotsIsBothHandlerAndRemote(t *testing.T) {
	svc := component.NewService(&Component{}, []component.Option{
		component.WithName("match"),
		component.WithNameFunc(strings.ToLower),
	})
	if err := svc.ExtractHandler(); err != nil {
		t.Fatalf("ExtractHandler: %v", err)
	}
	if err := svc.ExtractRemote(); err != nil {
		t.Fatalf("ExtractRemote: %v", err)
	}
	if _, ok := svc.Remotes["addbots"]; !ok {
		t.Fatal("AddBots 必须在 remote 表里（gm 是用 RPCTo 调的）")
	}
	if _, ok := svc.Handlers["addbots"]; !ok {
		t.Fatal("AddBots 同时也在 handler 表里 —— 这正是 gate 白名单必须存在的原因。" +
			"若这里失败了，说明 pitaya 的收录规则变了，白名单的前提要重新评估")
	}
}

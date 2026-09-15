package match

import (
	"context"
	"fmt"
	"testing"

	"joltgo/bot"
)

func TestQueueBotsEnqueuesRequestedCount(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()
	seq := 0
	next := func() string {
		seq++
		return bot.New(fmt.Sprintf("t%d", seq))
	}

	n, err := q.QueueBots(ctx, 3, next)
	if err != nil {
		t.Fatalf("QueueBots 报错: %v", err)
	}
	if n != 3 {
		t.Fatalf("入队 %d 个，期望 3", n)
	}
	if got := q.queueSize(ctx, t); got != 3 {
		t.Fatalf("队列 %d 人，期望 3", got)
	}

	members, err := q.rdb.ZRange(ctx, queueKey, 0, -1).Result()
	if err != nil {
		t.Fatal(err)
	}
	for _, uid := range members {
		if !bot.Is(uid) {
			t.Fatalf("队列里的机器人 uid %q 必须带 bot 前缀", uid)
		}
	}
}

func TestQueueBotsWithNonPositiveCountWritesNothing(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()
	called := false
	next := func() string { called = true; return bot.New("x") }

	for _, n := range []int{0, -1} {
		got, err := q.QueueBots(ctx, n, next)
		if err != nil {
			t.Fatalf("QueueBots(%d) 报错: %v", n, err)
		}
		if got != 0 {
			t.Fatalf("QueueBots(%d) = %d，期望 0", n, got)
		}
	}
	if called {
		t.Fatal("count<=0 时不应该生成 uid")
	}
	if got := q.queueSize(ctx, t); got != 0 {
		t.Fatalf("队列 %d 人，期望 0", got)
	}
}

func TestQueueBotsWithoutGeneratorWritesNothing(t *testing.T) {
	q, _ := newTestQueue(t)
	n, err := q.QueueBots(context.Background(), 3, nil)
	if err != nil || n != 0 {
		t.Fatalf("nil 生成器应空操作，得到 n=%d err=%v", n, err)
	}
}

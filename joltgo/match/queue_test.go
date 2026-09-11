package match

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestQueue(t *testing.T) (*Queue, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewQueue(rdb), mr
}

// queueSize 返回队列当前人数。用 ZCARD 而不是把队列内容取出来（后者会干扰断言）。
func (q *Queue) queueSize(ctx context.Context, t *testing.T) int {
	t.Helper()
	n, err := q.rdb.ZCard(ctx, queueKey).Result()
	if err != nil {
		t.Fatalf("ZCARD 报错: %v", err)
	}
	return int(n)
}

// nowMs 取 Redis 服务端当前时间（毫秒），与队列脚本用的是同一个时钟。
func (q *Queue) nowMs(ctx context.Context, t *testing.T) (int64, error) {
	t.Helper()
	return redis.NewScript(`local t = redis.call('TIME')
return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)`).
		Run(ctx, q.rdb, []string{}).Int64()
}

func TestPopPairNeedsTwo(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	// 一个人时不能弹：直接 ZPOPMIN 会把人白白弹出队列，
	// 弹出来又凑不齐只能丢回去，还会打乱等待顺序。
	if err := q.Enqueue(ctx, "a"); err != nil {
		t.Fatalf("Enqueue 报错: %v", err)
	}
	got, err := q.PopPair(ctx)
	if err != nil {
		t.Fatalf("PopPair 报错: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("不足 2 人时应返回空，得到 %v", got)
	}
	if n := q.queueSize(ctx, t); n != 1 {
		t.Fatalf("a 应还在队列里，得到 %d 人", n)
	}
}

func TestPopPairReturnsOldestFirst(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	// 直接写死 score 而不是靠 Enqueue 的时钟：同一毫秒内入队的两条 score 相同，
	// ZSET 会退化成按 member 字典序排 —— 那样测出来的顺序是巧合不是语义。
	for _, m := range []struct {
		uid   string
		score float64
	}{{"c", 30}, {"a", 10}, {"b", 20}} {
		if err := q.rdb.ZAdd(ctx, queueKey, redis.Z{Score: m.score, Member: m.uid}).Err(); err != nil {
			t.Fatalf("ZAdd 报错: %v", err)
		}
	}

	got, err := q.PopPair(ctx)
	if err != nil {
		t.Fatalf("PopPair 报错: %v", err)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("应弹出 score 最小的 a/b，得到 %v", got)
	}
	if q.queueSize(ctx, t) != 1 {
		t.Fatalf("应只剩 c")
	}
}

func TestEnqueueDedupsByUid(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	if err := q.Enqueue(ctx, "a"); err != nil {
		t.Fatalf("Enqueue 报错: %v", err)
	}
	if err := q.Enqueue(ctx, "a"); err != nil {
		t.Fatalf("重复 Enqueue 报错: %v", err)
	}
	if n := q.queueSize(ctx, t); n != 1 {
		t.Fatalf("同一个 uid 重复入队应只占一条（ZSET 去重），得到 %d", n)
	}

	// 再加一个人，应能立刻凑成一对 —— 若去重失效，这里会弹出 a/a。
	if err := q.Enqueue(ctx, "b"); err != nil {
		t.Fatalf("Enqueue(b) 报错: %v", err)
	}
	got, _ := q.PopPair(ctx)
	if len(got) != 2 || got[0] == got[1] {
		t.Fatalf("应弹出两个不同的人，得到 %v", got)
	}
}

func TestPopStale(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	// 刚入队的人不该被单人兜底拿走。注意不能用 miniredis 的 FastForward 来
	// 「等 10 秒」—— 队列的时间戳取自 Redis 的 TIME 命令，而 miniredis 的 TIME
	// 返回真实墙钟，不受 FastForward 影响。所以这里直接写 score。
	now, err := q.nowMs(ctx, t)
	if err != nil {
		t.Fatalf("取 Redis 时间报错: %v", err)
	}
	if err := q.rdb.ZAdd(ctx, queueKey, redis.Z{Score: float64(now), Member: "fresh"}).Err(); err != nil {
		t.Fatalf("ZAdd 报错: %v", err)
	}

	got, err := q.PopStale(ctx, 10*time.Second)
	if err != nil {
		t.Fatalf("PopStale 报错: %v", err)
	}
	if got != "" {
		t.Fatalf("未超时不该弹出，得到 %q", got)
	}

	// 早就入队的人（score 是 1 秒 = 1970 年）应被拿走。
	if err := q.rdb.ZAdd(ctx, queueKey, redis.Z{Score: 1000, Member: "stale"}).Err(); err != nil {
		t.Fatalf("ZAdd 报错: %v", err)
	}
	got, err = q.PopStale(ctx, 10*time.Second)
	if err != nil {
		t.Fatalf("PopStale 报错: %v", err)
	}
	if got != "stale" {
		t.Fatalf("超时后应弹出 stale，得到 %q", got)
	}
	if q.queueSize(ctx, t) != 1 {
		t.Fatalf("应只剩 fresh")
	}
}

// 多个 match 节点并发抢同一个人时，只有一个能拿到（Lua 原子取）。
func TestPopStaleIsAtomic(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()
	if err := q.rdb.ZAdd(ctx, queueKey, redis.Z{Score: 1000, Member: "stale"}).Err(); err != nil {
		t.Fatalf("ZAdd 报错: %v", err)
	}

	const n = 8
	results := make(chan string, n)
	for i := 0; i < n; i++ {
		go func() {
			got, _ := q.PopStale(context.Background(), 10*time.Second)
			results <- got
		}()
	}
	hit := 0
	for i := 0; i < n; i++ {
		if <-results != "" {
			hit++
		}
	}
	if hit != 1 {
		t.Fatalf("并发抢同一个人应恰好一个成功，得到 %d", hit)
	}
}

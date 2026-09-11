package match

import (
	"context"
	"sync"
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
//
// 单轮 8 个 goroutine 太容易全错过竞态窗口 —— 评审实测：把 Lua 换成
// 「ZRangeByScore 再 ZRem」的两趟实现，单轮版本 30 次里能过 28 次。
// 所以这里循环多轮，每轮独立播种同一个人。
func TestPopStaleIsAtomic(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	const rounds = 60
	const workers = 8
	for round := 0; round < rounds; round++ {
		if err := q.rdb.ZAdd(ctx, queueKey, redis.Z{Score: 1000, Member: "stale"}).Err(); err != nil {
			t.Fatalf("ZAdd 报错: %v", err)
		}
		results := make(chan string, workers)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				got, _ := q.PopStale(context.Background(), 10*time.Second)
				results <- got
			}()
		}
		wg.Wait()
		close(results)

		hit := 0
		for got := range results {
			if got != "" {
				hit++
			}
		}
		if hit != 1 {
			t.Fatalf("第 %d 轮：并发抢同一个人应恰好一个成功，得到 %d", round, hit)
		}
	}
}

// Enqueue 在正常路径上绝不能返回 redis.Nil。
//
// 脚本没有返回值时 go-redis 会把服务端的 nil bulk 报成 redis.Nil，于是
// 「正常入队」与「真出错」在调用方看来一模一样 —— 而 startMatch 里四条重新
// 入队路径全靠这个判断，误判会让玩家被静默丢弃（已原子弹出、无人再管，
// 客户端只发一次 match.join 然后永远等 onMatched）。
func TestEnqueueReturnsNilOnSuccess(t *testing.T) {
	q, _ := newTestQueue(t)
	if err := q.Enqueue(context.Background(), "a"); err != nil {
		t.Fatalf("正常入队不该报错，得到 %v", err)
	}
	// 确认确实入队了，而不是「错误被吞掉、其实什么也没做」。
	if got, _ := q.rdb.ZRange(context.Background(), queueKey, 0, -1).Result(); len(got) != 1 || got[0] != "a" {
		t.Fatalf("队列里应有 a，得到 %v", got)
	}
}

// 真出错时必须如实报错，不能被当成「成功但没返回值」吞掉。
func TestEnqueuePropagatesRealError(t *testing.T) {
	q, mr := newTestQueue(t)
	mr.SetError("LOADING Redis is loading the dataset in memory")
	if err := q.Enqueue(context.Background(), "a"); err == nil {
		t.Fatal("Redis 报错时 Enqueue 必须返回错误")
	}
}

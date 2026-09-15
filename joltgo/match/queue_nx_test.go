package match

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// 客户端在匹配期每 15 秒静默重发一次 match.join：入队必须是 NX，重发不能刷新
// 「入队时间」，否则服务端算出来的等待时长永远在 0–15 秒之间跳。
//
// 注意不能靠 miniredis 的 FastForward 造时间差：队列的时间戳取自 Redis 的 TIME，
// 而 miniredis 的 TIME 返回真实墙钟、不受 FastForward 影响（见 queue_test.go 里
// TestPopStale 的说明）。这里改成「两次入队之间真的睡几毫秒，再比对 score 是否
// 完全没变」—— 覆盖式 ZADD 下这段时间差必然写进 score。
func TestEnqueueKeepsOriginalWaitTime(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	if err := q.Enqueue(ctx, "u1"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	first, err := q.rdb.ZScore(ctx, queueKey, "u1").Result()
	if err != nil {
		t.Fatalf("ZScore: %v", err)
	}

	time.Sleep(5 * time.Millisecond)

	if err := q.Enqueue(ctx, "u1"); err != nil {
		t.Fatalf("重复 Enqueue: %v", err)
	}
	second, err := q.rdb.ZScore(ctx, queueKey, "u1").Result()
	if err != nil {
		t.Fatalf("ZScore: %v", err)
	}

	if n := q.queueSize(ctx, t); n != 1 {
		t.Fatalf("重复入队不应产生第二条，得到 %d", n)
	}
	if second != first {
		t.Fatalf("重复入队刷新了入队时间：%.0f → %.0f", first, second)
	}
}

func TestSnapshotReportsQueueSizeAndWait(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	now, err := q.nowMs(ctx, t)
	if err != nil {
		t.Fatalf("nowMs: %v", err)
	}
	// 直接播种 score（不能借 FastForward，理由同上一条用例）。
	if err := q.rdb.ZAdd(ctx, queueKey,
		redis.Z{Score: float64(now - 8000), Member: "u1"},
		redis.Z{Score: float64(now - 3000), Member: "u2"},
	).Err(); err != nil {
		t.Fatalf("ZAdd: %v", err)
	}

	total, entries, err := q.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if total != 2 || len(entries) != 2 {
		t.Fatalf("队列人数/条目数不对: total=%d entries=%+v", total, entries)
	}
	// ZRANGE 按 score 升序：先入队的在前。
	if entries[0].UID != "u1" || entries[1].UID != "u2" {
		t.Fatalf("顺序不对: %+v", entries)
	}
	if entries[0].WaitedSeconds != 8 || entries[1].WaitedSeconds != 3 {
		t.Fatalf("等待时长不对: %+v", entries)
	}

	// 空队列不是错误。
	q.rdb.Del(ctx, queueKey)
	total, entries, err = q.Snapshot(ctx)
	if err != nil || total != 0 || len(entries) != 0 {
		t.Fatalf("空队列: total=%d entries=%+v err=%v", total, entries, err)
	}
}

func TestRemoveTakesUIDOutOfQueue(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	if err := q.Enqueue(ctx, "u1"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	removed, err := q.Remove(ctx, "u1")
	if err != nil || !removed {
		t.Fatalf("Remove: removed=%v err=%v", removed, err)
	}
	if removed, err := q.Remove(ctx, "u1"); err != nil || removed {
		t.Fatalf("重复 Remove 应返回 false: removed=%v err=%v", removed, err)
	}
	if n := q.queueSize(ctx, t); n != 0 {
		t.Fatalf("移除后队列应清空，得到 %d", n)
	}
}

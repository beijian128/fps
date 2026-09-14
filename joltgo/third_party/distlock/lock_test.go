package distlock

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newTestClient 启动内存 Redis 并返回客户端。
func newTestClient(t *testing.T) (redis.UniversalClient, *miniredis.Miniredis) {
	t.Helper()
	s := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: s.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return client, s
}

func TestTryLockAndUnlock(t *testing.T) {
	client, _ := newTestClient(t)
	ctx := context.Background()
	l1 := New(client, "order:1")
	l2 := New(client, "order:1")

	ok, err := l1.TryLock(ctx)
	if err != nil || !ok {
		t.Fatalf("l1 TryLock: ok=%v err=%v", ok, err)
	}
	// 另一实例无法获取
	ok, err = l2.TryLock(ctx)
	if err != nil || ok {
		t.Fatalf("l2 TryLock while held: ok=%v err=%v", ok, err)
	}
	if err := l1.Unlock(ctx); err != nil {
		t.Fatalf("l1 Unlock: %v", err)
	}
	ok, err = l2.TryLock(ctx)
	if err != nil || !ok {
		t.Fatalf("l2 TryLock after release: ok=%v err=%v", ok, err)
	}
}

func TestUnlockNotOwned(t *testing.T) {
	client, _ := newTestClient(t)
	ctx := context.Background()
	l1 := New(client, "k")
	l2 := New(client, "k")

	if _, err := l1.TryLock(ctx); err != nil {
		t.Fatal(err)
	}
	// 他人无法释放我的锁
	if err := l2.Unlock(ctx); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("l2.Unlock: want ErrNotOwned, got %v", err)
	}
	if err := l1.Unlock(ctx); err != nil {
		t.Fatal(err)
	}
	// 已释放后再释放
	if err := l1.Unlock(ctx); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("double Unlock: want ErrNotOwned, got %v", err)
	}
}

func TestLockBlocksUntilReleased(t *testing.T) {
	client, _ := newTestClient(t)
	ctx := context.Background()
	l1 := New(client, "k")
	l2 := New(client, "k", WithRetryInterval(10*time.Millisecond))

	if _, err := l1.TryLock(ctx); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		if err := l2.Lock(ctx); err != nil {
			t.Errorf("l2.Lock: %v", err)
		}
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("l2 acquired while l1 still holds")
	case <-time.After(100 * time.Millisecond):
	}
	if err := l1.Unlock(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("l2 did not acquire after release")
	}
}

func TestLockTimeout(t *testing.T) {
	client, _ := newTestClient(t)
	l1 := New(client, "k")
	l2 := New(client, "k", WithRetryInterval(10*time.Millisecond))

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if _, err := l1.TryLock(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := l2.Lock(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Lock with expired ctx: want DeadlineExceeded, got %v", err)
	}
}

func TestReentrant(t *testing.T) {
	client, _ := newTestClient(t)
	ctx := context.Background()
	l1 := New(client, "k")
	l2 := New(client, "k")

	if err := l1.Lock(ctx); err != nil {
		t.Fatal(err)
	}
	if err := l1.Lock(ctx); err != nil { // 可重入
		t.Fatal(err)
	}
	ok, err := l2.TryLock(ctx)
	if err != nil || ok {
		t.Fatalf("l2 must not acquire while depth=2: ok=%v err=%v", ok, err)
	}
	if err := l1.Unlock(ctx); err != nil {
		t.Fatal(err)
	}
	// 深度 1，锁仍被持有
	ok, err = l2.TryLock(ctx)
	if err != nil || ok {
		t.Fatalf("l2 must not acquire while depth=1: ok=%v err=%v", ok, err)
	}
	if err := l1.Unlock(ctx); err != nil {
		t.Fatal(err)
	}
	ok, err = l2.TryLock(ctx)
	if err != nil || !ok {
		t.Fatalf("l2 must acquire after depth=0: ok=%v err=%v", ok, err)
	}
}

// TestWatchdogKeepsLockAlive 验证看门狗在超过一个 TTL 的时间后仍保持锁有效。
func TestWatchdogKeepsLockAlive(t *testing.T) {
	client, _ := newTestClient(t)
	ctx := context.Background()
	l1 := New(client, "k", WithTTL(400*time.Millisecond)) // 看门狗每 ~133ms 续期
	l2 := New(client, "k")

	if _, err := l1.TryLock(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond) // 远超 400ms TTL
	err := client.HGet(ctx, "k", l1.token).Err()
	if err != nil {
		t.Fatalf("watchdog failed to renew: lock expired: %v", err)
	}
	ok, err := l2.TryLock(ctx)
	if err != nil || ok {
		t.Fatalf("lock must still be held by l1: ok=%v err=%v", ok, err)
	}
}

// TestWatchdogDetectsLoss 验证锁在 Redis 端消失后，看门狗能识别丢失并回调。
func TestWatchdogDetectsLoss(t *testing.T) {
	client, s := newTestClient(t)
	ctx := context.Background()
	lost := make(chan struct{})
	l1 := New(client, "k", WithTTL(200*time.Millisecond), WithOnLost(func() { close(lost) }))

	if _, err := l1.TryLock(ctx); err != nil {
		t.Fatal(err)
	}
	s.FastForward(300 * time.Millisecond) // 锁过期（真实时钟上瞬时发生）
	select {
	case <-lost:
	case <-time.After(2 * time.Second):
		t.Fatal("onLost not fired after lock expired")
	}
	if !l1.IsLost() {
		t.Fatal("IsLost should be true")
	}
	if err := l1.Unlock(ctx); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("Unlock after loss: want ErrNotOwned, got %v", err)
	}
	// 丢失后实例可重新获取
	ok, err := l1.TryLock(ctx)
	if err != nil || !ok {
		t.Fatalf("re-acquire after loss: ok=%v err=%v", ok, err)
	}
}

// TestNoWatchdogExpires 验证关闭看门狗后，锁在 TTL 后正常过期。
func TestNoWatchdogExpires(t *testing.T) {
	client, s := newTestClient(t)
	ctx := context.Background()
	l1 := New(client, "k", WithTTL(200*time.Millisecond), WithWatchdog(false))
	l2 := New(client, "k")

	if _, err := l1.TryLock(ctx); err != nil {
		t.Fatal(err)
	}
	s.FastForward(300 * time.Millisecond)
	ok, err := l2.TryLock(ctx)
	if err != nil || !ok {
		t.Fatalf("l2 must acquire after TTL expiry: ok=%v err=%v", ok, err)
	}
}

// TestUnlockStopsWatchdog 验证解锁后看门狗停止，锁按 TTL 过期。
func TestUnlockStopsWatchdog(t *testing.T) {
	client, s := newTestClient(t)
	ctx := context.Background()
	l1 := New(client, "k", WithTTL(200*time.Millisecond))
	l2 := New(client, "k")

	if _, err := l1.TryLock(ctx); err != nil {
		t.Fatal(err)
	}
	if err := l1.Unlock(ctx); err != nil {
		t.Fatal(err)
	}
	// 解锁后等待超过 TTL，锁不应被残留的看门狗"复活"
	s.FastForward(300 * time.Millisecond)
	if n, err := client.Exists(ctx, "k").Result(); err != nil || n != 0 {
		t.Fatalf("key should not exist after unlock + TTL: n=%d err=%v", n, err)
	}
	ok, err := l2.TryLock(ctx)
	if err != nil || !ok {
		t.Fatalf("l2 must acquire: ok=%v err=%v", ok, err)
	}
}

func TestLockExpiresNaturallyIfNoRenewal(t *testing.T) {
	client, s := newTestClient(t)
	ctx := context.Background()
	l1 := New(client, "k", WithTTL(150*time.Millisecond), WithWatchdog(false))
	if _, err := l1.TryLock(ctx); err != nil {
		t.Fatal(err)
	}
	s.FastForward(200 * time.Millisecond)
	// 锁已过期，Redis 中不存在
	if n, err := client.Exists(ctx, "k").Result(); err != nil || n != 0 {
		t.Fatalf("key should have expired: n=%d err=%v", n, err)
	}
}

// TestConcurrentContention 多协程竞争同一把锁：持锁期间 Redis 中
// 只能有一个持有者字段，验证互斥性没有被破坏。
func TestConcurrentContention(t *testing.T) {
	client, _ := newTestClient(t)
	ctx := context.Background()
	const workers = 10
	const rounds = 30

	var wg sync.WaitGroup
	acquired := make(chan struct{}, workers*rounds)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l := New(client, "contention")
			for j := 0; j < rounds; j++ {
				ok, err := l.TryLock(ctx)
				if err != nil {
					t.Errorf("TryLock: %v", err)
					return
				}
				if !ok {
					continue
				}
				n, err := client.HLen(ctx, "contention").Result()
				if err != nil || n != 1 {
					t.Errorf("mutex violated: %d holders, err=%v", n, err)
				}
				acquired <- struct{}{}
				if err := l.Unlock(ctx); err != nil {
					t.Errorf("Unlock: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(acquired)
	if n := len(acquired); n == 0 {
		t.Fatal("no worker ever acquired the lock")
	}
}

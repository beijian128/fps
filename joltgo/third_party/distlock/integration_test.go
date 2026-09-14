package distlock

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// 集成测试：需要真实 Redis。设置环境变量 REDIS_ADDR（如 127.0.0.1:6379）后运行：
//
//	REDIS_ADDR=127.0.0.1:6379 go test -run TestIntegration -v
func TestIntegrationWithRealRedis(t *testing.T) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("set REDIS_ADDR to run integration tests")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()

	t.Run("acquire release", func(t *testing.T) {
		l1 := New(client, "it:1")
		l2 := New(client, "it:1")
		if ok, err := l1.TryLock(ctx); err != nil || !ok {
			t.Fatalf("l1: ok=%v err=%v", ok, err)
		}
		if ok, err := l2.TryLock(ctx); err != nil || ok {
			t.Fatalf("l2 must fail: ok=%v err=%v", ok, err)
		}
		if err := l1.Unlock(ctx); err != nil {
			t.Fatal(err)
		}
		if ok, err := l2.TryLock(ctx); err != nil || !ok {
			t.Fatalf("l2 after release: ok=%v err=%v", ok, err)
		}
	})

	t.Run("watchdog survives ttl", func(t *testing.T) {
		l := New(client, "it:2", WithTTL(300*time.Millisecond))
		if _, err := l.TryLock(ctx); err != nil {
			t.Fatal(err)
		}
		time.Sleep(900 * time.Millisecond) // 3 个 TTL
		if !l.IsHeld() {
			t.Fatal("lock should be held thanks to watchdog")
		}
		if err := l.Unlock(ctx); err != nil {
			t.Fatal(err)
		}
	})
}

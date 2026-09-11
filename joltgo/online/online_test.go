package online

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestStore(t *testing.T) (*Store, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewStore(rdb), mr
}

func TestGateUnknownAccountIsEmpty(t *testing.T) {
	s, _ := newTestStore(t)
	got, err := s.Gate(context.Background(), "42")
	if err != nil {
		t.Fatalf("查不存在的账号不该报错: %v", err)
	}
	if got != "" {
		t.Fatalf("不存在的账号应返回空串，得到 %q", got)
	}
}

func TestSetAndClear(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	if err := s.Set(ctx, "42", "gate-1"); err != nil {
		t.Fatalf("Set 报错: %v", err)
	}
	got, err := s.Gate(ctx, "42")
	if err != nil || got != "gate-1" {
		t.Fatalf("应读到 gate-1，得到 %q err=%v", got, err)
	}

	// 同一账号再登记到另一个 gate（换设备登录）应覆盖。
	if err := s.Set(ctx, "42", "gate-2"); err != nil {
		t.Fatalf("Set 覆盖报错: %v", err)
	}
	if got, _ := s.Gate(ctx, "42"); got != "gate-2" {
		t.Fatalf("应覆盖为 gate-2，得到 %q", got)
	}

	if err := s.Clear(ctx, "42"); err != nil {
		t.Fatalf("Clear 报错: %v", err)
	}
	if got, _ := s.Gate(ctx, "42"); got != "" {
		t.Fatalf("Clear 后应为空，得到 %q", got)
	}
}

func TestSetHasTTL(t *testing.T) {
	s, mr := newTestStore(t)
	if err := s.Set(context.Background(), "42", "gate-1"); err != nil {
		t.Fatalf("Set 报错: %v", err)
	}
	if ttl := mr.TTL("online:42"); ttl != TTL {
		t.Fatalf("TTL 应为 %v，得到 %v", TTL, ttl)
	}
}

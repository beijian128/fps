package logic

import (
	"context"
	"testing"

	"github.com/gomodule/redigo/redis"
	"joltgo/persist"
	protos "joltgo/persist/protos"
)

// seedAccount 在 miniredis 里造一个账号 Hash —— GrantCoins 要求目标账号真实存在
// （不隐式建档），而逻辑层测试环境只建玩家侧的钱包/背包，没有账号行。
func seedAccount(t *testing.T, env *serviceTestEnv, id uint64, username string) {
	t.Helper()
	conn, err := redis.Dial("tcp", env.mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Do("HSET", persist.AccountKey(id), protos.FieldDBAccount_Username, username); err != nil {
		t.Fatalf("造账号 Hash 失败: %v", err)
	}
}

func TestGrantCoinsAddsToExistingWallet(t *testing.T) {
	env, _ := newLockedServiceTestEnv(t)
	ctx := context.Background()
	if err := env.svc.EnsureProfile(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	seedAccount(t, env, 7, "alice")

	coins, err := env.svc.GrantCoins(ctx, "7", 500)
	if err != nil {
		t.Fatalf("GrantCoins 报错: %v", err)
	}
	if coins != 1500 {
		t.Fatalf("新余额 = %d，期望 1500（初始 1000 + 500）", coins)
	}
}

func TestGrantCoinsAllowsNegativeDelta(t *testing.T) {
	env, _ := newLockedServiceTestEnv(t)
	ctx := context.Background()
	if err := env.svc.EnsureProfile(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	seedAccount(t, env, 7, "alice")

	coins, err := env.svc.GrantCoins(ctx, "7", -400)
	if err != nil {
		t.Fatal(err)
	}
	if coins != 600 {
		t.Fatalf("新余额 = %d，期望 600", coins)
	}
}

func TestGrantCoinsDoesNotTakeTheAccountLock(t *testing.T) {
	// 单条 HINCRBY 本身就原子，套 user:lock 反而把原子操作降级。
	env, locks := newLockedServiceTestEnv(t)
	ctx := context.Background()
	if err := env.svc.EnsureProfile(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	seedAccount(t, env, 7, "alice")
	before := locks.Count()

	if _, err := env.svc.GrantCoins(ctx, "7", 100); err != nil {
		t.Fatal(err)
	}
	if locks.Count() != before {
		t.Fatalf("发钱不该获取账号级分布式锁，锁次数从 %d 涨到 %d", before, locks.Count())
	}
}

func TestGrantCoinsRejectsUnknownAccount(t *testing.T) {
	env, _ := newLockedServiceTestEnv(t)
	// 没有任何账号 Hash = 这个账号不存在。
	_, err := env.svc.GrantCoins(context.Background(), "999999", 500)
	if ReasonOf(err) != ReasonAccountNotFound {
		t.Fatalf("未知账号应报 %s，得到 %q", ReasonAccountNotFound, ReasonOf(err))
	}
}

func TestGrantCoinsDoesNotCreateWalletForUnknownAccount(t *testing.T) {
	env, _ := newLockedServiceTestEnv(t)
	ctx := context.Background()
	if _, err := env.svc.GrantCoins(ctx, "999999", 500); err == nil {
		t.Fatal("未知账号应报错")
	}
	if _, ok, err := env.store.GetWallet(ctx, 999999); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("给未知账号发钱不该凭空造出钱包行")
	}
}

func TestGrantCoinsRejectsBadAccountID(t *testing.T) {
	env, _ := newLockedServiceTestEnv(t)
	for _, id := range []string{"", "abc", "0"} {
		if _, err := env.svc.GrantCoins(context.Background(), id, 500); err == nil {
			t.Fatalf("accountID %q 应被拒绝", id)
		}
	}
}

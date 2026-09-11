package account

import (
	"context"
	"errors"
	"testing"
	"time"

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

// createTestAccount 建一个测试账号，返回 accountID。
func createTestAccount(t *testing.T, s *Store, username, pw string) string {
	t.Helper()
	hash, err := HashPassword(pw)
	if err != nil {
		t.Fatalf("HashPassword 报错: %v", err)
	}
	id, err := s.Create(context.Background(), username, hash)
	if err != nil {
		t.Fatalf("Create 报错: %v", err)
	}
	return id
}

func TestCreateAssignsIncreasingIDs(t *testing.T) {
	s, _ := newTestStore(t)
	a := createTestAccount(t, s, "alice", "hunter2")
	b := createTestAccount(t, s, "bob", "hunter2")
	if a == b {
		t.Fatalf("两个账号的 id 不能相同，都是 %q", a)
	}
	if a != "1" || b != "2" {
		t.Fatalf("id 应是从 1 开始递增的十进制串，得到 %q / %q", a, b)
	}
}

func TestCreateRejectsDuplicateName(t *testing.T) {
	s, _ := newTestStore(t)
	createTestAccount(t, s, "alice", "hunter2")

	hash, _ := HashPassword("hunter2")
	if _, err := s.Create(context.Background(), "alice", hash); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("重名应返回 ErrNameTaken，得到 %v", err)
	}
	// 大小写不同也算重名（占名键用规范化用户名）。
	if _, err := s.Create(context.Background(), "ALICE", hash); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("大小写不同的重名也应被拒，得到 %v", err)
	}
}

func TestCreateRollsBackNameOnFailure(t *testing.T) {
	s, mr := newTestStore(t)
	createTestAccount(t, s, "alice", "hunter2")

	// 制造「占名成功、分配 id 失败」这个中间态：把计数器设成非整数，
	// INCR 会报 "ERR value is not an integer or out of range"。
	//
	// 不能用 mr.SetError()：那会让**所有**命令失败，包括最开始的 SETNX ——
	// 名字压根没被占用，回滚分支根本走不到，测试就变成了假绿。
	mr.Set("acct:seq", "notanumber")

	hash, _ := HashPassword("hunter2")
	if _, err := s.Create(context.Background(), "bob", hash); err == nil {
		t.Fatal("INCR 失败时 Create 应报错")
	}
	// 回滚必须发生，否则 bob 会变成「占着名字却没有任何账号」的僵尸名 ——
	// 谁都注册不了它，也谁都登录不了它。
	if mr.Exists("acct:name:bob") {
		t.Fatal("失败后应回滚占名")
	}

	// 计数器修好后应能重新占名。
	mr.Set("acct:seq", "1")
	if id, err := s.Create(context.Background(), "bob", hash); err != nil {
		t.Fatalf("回滚后应能重新占名，得到 %v (id=%s)", err, id)
	}
}

// 占名成功后名字键必须是永久的（占位符的 TTL 已被成功路径的 Set(..., 0) 清掉）。
func TestCreateNameKeyIsPersistentAfterSuccess(t *testing.T) {
	s, mr := newTestStore(t)
	createTestAccount(t, s, "alice", "hunter2")

	if ttl := mr.TTL("acct:name:alice"); ttl != 0 {
		t.Fatalf("占名成功后应无 TTL（0 表示永久），得到 %v", ttl)
	}
}

func TestLookupByNameIsCaseInsensitive(t *testing.T) {
	s, _ := newTestStore(t)
	id := createTestAccount(t, s, "Alice", "hunter2")

	for _, q := range []string{"Alice", "alice", "ALICE"} {
		got, ok, err := s.LookupByName(context.Background(), q)
		if err != nil || !ok || got != id {
			t.Fatalf("查 %q 应命中 %s，得到 %q ok=%v err=%v", q, id, got, ok, err)
		}
	}

	if _, ok, err := s.LookupByName(context.Background(), "nobody"); ok || err != nil {
		t.Fatalf("不存在的名字应返回 ok=false err=nil，得到 ok=%v err=%v", ok, err)
	}
}

func TestGetAccountKeepsOriginalCase(t *testing.T) {
	s, _ := newTestStore(t)
	id := createTestAccount(t, s, "Alice", "hunter2")

	a, ok, err := s.GetAccount(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("应读到账号，得到 ok=%v err=%v", ok, err)
	}
	if a.Username != "Alice" {
		t.Fatalf("应保留原始大小写 Alice，得到 %q", a.Username)
	}
	if !CheckPassword(a.PassHash, "hunter2") {
		t.Fatal("存下来的哈希应能校验原密码")
	}
	if a.CreatedAt == 0 {
		t.Fatal("created_at 应被写入")
	}
}

func TestGetAccountMissing(t *testing.T) {
	s, _ := newTestStore(t)
	if _, ok, err := s.GetAccount(context.Background(), "999"); ok || err != nil {
		t.Fatalf("不存在的账号应 ok=false err=nil，得到 ok=%v err=%v", ok, err)
	}
}

func TestIssueTokenRotatesAndRevokesOld(t *testing.T) {
	s, _ := newTestStore(t)
	id := createTestAccount(t, s, "alice", "hunter2")
	ctx := context.Background()

	t1, err := s.IssueToken(ctx, id)
	if err != nil {
		t.Fatalf("IssueToken 报错: %v", err)
	}
	if got, ok, _ := s.ResolveToken(ctx, t1); !ok || got != id {
		t.Fatalf("t1 应解析到 %s，得到 %q ok=%v", id, got, ok)
	}

	t2, err := s.IssueToken(ctx, id)
	if err != nil {
		t.Fatalf("第二次 IssueToken 报错: %v", err)
	}
	if t1 == t2 {
		t.Fatal("轮换必须换出新 token")
	}
	if _, ok, _ := s.ResolveToken(ctx, t1); ok {
		t.Fatal("旧 token 必须立即失效（单会话强制）")
	}
	if got, ok, _ := s.ResolveToken(ctx, t2); !ok || got != id {
		t.Fatalf("t2 应解析到 %s，得到 %q ok=%v", id, got, ok)
	}
}

func TestResolveTokenExtendsTTL(t *testing.T) {
	s, mr := newTestStore(t)
	id := createTestAccount(t, s, "alice", "hunter2")
	ctx := context.Background()

	tok, _ := s.IssueToken(ctx, id)
	// 让时间走掉大半，再解析一次，TTL 应被续满。
	mr.FastForward(6 * 24 * time.Hour)
	if _, ok, _ := s.ResolveToken(ctx, tok); !ok {
		t.Fatal("6 天后 token 应还有效")
	}
	if ttl := mr.TTL("sess:" + tok); ttl != keySessTTL {
		t.Fatalf("解析后 TTL 应续满 %v，得到 %v", keySessTTL, ttl)
	}
}

// 活跃账号跨过第一个 TTL 周期后，轮换必须仍然有效。
//
// 回归测试：曾经只续 sess:{token} 而不续 sess:acct:{id}，于是指针先过期、
// IssueToken 读不到旧 token、跳过删除，旧 token 又多活 7 天 —— 单会话强制失效。
func TestResolveTokenKeepsRotationWorkingPastFirstTTL(t *testing.T) {
	s, mr := newTestStore(t)
	id := createTestAccount(t, s, "alice", "hunter2")
	ctx := context.Background()

	t1, err := s.IssueToken(ctx, id)
	if err != nil {
		t.Fatalf("IssueToken 报错: %v", err)
	}

	// 每天 resume 一次，持续 8 天（跨过 7 天的 TTL）。
	for day := 0; day < 8; day++ {
		mr.FastForward(24 * time.Hour)
		if _, ok, _ := s.ResolveToken(ctx, t1); !ok {
			t.Fatalf("第 %d 天 resume 应仍然有效", day+1)
		}
	}

	t2, err := s.IssueToken(ctx, id)
	if err != nil {
		t.Fatalf("IssueToken 报错: %v", err)
	}
	if _, ok, _ := s.ResolveToken(ctx, t1); ok {
		t.Fatal("活跃账号换设备登录后旧 token 仍有效 —— 轮换失效了")
	}
	if _, ok, _ := s.ResolveToken(ctx, t2); !ok {
		t.Fatal("新 token 应有效")
	}
}

func TestResolveTokenExpires(t *testing.T) {
	s, mr := newTestStore(t)
	id := createTestAccount(t, s, "alice", "hunter2")
	ctx := context.Background()

	tok, _ := s.IssueToken(ctx, id)
	mr.FastForward(keySessTTL + time.Hour)
	if _, ok, _ := s.ResolveToken(ctx, tok); ok {
		t.Fatal("过期 token 不应解析成功")
	}
}

func TestRevokeToken(t *testing.T) {
	s, _ := newTestStore(t)
	id := createTestAccount(t, s, "alice", "hunter2")
	ctx := context.Background()

	tok, _ := s.IssueToken(ctx, id)
	if err := s.RevokeToken(ctx, tok); err != nil {
		t.Fatalf("RevokeToken 报错: %v", err)
	}
	if _, ok, _ := s.ResolveToken(ctx, tok); ok {
		t.Fatal("登出后 token 应失效")
	}
	// 再撤回一次不应报错（登出是幂等的）。
	if err := s.RevokeToken(ctx, tok); err != nil {
		t.Fatalf("重复 RevokeToken 应幂等，得到 %v", err)
	}
	// 撤回后该账号应能重新签发。
	if _, err := s.IssueToken(ctx, id); err != nil {
		t.Fatalf("重新签发应成功，得到 %v", err)
	}
}

func TestAllowRateLimitsPerUsername(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < RateLimit; i++ {
		ok, err := s.Allow(ctx, "alice")
		if err != nil {
			t.Fatalf("第 %d 次 Allow 报错: %v", i+1, err)
		}
		if !ok {
			t.Fatalf("第 %d 次应放行（上限 %d）", i+1, RateLimit)
		}
	}
	if ok, _ := s.Allow(ctx, "alice"); ok {
		t.Fatalf("超过 %d 次后应被限流", RateLimit)
	}

	// 另一个用户名不受影响。
	if ok, _ := s.Allow(ctx, "bob"); !ok {
		t.Fatal("限流应按用户名隔离")
	}

	// 窗口过去后恢复。
	mr.FastForward(keyRateTTL + time.Second)
	if ok, _ := s.Allow(ctx, "alice"); !ok {
		t.Fatal("限流窗口过后应恢复")
	}
}

func TestAllowNormalizesUsername(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < RateLimit; i++ {
		s.Allow(ctx, "Alice")
	}
	// 换个大小写不应绕开限流。
	if ok, _ := s.Allow(ctx, "alice"); ok {
		t.Fatal("大小写不同不应绕开限流（限流键用规范化用户名）")
	}
}

func TestCurrentToken(t *testing.T) {
	s, _ := newTestStore(t)
	id := createTestAccount(t, s, "alice", "hunter2")
	ctx := context.Background()

	// 还没签发过：空串，不是错误。
	if tok, err := s.CurrentToken(ctx, id); err != nil || tok != "" {
		t.Fatalf("未签发时应返回空串且无错，得到 %q err=%v", tok, err)
	}

	issued, err := s.IssueToken(ctx, id)
	if err != nil {
		t.Fatalf("IssueToken 报错: %v", err)
	}
	if tok, _ := s.CurrentToken(ctx, id); tok != issued {
		t.Fatalf("应返回当前凭证 %q，得到 %q", issued, tok)
	}
}

package account

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"sync"
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

// assertExactlyOneLiveToken 断言 toks 里恰好有一个还有效，且它正是指针指向的那个。
func assertExactlyOneLiveToken(t *testing.T, s *Store, id string, toks []string) {
	t.Helper()
	ctx := context.Background()
	live := make([]string, 0, 1)
	for _, tok := range toks {
		if _, ok, _ := s.ResolveToken(ctx, tok); ok {
			live = append(live, tok)
		}
	}
	if len(live) != 1 {
		t.Fatalf("应恰好剩 1 个凭证有效，实际 %d 个（多出来的孤儿凭证再也不会被轮换删掉）: %v",
			len(live), live)
	}
	cur, err := s.CurrentToken(ctx, id)
	if err != nil {
		t.Fatalf("CurrentToken 报错: %v", err)
	}
	if cur != live[0] {
		t.Fatalf("指针应指向唯一活着的凭证 %q，得到 %q", live[0], cur)
	}
}

// 并发轮换：多路同时登录，最后必须只剩一个活着的凭证。
//
// 回归测试。曾经是「先 GET 旧指针，再 pipeline 写新凭证/换指针/删旧 token」：两个
// 并发调用读到同一个旧指针、各自删掉它，于是**两个 token 都活着** —— 而指针只指向
// 其中一个，另一个从此再也不会被任何一次轮换删掉（孤儿凭证永久有效）。轮换是
// 单会话强制的**权威**手段，这个窗口等于把它作废。
func TestIssueTokenConcurrentLeavesExactlyOneLiveToken(t *testing.T) {
	s, _ := newTestStore(t)
	id := createTestAccount(t, s, "alice", "hunter2")
	ctx := context.Background()

	// 先放一个存量凭证：第一次签发时没有旧指针可读，竞态窗口根本不存在。
	if _, err := s.IssueToken(ctx, id); err != nil {
		t.Fatalf("准备 token 失败: %v", err)
	}

	const n = 8
	toks := make([]string, n)
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // 尽量让 n 路同时进 IssueToken
			toks[i], errs[i] = s.IssueToken(ctx, id)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 路 IssueToken 报错: %v", i, err)
		}
	}
	assertExactlyOneLiveToken(t, s, id, toks)
}

// rotateRaceHook 卡住「读 sess:acct:{id} 旧指针」这一步，直到有 n 路调用都读到
// 同一个旧值才一起放行 —— 也就是把并发轮换的竞态窗口**确定性地**摆出来。
type rotateRaceHook struct {
	n        int
	mu       sync.Mutex
	seen     int
	disarmed bool
	gate     chan struct{}
}

func (h *rotateRaceHook) Disarm() {
	h.mu.Lock()
	h.disarmed = true
	h.mu.Unlock()
}

func (h *rotateRaceHook) DialHook(next redis.DialHook) redis.DialHook { return next }

// ProcessHook 对匹配的 GET **先真的执行**（next），再把结果扣住等其余各路也读完，
// 最后才把结果放回调用方 —— 这样 N 路调用一定是拿着**同一个旧值**继续往下走。
// 若在 next 之前卡，先被放行的那一路会先完成整套轮换，后一路读到的就是新指针，
// 竞态窗口等于没被构造出来（第一版就是这么写的，旧实现照样通过）。
func (h *rotateRaceHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		args := cmd.Args()
		h.mu.Lock()
		armed := !h.disarmed
		h.mu.Unlock()
		hit := false
		if armed && cmd.Name() == "get" && len(args) == 2 {
			if key, ok := args[1].(string); ok && strings.HasPrefix(key, "sess:acct:") {
				hit = true
			}
		}
		if !hit {
			return next(ctx, cmd)
		}

		err := next(ctx, cmd) // 读完成（值已在 cmd 里），但先不还给调用方
		h.mu.Lock()
		h.seen++
		if h.seen == h.n {
			close(h.gate)
		}
		h.mu.Unlock()
		select {
		case <-h.gate:
		case <-time.After(2 * time.Second): // 兜底：绝不让测试挂死
		}
		return err
	}
}

func (h *rotateRaceHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// 两路并发的**确定性**复现：钩子保证两路都读到同一个旧指针之后才放行。
//
// 新实现把「读旧值 + 换指针 + 删旧值」挪进了一个 Lua 脚本（Redis 串行执行），钩子
// 再也不会被触发，于是这个测试退化成一次普通的轮换断言；但只要有人改回「先 GET
// 再 pipeline」，它就会**必然**失败（而不是偶发失败）—— 这正是它存在的意义。
func TestIssueTokenConcurrentPairForcedInterleaving(t *testing.T) {
	s, _ := newTestStore(t)
	id := createTestAccount(t, s, "alice", "hunter2")
	ctx := context.Background()
	if _, err := s.IssueToken(ctx, id); err != nil {
		t.Fatalf("准备 token 失败: %v", err)
	}

	hook := &rotateRaceHook{n: 2, gate: make(chan struct{})}
	s.rdb.AddHook(hook)

	toks := make([]string, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			toks[i], errs[i] = s.IssueToken(ctx, id)
		}(i)
	}
	close(start)
	wg.Wait()
	// 断言阶段会自己 GET sess:acct:{id}（CurrentToken）—— 那不是竞态的一部分，
	// 撤掉钩子免得它在屏障上白等。
	hook.Disarm()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 路 IssueToken 报错: %v", i, err)
		}
	}
	assertExactlyOneLiveToken(t, s, id, toks)
}

// 续期失败必须留下日志（凭证本身仍然有效，所以不能报错）—— 只续上 token 而没续上
// 指针时，轮换会从指针过期那一刻起静默失效，没有这条日志就只能靠猜。
func TestResolveTokenLogsRenewalFailure(t *testing.T) {
	s, _ := newTestStore(t)
	id := createTestAccount(t, s, "alice", "hunter2")
	ctx := context.Background()
	tok, err := s.IssueToken(ctx, id)
	if err != nil {
		t.Fatalf("IssueToken 报错: %v", err)
	}

	s.rdb.AddHook(failExpireHook{})
	var logBuf bytes.Buffer
	restore := redirectLog(&logBuf)
	defer restore()

	if got, ok, err := s.ResolveToken(ctx, tok); err != nil || !ok || got != id {
		t.Fatalf("凭证本身有效，续期失败不该改变结果，得到 id=%q ok=%v err=%v", got, ok, err)
	}
	if !strings.Contains(logBuf.String(), "renew ttl") {
		t.Fatalf("续期失败必须记日志，实际日志: %q", logBuf.String())
	}
}

// failExpireHook 让整条 EXPIRE 流水线失败（不发往 Redis）。
type failExpireHook struct{}

func (failExpireHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (failExpireHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook { return next }

func (failExpireHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		for _, cmd := range cmds {
			if cmd.Name() == "expire" {
				return errors.New("boom: 模拟续期失败")
			}
		}
		return next(ctx, cmds)
	}
}

// redirectLog 把标准库 log 的输出临时改到 buf（被测代码用的是 log.Printf）。
func redirectLog(buf *bytes.Buffer) func() {
	prev := log.Writer()
	log.SetOutput(buf)
	return func() { log.SetOutput(prev) }
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

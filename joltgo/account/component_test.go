package account

import (
	"context"
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
	// pitaya 的 RPCTo 用的是老的 github.com/golang/protobuf/proto.Message
	// （见 third_party/pitaya/pkg/app.go），换成 google.golang.org/protobuf
	// 的同名类型就满足不了 pitaya.Pitaya 接口。
	"github.com/golang/protobuf/proto"
	"github.com/redis/go-redis/v9"
	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/session"
	"joltgo/game/protos"
	"joltgo/online"
)

// fakeSession 嵌入 session.Session 接口：只覆盖组件用到的几个方法，
// 未覆盖的方法一旦被调用就 panic（暴露意料之外的依赖）。
//
// onBind 用来模拟 gate 侧的 after-bind 钩子：真实链路里 Bind 会让 gate 把
// online 登记改写成「自己」。没有它就没法构造「跨 gate 顶号」这个场景 ——
// 而那是本次顶号设计最核心的一条断言。
type fakeSession struct {
	session.Session
	uid     string
	bindErr error
	onBind  func(uid string)
}

func (s *fakeSession) UID() string { return s.uid }

func (s *fakeSession) Bind(_ context.Context, uid string) error {
	if s.bindErr != nil {
		return s.bindErr
	}
	s.uid = uid
	if s.onBind != nil {
		s.onBind(uid)
	}
	return nil
}

// rpcCall 记录一次 RPCTo 调用。
type rpcCall struct {
	serverID string
	route    string
}

// fakeApp 只实现组件用到的部分：GetSessionFromCtx 与 RPCTo。
type fakeApp struct {
	pitaya.Pitaya
	sess  *fakeSession
	calls []rpcCall
}

func (a *fakeApp) GetSessionFromCtx(context.Context) session.Session { return a.sess }

func (a *fakeApp) RPCTo(_ context.Context, serverID, routeStr string, _ proto.Message, _ proto.Message) error {
	a.calls = append(a.calls, rpcCall{serverID: serverID, route: routeStr})
	return nil
}

// testEnv 把一次测试要碰的东西打包，避免每个用例拖一长串返回值。
type testEnv struct {
	comp   *Component
	store  *Store
	online *online.Store
	app    *fakeApp
	sess   *fakeSession
	mr     *miniredis.Miniredis
}

func newTestComponent(t *testing.T) *testEnv {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	sess := &fakeSession{}
	app := &fakeApp{sess: sess}
	store := NewStore(rdb)
	onl := online.NewStore(rdb)
	return &testEnv{
		comp:   New(app, store, onl),
		store:  store,
		online: onl,
		app:    app,
		sess:   sess,
		mr:     mr,
	}
}

// mustRegister 建一个账号（失败直接终止用例）。
func (e *testEnv) mustRegister(t *testing.T, username, password string) {
	t.Helper()
	r, err := e.comp.Register(context.Background(), &protos.RegisterMsg{Username: username, Password: password})
	if err != nil || !r.Ok {
		t.Fatalf("准备账号失败: %+v err=%v", r, err)
	}
}

func TestRegisterHappyPath(t *testing.T) {
	env := newTestComponent(t)
	ctx := context.Background()

	reply, err := env.comp.Register(ctx, &protos.RegisterMsg{Username: "Alice", Password: "hunter2"})
	if err != nil {
		t.Fatalf("Register 不该报错: %v", err)
	}
	if !reply.Ok {
		t.Fatalf("注册应成功，得到 reason=%q", reply.Reason)
	}
	if err := ValidateToken(reply.Token); err != nil {
		t.Fatalf("签发的 token 形态应合法: %v", err)
	}
	if reply.Username != "Alice" || reply.AccountId != "1" {
		t.Fatalf("应回显原始大小写与 accountID，得到 %+v", reply)
	}
	if env.sess.uid != "1" {
		t.Fatalf("会话应绑定到 accountID，得到 %q", env.sess.uid)
	}
	if id, ok, _ := env.store.ResolveToken(ctx, reply.Token); !ok || id != "1" {
		t.Fatalf("token 应解析到 1，得到 %q ok=%v", id, ok)
	}
}

func TestRegisterRejectsBadInput(t *testing.T) {
	env := newTestComponent(t)
	ctx := context.Background()

	// 格式错误走 reason，不返回 Go error —— 客户端要的是可展示的原因码。
	reply, err := env.comp.Register(ctx, &protos.RegisterMsg{Username: "ab", Password: "hunter2"})
	if err != nil {
		t.Fatalf("格式错误不该返回 Go error: %v", err)
	}
	if reply.Ok || reply.Reason != ReasonBadUsername {
		t.Fatalf("应回 bad_username，得到 %+v", reply)
	}

	reply, _ = env.comp.Register(ctx, &protos.RegisterMsg{Username: "alice", Password: "123"})
	if reply.Ok || reply.Reason != ReasonBadPassword {
		t.Fatalf("应回 bad_password，得到 %+v", reply)
	}
}

func TestRegisterRejectsDuplicate(t *testing.T) {
	env := newTestComponent(t)
	ctx := context.Background()
	env.mustRegister(t, "alice", "hunter2")

	// 换一个未绑定的会话：抢名字的必然是另一个客户端。同一个会话再注册会先被
	// 「已绑定」守卫挡掉（见 TestRegisterRefusesOnAlreadyBoundSession），
	// 那条路径根本走不到重名判断。
	env.app.sess = &fakeSession{}

	r, err := env.comp.Register(ctx, &protos.RegisterMsg{Username: "alice", Password: "hunter2"})
	if err != nil {
		t.Fatalf("重名不该返回 Go error: %v", err)
	}
	if r.Ok || r.Reason != ReasonNameTaken {
		t.Fatalf("应回 name_taken，得到 %+v", r)
	}
}

// 已绑定的会话上注册必须被挡在「创建账号」之前 —— 否则会留下一个用户名被永久
// 占用、谁都登不进去的无主账号，而且换个用户名就能无限刷。
func TestRegisterRefusesOnAlreadyBoundSession(t *testing.T) {
	env := newTestComponent(t)
	ctx := context.Background()
	env.mustRegister(t, "alice", "hunter2") // 会话绑到 "1"

	// 同一个会话再注册一个新账号。
	r, err := env.comp.Register(ctx, &protos.RegisterMsg{Username: "bob", Password: "hunter2"})
	if err != nil {
		t.Fatalf("不该返回 Go error: %v", err)
	}
	if r.Ok || r.Reason != ReasonInternal {
		t.Fatalf("已绑定会话上的注册应被拒，得到 %+v", r)
	}

	// 关键断言：bob **没有**被创建出来 —— 名字必须还能正常注册。
	_, taken, err := env.store.LookupByName(ctx, "bob")
	if err != nil {
		t.Fatalf("LookupByName 报错: %v", err)
	}
	if taken {
		t.Fatal("被拒的注册不能留下账号（否则就是占用用户名的无主账号）")
	}
}

func TestLoginHappyPathAndWrongPassword(t *testing.T) {
	env := newTestComponent(t)
	ctx := context.Background()
	env.mustRegister(t, "alice", "hunter2")

	// 换一个会话，模拟另一台设备。
	env.app.sess = &fakeSession{}

	r, err := env.comp.Login(ctx, &protos.LoginMsg{Username: "alice", Password: "hunter2"})
	if err != nil || !r.Ok {
		t.Fatalf("登录应成功，得到 %+v err=%v", r, err)
	}
	if id, ok, _ := env.store.ResolveToken(ctx, r.Token); !ok || id != "1" {
		t.Fatalf("新 token 应解析到 1，得到 %q ok=%v", id, ok)
	}

	// 错密码与不存在的用户名必须给同一个 reason（不泄露账号是否存在）。
	r1, _ := env.comp.Login(ctx, &protos.LoginMsg{Username: "alice", Password: "wrong!"})
	r2, _ := env.comp.Login(ctx, &protos.LoginMsg{Username: "nobody", Password: "hunter2"})
	if r1.Ok || r2.Ok {
		t.Fatal("错密码与不存在的用户都不该登录成功")
	}
	if r1.Reason != ReasonBadCredentials || r2.Reason != ReasonBadCredentials {
		t.Fatalf("两者都应是 bad_credentials，得到 %q / %q", r1.Reason, r2.Reason)
	}
}

// 换设备登录：凭证轮换 + 定点踢掉旧 gate 上的连接。
func TestLoginRotatesTokenAndKicksOldGate(t *testing.T) {
	env := newTestComponent(t)
	ctx := context.Background()
	env.mustRegister(t, "alice", "hunter2")

	first, err := env.store.IssueToken(ctx, "1")
	if err != nil {
		t.Fatalf("准备 token 失败: %v", err)
	}
	// 上次登录在 gate-A；这次从另一台设备进来，绑定会把登记改写成 gate-B。
	if err := env.online.Set(ctx, "1", "gate-A"); err != nil {
		t.Fatalf("准备 online 失败: %v", err)
	}
	env.app.sess = &fakeSession{onBind: func(uid string) {
		if err := env.online.Set(context.Background(), uid, "gate-B"); err != nil {
			t.Errorf("onBind 里写 online 失败: %v", err)
		}
	}}

	r, err := env.comp.Login(ctx, &protos.LoginMsg{Username: "alice", Password: "hunter2"})
	if err != nil || !r.Ok {
		t.Fatalf("登录应成功，得到 %+v err=%v", r, err)
	}
	if r.Token == first {
		t.Fatal("登录必须轮换 token")
	}
	if _, ok, _ := env.store.ResolveToken(ctx, first); ok {
		t.Fatal("旧 token 必须立即失效（顶号的权威手段）")
	}
	if len(env.app.calls) != 1 {
		t.Fatalf("应只在旧 gate 上踢一次，得到 %+v", env.app.calls)
	}
	if env.app.calls[0].serverID != "gate-A" || env.app.calls[0].route != kickRoute {
		t.Fatalf("应定点踢 gate-A，得到 %+v", env.app.calls[0])
	}
}

// 同一个 gate 上重复登录不该踢 —— Bind 已经同步把旧会话关掉了，再踢会踢到自己。
func TestLoginDoesNotKickWhenSameGate(t *testing.T) {
	env := newTestComponent(t)
	ctx := context.Background()
	env.mustRegister(t, "alice", "hunter2")

	if err := env.online.Set(ctx, "1", "gate-A"); err != nil {
		t.Fatalf("准备 online 失败: %v", err)
	}
	// 绑定后登记仍是 gate-A，说明旧会话就在本节点。
	env.app.sess = &fakeSession{onBind: func(uid string) {
		_ = env.online.Set(context.Background(), uid, "gate-A")
	}}

	r, _ := env.comp.Login(ctx, &protos.LoginMsg{Username: "alice", Password: "hunter2"})
	if !r.Ok {
		t.Fatalf("登录应成功，得到 %+v", r)
	}
	if len(env.app.calls) != 0 {
		t.Fatalf("同 gate 不该踢，得到 %+v", env.app.calls)
	}
}

func TestLoginRateLimited(t *testing.T) {
	env := newTestComponent(t)
	ctx := context.Background()
	for i := 0; i < RateLimit; i++ {
		env.comp.Login(ctx, &protos.LoginMsg{Username: "alice", Password: "wrong!"})
	}
	r, _ := env.comp.Login(ctx, &protos.LoginMsg{Username: "alice", Password: "wrong!"})
	if r.Ok || r.Reason != ReasonRateLimited {
		t.Fatalf("超限应回 rate_limited，得到 %+v", r)
	}
}

func TestResumeHappyPathAndInvalid(t *testing.T) {
	env := newTestComponent(t)
	ctx := context.Background()

	hash, err := HashPassword("hunter2")
	if err != nil {
		t.Fatalf("HashPassword 报错: %v", err)
	}
	if _, err := env.store.Create(ctx, "alice", hash); err != nil {
		t.Fatalf("准备账号失败: %v", err)
	}
	tok, err := env.store.IssueToken(ctx, "1")
	if err != nil {
		t.Fatalf("准备 token 失败: %v", err)
	}

	r, err := env.comp.Resume(ctx, &protos.ResumeMsg{Token: tok})
	if err != nil || !r.Ok {
		t.Fatalf("resume 应成功，得到 %+v err=%v", r, err)
	}
	if r.AccountId != "1" || r.Username != "alice" {
		t.Fatalf("应回账号信息，得到 %+v", r)
	}
	if env.sess.uid != "1" {
		t.Fatalf("会话应绑定到 1，得到 %q", env.sess.uid)
	}

	for _, bad := range []string{"garbage", ""} {
		r, _ := env.comp.Resume(ctx, &protos.ResumeMsg{Token: bad})
		if r.Ok || r.Reason != ReasonTokenInvalid {
			t.Fatalf("无效 token %q 应回 token_invalid，得到 %+v", bad, r)
		}
	}
}

// Bind 失败时不能把「登录成功」报给客户端：会话没绑上，之后的 match.join
// 会被当成未登录而忽略，玩家会卡在「正在匹配…」无从排查。
func TestLoginReportsBindFailure(t *testing.T) {
	env := newTestComponent(t)
	ctx := context.Background()
	env.mustRegister(t, "alice", "hunter2")

	env.app.sess = &fakeSession{bindErr: errors.New("boom")}

	r, err := env.comp.Login(ctx, &protos.LoginMsg{Username: "alice", Password: "hunter2"})
	if err != nil {
		t.Fatalf("不该返回 Go error: %v", err)
	}
	if r.Ok || r.Reason != ReasonInternal {
		t.Fatalf("Bind 失败应回 internal，得到 %+v", r)
	}
}

// 同一连接上重复登录必须幂等：不能轮换凭证（客户端正在用它），也不能失败。
func TestLoginOnAlreadyBoundSessionIsIdempotent(t *testing.T) {
	env := newTestComponent(t)
	ctx := context.Background()
	env.mustRegister(t, "alice", "hunter2")

	before, err := env.store.CurrentToken(ctx, "1")
	if err != nil || before == "" {
		t.Fatalf("准备失败: token=%q err=%v", before, err)
	}

	r, err := env.comp.Login(ctx, &protos.LoginMsg{Username: "alice", Password: "hunter2"})
	if err != nil || !r.Ok {
		t.Fatalf("重复登录应幂等成功，得到 %+v err=%v", r, err)
	}
	if r.Token != before {
		t.Fatalf("不能轮换凭证（客户端正拿它连着），得到 %q 期望 %q", r.Token, before)
	}
	if _, ok, _ := env.store.ResolveToken(ctx, before); !ok {
		t.Fatal("原凭证必须仍然有效")
	}
}

// 会话绑在别的账号上时登录应被拒，且不能动被登录账号的凭证。
func TestLoginOnSessionBoundToAnotherAccountFails(t *testing.T) {
	env := newTestComponent(t)
	ctx := context.Background()
	env.mustRegister(t, "alice", "hunter2") // 这个会话绑到 "1"

	env.app.sess = &fakeSession{}         // 换一个未绑定的会话
	env.mustRegister(t, "bob", "hunter2") // 它绑到 "2"

	before, _ := env.store.CurrentToken(ctx, "1")

	r, err := env.comp.Login(ctx, &protos.LoginMsg{Username: "alice", Password: "hunter2"})
	if err != nil {
		t.Fatalf("不该返回 Go error: %v", err)
	}
	if r.Ok || r.Reason != ReasonInternal {
		t.Fatalf("应回 internal，得到 %+v", r)
	}
	if after, _ := env.store.CurrentToken(ctx, "1"); after != before {
		t.Fatal("被拒的登录不能改动 alice 的凭证")
	}
}

// 绑定之后再读登记失败时不能踢：读不到完全可能发生在刚绑到「同一个 gate」
// 之后，此时若把 me 当成空串，oldGate 非空就会去踢，那一脚正好踢掉自己刚
// 建立的会话 —— 而凭证已经轮换过，客户端连 resume 都回不来。
func TestLoginDoesNotKickWhenSecondOnlineReadFails(t *testing.T) {
	env := newTestComponent(t)
	ctx := context.Background()
	env.mustRegister(t, "alice", "hunter2")

	if err := env.online.Set(ctx, "1", "gate-A"); err != nil {
		t.Fatalf("准备 online 失败: %v", err)
	}
	// onBind 在第 3 步 Bind 里触发，正好夹在两次 online 读之间：
	// 让 redis 从这一刻起报错，第 4 步的复读就必然失败。
	env.app.sess = &fakeSession{onBind: func(string) { env.mr.SetError("boom") }}
	t.Cleanup(func() { env.mr.SetError("") })

	r, err := env.comp.Login(ctx, &protos.LoginMsg{Username: "alice", Password: "hunter2"})
	if err != nil || !r.Ok {
		t.Fatalf("复读失败不该影响登录本身，得到 %+v err=%v", r, err)
	}
	if len(env.app.calls) != 0 {
		t.Fatalf("读不到当前归属时宁可漏踢也不能误踢，得到 %+v", env.app.calls)
	}
}

// component.go 是 account 服务的 pitaya handler：注册、登录、凭证恢复。
//
// 三个 handler 都返回 (*protos.LoginReply, error) —— 返回值决定消息类型：
// pitaya 的 suitableHandlerMethods 里「有返回值 = Request，无返回值 = Notify」，
// 所以它们是 Request，客户端按 mid 收 Response。这一点是必须的，不是风格选择：
// NATS 模式下 uid 未绑定就 push 不了（agent_remote.go 的 Push 直接返回
// ErrNoUIDBind），而登录失败时 uid 恰恰是空的，结果根本推不回去。
package account

import (
	"context"
	"log"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/component"
	pitayaprotos "github.com/topfreegames/pitaya/v3/pkg/protos"
	"joltgo/game/protos"
	"joltgo/online"
)

// 原因码。客户端按它显示文案，服务端不给自由文本。
const (
	ReasonBadCredentials = "bad_credentials"
	ReasonNameTaken      = "name_taken"
	ReasonBadUsername    = "bad_username"
	ReasonBadPassword    = "bad_password"
	ReasonRateLimited    = "rate_limited"
	ReasonTokenInvalid   = "token_invalid"
	ReasonInternal       = "internal"
)

// kickRoute 是 pitaya 内置的定点踢人 RPC（remote.Sys.Kick 注册在服务名 "sys" 下）。
const kickRoute = "gate.sys.kick"

// Component 是 account 服务的 pitaya 组件。
type Component struct {
	component.Base
	app    pitaya.Pitaya
	store  *Store
	online *online.Store
}

// New 构造 account 组件（在 main.go 里 Register 到 app）。
func New(app pitaya.Pitaya, store *Store, onl *online.Store) *Component {
	return &Component{app: app, store: store, online: onl}
}

// boundAccount 返回当前会话已绑定的账号 ID；未绑定（或拿不到会话）返回空串。
func (c *Component) boundAccount(ctx context.Context) string {
	if s := c.app.GetSessionFromCtx(ctx); s != nil {
		return s.UID()
	}
	return ""
}

// Register 是 register handler（route "account.account.register"）。
func (c *Component) Register(ctx context.Context, msg *protos.RegisterMsg) (*protos.LoginReply, error) {
	// 注册意味着「建一个新账号并登进去」，在一个已经绑定了账号的会话上做不到。
	// 必须在这里就挡住：Create 会**真的写出一个新账号**，等 finishLogin 再拒绝时
	// 它已经成了无主账号 —— 用户名被永久占用、谁都登不进去，而且换个用户名就能
	// 无限刷（限流是按用户名的，挡不住这个）。
	// 现实中这条路径来自客户端状态错乱（登录面板在 resume 还没回来时被点了）。
	if cur := c.boundAccount(ctx); cur != "" {
		log.Printf("account: register refused: session already bound to %s", cur)
		return fail(ReasonInternal), nil
	}
	if err := ValidateUsername(msg.Username); err != nil {
		return fail(ReasonBadUsername), nil
	}
	if err := ValidatePassword(msg.Password); err != nil {
		return fail(ReasonBadPassword), nil
	}

	// 限流按用户名（见 store.go 的 RateLimit 说明：account 拿不到客户端 IP）。
	allowed, err := c.store.Allow(ctx, msg.Username)
	if err != nil {
		log.Printf("account: rate limit check failed: %v", err)
		return fail(ReasonInternal), nil
	}
	if !allowed {
		return fail(ReasonRateLimited), nil
	}

	hash, err := HashPassword(msg.Password)
	if err != nil {
		return fail(ReasonBadPassword), nil
	}
	id, err := c.store.Create(ctx, msg.Username, hash)
	if err != nil {
		if err == ErrNameTaken {
			return fail(ReasonNameTaken), nil
		}
		log.Printf("account: create failed: %v", err)
		return fail(ReasonInternal), nil
	}

	return c.finishLogin(ctx, id, msg.Username)
}

// Login 是 login handler（route "account.account.login"）。
func (c *Component) Login(ctx context.Context, msg *protos.LoginMsg) (*protos.LoginReply, error) {
	allowed, err := c.store.Allow(ctx, msg.Username)
	if err != nil {
		log.Printf("account: rate limit check failed: %v", err)
		return fail(ReasonInternal), nil
	}
	if !allowed {
		return fail(ReasonRateLimited), nil
	}

	// 用户名不存在与密码错误返回同一个 reason：不泄露账号是否存在。
	id, ok, err := c.store.LookupByName(ctx, msg.Username)
	if err != nil {
		log.Printf("account: lookup failed: %v", err)
		return fail(ReasonInternal), nil
	}
	if !ok {
		return fail(ReasonBadCredentials), nil
	}
	acct, ok, err := c.store.GetAccount(ctx, id)
	if err != nil {
		log.Printf("account: get account failed: %v", err)
		return fail(ReasonInternal), nil
	}
	if !ok || !CheckPassword(acct.PassHash, msg.Password) {
		return fail(ReasonBadCredentials), nil
	}

	return c.finishLogin(ctx, id, acct.Username)
}

// Resume 是 resume handler（route "account.account.resume"）：拿凭证换回会话。
func (c *Component) Resume(ctx context.Context, msg *protos.ResumeMsg) (*protos.LoginReply, error) {
	if err := ValidateToken(msg.Token); err != nil {
		return fail(ReasonTokenInvalid), nil
	}
	id, ok, err := c.store.ResolveToken(ctx, msg.Token)
	if err != nil {
		log.Printf("account: resolve token failed: %v", err)
		return fail(ReasonInternal), nil
	}
	if !ok {
		return fail(ReasonTokenInvalid), nil
	}
	acct, ok, err := c.store.GetAccount(ctx, id)
	if err != nil {
		// 瞬时读失败**不是**凭证无效。映射成 token_invalid 是破坏性的：客户端的
		// 失效凭证清理正是按这个原因码删本地 token 的（godot_client/scripts/
		// fps_client.gd 的 _clear_token），一次 Redis 抖动就会把好凭证删掉、逼用户
		// 重新输密码。与上面 ResolveToken 的错误映射保持一致（那里也是这样分的）。
		log.Printf("account: get account %s for a valid token failed: %v", id, err)
		return fail(ReasonInternal), nil
	}
	if !ok {
		// 凭证有效、指针也对，但账号数据没了（被清库/手工删除）：这才是真的恢复
		// 不了，按凭证无效处理。
		log.Printf("account: account %s for a valid token is missing", id)
		return fail(ReasonTokenInvalid), nil
	}
	return c.finishLogin(ctx, id, acct.Username)
}

// finishLogin 是三个 handler 共用的收尾：记下旧 gate → 轮换凭证 → 绑定会话 →
// 定点踢掉旧连接。
//
// 顺序是关键。第 3 步 Bind 会把**同一个 gate 上**的旧会话同步关掉（框架的
// sessionsByUID 逻辑），所以第 4 步只需要处理「旧会话在另一个 gate」的情况 ——
// 而那一脚绝不会打到自己，顶号竞态因此从设计上消失，不靠自愈。
func (c *Component) finishLogin(ctx context.Context, accountID, username string) (*protos.LoginReply, error) {
	// 同一连接上的重复登录（双击、慢响应重试）会走到这里，而此时会话已经绑定过。
	// pitaya 的 Bind 对已绑定会话返回 ErrSessionAlreadyBound，且凭证轮换发生在
	// Bind 之前 —— 不挡的话第二次调用会先删掉第一次刚签发、客户端正在用的凭证，
	// 再回一个失败，客户端手里只剩死凭证，且每次重试都重复这个过程。
	s := c.app.GetSessionFromCtx(ctx)
	if cur := s.UID(); cur != "" {
		if cur != accountID {
			// 会话绑的是别的账号：客户端状态错乱，返回失败让它重连，
			// 但绝不能动任何一个账号的凭证。
			log.Printf("account: session already bound to %q, refusing to bind %q", cur, accountID)
			return fail(ReasonInternal), nil
		}
		token, err := c.store.CurrentToken(ctx, accountID)
		if err != nil || token == "" {
			log.Printf("account: current token for %s unavailable: %v", accountID, err)
			return fail(ReasonInternal), nil
		}
		return &protos.LoginReply{
			Ok:        true,
			Token:     token,
			Username:  username,
			AccountId: accountID,
		}, nil
	}

	// 1) 记下这次登录之前，该账号登记的 gate（可能是空 = 没在线）。
	oldGate, err := c.online.Gate(ctx, accountID)
	if err != nil {
		log.Printf("account: online lookup for %s failed: %v", accountID, err)
		// 读不到只影响「能不能踢掉旧连接」，不影响登录本身，继续。
		oldGate = ""
	}

	// 2) 轮换凭证。这一步是顶号的权威手段：旧客户端的 token 立即作废。
	token, err := c.store.IssueToken(ctx, accountID)
	if err != nil {
		log.Printf("account: issue token failed: %v", err)
		return fail(ReasonInternal), nil
	}

	// 3) 绑定会话。未绑定就没有身份，一切后续消息都会被当成未登录。
	if err := s.Bind(ctx, accountID); err != nil {
		log.Printf("account: bind session to %s failed: %v", accountID, err)
		// Bind 失败必须如实回 internal：若报成功，客户端会去发 match.join，
		// 而它会因会话未绑定被静默忽略，玩家卡在「正在匹配…」无从排查。
		return fail(ReasonInternal), nil
	}

	// 4) 定点踢掉旧 gate 上的连接。Bind 返回时 gate 的 after-bind 钩子已经写完
	//    登记（跨节点 RPC，天然有序），所以这里读到的是本节点。
	me, err := c.online.Gate(ctx, accountID)
	if err != nil {
		// 读不到当前归属就不能踢：把 me 视作 oldGate，让下面的判断退化为「不踢」。
		// 若让 me 留空，oldGate 非空时就会去踢 —— 而「读不到」完全可能发生在
		// 刚绑定到同一个 gate 之后，那一脚会踢掉自己刚建立的会话；此时凭证已经
		// 轮换过，客户端连 resume 都回不来。宁可漏踢（旧连接多活一会儿，
		// 反正它的凭证已经作废），也不能误踢。
		log.Printf("account: online re-read for %s failed: %v", accountID, err)
		me = oldGate
	}
	if oldGate != "" && oldGate != me {
		if err := c.kickOn(ctx, oldGate, accountID); err != nil {
			// 踢不掉不影响正确性（旧 token 已作废，它 resume 不回来），
			// 只影响「旧连接早一点闭嘴」。
			log.Printf("account: kick %s on %s failed: %v", accountID, oldGate, err)
		}
	}

	return &protos.LoginReply{
		Ok:        true,
		Token:     token,
		Username:  username,
		AccountId: accountID,
	}, nil
}

// kickOn 请指定 gate 关掉该账号的会话。用的是 pitaya 自带的踢人消息类型
// （本模块的 protos 包里没有、也不该自己造一套）。
func (c *Component) kickOn(ctx context.Context, gateID, accountID string) error {
	return c.app.RPCTo(ctx, gateID, kickRoute,
		&pitayaprotos.KickAnswer{},
		&pitayaprotos.KickMsg{UserId: accountID})
}

// fail 构造一个失败应答。
func fail(reason string) *protos.LoginReply {
	return &protos.LoginReply{Ok: false, Reason: reason}
}

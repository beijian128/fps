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

// Register 是 register handler（route "account.account.register"）。
func (c *Component) Register(ctx context.Context, msg *protos.RegisterMsg) (*protos.LoginReply, error) {
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
	if err != nil || !ok {
		log.Printf("account: account %s for a valid token is missing (err=%v)", id, err)
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
	s := c.app.GetSessionFromCtx(ctx)
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
		log.Printf("account: online re-read for %s failed: %v", accountID, err)
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

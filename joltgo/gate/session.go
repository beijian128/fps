// session.go 是 gate 的会话归属逻辑：把「账号在哪个 gate 在线」登记进 Redis，
// 并接受后端请托写会话数据。
//
// 为什么 gate 要连 Redis：会话归属只有 gate 自己知道。后端（account / match）
// 手里只有 uid，没有会话对象 —— backend 的会话池里没有前端会话。这条登记是
// best-effort 的：写失败只降级（顶号少踢一次、探活误判），正确性由凭证轮换兜底
// （被顶掉的客户端拿着已作废的 token，重连也 resume 不回来）。
package gate

import (
	"context"
	"log"
	"time"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/component"
	"github.com/topfreegames/pitaya/v3/pkg/session"
	"joltgo/game/protos"
	"joltgo/online"
)

// onlineTimeout 是会话归属读写的超时上限。
//
// 这两个调用都跑在关键路径上：markOnline 在 Bind 内部（登录链路），
// clearOnline 在连接关闭时、且持有 agent 的 closeMutex。默认 ctx 没有 deadline，
// 遇到 Redis 被黑洞（丢包而非拒连）时 go-redis 会耗掉整个重试预算（~10s），
// 把登录或连接关闭卡住 —— 而这条登记是 best-effort 的，宁可快速放弃。
const onlineTimeout = 1500 * time.Millisecond

// SessionComponent 是 gate 上的会话组件。它只经 RegisterRemote 注册（供后端
// RPC 调用），不进 handler 池：客户端发 gate.* 会在路由阶段就找不到而报错。
type SessionComponent struct {
	component.Base
	app  pitaya.Pitaya
	pool session.SessionPool
}

// NewSessionComponent 构造会话组件。
//
// 需要 pool 而不是只用 app：pitaya.Pitaya 接口上**没有** GetSessionByUID
// （只有 GetSessionFromCtx），要按 uid 找会话只能拿 SessionPool。
func NewSessionComponent(app pitaya.Pitaya, pool session.SessionPool) *SessionComponent {
	return &SessionComponent{app: app, pool: pool}
}

// RegisterSessionHooks 在 gate 的会话池上挂「绑定后 / 断开时」两个钩子，
// 维护 online 登记。必须在 app.Start() 之前调用（钩子只在启动时装配一次）。
func RegisterSessionHooks(pool session.SessionPool, serverID string, onl *online.Store) {
	// OnAfterSessionBind 在 Bind 内部、所有 sessionBindCallbacks 之后触发，
	// 此时 uid 已确定。
	pool.OnAfterSessionBind(func(ctx context.Context, s session.Session) error {
		markOnline(ctx, s, serverID, onl)
		return nil
	})
	pool.OnSessionClose(func(s session.Session) {
		clearOnline(pool, s, serverID, onl)
	})
}

// markOnline 是绑定钩子的实体（抽成函数是为了能单测：sessionPool 的钩子
// 没有公开的 getter，注册进去就取不出来了）。
func markOnline(ctx context.Context, s session.Session, serverID string, onl *online.Store) {
	uid := s.UID()
	if uid == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, onlineTimeout)
	defer cancel()
	if err := onl.Set(ctx, uid, serverID); err != nil {
		// best-effort：登记失败不阻塞连接建立，只降级。
		log.Printf("gate: set online for %s failed: %v", uid, err)
	}
}

// clearOnline 是断开钩子的实体。
//
// 两道守卫，都是为了「不要清掉别人的登记」。关闭钩子不保证跑在登录流程之前：
// 旧连接的收尾（读循环出错、心跳超时）在它自己的 goroutine 里，完全可能落在
// 新连接 Bind → markOnline 之后。而抹掉登记的后果不是「少一条记录」——
// account.finishLogin 的第 4 步会重新读一次归属，读到空就判定「旧 gate 与当前
// 不同」，一脚踢掉刚刚建立的那条连接（此时凭证已轮换，客户端 resume 也回不来）。
//
//  1. 本 gate 的会话池里已经是另一条更新的会话认领了这个 uid —— 那条登记归它，
//     不能清（顶号重连到同一个 gate 的情形）。
//  2. 登记指向的不是本节点 —— 玩家已经连到别的 gate 了，同样不能清。
//
// 反过来「该清没清」是安全的：陈旧条目指向的 gate 上已经没有该会话，定点踢过去
// 会得到 ErrSessionNotFound 并被忽略（见 online 包的说明）。所以读不到归属时
// 一律选择不清。
func clearOnline(pool session.SessionPool, s session.Session, serverID string, onl *online.Store) {
	uid := s.UID()
	if uid == "" {
		return
	}
	if cur := pool.GetSessionByUID(uid); cur != nil && cur.ID() != s.ID() {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), onlineTimeout)
	defer cancel()
	gateID, err := onl.Gate(ctx, uid)
	if err != nil {
		log.Printf("gate: online lookup for %s failed, keeping entry: %v", uid, err)
		return
	}
	if gateID != serverID {
		// 空串（没登记）也走这里：本来就没什么可清的。
		return
	}
	if err := onl.Clear(ctx, uid); err != nil {
		log.Printf("gate: clear online for %s failed: %v", uid, err)
	}
}

// BindGame 是远端 RPC remote（route "gate.gate.bindgame"）：后端（match）请本
// gate 把对局归属写进玩家自己的会话数据。
//
// 让 gate 改而不是 match 隔着 NATS 用 PushToFront 改：会话属于前端，请它自己改
// 比跨进程改别人的状态更正确；而且 match 手里只有 uid，根本拿不到会话对象。
func (c *SessionComponent) BindGame(ctx context.Context, msg *protos.BindGameMsg) (*protos.BindGameReply, error) {
	// 纵深防御。正常情况下客户端到不了这里 —— 本组件只经 RegisterRemote 暴露
	// （见 main.go），客户端发的 gate.gate.bindgame 会进 gate 的 handler 池、
	// 找不到而报错。但若将来有人把 app.Register 也加上，这条守卫就是唯一的
	// 拦截点：客户端会话 IsFrontend()==true，后端 RPC 的 ctx 里没有会话（nil）。
	if s := c.app.GetSessionFromCtx(ctx); s != nil && s.GetIsFrontend() {
		log.Printf("gate: reject bindgame from client session uid=%s", s.UID())
		return &protos.BindGameReply{Found: false}, nil
	}

	target := c.pool.GetSessionByUID(msg.Uid)
	if target == nil {
		// 玩家已不在本 gate 上（掉线或被顶号关掉了）。
		return &protos.BindGameReply{Found: false}, nil
	}

	// 空 game_server_id 表示回滚（建局失败时清掉归属），此时写入空串 ——
	// gate 的 routeGame 读不到非空 gsid 就会拒绝 game.*，正是想要的效果。
	if err := target.Set("gameServerId", msg.GameServerId); err != nil {
		log.Printf("gate: set gameServerId for %s failed: %v", msg.Uid, err)
		return &protos.BindGameReply{Found: false}, nil
	}
	return &protos.BindGameReply{Found: true}, nil
}

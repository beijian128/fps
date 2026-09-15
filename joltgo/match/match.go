// Package match 是 match 服务（backend）：对局匹配。凑齐 2 人（或超时兜底单人）
// 后挑一个 game 节点，请各玩家所属的 gate 写会话数据，并把结果推给双方客户端。
//
// 节点无状态：排队队列在 Redis（ZSET），会话归属也在 Redis（online:），
// 因此多个 match 节点可以同时跑、共享同一个队列。
package match

import (
	"context"
	"errors"
	"log"
	"sync/atomic"
	"time"

	"github.com/nats-io/nuid"
	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/cluster"
	"github.com/topfreegames/pitaya/v3/pkg/component"
	"joltgo/game/protos"
	"joltgo/online"
)

const (
	gameServerType  = "game"               // game 服务类型（AddRoute 与服务发现用）
	matchedRoute    = "onMatched"          // match → 客户端 push 的 route
	statusRoute     = "onMatchStatus"      // match → 客户端 push 的 route（匹配期队列状态）
	gameCreateRoute = "game.game.create"   // game 服务的创建对局 RPC route（三段式）
	gameRejoinRoute = "game.game.rejoin"   // 回局查询 RPC route（三段式）
	gameLeaveRoute  = "game.game.leave"    // 把玩家从存量实例里释放出来的 RPC route
	bindGameRoute   = "gate.gate.bindgame" // 请玩家所属 gate 写会话数据（三段式）
	tickInterval    = time.Second          // 抢配对的轮询间隔
)

// errPlayerGone 表示对方 gate 应答 found=false：该 gate 上已经没有这个会话
// （掉线，或被顶号顶掉了）。RPC 本身是成功的，所以不能只看 error —— 探活的
// 全部意义就在这个字段上：online 登记允许陈旧（见 online 包的说明），
// 「登记还在但人已经没了」正是它要挡下来的情况。
var errPlayerGone = errors.New("player not on that gate")

// RejoinResult 是一次回局查询的结果。GameServerID 是托管该实例的 game 节点。
type RejoinResult struct {
	Found        bool
	MatchID      string
	PlayerIdx    int
	GameServerID string
}

// firstFound 从各 game 节点的应答里挑出第一个命中的。replies 的键是 game 节点 id。
// 抽成纯函数是为了能脱离 pitaya 直接单测。
func firstFound(replies map[string]*RejoinResult) (*RejoinResult, bool) {
	for _, r := range replies {
		if r.Found {
			return r, true
		}
	}
	return nil, false
}

// Component 是 match 服务的 pitaya 组件。
type Component struct {
	component.Base
	app    pitaya.Pitaya
	queue  *Queue
	online *online.Store

	// secret 是 GM 管理指令的共享密钥（来自 -gmkey）。为空 = 不提供管理入口，
	// 因此 adminKeyAllowed 对空密钥一律拒绝。启动时由 main.go 写入。
	//
	// 用 atomic.Value 而不是裸字段：它由 main.go 在 app.Start() 之前写入、
	// 由 RPC handler goroutine 读取，两者之间没有 happens-before（pitaya 的 RPC
	// 走 NATS 回调），裸字段在这里是数据竞争。
	secret atomic.Value // string
}

// UpdateSecret 更新 GM 管理密钥。
func (c *Component) UpdateSecret(secret string) { c.secret.Store(secret) }

// adminSecret 读当前密钥，未设置时返回空串（adminKeyAllowed 会因此拒绝一切请求）。
func (c *Component) adminSecret() string {
	v, _ := c.secret.Load().(string)
	return v
}

// New 构造 match 组件。secret 为空表示本节点不提供 GM 管理入口。
func New(app pitaya.Pitaya, queue *Queue, onl *online.Store, secret string) *Component {
	c := &Component{app: app, queue: queue, online: onl}
	c.UpdateSecret(secret)
	return c
}

// Join 是远端 RPC handler（route "match.join"）：把已登录的会话加入匹配队列。
//
// 身份来自会话绑定 —— account 服务在登录成功时做过 s.Bind(ctx, accountID)，
// 这里只读会话 UID，不再看任何客户端传来的凭证。未绑定的会话说明客户端没登录
// （或登录失败后擅自发了 join），静默忽略并记日志。
func (c *Component) Join(ctx context.Context, msg *protos.JoinMsg) {
	s := c.app.GetSessionFromCtx(ctx)
	uid := s.UID()
	if uid == "" {
		log.Printf("match: join rejected: session not bound")
		return
	}

	// 回局优先：存量对局还在就直接回去，不入匹配队列。
	if c.tryRejoin(ctx, uid) {
		return
	}

	if err := c.queue.Enqueue(ctx, uid); err != nil {
		log.Printf("match: enqueue %s failed: %v", uid, err)
		return
	}
	c.tryMatch(ctx)
}

// findInstance 询问所有 game 节点是否托管着该 uid 的存量实例，返回第一个命中的。
//
// 只按 uid 定址：会话对象本身用不上 —— 会话数据由该 uid 所在的 gate 去写
// （bindGameOn），不是在这里改。抽出来是因为三处用同一份查询：
// tryRejoin（回局）/ Pending（进大厅时问「我有没有没打完的局」）/ Abandon（放弃对局）。
func (c *Component) findInstance(ctx context.Context, uid string) (*RejoinResult, bool) {
	servers, err := c.app.GetServersByType(gameServerType)
	if err != nil || len(servers) == 0 {
		return nil, false
	}
	replies := map[string]*RejoinResult{}
	for id, srv := range servers {
		reply := &protos.RejoinReply{}
		// RejoinMsg 的线上字段名仍叫 Token，但传的是账号 uid（会话 uid）——
		// 回局查询早已按 uid 定址，字段名是历史包袱，不改 proto。
		if err := c.app.RPCTo(ctx, srv.ID, gameRejoinRoute, reply, &protos.RejoinMsg{Token: uid}); err != nil {
			continue // 该节点不可达，跳过
		}
		replies[id] = &RejoinResult{
			Found:        reply.Found,
			MatchID:      reply.MatchId,
			PlayerIdx:    int(reply.PlayerIdx),
			GameServerID: srv.ID,
		}
	}
	return firstFound(replies)
}

// tryRejoin 命中存量实例则走与首次匹配相同的收尾路径（写会话数据 + 推 onMatched），
// 返回 true。
func (c *Component) tryRejoin(ctx context.Context, uid string) bool {
	hit, ok := c.findInstance(ctx, uid)
	if !ok {
		return false
	}
	log.Printf("match: uid %s rejoined match %s on game %s as slot %d",
		uid, hit.MatchID, hit.GameServerID, hit.PlayerIdx)

	gateID, err := c.online.Gate(ctx, uid)
	if err != nil || gateID == "" {
		log.Printf("match: rejoin %s: no online gate (err=%v)", uid, err)
		return false
	}
	if err := c.bindGameOn(ctx, gateID, uid, hit.MatchID, hit.GameServerID, hit.PlayerIdx); err != nil {
		log.Printf("match: rejoin bind game for %s failed: %v", uid, err)
		return false
	}
	c.pushMatched(uid, hit.MatchID, hit.GameServerID, hit.PlayerIdx)
	return true
}

// AfterInit 启动配对循环：每秒尝试凑满两人开局，并把队列状态推给正在等待的玩家。
//
// 没有单人兜底：等不到第二个人就一直等，玩家可以主动取消（见 Cancel）。
func (c *Component) AfterInit() {
	go func() {
		ticker := time.NewTicker(tickInterval)
		defer ticker.Stop()
		for range ticker.C {
			ctx := context.Background()
			c.tryMatch(ctx)
			c.pushMatchStatus(ctx)
		}
	}()
}

// pushMatchStatus 把队列状态推给每个正在等待的玩家。
//
// 逐个推而不是一批推同一个载荷：等待时长因人而异。队列规模小时成本可忽略；
// 将来队列上千就改成「批量推人数 + 客户端本地计时」（见 spec §6.4）。
func (c *Component) pushMatchStatus(ctx context.Context) {
	total, entries, err := c.queue.Snapshot(ctx)
	if err != nil {
		log.Printf("match: queue snapshot failed: %v", err)
		return
	}
	if total == 0 {
		return
	}
	for _, entry := range entries {
		status := &protos.MatchStatus{
			QueuedPlayers: int32(total),
			WaitedSeconds: entry.WaitedSeconds,
		}
		if _, err := c.app.SendPushToUsers(statusRoute, status, []string{entry.UID}, "gate"); err != nil {
			log.Printf("match: push %s to %s failed: %v", statusRoute, entry.UID, err)
		}
	}
}

// Pending 是客户端请求 handler（route "match.pending"）：**只查询**「我有没有没打完
// 的局」，命中就把 match_id 回给客户端，由客户端弹「回到对局 / 放弃对局」的询问框。
//
// 与 match.join 的回局分支共用一份查询（findInstance），但**故意什么都不改**：
// 不写会话数据、不推 onMatched、不入队。进大厅只是想知道「要不要弹这个框」——
// 在这里顺手把人塞回对局，就等于把「询问」变成「强制重连」，玩家没得选。
func (c *Component) Pending(ctx context.Context, _ *protos.PendingMatchMsg) (*protos.PendingMatchReply, error) {
	s := c.app.GetSessionFromCtx(ctx)
	if s == nil || s.UID() == "" {
		// 未登录的会话问不出任何东西（也没有 uid 可查）。查询类接口不报错，回 found=false。
		log.Printf("match: pending rejected: session not bound")
		return &protos.PendingMatchReply{Found: false}, nil
	}
	hit, ok := c.findInstance(ctx, s.UID())
	if !ok {
		return &protos.PendingMatchReply{Found: false}, nil
	}
	log.Printf("match: uid %s has pending match %s on game %s as slot %d",
		s.UID(), hit.MatchID, hit.GameServerID, hit.PlayerIdx)
	return &protos.PendingMatchReply{Found: true, MatchId: hit.MatchID}, nil
}

// Abandon 是客户端请求 handler（route "match.abandon"）：放弃那场没打完的局。
//
// **不是终止对局**：只是请托管实例的 game 节点把当前玩家释放出来（route
// game.game.leave）—— 他不再收帧、不再参与结算；对局本身继续跑，对手那一局照常打到
// 分出胜负。所以这里唯一做的事就是把 RPC 转过去，外加把会话里那份对局归属清掉。
//
// reason：released（已释放）/ not_found（此刻查不到他的存量对局）/ unauthenticated /
// internal（RPC 不通）。ok=false 时客户端保留询问框、提示失败，不假装成功。
func (c *Component) Abandon(ctx context.Context, _ *protos.AbandonMatchMsg) (*protos.AbandonMatchReply, error) {
	s := c.app.GetSessionFromCtx(ctx)
	if s == nil || s.UID() == "" {
		log.Printf("match: abandon rejected: session not bound")
		return &protos.AbandonMatchReply{Ok: false, Reason: "unauthenticated"}, nil
	}
	uid := s.UID()

	hit, ok := c.findInstance(ctx, uid)
	if !ok {
		return &protos.AbandonMatchReply{Ok: false, Reason: "not_found"}, nil
	}
	reply := &protos.LeaveReply{}
	if err := c.app.RPCTo(ctx, hit.GameServerID, gameLeaveRoute, reply,
		&protos.LeaveMsg{Uid: uid}); err != nil {
		log.Printf("match: abandon %s: leave on %s failed: %v", uid, hit.GameServerID, err)
		return &protos.AbandonMatchReply{Ok: false, Reason: "internal"}, nil
	}
	if !reply.Ok {
		// 节点在、但此刻已经没有他的实例（刚好打完了 / 已经释放过）。语义上等同 not_found。
		log.Printf("match: abandon %s: game %s has no instance anymore", uid, hit.GameServerID)
		return &protos.AbandonMatchReply{Ok: false, Reason: "not_found"}, nil
	}
	log.Printf("match: uid %s abandoned match %s on game %s (slot %d)",
		uid, hit.MatchID, hit.GameServerID, hit.PlayerIdx)

	// 清掉会话里那份对局归属（最好努力）：他不再属于那一局，而会话数据里若还留着
	// gameServerId，客户端一旦发出 game.cmd 会被定点路由到旧节点。失败只记日志 ——
	// 真正管用的是 game 侧已经把 uid 从实例注册表里摘掉了（那些消息查不到实例、静默丢弃）。
	if gateID, err := c.online.Gate(ctx, uid); err != nil || gateID == "" {
		log.Printf("match: abandon %s: no online gate (err=%v)", uid, err)
	} else if err := c.bindGameOn(ctx, gateID, uid, "", "", 0); err != nil {
		log.Printf("match: abandon %s: clear game binding failed: %v", uid, err)
	}
	return &protos.AbandonMatchReply{Ok: true, Reason: "released"}, nil
}

// Cancel 是客户端请求 handler（route "match.cancel"）：把已登录会话移出匹配队列。
//
// 三态语义（见 spec §5.3）：真的移出了才 ok=true；ZREM 返回 0 说明人已经不在这条队列里
// —— 可能是 tick 刚把他配对走（那就顺手走一次回局查询，把他带进已经开好的对局，而不是
// 从局里拽出来），也可能只是从来没排过队。
func (c *Component) Cancel(ctx context.Context, _ *protos.MatchCancelMsg) (*protos.MatchCancelReply, error) {
	s := c.app.GetSessionFromCtx(ctx)
	if s == nil || s.UID() == "" {
		log.Printf("match: cancel rejected: session not bound")
		return &protos.MatchCancelReply{Ok: false, Reason: "unauthenticated"}, nil
	}
	uid := s.UID()
	removed, err := c.queue.Remove(ctx, uid)
	if err != nil {
		log.Printf("match: cancel %s failed: %v", uid, err)
		return &protos.MatchCancelReply{Ok: false, Reason: "internal"}, nil
	}
	if removed {
		log.Printf("match: uid %s cancelled matchmaking", uid)
		return &protos.MatchCancelReply{Ok: true, Reason: "cancelled"}, nil
	}
	// 竞态窗口：tick 可能刚把他弹出去、实例还没建好。这里查不到就返回 not_queued，
	// 紧接着 onMatched 会照常到达，客户端切进对局 —— 不会出现「悬空玩家」。
	if c.tryRejoin(ctx, uid) {
		return &protos.MatchCancelReply{Ok: false, Reason: "already_matched"}, nil
	}
	return &protos.MatchCancelReply{Ok: false, Reason: "not_queued"}, nil
}

// tryMatch 尝试配对：队列凑满 2 人即开局。多个 match 节点同时抢也只有一个
// 能弹出（Lua 原子），不需要选主。
func (c *Component) tryMatch(ctx context.Context) {
	uids, err := c.queue.PopPair(ctx)
	if err != nil {
		log.Printf("match: pop pair failed: %v", err)
		return
	}
	if len(uids) == 0 {
		return
	}
	c.startMatch(ctx, uids)
}

// bindGameOn 请指定 gate 把对局归属写进玩家的会话数据。
// gameServerID 为空表示回滚（清掉归属）。
//
// found=false 当作 errPlayerGone 返回：RPC 通了不代表人还在 —— 那个 gate 上
// 可能已经没有这个会话了（掉线 / 被顶号）。这正是开局前探活的判据。
func (c *Component) bindGameOn(ctx context.Context, gateID, uid, matchID, gameServerID string, playerIdx int) error {
	reply := &protos.BindGameReply{}
	if err := c.app.RPCTo(ctx, gateID, bindGameRoute, reply,
		&protos.BindGameMsg{
			Uid:          uid,
			GameServerId: gameServerID,
			MatchId:      matchID,
			PlayerIdx:    int32(playerIdx),
		}); err != nil {
		return err
	}
	if !reply.Found {
		return errPlayerGone
	}
	return nil
}

// pushMatched 把匹配结果推给客户端。
//
// 走全局 SendPushToUsers（发布到 pitaya/gate/user/{uid}/push）而不是定点 RPC：
// 推送与玩家连在哪个 gate 无关，NATS 会投给持有该会话的那个 gate。
func (c *Component) pushMatched(uid, matchID, gameServerID string, playerIdx int) {
	if _, err := c.app.SendPushToUsers(matchedRoute, &protos.MatchResult{
		MatchId:      matchID,
		GameServerId: gameServerID,
		PlayerIdx:    int32(playerIdx),
	}, []string{uid}, "gate"); err != nil {
		log.Printf("match: push onMatched to %s failed: %v", uid, err)
	}
}

// startMatch 挑一个 game 节点、请各玩家的 gate 写会话数据、创建对局，最后推结果。
//
// 顺序是先定地址再建局：排队者可能已经掉线（match 侧没有断线钩子），先读在线
// 登记再 RPC 请对方 gate 写会话数据，能把掉线的人在建局前剔掉 —— 不会留下
// 「占着槽位的幽灵玩家」，也不会出现客户端收到 onMatched 但 resync 路由不到的
// 死局。槽位（player_idx）在剔除过程中就定下来，因为 game.create 的 uids
// 下标就是 player_idx。
func (c *Component) startMatch(ctx context.Context, uids []string) {
	servers, err := c.app.GetServersByType(gameServerType)
	if err != nil || len(servers) == 0 {
		// 没有可用的 game 节点是集群侧的瞬时状况，跟这几个人在不在线无关：
		// 他们已经被原子弹出队列，直接 return 就是静默丢人。放回去等下一轮。
		log.Printf("match: no game server available, requeue %v: %v", uids, err)
		for _, uid := range uids {
			c.requeue(ctx, uid)
		}
		return
	}
	// 简单挑选：取第一个 game 节点（demo 规模足够；生产可做负载均衡）。
	var target *cluster.Server
	for _, srv := range servers {
		target = srv
		break
	}

	matchID := nuid.New().Next()

	// 1) 定地址 + 探活：读不到在线登记、或对方的 gate 写不进去，就当这个人没了。
	alive := make([]string, 0, len(uids))
	gates := make([]string, 0, len(uids))
	for _, uid := range uids {
		gateID, err := c.online.Gate(ctx, uid)
		if err != nil {
			// 瞬时故障（Redis 抖动等）：人还在线，不能就这么丢掉 —— 他已经被
			// 原子弹出队列，不重新入队的话没人会再管他，而客户端只发一次
			// match.join 然后一直等 onMatched，永远等不到。
			log.Printf("match: requeue %s from match %s: online lookup failed: %v", uid, matchID, err)
			c.requeue(ctx, uid)
			continue
		}
		if gateID == "" {
			// 确实不在了：登记里没有他。
			log.Printf("match: dropping %s from match %s: offline", uid, matchID)
			continue
		}
		slot := len(alive) // 槽位 = 在存活名单里的位置
		if err := c.bindGameOn(ctx, gateID, uid, matchID, target.ID, slot); err != nil {
			if errors.Is(err, errPlayerGone) {
				// 那个 gate 上已经没有这个会话：人确实走了。
				log.Printf("match: dropping %s from match %s: offline", uid, matchID)
			} else {
				log.Printf("match: requeue %s from match %s: bind failed: %v", uid, matchID, err)
				c.requeue(ctx, uid)
			}
			continue
		}
		alive = append(alive, uid)
		gates = append(gates, gateID)
	}
	if len(alive) == 0 {
		return
	}

	// 2) 建局。失败则回滚会话数据，否则客户端会拿着一个不存在的 game 节点去发
	//    game.cmd，全部被静默丢弃（routeGame 找不到 gameServerId 对应节点）。
	reply := &protos.CreateGameReply{}
	if err := c.app.RPCTo(ctx, target.ID, gameCreateRoute, reply, &protos.CreateGameMsg{
		MatchId: matchID,
		Uids:    alive,
	}); err != nil {
		log.Printf("match: create game on %s failed: %v", target.ID, err)
		// 这些人几秒前刚被探活过（bindGameOn 应答 found=true），建局失败是
		// 集群侧的问题，不是他们的问题 —— 回滚会话数据后重新入队，否则他们
		// 已经被原子弹出、又收不到 onMatched，就此静默消失。
		for i, uid := range alive {
			if err := c.bindGameOn(ctx, gates[i], uid, "", "", i); err != nil {
				if errors.Is(err, errPlayerGone) {
					// 回滚时人已经走了：会话本来就没了，无需回滚也无需重新入队。
					log.Printf("match: rollback for %s skipped: already offline", uid)
					continue
				}
				// 回滚没成功（RPC 不通）：会话里留着指向不存在对局的归属。
				// 仍然重新入队 —— 下一次开局的 bindGameOn 会覆盖掉这份脏数据。
				log.Printf("match: rollback bind for %s failed: %v", uid, err)
			}
			c.requeue(ctx, uid)
		}
		return
	}

	// 3) 推结果。
	for slot, uid := range alive {
		c.pushMatched(uid, matchID, target.ID, slot)
	}
	log.Printf("match: started match %s on game %s with %d players", matchID, target.ID, len(alive))
}

// requeue 把因为瞬时故障没能进局的玩家放回队列。
//
// 他已经被原子弹出，不这么做就没人再管他了；客户端只发一次 match.join，
// 之后一直等 onMatched —— 静默丢失比开局失败糟得多。ZSET 按 uid 去重，
// 重复入队只会刷新他的等待时间，所以这里不需要判断是否已在队列里。
//
// 自己记日志、不返回错误：入队失败不该把正在组建的这一局也带下去。
func (c *Component) requeue(ctx context.Context, uid string) {
	if err := c.queue.Enqueue(ctx, uid); err != nil {
		log.Printf("match: requeue %s failed: %v", uid, err)
	}
}

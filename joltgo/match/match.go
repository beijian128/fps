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
	gameCreateRoute = "game.game.create"   // game 服务的创建对局 RPC route（三段式）
	gameRejoinRoute = "game.game.rejoin"   // 回局查询 RPC route（三段式）
	bindGameRoute   = "gate.gate.bindgame" // 请玩家所属 gate 写会话数据（三段式）
	timeout         = 10 * time.Second     // 单人兜底开局的等待超时
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
}

// New 构造 match 组件。
func New(app pitaya.Pitaya, queue *Queue, onl *online.Store) *Component {
	return &Component{app: app, queue: queue, online: onl}
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

// tryRejoin 询问所有 game 节点是否托管着该 uid 的存量实例。命中则走与首次匹配
// 相同的收尾路径（写会话数据 + 推 onMatched），返回 true。
//
// 只按 uid 定址：会话对象本身用不上 —— 会话数据由该 uid 所在的 gate 去写
// （bindGameOn），不是在这里改。
func (c *Component) tryRejoin(ctx context.Context, uid string) bool {
	servers, err := c.app.GetServersByType(gameServerType)
	if err != nil || len(servers) == 0 {
		return false
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
	hit, ok := firstFound(replies)
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

// AfterInit 启动兜底定时器：长时间等不到第二人的玩家单人开局。
func (c *Component) AfterInit() {
	go func() {
		ticker := time.NewTicker(tickInterval)
		defer ticker.Stop()
		for range ticker.C {
			c.tryMatch(context.Background())
			c.tryMatchTimeout(context.Background())
		}
	}()
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

// tryMatchTimeout 兜底：单人等待超过 timeout 即单人开局。
func (c *Component) tryMatchTimeout(ctx context.Context) {
	uid, err := c.queue.PopStale(ctx, timeout)
	if err != nil {
		log.Printf("match: pop stale failed: %v", err)
		return
	}
	if uid == "" {
		return
	}
	c.startMatch(ctx, []string{uid})
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

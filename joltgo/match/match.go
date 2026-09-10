// Package match 是 match 服务（backend）：对局匹配。凑齐 2 人（或超时兜底单人）
// 后挑一个 game 节点，RPC 通知它创建对局实例，并把结果推给双方客户端。
package match

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/nats-io/nuid"
	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/cluster"
	"github.com/topfreegames/pitaya/v3/pkg/component"
	"github.com/topfreegames/pitaya/v3/pkg/session"
	"joltgo/game/protos"
)

const (
	gameServerType  = "game"             // game 服务类型（AddRoute 与服务发现用）
	matchedRoute    = "onMatched"        // match → 客户端 push 的 route
	gameCreateRoute = "game.game.create" // game 服务的创建对局 RPC route（三段式 server.service.method）
	gameRejoinRoute = "game.game.rejoin" // 回局查询 RPC route（三段式）
	timeout         = 10 * time.Second   // 单人兜底开局的等待超时
)

// RejoinResult 是一次回局查询的结果。GameServerID 是托管该实例的 game 节点。
type RejoinResult struct {
	Found        bool
	MatchID      string
	PlayerIdx    int
	GameServerID string
}

// firstFound 从各 game 节点的应答里挑出第一个命中的。nodes 是节点 id。
// 抽成纯函数是为了能脱离 pitaya 直接单测。
func firstFound(replies map[string]*RejoinResult) (*RejoinResult, bool) {
	for _, r := range replies {
		if r.Found {
			return r, true
		}
	}
	return nil, false
}

// queuedPlayer 是匹配队列里的一个等待者。
type queuedPlayer struct {
	uid      string
	session  session.Session // 远端会话（用于 Set/PushToFront 下发 gameServerId）
	joinedAt time.Time
}

// Component 是 match 服务的 pitaya 组件。
type Component struct {
	component.Base
	app pitaya.Pitaya

	mu    sync.Mutex
	queue []queuedPlayer
}

// New 构造 match 组件。
func New(app pitaya.Pitaya) *Component {
	return &Component{app: app}
}

// Join 是远端 RPC handler（route "match.join"）：绑定会话 UID 并加入匹配队列。
// uid 用客户端持久化的 token 而不是每次新建的 nuid —— 重连时同一个 token 会
// 让 pitaya 前端把旧会话顶掉（session.go:460-464），从而让对局实例的 uids
// 数组依然指向正确的连接。
func (c *Component) Join(ctx context.Context, msg *protos.JoinMsg) {
	s := c.app.GetSessionFromCtx(ctx)
	uid := msg.Token
	if uid == "" {
		uid = nuid.New().Next() // 未带 token 的旧客户端：退化成一次性身份
	}
	if err := s.Bind(ctx, uid); err != nil {
		log.Printf("match: bind session failed: %v", err)
		return
	}

	if c.tryRejoin(ctx, s, uid) {
		return // 已回到存量对局，不入匹配队列
	}

	c.mu.Lock()
	c.queue = append(c.queue, queuedPlayer{uid: uid, session: s, joinedAt: time.Now()})
	c.mu.Unlock()

	c.tryMatch()
}

// tryRejoin 询问所有 game 节点是否托管着该 token 的存量实例。命中则把它当作
// 一次「匹配成功」收尾（写会话数据 + 推 onMatched），返回 true。
func (c *Component) tryRejoin(ctx context.Context, s session.Session, token string) bool {
	servers, err := c.app.GetServersByType(gameServerType)
	if err != nil || len(servers) == 0 {
		return false
	}
	replies := map[string]*RejoinResult{}
	for id, srv := range servers {
		reply := &protos.RejoinReply{}
		if err := c.app.RPCTo(ctx, srv.ID, gameRejoinRoute, reply, &protos.RejoinMsg{Token: token}); err != nil {
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
	log.Printf("match: token %s rejoined match %s on game %s as slot %d",
		token, hit.MatchID, hit.GameServerID, hit.PlayerIdx)
	c.bindPlayer(s, token, hit.MatchID, hit.PlayerIdx, hit.GameServerID)
	return true
}

// bindPlayer 把对局归属写进会话数据（gate 据此路由 game.*），并推送匹配结果。
// 初次匹配与重连回局共用这条收尾路径。
func (c *Component) bindPlayer(s session.Session, uid, matchID string, playerIdx int, gameServerID string) {
	if err := s.Set("gameServerId", gameServerID); err == nil {
		if err := s.PushToFront(context.Background()); err != nil {
			log.Printf("match: push session data failed: %v", err)
		}
	}
	if _, err := c.app.SendPushToUsers(matchedRoute, &protos.MatchResult{
		MatchId:      matchID,
		GameServerId: gameServerID,
		PlayerIdx:    int32(playerIdx),
	}, []string{uid}, "gate"); err != nil {
		log.Printf("match: push onMatched to %s failed: %v", uid, err)
	}
}

// AfterInit 启动兜底定时器：长时间等不到第二人的玩家单人开局。
func (c *Component) AfterInit() {
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for range ticker.C {
			c.tryMatchTimeout()
		}
	}()
}

// tryMatch 尝试配对（每次 Join 后调用）：队列里凑满 2 人即开局。
func (c *Component) tryMatch() {
	c.mu.Lock()
	if len(c.queue) < 2 {
		c.mu.Unlock()
		return
	}
	pair := []queuedPlayer{c.queue[0], c.queue[1]}
	c.queue = c.queue[2:]
	c.mu.Unlock()

	c.startMatch(pair)
}

// tryMatchTimeout 兜底：单人等待超过 timeout 即单人开局。
func (c *Component) tryMatchTimeout() {
	c.mu.Lock()
	for i := 0; i < len(c.queue); i++ {
		if time.Since(c.queue[i].joinedAt) >= timeout {
			pair := []queuedPlayer{c.queue[i]}
			c.queue = append(c.queue[:i], c.queue[i+1:]...)
			c.mu.Unlock()
			c.startMatch(pair)
			return
		}
	}
	c.mu.Unlock()
}

// startMatch 挑一个 game 节点、RPC 创建对局，然后把结果推给所有玩家。
func (c *Component) startMatch(players []queuedPlayer) {
	servers, err := c.app.GetServersByType(gameServerType)
	if err != nil || len(servers) == 0 {
		log.Printf("match: no game server available: %v", err)
		return
	}
	// 简单挑选：取第一个 game 节点（demo 规模足够；生产可做负载均衡）。
	var target *cluster.Server
	for _, srv := range servers {
		target = srv
		break
	}

	matchID := nuid.New().Next()
	uids := make([]string, len(players))
	for i, p := range players {
		uids[i] = p.uid
	}

	reply := &protos.CreateGameReply{}
	if err := c.app.RPCTo(context.Background(), target.ID, gameCreateRoute, reply, &protos.CreateGameMsg{
		MatchId: matchID,
		Uids:    uids,
	}); err != nil {
		log.Printf("match: create game on %s failed: %v", target.ID, err)
		return
	}

	for i, p := range players {
		c.bindPlayer(p.session, p.uid, matchID, i, target.ID)
	}
	log.Printf("match: started match %s on game %s with %d players", matchID, target.ID, len(players))
}

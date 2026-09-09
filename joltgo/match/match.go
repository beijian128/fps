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
	timeout         = 10 * time.Second   // 单人兜底开局的等待超时
)

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
func (c *Component) Join(ctx context.Context, _ *protos.JoinMsg) {
	s := c.app.GetSessionFromCtx(ctx)
	uid := nuid.New().Next()
	if err := s.Bind(ctx, uid); err != nil {
		log.Printf("match: bind session failed: %v", err)
		return
	}

	c.mu.Lock()
	c.queue = append(c.queue, queuedPlayer{uid: uid, session: s, joinedAt: time.Now()})
	c.mu.Unlock()

	c.tryMatch()
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

	// 通知每个玩家：把 game 节点 id 写进会话数据（gate 据此路由 game.* 消息），
	// 并推送匹配结果（含玩家槽位）。
	for i, p := range players {
		if err := p.session.Set("gameServerId", target.ID); err == nil {
			if err := p.session.PushToFront(context.Background()); err != nil {
				log.Printf("match: push session data failed: %v", err)
			}
		}
		if _, err := c.app.SendPushToUsers(matchedRoute, &protos.MatchResult{
			MatchId:      matchID,
			GameServerId: target.ID,
			PlayerIdx:    int32(i),
		}, []string{p.uid}, "gate"); err != nil {
			log.Printf("match: push onMatched to %s failed: %v", p.uid, err)
		}
	}
	log.Printf("match: started match %s on game %s with %d players", matchID, target.ID, len(players))
}

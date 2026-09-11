package match

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/golang/protobuf/proto"
	"github.com/redis/go-redis/v9"
	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/cluster"
	"github.com/topfreegames/pitaya/v3/pkg/session"
	"joltgo/game/protos"
	"joltgo/online"
)

func TestFirstFound(t *testing.T) {
	if _, ok := firstFound(map[string]*RejoinResult{}); ok {
		t.Fatal("没有任何应答时不应命中")
	}
	if _, ok := firstFound(map[string]*RejoinResult{
		"g1": {Found: false},
	}); ok {
		t.Fatal("全部未命中时不应命中")
	}
	got, ok := firstFound(map[string]*RejoinResult{
		"g1": {Found: false},
		"g2": {Found: true, MatchID: "m1", PlayerIdx: 1, GameServerID: "g2"},
	})
	if !ok {
		t.Fatal("有节点命中时应返回 ok")
	}
	if got.MatchID != "m1" || got.PlayerIdx != 1 || got.GameServerID != "g2" {
		t.Fatalf("应命中 g2 的实例，得到 %+v", got)
	}
}

// Component 只需要 pitaya.Pitaya 接口，所以「嵌入接口 + 覆盖用得到的方法」
// 就够驱动 Join 了 —— 不必引入 gomock。
type joinTestApp struct {
	pitaya.Pitaya
	sess session.Session
}

func (a *joinTestApp) GetSessionFromCtx(context.Context) session.Session { return a.sess }

func (a *joinTestApp) GetServersByType(string) (map[string]*cluster.Server, error) {
	return nil, errors.New("no game server") // 回局查询必然是 miss
}

// joinTestSession 只实现 Join 用到的 UID。
type joinTestSession struct {
	session.Session
	uid string
}

func (s *joinTestSession) UID() string { return s.uid }

func newTestComponent(t *testing.T, sess session.Session) (*Component, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return New(&joinTestApp{sess: sess}, NewQueue(rdb), online.NewStore(rdb)), mr
}

// 排队等待期间断线重连：同一个 uid 再 Join 一次，队列里必须还是只有一条
// （ZSET 按 member 去重）。不去重的话这里会是 2 条，进而可能自己跟自己配对、
// 或单人兜底时开两局。
func TestJoinDedupsQueuedUid(t *testing.T) {
	c, _ := newTestComponent(t, &joinTestSession{uid: "T"})
	ctx := context.Background()

	c.Join(ctx, &protos.JoinMsg{})
	if n, _ := c.queue.rdb.ZCard(ctx, queueKey).Result(); n != 1 {
		t.Fatalf("首次 Join 应入队一条，得到 %d", n)
	}
	c.Join(ctx, &protos.JoinMsg{})
	if n, _ := c.queue.rdb.ZCard(ctx, queueKey).Result(); n != 1 {
		t.Fatalf("同 uid 重连不应重复入队，得到 %d", n)
	}
	if got, _ := c.queue.rdb.ZRange(ctx, queueKey, 0, -1).Result(); len(got) != 1 || got[0] != "T" {
		t.Fatalf("队列里应是 T，得到 %v", got)
	}
}

// 未登录（会话未绑定）的 Join 必须被忽略：不入队。
func TestJoinRejectsUnboundSession(t *testing.T) {
	c, _ := newTestComponent(t, &joinTestSession{uid: ""})
	ctx := context.Background()

	c.Join(ctx, &protos.JoinMsg{})
	if n, _ := c.queue.rdb.ZCard(ctx, queueKey).Result(); n != 0 {
		t.Fatalf("未绑定会话不应入队，得到 %d 条", n)
	}
}

// ---- startMatch：瞬时故障不能把已经弹出队列的活人丢掉 ----

// startMatchTestApp 驱动 startMatch：servers 决定有没有可用 game 节点，
// bindErr/bindFound 决定 gate.bindgame 的结果，createErr 决定 game.create 的结果。
type startMatchTestApp struct {
	pitaya.Pitaya
	servers   map[string]*cluster.Server
	bindErr   error // 非 nil：bindgame 的传输错误（NATS 不通）
	bindFound bool  // bindgame 应答里的 found（false = 那个 gate 上已经没这个会话）
	createErr error // 非 nil：game.create 失败
	pushed    []string
}

func (a *startMatchTestApp) GetServersByType(string) (map[string]*cluster.Server, error) {
	if len(a.servers) == 0 {
		return nil, errors.New("no game server")
	}
	return a.servers, nil
}

func (a *startMatchTestApp) RPCTo(_ context.Context, _, route string, reply proto.Message, _ proto.Message) error {
	switch route {
	case bindGameRoute:
		if a.bindErr != nil {
			return a.bindErr
		}
		reply.(*protos.BindGameReply).Found = a.bindFound
		return nil
	case gameCreateRoute:
		return a.createErr
	}
	return nil
}

func (a *startMatchTestApp) SendPushToUsers(_ string, _ interface{}, uids []string, _ string) ([]string, error) {
	a.pushed = append(a.pushed, uids...)
	return nil, nil
}

// newStartMatchComponent 用**两个独立的 miniredis**：队列一个、在线登记一个。
// 这样才能只让在线登记报错（SetError 是整个实例级别的），同时队列还能正常收人 ——
// 「读登记失败就重新入队」的断言依赖队列本身是好的。
func newStartMatchComponent(t *testing.T, app *startMatchTestApp) (*Component, *miniredis.Miniredis) {
	t.Helper()
	qmr := miniredis.RunT(t)
	omr := miniredis.RunT(t)
	qrdb := redis.NewClient(&redis.Options{Addr: qmr.Addr()})
	ordb := redis.NewClient(&redis.Options{Addr: omr.Addr()})
	t.Cleanup(func() { _ = qrdb.Close(); _ = ordb.Close() })
	return New(app, NewQueue(qrdb), online.NewStore(ordb)), omr
}

// queued 返回队列里现有的 uid（升序）。
func queued(t *testing.T, c *Component) []string {
	t.Helper()
	got, err := c.queue.rdb.ZRange(context.Background(), queueKey, 0, -1).Result()
	if err != nil {
		t.Fatalf("ZRANGE 报错: %v", err)
	}
	sort.Strings(got)
	return got
}

func oneGameServer() map[string]*cluster.Server {
	return map[string]*cluster.Server{"g1": {ID: "g1", Type: gameServerType}}
}

// 读在线登记时 Redis 抖了一下：人还在线，不能当他掉线丢掉 —— 必须重新入队。
func TestStartMatchRequeuesOnOnlineLookupError(t *testing.T) {
	app := &startMatchTestApp{servers: oneGameServer(), bindFound: true}
	c, omr := newStartMatchComponent(t, app)
	omr.SetError("LOADING Redis is loading the dataset in memory")

	c.startMatch(context.Background(), []string{"a", "b"})

	if got := queued(t, c); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("读登记失败的玩家应回到队列，得到 %v", got)
	}
	if len(app.pushed) != 0 {
		t.Fatalf("没人进局，不该推 onMatched，得到 %v", app.pushed)
	}
}

// 在线登记里查无此人 = 他真的走了：丢掉，不重新入队（否则队列里会攒一堆幽灵）。
func TestStartMatchDropsGenuinelyOfflinePlayer(t *testing.T) {
	app := &startMatchTestApp{servers: oneGameServer(), bindFound: true}
	c, _ := newStartMatchComponent(t, app)

	c.startMatch(context.Background(), []string{"ghost"})

	if got := queued(t, c); len(got) != 0 {
		t.Fatalf("确实离线的人不该回到队列，得到 %v", got)
	}
}

// bindgame 的 RPC 本身不通（NATS 抖动）：人还在线，重新入队。
func TestStartMatchRequeuesOnBindTransportError(t *testing.T) {
	app := &startMatchTestApp{servers: oneGameServer(), bindErr: errors.New("nats: timeout")}
	c, _ := newStartMatchComponent(t, app)
	ctx := context.Background()
	if err := c.online.Set(ctx, "a", "gate-1"); err != nil {
		t.Fatalf("Set 报错: %v", err)
	}

	c.startMatch(ctx, []string{"a"})

	if got := queued(t, c); len(got) != 1 || got[0] != "a" {
		t.Fatalf("bind 传输失败的玩家应回到队列，得到 %v", got)
	}
}

// bindgame 通了但 found=false：那个 gate 上已经没有这个会话 —— 人真的走了，丢掉。
func TestStartMatchDropsWhenGateSaysPlayerGone(t *testing.T) {
	app := &startMatchTestApp{servers: oneGameServer(), bindFound: false}
	c, _ := newStartMatchComponent(t, app)
	ctx := context.Background()
	if err := c.online.Set(ctx, "a", "gate-1"); err != nil {
		t.Fatalf("Set 报错: %v", err)
	}

	c.startMatch(ctx, []string{"a"})

	if got := queued(t, c); len(got) != 0 {
		t.Fatalf("gate 说人没了就是真没了，不该回到队列，得到 %v", got)
	}
}

// 没有可用 game 节点：跟这些人在不在线无关，全部放回队列等下一轮。
func TestStartMatchRequeuesWhenNoGameServer(t *testing.T) {
	app := &startMatchTestApp{bindFound: true} // servers 为空
	c, _ := newStartMatchComponent(t, app)

	c.startMatch(context.Background(), []string{"a", "b"})

	if got := queued(t, c); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("没有 game 节点时应把人放回队列，得到 %v", got)
	}
}

// game.create 失败：这些人刚被探活过，回滚会话数据后重新入队。
func TestStartMatchRequeuesOnCreateGameFailure(t *testing.T) {
	app := &startMatchTestApp{
		servers:   oneGameServer(),
		bindFound: true,
		createErr: errors.New("game node down"),
	}
	c, _ := newStartMatchComponent(t, app)
	ctx := context.Background()
	for _, uid := range []string{"a", "b"} {
		if err := c.online.Set(ctx, uid, "gate-1"); err != nil {
			t.Fatalf("Set 报错: %v", err)
		}
	}

	c.startMatch(ctx, []string{"a", "b"})

	if got := queued(t, c); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("建局失败应把人放回队列，得到 %v", got)
	}
	if len(app.pushed) != 0 {
		t.Fatalf("建局失败不该推 onMatched，得到 %v", app.pushed)
	}
}

// 成功路径：两个人都进局，队列清空，各收到一次 onMatched。
func TestStartMatchSuccessDoesNotRequeue(t *testing.T) {
	app := &startMatchTestApp{servers: oneGameServer(), bindFound: true}
	c, _ := newStartMatchComponent(t, app)
	ctx := context.Background()
	for _, uid := range []string{"a", "b"} {
		if err := c.online.Set(ctx, uid, "gate-1"); err != nil {
			t.Fatalf("Set 报错: %v", err)
		}
	}

	c.startMatch(ctx, []string{"a", "b"})

	if got := queued(t, c); len(got) != 0 {
		t.Fatalf("开局成功后队列应为空，得到 %v", got)
	}
	sort.Strings(app.pushed)
	if len(app.pushed) != 2 || app.pushed[0] != "a" || app.pushed[1] != "b" {
		t.Fatalf("两人都该收到 onMatched，得到 %v", app.pushed)
	}
}

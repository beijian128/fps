package match

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/golang/protobuf/proto"
	"github.com/redis/go-redis/v9"
	"github.com/topfreegames/pitaya/v3/pkg/cluster"
	"github.com/topfreegames/pitaya/v3/pkg/session"
	"joltgo/bot"
	"joltgo/game/protos"
	"joltgo/online"
)

// botPairApp 在 startMatchTestApp 之上记录 bindgame 与 game.create 的完整入参，
// 用来断言「机器人没有被当成掉线真人剔除」与「槽位怎么分」。
//
// 复用既有 stub 而不是新造一套：startMatchTestApp 已经有 servers / pushed /
// GetServersByType / SendPushToUsers，这里只补「记录 RPC 参数」这一点。
type botPairApp struct {
	startMatchTestApp
	bound   []*protos.BindGameMsg
	created []*protos.CreateGameMsg
}

func (a *botPairApp) RPCTo(_ context.Context, _ string, routeStr string, reply proto.Message, arg proto.Message) error {
	switch routeStr {
	case bindGameRoute:
		a.bound = append(a.bound, proto.Clone(arg).(*protos.BindGameMsg))
		reply.(*protos.BindGameReply).Found = true
		return nil
	case gameCreateRoute:
		a.created = append(a.created, proto.Clone(arg).(*protos.CreateGameMsg))
		reply.(*protos.CreateGameReply).Code = 0
		return nil
	}
	return nil
}

func (a *botPairApp) boundUIDs() []string {
	out := make([]string, 0, len(a.bound))
	for _, m := range a.bound {
		out = append(out, m.Uid)
	}
	return out
}

// GetSessionFromCtx 显式返回 nil：本文件驱动的是**后端**路径。
// 不实现的话会提升到 startMatchTestApp 的版本，而它内嵌的 sess 是 nil，
// 调 UID() 会直接 panic（isClientCall 只判「有没有会话」）。
func (a *botPairApp) GetSessionFromCtx(context.Context) session.Session { return nil }

// newBotPairEnv 造一个「有 game 节点可分配」的 match 组件，配独立的队列 miniredis
// 与独立的在线登记 miniredis（后者用来把真人标成在线 / 离线）。
func newBotPairEnv(t *testing.T) (*Component, *botPairApp, *online.Store) {
	t.Helper()
	app := &botPairApp{}
	app.servers = map[string]*cluster.Server{"g1": {ID: "g1", Type: gameServerType}}
	comp, _, onl := newRawMatchEnv(t, app, "s3cret")
	return comp, app, onl
}

// newRawMatchEnv 与 newStartMatchComponent 同款，但分开返回组件与在线登记仓储，
// 并允许指定管理密钥。不复用 newStartMatchComponent 是因为它把 app 与 secret
// 都写死了（""），而本文件的用例需要一个有效密钥、并把 app 换成记录版 stub。
func newRawMatchEnv(t *testing.T, app *botPairApp, secret string) (*Component, *miniredis.Miniredis, *online.Store) {
	t.Helper()
	qmr := miniredis.RunT(t)
	omr := miniredis.RunT(t)
	qrdb := redis.NewClient(&redis.Options{Addr: qmr.Addr()})
	ordb := redis.NewClient(&redis.Options{Addr: omr.Addr()})
	t.Cleanup(func() { _ = qrdb.Close(); _ = ordb.Close() })
	onl := online.NewStore(ordb)
	comp := New(app, NewQueue(qrdb), onl, secret)
	comp.app = app
	return comp, omr, onl
}

// countBots 数一数槽位名单里有几个机器人。
func countBots(uids []string) int {
	n := 0
	for _, uid := range uids {
		if bot.Is(uid) {
			n++
		}
	}
	return n
}

// 机器人不需要在线登记就能开局；真人照旧要探活。
func TestStartMatchRequiresHumanOnlineButNotBot(t *testing.T) {
	c, app, onl := newBotPairEnv(t)
	ctx := context.Background()

	if _, err := c.AddBots(ctx, &protos.AddBotsMsg{Count: 1, AdminKey: "s3cret"}); err != nil {
		t.Fatal(err)
	}
	// 先让配对跑一次：此时队列里只有一个机器人，凑不满两人，什么都不该发生。
	c.tryMatch(ctx)
	if len(app.created) != 0 {
		t.Fatalf("只有一个机器人时不该建局，实际 %d 局", len(app.created))
	}

	// 一个真人上线并排队 -> 与排队中的机器人配成一对。
	if err := onl.Set(ctx, "10001", "gate-1"); err != nil {
		t.Fatal(err)
	}
	if err := c.queue.Enqueue(ctx, "10001"); err != nil {
		t.Fatal(err)
	}
	c.tryMatch(ctx)

	if len(app.created) != 1 {
		t.Fatalf("真人在线时应与机器人配成一局，实际 %d 局", len(app.created))
	}
	uids := app.created[0].Uids
	if len(uids) != 2 || countBots(uids) != 1 {
		t.Fatalf("应有一个机器人一个真人，得到 %v", uids)
	}
	if bound := app.boundUIDs(); len(bound) != 1 || bound[0] != "10001" {
		t.Fatalf("只有真人该收到 bindgame，得到 %v", bound)
	}
}

func TestStartMatchDropsOfflineHumanAndKeepsBotQueued(t *testing.T) {
	c, app, _ := newBotPairEnv(t)
	ctx := context.Background()

	if _, err := c.AddBots(ctx, &protos.AddBotsMsg{Count: 1, AdminKey: "s3cret"}); err != nil {
		t.Fatal(err)
	}
	// 这个真人没有在线登记 = 已经掉线。
	if err := c.queue.Enqueue(ctx, "10001"); err != nil {
		t.Fatal(err)
	}
	c.tryMatch(ctx)

	if len(app.created) != 0 {
		t.Fatalf("真人掉线时不该建局，实际 %d 局", len(app.created))
	}
	if got := c.queue.queueSize(ctx, t); got != 1 {
		t.Fatalf("机器人应留在队列里等下一个真人，实际 %d 人", got)
	}
}

func TestTryMatchDiscardsAllBotPair(t *testing.T) {
	c, app, _ := newBotPairEnv(t)
	ctx := context.Background()

	reply, err := c.AddBots(ctx, &protos.AddBotsMsg{Count: 2, AdminKey: "s3cret"})
	if err != nil || !reply.Ok || reply.Enqueued != 2 {
		t.Fatalf("准备机器人失败: reply=%+v err=%v", reply, err)
	}
	// QueueBots 收尾就会试一次配对 —— 「清空队列」在它返回时已经发生，
	// 不需要（也不该）再手动 tryMatch 一次。

	if len(app.created) != 0 {
		t.Fatalf("全机器人配对不该建局，实际 %d 局", len(app.created))
	}
	if got := c.queue.queueSize(ctx, t); got != 0 {
		t.Fatalf("被丢弃的一对不该放回队列（放回会让它们反复被弹出），实际 %d 人", got)
	}
}

func TestPushMatchStatusSkipsBotsButCountsThem(t *testing.T) {
	c, app, _ := newBotPairEnv(t)
	ctx := context.Background()

	if _, err := c.AddBots(ctx, &protos.AddBotsMsg{Count: 1, AdminKey: "s3cret"}); err != nil {
		t.Fatal(err)
	}
	if err := c.queue.Enqueue(ctx, "10001"); err != nil {
		t.Fatal(err)
	}

	c.pushMatchStatus(ctx)

	if len(app.pushed) != 1 || app.pushed[0] != "10001" {
		t.Fatalf("只该给真人推状态，得到 %v", app.pushed)
	}
}

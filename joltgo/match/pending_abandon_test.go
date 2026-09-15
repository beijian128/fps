package match

// 「进大厅时问一句」与「放弃那场没打完的局」的服务端测试。
//
// 两条语义是这组测试的重点：
//   - Pending 是**纯查询**：命中存量对局只回 found/match_id，绝不写会话数据、不推
//     onMatched、不入队 —— 它只是决定客户端要不要弹询问框；
//   - Abandon 是**释放**：请 game 节点把玩家摘出来（route game.game.leave），实例
//     与对手那一局都不受影响。

import (
	"context"
	"errors"
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

// pendingAbandonApp 驱动 Pending / Abandon：既能当「后端 RPC 调用方」，
// 也记录它打出去的 RPC 与推送，供断言核对。
type pendingAbandonApp struct {
	pitaya.Pitaya
	sess session.Session

	rejoinFound   bool
	rejoinMatchID string
	leaveOK       bool
	leaveErr      error
	bindFound     bool

	rpcRoutes []string
	leaveUIDs []string
	bindMsgs  []*protos.BindGameMsg
	pushed    []string
}

func (a *pendingAbandonApp) GetSessionFromCtx(context.Context) session.Session { return a.sess }

func (a *pendingAbandonApp) GetServersByType(string) (map[string]*cluster.Server, error) {
	return map[string]*cluster.Server{"g1": {ID: "g1", Type: gameServerType}}, nil
}

func (a *pendingAbandonApp) RPCTo(_ context.Context, _ string, route string, reply proto.Message, arg proto.Message) error {
	a.rpcRoutes = append(a.rpcRoutes, route)
	switch route {
	case gameRejoinRoute:
		msg := reply.(*protos.RejoinReply)
		msg.Found = a.rejoinFound
		msg.MatchId = a.rejoinMatchID
		msg.PlayerIdx = 1
		return nil
	case gameLeaveRoute:
		a.leaveUIDs = append(a.leaveUIDs, arg.(*protos.LeaveMsg).Uid)
		if a.leaveErr != nil {
			return a.leaveErr
		}
		reply.(*protos.LeaveReply).Ok = a.leaveOK
		return nil
	case bindGameRoute:
		a.bindMsgs = append(a.bindMsgs, arg.(*protos.BindGameMsg))
		reply.(*protos.BindGameReply).Found = a.bindFound
		return nil
	}
	return nil
}

func (a *pendingAbandonApp) SendPushToUsers(_ string, _ interface{}, uids []string, _ string) ([]string, error) {
	a.pushed = append(a.pushed, uids...)
	return nil, nil
}

func newPendingAbandonComponent(t *testing.T, app *pendingAbandonApp) (*Component, *redis.Client) {
	t.Helper()
	qmr := miniredis.RunT(t)
	omr := miniredis.RunT(t)
	qrdb := redis.NewClient(&redis.Options{Addr: qmr.Addr()})
	ordb := redis.NewClient(&redis.Options{Addr: omr.Addr()})
	t.Cleanup(func() { _ = qrdb.Close(); _ = ordb.Close() })
	return New(app, NewQueue(qrdb), online.NewStore(ordb)), ordb
}

// 命中存量对局：只回 match_id，一个字节的副作用都不许有。
func TestPendingReportsExistingMatchWithoutSideEffects(t *testing.T) {
	app := &pendingAbandonApp{
		sess:          &joinTestSession{uid: "7"},
		rejoinFound:   true,
		rejoinMatchID: "m1",
	}
	c, _ := newPendingAbandonComponent(t, app)

	reply, err := c.Pending(context.Background(), &protos.PendingMatchMsg{})
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if !reply.Found || reply.MatchId != "m1" {
		t.Fatalf("应报告存量对局 m1，得到 %+v", reply)
	}
	if len(app.pushed) != 0 {
		t.Fatalf("查询不该推 onMatched（询问框弹出前人还在大厅）: %v", app.pushed)
	}
	if len(app.rpcRoutes) != 1 || app.rpcRoutes[0] != gameRejoinRoute {
		t.Fatalf("查询只该走一次 rejoin 探测，得到 %v", app.rpcRoutes)
	}
	if n, _ := c.queue.rdb.ZCard(context.Background(), queueKey).Result(); n != 0 {
		t.Fatalf("查询不该把人塞进队列，得到 %d 条", n)
	}
}

// 没有存量对局：found=false（大厅照常显示，不弹框）。
func TestPendingReportsNoMatch(t *testing.T) {
	app := &pendingAbandonApp{sess: &joinTestSession{uid: "7"}}
	c, _ := newPendingAbandonComponent(t, app)

	reply, err := c.Pending(context.Background(), &protos.PendingMatchMsg{})
	if err != nil || reply.Found {
		t.Fatalf("没有存量对局时应回 found=false，得到 %+v err=%v", reply, err)
	}
}

// 未登录（会话未绑定）的查询：回 found=false，且不去打扰 game 节点。
func TestPendingRejectsUnboundSession(t *testing.T) {
	app := &pendingAbandonApp{sess: &joinTestSession{uid: ""}}
	c, _ := newPendingAbandonComponent(t, app)

	reply, err := c.Pending(context.Background(), &protos.PendingMatchMsg{})
	if err != nil || reply.Found {
		t.Fatalf("未绑定会话应回 found=false，得到 %+v err=%v", reply, err)
	}
	if len(app.rpcRoutes) != 0 {
		t.Fatalf("未绑定会话不该发起探测，得到 %v", app.rpcRoutes)
	}
}

// 放弃对局：把玩家交给 game 节点释放，并把会话里那份对局归属清掉。
func TestAbandonReleasesAndClearsSessionBinding(t *testing.T) {
	app := &pendingAbandonApp{
		sess:          &joinTestSession{uid: "7"},
		rejoinFound:   true,
		rejoinMatchID: "m1",
		leaveOK:       true,
		bindFound:     true,
	}
	c, _ := newPendingAbandonComponent(t, app)
	ctx := context.Background()
	if err := c.online.Set(ctx, "7", "gate-1"); err != nil {
		t.Fatalf("online.Set: %v", err)
	}

	reply, err := c.Abandon(ctx, &protos.AbandonMatchMsg{})
	if err != nil {
		t.Fatalf("Abandon: %v", err)
	}
	if !reply.Ok || reply.Reason != "released" {
		t.Fatalf("放弃应成功（reason=released），得到 %+v", reply)
	}
	if len(app.leaveUIDs) != 1 || app.leaveUIDs[0] != "7" {
		t.Fatalf("应请 game 释放 uid 7，得到 %v", app.leaveUIDs)
	}
	if len(app.bindMsgs) != 1 {
		t.Fatalf("应清一次会话归属，得到 %d 次", len(app.bindMsgs))
	}
	if got := app.bindMsgs[0]; got.Uid != "7" || got.GameServerId != "" || got.MatchId != "" {
		t.Fatalf("清归属应写空 game_server_id/match_id，得到 %+v", got)
	}
	if len(app.pushed) != 0 {
		t.Fatalf("放弃对局不该推任何东西给玩家: %v", app.pushed)
	}
}

// 此刻已经查不到他的存量对局（刚好打完 / 已经释放过）：ok=false + not_found，
// 客户端据此保留询问框并提示，而不是假装成功。
func TestAbandonReportsNotFoundWhenNoInstance(t *testing.T) {
	app := &pendingAbandonApp{sess: &joinTestSession{uid: "7"}}
	c, _ := newPendingAbandonComponent(t, app)

	reply, err := c.Abandon(context.Background(), &protos.AbandonMatchMsg{})
	if err != nil {
		t.Fatalf("Abandon 不该报错: %v", err)
	}
	if reply.Ok || reply.Reason != "not_found" {
		t.Fatalf("没有存量对局应回 not_found，得到 %+v", reply)
	}
	if len(app.leaveUIDs) != 0 {
		t.Fatalf("没命中就不该请求释放，得到 %v", app.leaveUIDs)
	}
}

// 释放 RPC 打不通（NATS 抖动）：ok=false + internal，玩家可以重试。
func TestAbandonReportsInternalWhenLeaveRPCFails(t *testing.T) {
	app := &pendingAbandonApp{
		sess:        &joinTestSession{uid: "7"},
		rejoinFound: true,
		leaveErr:    errors.New("nats: timeout"),
	}
	c, _ := newPendingAbandonComponent(t, app)

	reply, err := c.Abandon(context.Background(), &protos.AbandonMatchMsg{})
	if err != nil {
		t.Fatalf("Abandon 不该把传输错误抛给调用方: %v", err)
	}
	if reply.Ok || reply.Reason != "internal" {
		t.Fatalf("释放失败应回 internal，得到 %+v", reply)
	}
}

// game 节点答 ok=false（它此刻已经没有这个实例了）：等同 not_found。
func TestAbandonReportsNotFoundWhenGameHasNoInstance(t *testing.T) {
	app := &pendingAbandonApp{
		sess:        &joinTestSession{uid: "7"},
		rejoinFound: true,
		leaveOK:     false,
	}
	c, _ := newPendingAbandonComponent(t, app)

	reply, err := c.Abandon(context.Background(), &protos.AbandonMatchMsg{})
	if err != nil {
		t.Fatalf("Abandon 不该报错: %v", err)
	}
	if reply.Ok || reply.Reason != "not_found" {
		t.Fatalf("game 侧没有实例应回 not_found，得到 %+v", reply)
	}
}

// 未登录会话请求放弃：unauthenticated（没有 uid 就无从谈起「你的哪一局」）。
func TestAbandonRejectsUnboundSession(t *testing.T) {
	app := &pendingAbandonApp{sess: &joinTestSession{uid: ""}}
	c, _ := newPendingAbandonComponent(t, app)

	reply, err := c.Abandon(context.Background(), &protos.AbandonMatchMsg{})
	if err != nil {
		t.Fatalf("Abandon 不该报错: %v", err)
	}
	if reply.Ok || reply.Reason != "unauthenticated" {
		t.Fatalf("未绑定会话应回 unauthenticated，得到 %+v", reply)
	}
}

// 清会话归属是「最好努力」：它失败（那个 gate 上已经没这个会话了）也必须回
// released —— 玩家确实已经被释放，不能因为一次清归属失败就骗他说没成功。
func TestAbandonSucceedsEvenIfBindingClearFails(t *testing.T) {
	app := &pendingAbandonApp{
		sess:        &joinTestSession{uid: "7"},
		rejoinFound: true,
		leaveOK:     true,
		bindFound:   false, // 那个 gate 上已经没有这个会话
	}
	c, _ := newPendingAbandonComponent(t, app)
	ctx := context.Background()
	if err := c.online.Set(ctx, "7", "gate-1"); err != nil {
		t.Fatalf("online.Set: %v", err)
	}

	reply, err := c.Abandon(ctx, &protos.AbandonMatchMsg{})
	if err != nil || !reply.Ok || reply.Reason != "released" {
		t.Fatalf("清归属失败不该改变结论，得到 %+v err=%v", reply, err)
	}
	if len(app.bindMsgs) != 1 {
		t.Fatalf("仍然尝试清了一次归属，得到 %d 次", len(app.bindMsgs))
	}
}

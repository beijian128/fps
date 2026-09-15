package match

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/cluster"
	"github.com/topfreegames/pitaya/v3/pkg/session"
	"joltgo/game/protos"
	"joltgo/online"
)

// pushRecordingApp 记录推送；Join/Cancel 用不到的 pitaya 能力不实现。
type pushRecordingApp struct {
	pitaya.Pitaya
	sess   session.Session
	mu     sync.Mutex
	routes []string
	args   []interface{}
	uids   [][]string
}

func (a *pushRecordingApp) GetSessionFromCtx(context.Context) session.Session { return a.sess }

func (a *pushRecordingApp) GetServersByType(string) (map[string]*cluster.Server, error) {
	return nil, errors.New("no game server") // 回局查询必然 miss
}

func (a *pushRecordingApp) SendPushToUsers(route string, v interface{}, uids []string, frontendType string) ([]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.routes = append(a.routes, route)
	a.args = append(a.args, v)
	a.uids = append(a.uids, uids)
	return nil, nil
}

func newPushTestComponent(t *testing.T, sess session.Session) (*Component, *pushRecordingApp) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	app := &pushRecordingApp{sess: sess}
	return New(app, NewQueue(rdb), online.NewStore(rdb)), app
}

func TestCancelRemovesFromQueue(t *testing.T) {
	c, _ := newPushTestComponent(t, &joinTestSession{uid: "7"})
	ctx := context.Background()
	if err := c.queue.Enqueue(ctx, "7"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	reply, err := c.Cancel(ctx, &protos.MatchCancelMsg{})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if !reply.Ok || reply.Reason != "cancelled" {
		t.Fatalf("取消应答不对: %+v", reply)
	}
	if n := c.queue.queueSize(ctx, t); n != 0 {
		t.Fatalf("取消后队列应清空，得到 %d", n)
	}
}

func TestCancelReportsNotQueued(t *testing.T) {
	c, _ := newPushTestComponent(t, &joinTestSession{uid: "7"})

	reply, err := c.Cancel(context.Background(), &protos.MatchCancelMsg{})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if reply.Ok || reply.Reason != "not_queued" {
		t.Fatalf("不在队列时应返回 not_queued（ok=false），得到 %+v", reply)
	}
}

func TestCancelRejectsUnboundSession(t *testing.T) {
	c, _ := newPushTestComponent(t, &joinTestSession{uid: ""})

	reply, err := c.Cancel(context.Background(), &protos.MatchCancelMsg{})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if reply.Ok || reply.Reason != "unauthenticated" {
		t.Fatalf("未绑定会话应返回 unauthenticated，得到 %+v", reply)
	}
}

// 匹配状态推送：队列里每人一条，载荷是总人数与自己已等待的秒数。
//
// 等待时长直接由 score 播种（不能用 miniredis 的 FastForward —— 队列的 TIME 取真实
// 墙钟，见 queue_test.go 的说明）。
func TestPushMatchStatusCoversQueue(t *testing.T) {
	c, app := newPushTestComponent(t, nil)
	ctx := context.Background()

	now, err := c.queue.nowMs(ctx, t)
	if err != nil {
		t.Fatalf("nowMs: %v", err)
	}
	if err := c.queue.rdb.ZAdd(ctx, queueKey,
		redis.Z{Score: float64(now - 6000), Member: "7"},
		redis.Z{Score: float64(now - 2000), Member: "8"},
	).Err(); err != nil {
		t.Fatalf("ZAdd: %v", err)
	}

	c.pushMatchStatus(ctx)

	if len(app.routes) != 2 {
		t.Fatalf("队列里 2 人应推 2 条，得到 %d", len(app.routes))
	}
	for i, route := range app.routes {
		if route != statusRoute {
			t.Fatalf("第 %d 条 route = %s，期望 %s", i, route, statusRoute)
		}
		status, ok := app.args[i].(*protos.MatchStatus)
		if !ok {
			t.Fatalf("第 %d 条载荷类型不对: %T", i, app.args[i])
		}
		if status.QueuedPlayers != 2 {
			t.Fatalf("第 %d 条队列人数 = %d，期望 2", i, status.QueuedPlayers)
		}
	}
	first := app.args[0].(*protos.MatchStatus)
	second := app.args[1].(*protos.MatchStatus)
	if first.WaitedSeconds != 6 || second.WaitedSeconds != 2 {
		t.Fatalf("等待时长不对: 第一条=%d 第二条=%d", first.WaitedSeconds, second.WaitedSeconds)
	}
	if app.uids[0][0] != "7" || app.uids[1][0] != "8" {
		t.Fatalf("推送目标不对: %v / %v", app.uids[0], app.uids[1])
	}
}

// 空队列不该产生任何推送。
func TestPushMatchStatusSkipsEmptyQueue(t *testing.T) {
	c, app := newPushTestComponent(t, nil)

	c.pushMatchStatus(context.Background())

	if len(app.routes) != 0 {
		t.Fatalf("空队列不应推送，得到 %v", app.routes)
	}
}

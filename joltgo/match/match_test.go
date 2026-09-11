package match

import (
	"context"
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
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

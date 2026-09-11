package match

import (
	"context"
	"errors"
	"testing"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/cluster"
	"github.com/topfreegames/pitaya/v3/pkg/session"
	"joltgo/game/protos"
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

// 排队期间断线重连会带着同一个 token 再 Join 一次，队列里必须只留最新那条。
func TestRemoveQueued(t *testing.T) {
	q := []queuedPlayer{{uid: "a"}, {uid: "b"}, {uid: "a"}}
	got := removeQueued(q, "a")
	if len(got) != 1 || got[0].uid != "b" {
		t.Fatalf("应只留下 b，得到 %+v", got)
	}
	if len(q) != 3 || q[0].uid != "a" {
		t.Fatalf("removeQueued 不应改动入参，得到 %+v", q)
	}
	if got := removeQueued(q, "zzz"); len(got) != 3 {
		t.Fatalf("没有匹配项时队列应原样返回，得到 %+v", got)
	}
}

// Component 只需要 pitaya.Pitaya 接口，所以「嵌入接口 + 覆盖用得到的两个方法」
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

// 排队等待期间断线重连：同一个 uid 再 Join 一次，队列里必须还是只有一条
// （不去重的话这里会是 2 条，进而可能自己跟自己配对、或单人兜底时开两局）。
func TestJoinDedupsQueuedUid(t *testing.T) {
	c := New(&joinTestApp{sess: &joinTestSession{uid: "T"}})
	ctx := context.Background()

	c.Join(ctx, &protos.JoinMsg{})
	if len(c.queue) != 1 {
		t.Fatalf("首次 Join 应入队一条，得到 %d", len(c.queue))
	}
	c.Join(ctx, &protos.JoinMsg{})
	if len(c.queue) != 1 {
		t.Fatalf("同 uid 重连不应重复入队，得到 %d", len(c.queue))
	}
	if c.queue[0].uid != "T" {
		t.Fatalf("队列里应是最新那条会话，得到 %q", c.queue[0].uid)
	}
}

// 未登录（会话未绑定）的 Join 必须被忽略：不 Bind、不入队。
func TestJoinRejectsUnboundSession(t *testing.T) {
	c := New(&joinTestApp{sess: &joinTestSession{uid: ""}})
	c.Join(context.Background(), &protos.JoinMsg{})
	if len(c.queue) != 0 {
		t.Fatalf("未绑定会话不应入队，得到 %d 条", len(c.queue))
	}
}

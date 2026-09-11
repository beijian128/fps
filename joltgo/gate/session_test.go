package gate

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/session"
	"joltgo/game/protos"
	"joltgo/online"
)

// fakeSession 嵌入 session.Session，只覆盖组件用到的部分。
type fakeSession struct {
	session.Session
	id         int64
	uid        string
	isFrontend bool
	data       map[string]interface{}
}

func (s *fakeSession) ID() int64           { return s.id }
func (s *fakeSession) UID() string         { return s.uid }
func (s *fakeSession) GetIsFrontend() bool { return s.isFrontend }
func (s *fakeSession) Set(k string, v interface{}) error {
	if s.data == nil {
		s.data = map[string]interface{}{}
	}
	s.data[k] = v
	return nil
}

// fakePool 只实现 GetSessionByUID。
type fakePool struct {
	session.SessionPool
	byUID map[string]session.Session
}

func (p *fakePool) GetSessionByUID(uid string) session.Session { return p.byUID[uid] }

// fakeApp 只实现 GetSessionFromCtx。
type fakeApp struct {
	pitaya.Pitaya
	sess session.Session
}

func (a *fakeApp) GetSessionFromCtx(context.Context) session.Session { return a.sess }

func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// 客户端直接发 gate.gate.bindgame 时必须被拒：否则任何人都能把自己的会话绑到
// 任意 game 节点，绕过匹配直接对着别人的对局发命令。
func TestBindGameRejectsClientSession(t *testing.T) {
	target := &fakeSession{uid: "1", data: map[string]interface{}{}}
	pool := &fakePool{byUID: map[string]session.Session{"1": target}}
	app := &fakeApp{sess: &fakeSession{uid: "1", isFrontend: true}} // 客户端会话
	c := NewSessionComponent(app, pool)

	reply, err := c.BindGame(context.Background(), &protos.BindGameMsg{Uid: "1", GameServerId: "g-1"})
	if err != nil {
		t.Fatalf("不该返回 Go error: %v", err)
	}
	if reply.Found {
		t.Fatal("客户端会话发来的 bindgame 必须被拒绝")
	}
	if _, ok := target.data["gameServerId"]; ok {
		t.Fatal("被拒的请求不能改动会话数据")
	}
}

// 后端 RPC（会话是 Remote，IsFrontend=false）应正常写入。
func TestBindGameFromBackendWritesSessionData(t *testing.T) {
	target := &fakeSession{uid: "1"}
	pool := &fakePool{byUID: map[string]session.Session{"1": target}}
	app := &fakeApp{sess: &fakeSession{uid: "remote", isFrontend: false}}
	c := NewSessionComponent(app, pool)

	reply, err := c.BindGame(context.Background(), &protos.BindGameMsg{
		Uid: "1", GameServerId: "g-1", MatchId: "m-1", PlayerIdx: 1,
	})
	if err != nil || !reply.Found {
		t.Fatalf("后端请求应成功，得到 %+v err=%v", reply, err)
	}
	if got := target.data["gameServerId"]; got != "g-1" {
		t.Fatalf("会话数据应写入 g-1，得到 %v", got)
	}
}

// 玩家已不在本 gate 上（掉线）时 found=false，调用方据此剔除。
func TestBindGameNotFound(t *testing.T) {
	pool := &fakePool{byUID: map[string]session.Session{}}
	app := &fakeApp{sess: &fakeSession{isFrontend: false}}
	c := NewSessionComponent(app, pool)

	reply, err := c.BindGame(context.Background(), &protos.BindGameMsg{Uid: "gone", GameServerId: "g-1"})
	if err != nil {
		t.Fatalf("不该返回 Go error: %v", err)
	}
	if reply.Found {
		t.Fatal("本 gate 没有该会话时应回 found=false")
	}
}

// 回滚：game_server_id 为空表示清掉归属。
func TestBindGameRollbackClearsGameServer(t *testing.T) {
	target := &fakeSession{uid: "1", data: map[string]interface{}{"gameServerId": "g-1"}}
	pool := &fakePool{byUID: map[string]session.Session{"1": target}}
	app := &fakeApp{sess: &fakeSession{isFrontend: false}}
	c := NewSessionComponent(app, pool)

	if _, err := c.BindGame(context.Background(), &protos.BindGameMsg{Uid: "1"}); err != nil {
		t.Fatalf("回滚不该报错: %v", err)
	}
	if got := target.data["gameServerId"]; got != "" {
		t.Fatalf("回滚应清空，得到 %v", got)
	}
}

func TestMarkOnline(t *testing.T) {
	rdb := newTestRedis(t)
	onl := online.NewStore(rdb)
	ctx := context.Background()
	pool := &fakePool{byUID: map[string]session.Session{}}

	markOnline(ctx, &fakeSession{id: 1, uid: "7"}, "gate-A", onl)
	if got, _ := onl.Gate(ctx, "7"); got != "gate-A" {
		t.Fatalf("应登记到 gate-A，得到 %q", got)
	}

	// 未绑定的会话（uid 为空）不该写登记。
	markOnline(ctx, &fakeSession{id: 2, uid: ""}, "gate-A", onl)
	if got, _ := onl.Gate(ctx, ""); got != "" {
		t.Fatalf("空 uid 不该登记，得到 %q", got)
	}

	clearOnline(pool, &fakeSession{id: 1, uid: "7"}, "gate-A", onl)
	if got, _ := onl.Gate(ctx, "7"); got != "" {
		t.Fatalf("清除后应为空，得到 %q", got)
	}
}

// 登记写失败不能 panic、也不能阻塞（best-effort）。
func TestMarkOnlineSurvivesRedisFailure(t *testing.T) {
	rdb := newTestRedis(t)
	onl := online.NewStore(rdb)
	pool := &fakePool{byUID: map[string]session.Session{}}
	markOnline(context.Background(), &fakeSession{id: 1, uid: "7"}, "gate-A", onl)
	// 关掉 redis 再写一次：只应记日志。
	_ = rdb.Close()
	markOnline(context.Background(), &fakeSession{id: 2, uid: "8"}, "gate-A", onl)
	// 清除同理：读失败时保守地什么都不做，不能 panic。
	clearOnline(pool, &fakeSession{id: 1, uid: "7"}, "gate-A", onl)
}

// 顶号竞态：同一个 gate 上，新连接已经绑定并写好登记之后，旧连接的关闭钩子
// 才姗姗来迟（旧 agent 的读循环 / 心跳超时在另一个 goroutine 里收尾）。此时
// 若无脑 Clear，就会把新会话的登记抹掉 —— 紧接着 account.finishLogin 的第 4 步
// 读到空归属，判定「旧 gate != 当前 gate」，一脚踢掉刚建立的那条连接。
func TestClearOnlineKeepsEntryClaimedByNewerSession(t *testing.T) {
	rdb := newTestRedis(t)
	onl := online.NewStore(rdb)
	ctx := context.Background()

	newer := &fakeSession{id: 2, uid: "7"}
	pool := &fakePool{byUID: map[string]session.Session{"7": newer}}
	markOnline(ctx, newer, "gate-A", onl)

	// 旧会话（同 uid、不同 session id）现在才关闭。
	clearOnline(pool, &fakeSession{id: 1, uid: "7"}, "gate-A", onl)

	if got, _ := onl.Gate(ctx, "7"); got != "gate-A" {
		t.Fatalf("新会话已认领该 uid，旧会话的关闭钩子不该清登记，得到 %q", got)
	}
}

// 换节点重连：玩家已经连到 gate-B 并登记，gate-A 上的旧会话才关闭。
// gate-A 不能清掉指向 gate-B 的登记。
func TestClearOnlineKeepsEntryOnAnotherGate(t *testing.T) {
	rdb := newTestRedis(t)
	onl := online.NewStore(rdb)
	ctx := context.Background()

	// gate-A 的池里已经没有这个 uid 了，但登记已经指向 gate-B。
	pool := &fakePool{byUID: map[string]session.Session{}}
	markOnline(ctx, &fakeSession{id: 2, uid: "7"}, "gate-B", onl)

	clearOnline(pool, &fakeSession{id: 1, uid: "7"}, "gate-A", onl)

	if got, _ := onl.Gate(ctx, "7"); got != "gate-B" {
		t.Fatalf("登记指向别的 gate 时不该清，得到 %q", got)
	}
}

// 同一个会话自己关闭（池里已经没有它了、登记还指向本节点）时必须真的清掉，
// 否则守卫就退化成「永不清理」。
func TestClearOnlineClearsOwnEntry(t *testing.T) {
	rdb := newTestRedis(t)
	onl := online.NewStore(rdb)
	ctx := context.Background()

	s := &fakeSession{id: 1, uid: "7"}
	// 池里仍然是它自己（会话池的删除与关闭钩子的先后不做假设）。
	pool := &fakePool{byUID: map[string]session.Session{"7": s}}
	markOnline(ctx, s, "gate-A", onl)

	clearOnline(pool, s, "gate-A", onl)

	if got, _ := onl.Gate(ctx, "7"); got != "" {
		t.Fatalf("自己的登记应被清掉，得到 %q", got)
	}
}

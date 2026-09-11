package game

import (
	"context"
	"strings"
	"testing"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/agent"
	pitayaprotos "github.com/topfreegames/pitaya/v3/pkg/protos"
	"github.com/topfreegames/pitaya/v3/pkg/session"
	"joltgo/game/protos"
	"joltgo/sim"
)

func TestForgetKeepsUIDsOwnedByAnotherInstance(t *testing.T) {
	c := New(nil)
	old := &Instance{matchID: "m1"}
	fresh := &Instance{matchID: "m2"}
	c.instances = map[string]*Instance{"m1": old, "m2": fresh}
	c.uidToInst = map[string]*Instance{"u": fresh} // 玩家已经匹配进新对局
	c.uidToIndex = map[string]int{"u": 1}

	c.forget(old, "m1", []string{"u"})

	if c.uidToInst["u"] != fresh {
		t.Fatal("旧实例回收不应抹掉新对局的 uid 映射")
	}
	if c.uidToIndex["u"] != 1 {
		t.Fatal("旧实例回收不应抹掉新对局的槽位映射（否则会把错误的玩家当成调用者）")
	}
	if _, ok := c.instances["m1"]; ok {
		t.Fatal("旧实例应从 instances 里摘掉")
	}
	if _, ok := c.instances["m2"]; !ok {
		t.Fatal("新实例不应受影响")
	}
}

// 正常情况（uid 仍归本实例）必须照常清掉，否则注册表会泄漏。
func TestForgetRemovesOwnUIDs(t *testing.T) {
	c := New(nil)
	inst := &Instance{matchID: "m1"}
	c.instances = map[string]*Instance{"m1": inst}
	c.uidToInst = map[string]*Instance{"u": inst}
	c.uidToIndex = map[string]int{"u": 0}

	c.forget(inst, "m1", []string{"u"})

	if _, ok := c.uidToInst["u"]; ok {
		t.Fatal("属于本实例的 uid 应被摘掉")
	}
	if _, ok := c.uidToIndex["u"]; ok {
		t.Fatal("属于本实例的 uid 索引应被摘掉")
	}
}

// fakeApp 只实现组件用到的部分：GetSessionFromCtx（守卫与 lookup 都靠它）与
// SendPushToUsers（实例每 tick 推帧；不实现的话会打到嵌入的 nil 接口上 panic）。
type fakeApp struct {
	pitaya.Pitaya
	sess session.Session
}

func (a *fakeApp) GetSessionFromCtx(context.Context) session.Session { return a.sess }

func (a *fakeApp) SendPushToUsers(string, interface{}, []string, string) ([]string, error) {
	return nil, nil
}

// clientSession 造出**客户端消息在本节点上真正的样子**。
//
// 客户端发 game.game.create 时不是直接到本节点的：gate 按 route 把它转成
// RPCType_Sys 转发过来（service/handler.go 的 remoteProcess），本节点由
// handleRPCSys 处理，而它交给 handler 的是 `agent.NewRemote(...)` 建出来的会话
// —— 也就是 sessionPool.NewSession(a, false, sess.GetUid())。所以这里直接调
// agent.NewRemote，而不是自己编一个 fakeSession，否则测试会把「这个会话长什么样」
// 的假设固化下来，而不是验证真实框架的行为（IsFrontend 恒为 false 就是这么来的）。
func clientSession(t *testing.T) session.Session {
	t.Helper()
	pool := session.NewSessionPool()
	a, err := agent.NewRemote(&pitayaprotos.Session{Uid: "13", Id: 7},
		"", nil, nil, nil, nil, "gate-id", nil, pool)
	if err != nil {
		t.Fatalf("agent.NewRemote 报错（测试自身失效）: %v", err)
	}
	if a.Session.GetIsFrontend() {
		t.Fatal("铺垫失效：Sys RPC 建的 Remote 会话本应是 IsFrontend=false，" +
			"若框架改成 true，本包的守卫判据要重新评估")
	}
	return a.Session
}

// 客户端直接发 game.game.create 必须被拒，且**不能留下任何实例**。
//
// 这是被实测利用过的洞：Create 用任意 uids 建实例就会覆盖 uidToInst/uidToIndex，
// 把受害者的 game.cmd 路由进攻击者的实例（他同时收到两路帧流），而账号 ID 是
// 连续十进制、枚举成本为零；每次调用还会真的建一个 Jolt 世界，没有限流。
func TestCreateRejectsClientSession(t *testing.T) {
	app := &fakeApp{sess: clientSession(t)}
	c := New(app)

	reply, err := c.Create(context.Background(), &protos.CreateGameMsg{
		MatchId: "probe-rogue-match", Uids: []string{"999998", "999999"},
	})
	if err == nil {
		t.Fatal("客户端发来的 create 必须报错 —— match 只看 RPC 是否报错，nil error 等于放行")
	}
	if reply == nil || reply.Code == 0 {
		t.Fatalf("被拒的 create 不能回 Code=0（成功），得到 %+v", reply)
	}
	if len(c.instances) != 0 || len(c.uidToInst) != 0 {
		t.Fatalf("被拒的 create 不能建实例或写 uid 映射，得到 instances=%v uidToInst=%v",
			c.instances, c.uidToInst)
	}
}

// 客户端直接发 game.game.rejoin 也必须被拒 —— 否则可以拿 uid 探测「谁在哪局、
// 坐哪个槽位」，被顶号/换局的玩家还会被领回旧实例。
func TestRejoinRejectsClientSession(t *testing.T) {
	app := &fakeApp{sess: clientSession(t)}
	c := New(app)
	inst := &Instance{matchID: "m1"}
	c.instances = map[string]*Instance{"m1": inst}
	c.uidToInst = map[string]*Instance{"1": inst}
	c.uidToIndex = map[string]int{"1": 1}

	reply, err := c.Rejoin(context.Background(), &protos.RejoinMsg{Token: "1"})
	if err != nil {
		t.Fatalf("查询类接口被拒时不该报 Go error（与 gate.bindgame 一致）: %v", err)
	}
	if reply.Found {
		t.Fatal("客户端发来的 rejoin 不能报告命中")
	}
}

// 前端会话（localProcess，IsFrontend=true）同样要拒：判据是「ctx 里有没有会话」，
// 不依赖 IsFrontend，所以前端/Remote 两种会话都挡得住。
func TestCreateAndRejoinRejectFrontendSession(t *testing.T) {
	sess := session.NewSessionPool().NewSession(nil, true, "13")
	c := New(&fakeApp{sess: sess})

	if _, err := c.Create(context.Background(), &protos.CreateGameMsg{
		MatchId: "m", Uids: []string{"1"},
	}); err == nil {
		t.Fatal("前端会话发来的 create 必须被拒")
	}
	if reply, _ := c.Rejoin(context.Background(), &protos.RejoinMsg{Token: "1"}); reply.Found {
		t.Fatal("前端会话发来的 rejoin 不能报告命中")
	}
}

// 后端 RPC（match 的 app.RPCTo → RPCType_User → handleRPCUser）的 ctx 里**没有**
// 会话，必须照常放行，否则匹配到了却建不了局。
func TestCreateAcceptsBackendCall(t *testing.T) {
	c := New(&fakeApp{sess: nil})
	// 建实例会真的创建 Jolt 世界（physics.New）；测完立刻 Shutdown 收干净。
	t.Cleanup(c.Shutdown)

	reply, err := c.Create(context.Background(), &protos.CreateGameMsg{
		MatchId: "m-backend", Uids: []string{"1"},
	})
	if err != nil || reply == nil || reply.Code != 0 {
		t.Fatalf("后端 RPC 应被放行，得到 %+v err=%v", reply, err)
	}
	if c.uidToInst["1"] == nil {
		t.Fatal("建成的实例应登记 uid 映射")
	}
	if c.instances["m-backend"] == nil {
		t.Fatal("建成的实例应登记在 instances 里")
	}
}

// 后端 rejoin 照常工作（真的能命中）。
func TestRejoinAcceptsBackendCall(t *testing.T) {
	c := New(&fakeApp{sess: nil})
	inst := &Instance{matchID: "m1"}
	c.instances = map[string]*Instance{"m1": inst}
	c.uidToInst = map[string]*Instance{"7": inst}
	c.uidToIndex = map[string]int{"7": 1}

	reply, err := c.Rejoin(context.Background(), &protos.RejoinMsg{Token: "7"})
	if err != nil || !reply.Found || reply.MatchId != "m1" || reply.PlayerIdx != 1 {
		t.Fatalf("后端 RPC 的 rejoin 应命中 m1 槽位 1，得到 %+v err=%v", reply, err)
	}
}

// 后端调用仍要走原有的「名单超员」校验 —— 守卫不能把 create 的其它前置检查短路掉。
func TestCreateBackendStillRejectsOversizedRoster(t *testing.T) {
	c := New(&fakeApp{sess: nil})
	uids := make([]string, sim.MaxPlayers+1)
	reply, err := c.Create(context.Background(), &protos.CreateGameMsg{MatchId: "m", Uids: uids})
	if err == nil || reply.Code == 0 {
		t.Fatalf("超员名单必须被拒，得到 %+v err=%v", reply, err)
	}
	if strings.Contains(err.Error(), "client call") {
		t.Fatalf("后端调用不该撞上客户端守卫: %v", err)
	}
}

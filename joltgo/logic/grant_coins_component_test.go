package logic

import (
	"context"
	"testing"

	"joltgo/game/protos"
)

// newGrantCoinsComponentEnv 造一个 logic 组件。
// uid 非空表示「调用带会话」（客户端经 gate 转发时的形状），为空表示后端 RPC。
//
// 两者现在**行为相同**：可达性由 gate 的转发白名单负责（logic.grantcoins 不在
// 白名单里，客户端发不过来），handler 不再判调用方。
func newGrantCoinsComponentEnv(t *testing.T, uid string) (*Component, *serviceTestEnv) {
	t.Helper()
	env, _ := newLockedServiceTestEnv(t)
	if err := env.svc.EnsureProfile(context.Background(), "7"); err != nil {
		t.Fatal(err)
	}
	seedAccount(t, env, 7, "alice")
	app := &fakeApp{}
	if uid != "" {
		app.sess = &fakeSession{uid: uid}
	}
	return NewComponent(app, env.svc), env
}

func TestGrantCoinsHandlerDoesNotCheckCaller(t *testing.T) {
	// 带会话的调用（客户端经 gate 转发时的样子）现在也照常执行 ——
	// 这是 2026-09-16 的明确取舍：handler 不做鉴权，白名单负责可达性。
	c, env := newGrantCoinsComponentEnv(t, "7")
	reply, err := c.GrantCoins(context.Background(), &protos.GrantCoinsMsg{
		AccountId: "7", Delta: 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reply.Ok || reply.Coins != 1500 {
		t.Fatalf("带会话的调用应照常执行，得到 %+v", reply)
	}
	state, err := env.svc.State(context.Background(), "7")
	if err != nil {
		t.Fatal(err)
	}
	if state.Coins != 1500 {
		t.Fatalf("钱包余额 = %d，期望 1500", state.Coins)
	}
}

func TestGrantCoinsHandlerNeedsNoSecret(t *testing.T) {
	// 无会话的后端调用，且请求里没有任何密钥字段，应照常成功。
	c, _ := newGrantCoinsComponentEnv(t, "")
	reply, err := c.GrantCoins(context.Background(), &protos.GrantCoinsMsg{
		AccountId: "7", Delta: 1,
	})
	if err != nil || !reply.Ok || reply.Coins != 1001 {
		t.Fatalf("reply=%+v err=%v，期望 ok 且 1001", reply, err)
	}
}

func TestGrantCoinsHandlerGrants(t *testing.T) {
	c, env := newGrantCoinsComponentEnv(t, "")
	reply, err := c.GrantCoins(context.Background(), &protos.GrantCoinsMsg{
		AccountId: "7", Delta: 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reply.Ok || reply.Coins != 1500 {
		t.Fatalf("reply=%+v，期望 ok 且 1500", reply)
	}
	state, err := env.svc.State(context.Background(), "7")
	if err != nil {
		t.Fatal(err)
	}
	if state.Coins != 1500 {
		t.Fatalf("钱包余额 = %d，期望 1500", state.Coins)
	}
}

func TestGrantCoinsHandlerReportsUnknownAccount(t *testing.T) {
	c, _ := newGrantCoinsComponentEnv(t, "")
	reply, err := c.GrantCoins(context.Background(), &protos.GrantCoinsMsg{
		AccountId: "999999", Delta: 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Ok || reply.Reason != ReasonAccountNotFound {
		t.Fatalf("未知账号应回 %s，得到 %+v", ReasonAccountNotFound, reply)
	}
}

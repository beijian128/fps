package logic

import (
	"context"
	"testing"

	"joltgo/game/protos"
)

// newGrantCoinsComponentEnv 造一个 logic 组件。
//
// 这里不构造会话：GrantCoins 不再看调用方是谁（可达性由 gate 白名单负责，
// 见 component.go 上那段说明），所以带不带会话的调用行为完全一样。
func newGrantCoinsComponentEnv(t *testing.T) (*Component, *serviceTestEnv) {
	t.Helper()
	env, _ := newLockedServiceTestEnv(t)
	if err := env.svc.EnsureProfile(context.Background(), "7"); err != nil {
		t.Fatal(err)
	}
	seedAccount(t, env, 7, "alice")
	return NewComponent(&fakeApp{}, env.svc), env
}

func TestGrantCoinsHandlerGrants(t *testing.T) {
	// handler 不判调用方；可达性由 gate 的转发白名单保证。
	c, env := newGrantCoinsComponentEnv(t)
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

func TestGrantCoinsHandlerNeedsNoSecret(t *testing.T) {
	// 请求里没有任何密钥字段，应照常成功。
	c, _ := newGrantCoinsComponentEnv(t)
	reply, err := c.GrantCoins(context.Background(), &protos.GrantCoinsMsg{
		AccountId: "7", Delta: 1,
	})
	if err != nil || !reply.Ok || reply.Coins != 1001 {
		t.Fatalf("reply=%+v err=%v，期望 ok 且 1001", reply, err)
	}
}

func TestGrantCoinsHandlerReportsUnknownAccount(t *testing.T) {
	c, _ := newGrantCoinsComponentEnv(t)
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

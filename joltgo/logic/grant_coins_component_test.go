package logic

import (
	"context"
	"testing"

	"joltgo/game/protos"
)

// newGrantCoinsComponentEnv 造一个带管理密钥的 logic 组件。
// uid 非空表示「调用带会话」（客户端路径），为空表示后端 RPC。
func newGrantCoinsComponentEnv(t *testing.T, uid string) (*Component, *serviceTestEnv) {
	t.Helper()
	env, _ := newLockedServiceTestEnv(t)
	if err := env.svc.EnsureProfile(context.Background(), "7"); err != nil {
		t.Fatal(err)
	}
	seedAccount(t, env, 7, "alice")
	app := &fakeApp{}
	if uid != "" {
		// 带会话 = 客户端经 gate 转发来的调用；不带会话 = 后端 RPC。
		app.sess = &fakeSession{uid: uid}
	}
	return NewComponentWithSecret(app, env.svc, "s3cret"), env
}

func TestGrantCoinsHandlerRejectsClientCall(t *testing.T) {
	c, env := newGrantCoinsComponentEnv(t, "7")
	reply, err := c.GrantCoins(context.Background(), &protos.GrantCoinsMsg{
		AccountId: "7", Delta: 500, AdminKey: "s3cret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Ok || reply.Reason != ReasonForbidden {
		t.Fatalf("带会话的调用必须被拒（route 名不是权限），得到 %+v", reply)
	}
	state, err := env.svc.State(context.Background(), "7")
	if err != nil {
		t.Fatal(err)
	}
	if state.Coins != 1000 {
		t.Fatalf("被拒的调用不该改钱包，余额 %d", state.Coins)
	}
}

func TestGrantCoinsHandlerRejectsWrongKey(t *testing.T) {
	c, env := newGrantCoinsComponentEnv(t, "")
	reply, err := c.GrantCoins(context.Background(), &protos.GrantCoinsMsg{
		AccountId: "7", Delta: 500, AdminKey: "nope",
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Ok || reply.Reason != ReasonForbidden {
		t.Fatalf("密钥错误必须被拒，得到 %+v", reply)
	}
	state, err := env.svc.State(context.Background(), "7")
	if err != nil {
		t.Fatal(err)
	}
	if state.Coins != 1000 {
		t.Fatalf("被拒的调用不该改钱包，余额 %d", state.Coins)
	}
}

func TestGrantCoinsHandlerRejectsEmptySecret(t *testing.T) {
	// 服务端没配密钥（secret=""）时应当拒绝一切请求，而不是放行。
	env, _ := newLockedServiceTestEnv(t)
	if err := env.svc.EnsureProfile(context.Background(), "7"); err != nil {
		t.Fatal(err)
	}
	seedAccount(t, env, 7, "alice")
	c := NewComponentWithSecret(&fakeApp{}, env.svc, "")

	reply, err := c.GrantCoins(context.Background(), &protos.GrantCoinsMsg{
		AccountId: "7", Delta: 500, AdminKey: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Ok || reply.Reason != ReasonForbidden {
		t.Fatalf("未配密钥时一切请求都该被拒，得到 %+v", reply)
	}
}

func TestGrantCoinsHandlerGrants(t *testing.T) {
	c, env := newGrantCoinsComponentEnv(t, "")
	reply, err := c.GrantCoins(context.Background(), &protos.GrantCoinsMsg{
		AccountId: "7", Delta: 500, AdminKey: "s3cret",
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
		AccountId: "999999", Delta: 500, AdminKey: "s3cret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Ok || reply.Reason != ReasonAccountNotFound {
		t.Fatalf("未知账号应回 %s，得到 %+v", ReasonAccountNotFound, reply)
	}
}

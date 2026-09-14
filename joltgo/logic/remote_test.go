package logic

import (
	"context"
	"testing"

	"joltgo/game/protos"
)

func TestLogicRemoteOnlineEnsuresProfile(t *testing.T) {
	env, _ := newLockedServiceTestEnv(t)
	reply, err := NewRemote(env.svc).Online(context.Background(), &protos.UserOnlineMsg{AccountId: "7"})
	if err != nil || !reply.Ok {
		t.Fatalf("Online: %+v err=%v", reply, err)
	}
	if _, ok, _ := env.store.GetWallet(context.Background(), 7); !ok {
		t.Fatal("Online 应创建钱包")
	}
}

func TestLogicRemoteOnlineRejectsBadAccount(t *testing.T) {
	env, _ := newLockedServiceTestEnv(t)
	reply, err := NewRemote(env.svc).Online(context.Background(), &protos.UserOnlineMsg{})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Ok || reply.Reason != ReasonUnauthenticated {
		t.Fatalf("reply=%+v", reply)
	}
}

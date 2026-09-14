package logic

import (
	"context"
	"testing"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/session"
	"joltgo/game/protos"
)

type fakeSession struct {
	session.Session
	uid string
}

func (s *fakeSession) UID() string { return s.uid }

type fakeApp struct {
	pitaya.Pitaya
	sess session.Session
}

func (a *fakeApp) GetSessionFromCtx(context.Context) session.Session { return a.sess }

func newComponentTestEnv(t *testing.T, uid string) (*Component, *serviceTestEnv) {
	t.Helper()
	env, _ := newLockedServiceTestEnv(t)
	if uid != "" {
		if err := env.svc.EnsureProfile(context.Background(), uid); err != nil {
			t.Fatal(err)
		}
	}
	return NewComponent(&fakeApp{sess: &fakeSession{uid: uid}}, env.svc), env
}

func TestLogicComponentUsesSessionUID(t *testing.T) {
	component, env := newComponentTestEnv(t, "7")
	ctx := context.Background()
	if _, err := env.svc.Purchase(ctx, "7", "medkit", 1); err != nil {
		t.Fatal(err)
	}
	reply, err := component.State(ctx, &protos.LogicStateMsg{})
	if err != nil || !reply.Ok {
		t.Fatalf("State: %+v err=%v", reply, err)
	}
	if reply.Coins != 950 || len(reply.Items) != 4 || reply.Items[3].OwnedQuantity != 1 {
		t.Fatalf("reply=%+v", reply)
	}
}

func TestLogicComponentMapsErrors(t *testing.T) {
	component, _ := newComponentTestEnv(t, "")
	reply, err := component.Purchase(context.Background(), &protos.PurchaseMsg{ItemId: "rifle", Quantity: 1})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Ok || reply.Reason != ReasonUnauthenticated {
		t.Fatalf("reply=%+v", reply)
	}
}

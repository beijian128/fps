package logic

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	redigo "github.com/gomodule/redigo/redis"
	"joltgo/persist"
)

type noopLock struct{}

func (noopLock) Lock(context.Context) error   { return nil }
func (noopLock) Unlock(context.Context) error { return nil }
func (noopLock) IsLost() bool                 { return false }

type noopLockFactory struct{}

func (noopLockFactory) New(uint64) Lock { return noopLock{} }

type serviceTestEnv struct {
	svc   *Service
	store *persist.PlayerStore
}

func newServiceTestEnv(t *testing.T) *serviceTestEnv {
	t.Helper()
	mr := miniredis.RunT(t)
	pool := &redigo.Pool{
		MaxIdle: 2,
		Dial:    func() (redigo.Conn, error) { return redigo.Dial("tcp", mr.Addr()) },
	}
	t.Cleanup(func() { _ = pool.Close() })
	store := persist.NewPlayerStore(pool)
	return &serviceTestEnv{
		svc:   NewService(store, MustDefaultCatalog(), noopLockFactory{}),
		store: store,
	}
}

func mustReason(t *testing.T, err error, want string) {
	t.Helper()
	if got := ReasonOf(err); got != want {
		t.Fatalf("reason=%q want=%q err=%v", got, want, err)
	}
}

func TestStateBuildsCatalogAndQuantities(t *testing.T) {
	env := newServiceTestEnv(t)
	ctx := context.Background()
	if err := env.store.SaveWallet(ctx, 1, persist.PlayerWallet{Coins: 1000}); err != nil {
		t.Fatal(err)
	}
	if err := env.store.SaveBag(ctx, 1, persist.PlayerBag{
		Items: []persist.PlayerItem{
			{ItemID: "rifle", Quantity: 2, AcquiredAt: 1},
			{ItemID: "medkit", Quantity: 5, AcquiredAt: 2},
		},
		EquippedPrimaryWeapon: "rifle",
	}); err != nil {
		t.Fatal(err)
	}

	state, err := env.svc.State(ctx, "1")
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if state.Coins != 1000 || state.EquippedPrimaryWeapon != "rifle" {
		t.Fatalf("state=%+v", state)
	}
	if len(state.Items) != 4 || state.Items[0].ItemID != "rifle" || state.Items[0].OwnedQuantity != 2 {
		t.Fatalf("items=%+v", state.Items)
	}
	if state.Items[1].ItemID != "pistol" || state.Items[1].OwnedQuantity != 0 {
		t.Fatalf("items=%+v", state.Items)
	}
}

func TestStateRejectsMissingProfileAndBadUID(t *testing.T) {
	env := newServiceTestEnv(t)
	_, err := env.svc.State(context.Background(), "1")
	mustReason(t, err, ReasonProfileMissing)

	_, err = env.svc.State(context.Background(), "")
	mustReason(t, err, ReasonUnauthenticated)

	_, err = env.svc.State(context.Background(), "abc")
	mustReason(t, err, ReasonUnauthenticated)
}

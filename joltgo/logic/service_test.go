package logic

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	redigo "github.com/gomodule/redigo/redis"
	"github.com/redis/go-redis/v9"
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

type countingLockFactory struct {
	inner LockFactory
	mu    sync.Mutex
	count int
}

func (f *countingLockFactory) New(accountID uint64) Lock {
	f.mu.Lock()
	f.count++
	f.mu.Unlock()
	return f.inner.New(accountID)
}

func (f *countingLockFactory) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.count
}

type blockingLock struct{}

func (blockingLock) Lock(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}
func (blockingLock) Unlock(context.Context) error { return nil }
func (blockingLock) IsLost() bool                 { return false }

type blockingLockFactory struct{}

func (blockingLockFactory) New(uint64) Lock { return blockingLock{} }

func newLockedServiceTestEnv(t *testing.T) (*serviceTestEnv, *countingLockFactory) {
	t.Helper()
	mr := miniredis.RunT(t)
	pool := &redigo.Pool{
		MaxIdle: 2,
		Dial:    func() (redigo.Conn, error) { return redigo.Dial("tcp", mr.Addr()) },
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = pool.Close(); _ = rdb.Close() })
	store := persist.NewPlayerStore(pool)
	locks := &countingLockFactory{inner: NewRedisLockFactory(rdb)}
	return &serviceTestEnv{svc: NewService(store, MustDefaultCatalog(), locks), store: store}, locks
}

func TestEnsureProfileIsIdempotent(t *testing.T) {
	env, locks := newLockedServiceTestEnv(t)
	ctx := context.Background()
	if AccountLockKey(7) != "user:lock:7" {
		t.Fatalf("lock key=%q", AccountLockKey(7))
	}

	if err := env.svc.EnsureProfile(ctx, "7"); err != nil {
		t.Fatalf("first EnsureProfile: %v", err)
	}
	if locks.Count() != 1 {
		t.Fatalf("首次建档应获取一次锁，得到 %d", locks.Count())
	}
	wallet, ok, _ := env.store.GetWallet(ctx, 7)
	if !ok || wallet.Coins != 1000 {
		t.Fatalf("wallet=%+v ok=%v", wallet, ok)
	}
	bag, ok, _ := env.store.GetBag(ctx, 7)
	if !ok || len(bag.Items) != 0 || bag.EquippedPrimaryWeapon != "" {
		t.Fatalf("bag=%+v ok=%v", bag, ok)
	}

	if err := env.svc.EnsureProfile(ctx, "7"); err != nil {
		t.Fatalf("second EnsureProfile: %v", err)
	}
	if locks.Count() != 1 {
		t.Fatalf("档案已存在时不应获取锁，得到 %d", locks.Count())
	}
}

func TestEnsureProfileRepairsOnlyMissingParts(t *testing.T) {
	env, _ := newLockedServiceTestEnv(t)
	ctx := context.Background()
	if err := env.store.SaveWallet(ctx, 7, persist.PlayerWallet{Coins: 321}); err != nil {
		t.Fatal(err)
	}

	if err := env.svc.EnsureProfile(ctx, "7"); err != nil {
		t.Fatalf("EnsureProfile: %v", err)
	}
	wallet, ok, _ := env.store.GetWallet(ctx, 7)
	if !ok || wallet.Coins != 321 {
		t.Fatalf("不能重置已有钱包: %+v ok=%v", wallet, ok)
	}
	if _, ok, _ := env.store.GetBag(ctx, 7); !ok {
		t.Fatal("缺失的背包应被补建")
	}
}

func TestEnsureProfileConcurrentInitializesOnce(t *testing.T) {
	env, _ := newLockedServiceTestEnv(t)
	const workers = 12
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- env.svc.EnsureProfile(context.Background(), "9")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("EnsureProfile: %v", err)
		}
	}
	wallet, ok, _ := env.store.GetWallet(context.Background(), 9)
	if !ok || wallet.Coins != 1000 {
		t.Fatalf("并发初始化只能发一次金币: %+v ok=%v", wallet, ok)
	}
}

func TestEnsureProfileLockTimeoutReturnsBusy(t *testing.T) {
	env := newServiceTestEnv(t)
	env.svc.locks = blockingLockFactory{}
	err := env.svc.EnsureProfile(context.Background(), "7")
	mustReason(t, err, ReasonBusy)
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

type bagFailStore struct {
	Store
	fail bool
}

func (s *bagFailStore) SaveBag(ctx context.Context, id uint64, bag persist.PlayerBag) error {
	if s.fail {
		return errors.New("boom: save bag")
	}
	return s.Store.SaveBag(ctx, id, bag)
}

func TestPurchaseSuccessAndStacking(t *testing.T) {
	env, _ := newLockedServiceTestEnv(t)
	ctx := context.Background()
	if err := env.svc.EnsureProfile(ctx, "7"); err != nil {
		t.Fatal(err)
	}

	state, err := env.svc.Purchase(ctx, "7", "rifle", 2)
	if err != nil {
		t.Fatalf("Purchase: %v", err)
	}
	if state.Coins != 400 {
		t.Fatalf("coins=%d want=400", state.Coins)
	}
	if state.Items[0].OwnedQuantity != 2 {
		t.Fatalf("rifle quantity=%d want=2", state.Items[0].OwnedQuantity)
	}

	firstBag, _, _ := env.store.GetBag(ctx, 7)
	acquiredAt := firstBag.Items[0].AcquiredAt

	state, err = env.svc.Purchase(ctx, "7", "rifle", 1)
	if err != nil {
		t.Fatalf("second Purchase: %v", err)
	}
	if state.Coins != 100 || state.Items[0].OwnedQuantity != 3 {
		t.Fatalf("state=%+v", state)
	}
	secondBag, _, _ := env.store.GetBag(ctx, 7)
	if secondBag.Items[0].AcquiredAt != acquiredAt {
		t.Fatalf("重复购买必须保留首次获得时间: %d -> %d", acquiredAt, secondBag.Items[0].AcquiredAt)
	}
}

func TestPurchaseRequiresOnlineProfile(t *testing.T) {
	env, _ := newLockedServiceTestEnv(t)
	_, err := env.svc.Purchase(context.Background(), "7", "medkit", 1)
	mustReason(t, err, ReasonProfileMissing)
}

func TestPurchaseRejectsWithoutWriting(t *testing.T) {
	env, locks := newLockedServiceTestEnv(t)
	ctx := context.Background()
	if err := env.svc.EnsureProfile(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	before := locks.Count()

	_, err := env.svc.Purchase(ctx, "7", "shotgun", 3)
	mustReason(t, err, ReasonInsufficientFund)
	if locks.Count() != before {
		t.Fatalf("余额不足不应加锁: before=%d after=%d", before, locks.Count())
	}

	_, err = env.svc.Purchase(ctx, "7", "rifle", 0)
	mustReason(t, err, ReasonBadQuantity)
	_, err = env.svc.Purchase(ctx, "7", "rifle", 100)
	mustReason(t, err, ReasonBadQuantity)
	_, err = env.svc.Purchase(ctx, "7", "rocket", 1)
	mustReason(t, err, ReasonItemNotFound)
}

func TestPurchaseCompensatesWalletWhenBagSaveFails(t *testing.T) {
	env := newServiceTestEnv(t)
	ctx := context.Background()
	if err := env.store.SaveWallet(ctx, 7, persist.PlayerWallet{Coins: 1000}); err != nil {
		t.Fatal(err)
	}
	if err := env.store.SaveBag(ctx, 7, persist.PlayerBag{}); err != nil {
		t.Fatal(err)
	}
	failing := &bagFailStore{Store: env.store, fail: true}
	env.svc.store = failing
	env.svc.locks = noopLockFactory{}

	_, err := env.svc.Purchase(ctx, "7", "rifle", 1)
	if ReasonOf(err) != ReasonInternal {
		t.Fatalf("err=%v want internal", err)
	}
	wallet, ok, err := env.store.GetWallet(ctx, 7)
	if err != nil || !ok || wallet.Coins != 1000 {
		t.Fatalf("钱包未补偿: %+v ok=%v err=%v", wallet, ok, err)
	}
	bag, _, _ := env.store.GetBag(ctx, 7)
	if len(bag.Items) != 0 {
		t.Fatalf("背包失败时不应发货: %+v", bag)
	}
}

func TestPurchaseLockTimeoutReturnsBusy(t *testing.T) {
	env, _ := newLockedServiceTestEnv(t)
	ctx := context.Background()
	if err := env.svc.EnsureProfile(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	env.svc.locks = blockingLockFactory{}
	_, err := env.svc.Purchase(ctx, "7", "medkit", 1)
	mustReason(t, err, ReasonBusy)
}

func TestPurchaseConcurrentSameAccount(t *testing.T) {
	env, _ := newLockedServiceTestEnv(t)
	ctx := context.Background()
	if err := env.svc.EnsureProfile(ctx, "7"); err != nil {
		t.Fatal(err)
	}

	const workers = 10
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := env.svc.Purchase(context.Background(), "7", "medkit", 1)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent purchase: %v", err)
		}
	}
	state, err := env.svc.State(ctx, "7")
	if err != nil {
		t.Fatal(err)
	}
	if state.Coins != 500 || state.Items[3].OwnedQuantity != 10 {
		t.Fatalf("并发购买发生丢更新: %+v", state)
	}
}

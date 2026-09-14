package persist

import (
	"context"
	"reflect"
	"testing"

	"github.com/alicebob/miniredis/v2"
	redigo "github.com/gomodule/redigo/redis"
	playerpb "joltgo/persist/protos/player"
)

func newTestPlayerStore(t *testing.T) (*PlayerStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	pool := &redigo.Pool{
		MaxIdle: 2,
		Dial:    func() (redigo.Conn, error) { return redigo.Dial("tcp", mr.Addr()) },
	}
	t.Cleanup(func() { _ = pool.Close() })
	return NewPlayerStore(pool), mr
}

func TestPlayerStoreRoundTrip(t *testing.T) {
	store, mr := newTestPlayerStore(t)
	ctx := context.Background()

	wallet := PlayerWallet{Coins: 1000}
	if err := store.SaveWallet(ctx, 42, wallet); err != nil {
		t.Fatalf("SaveWallet: %v", err)
	}
	bag := PlayerBag{
		Items: []PlayerItem{
			{ItemID: "rifle", Quantity: 2, AcquiredAt: 111},
			{ItemID: "medkit", Quantity: 5, AcquiredAt: 222},
		},
		EquippedPrimaryWeapon: "rifle",
	}
	if err := store.SaveBag(ctx, 42, bag); err != nil {
		t.Fatalf("SaveBag: %v", err)
	}

	if got := PlayerWalletKey(42); got != "REDB#2:42:0" {
		t.Fatalf("PlayerWalletKey = %q", got)
	}
	if got := PlayerBagKey(42); got != "REDB#1:42:0" {
		t.Fatalf("PlayerBagKey = %q", got)
	}
	if !mr.Exists(PlayerWalletKey(42)) || !mr.Exists(PlayerBagKey(42)) {
		t.Fatal("钱包或背包 Hash 未写入")
	}

	gotWallet, ok, err := store.GetWallet(ctx, 42)
	if err != nil || !ok || gotWallet != wallet {
		t.Fatalf("GetWallet: got=%+v ok=%v err=%v", gotWallet, ok, err)
	}
	gotBag, ok, err := store.GetBag(ctx, 42)
	if err != nil || !ok {
		t.Fatalf("GetBag: ok=%v err=%v", ok, err)
	}
	if gotBag.EquippedPrimaryWeapon != "rifle" || len(gotBag.Items) != 2 {
		t.Fatalf("GetBag 不一致: %+v", gotBag)
	}

	if err := store.AddCoins(ctx, 42, 250); err != nil {
		t.Fatalf("AddCoins: %v", err)
	}
	gotWallet, ok, err = store.GetWallet(ctx, 42)
	if err != nil || !ok || gotWallet.Coins != 1250 {
		t.Fatalf("AddCoins 后 GetWallet: got=%+v ok=%v err=%v", gotWallet, ok, err)
	}
	gotBagAfterCoins, ok, err := store.GetBag(ctx, 42)
	if err != nil || !ok || !reflect.DeepEqual(gotBagAfterCoins, gotBag) {
		t.Fatalf("AddCoins 不应修改背包: got=%+v want=%+v ok=%v err=%v", gotBagAfterCoins, gotBag, ok, err)
	}

	conn := store.pool.Get()
	defer conn.Close()
	schema, err := redigo.Int(conn.Do("HGET", PlayerWalletKey(42), playerpb.FieldDBUserWallet_SchemaVersion))
	if err != nil || schema != int(playerpb.DBSchemaVersion_DB_SCHEMA_VERSION_CURRENT) {
		t.Fatalf("schema=%d err=%v", schema, err)
	}

	if err := store.DeleteWallet(ctx, 42); err != nil {
		t.Fatalf("DeleteWallet: %v", err)
	}
	if err := store.DeleteBag(ctx, 42); err != nil {
		t.Fatalf("DeleteBag: %v", err)
	}
	if _, ok, _ := store.GetWallet(ctx, 42); ok {
		t.Fatal("DeleteWallet 后仍存在")
	}
	if _, ok, _ := store.GetBag(ctx, 42); ok {
		t.Fatal("DeleteBag 后仍存在")
	}
}

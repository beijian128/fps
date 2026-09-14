package persist

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	redigo "github.com/gomodule/redigo/redis"
	protos "joltgo/persist/protos"
)

func newTestAccountStore(t *testing.T) (*AccountStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	pool := &redigo.Pool{
		MaxIdle: 2,
		Dial: func() (redigo.Conn, error) {
			return redigo.Dial("tcp", mr.Addr())
		},
	}
	t.Cleanup(func() { _ = pool.Close() })
	return NewAccountStore(pool), mr
}

func TestAccountStoreRoundTrip(t *testing.T) {
	store, mr := newTestAccountStore(t)
	ctx := context.Background()

	if err := store.Save(ctx, 42, "Alice", "bcrypt-hash", 123456); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if key := AccountKey(42); key != "acct:1:42:0" {
		t.Fatalf("AccountKey = %q，期望 acct:1:42:0", key)
	}
	if !mr.Exists(AccountKey(42)) {
		t.Fatalf("生成的 Redis Hash %s 不存在", AccountKey(42))
	}

	row, ok, err := store.Get(ctx, 42)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if row.Username != "Alice" || row.PassHash != "bcrypt-hash" || row.CreatedAt != 123456 {
		t.Fatalf("Get 回读不一致: %+v", row)
	}
	conn := store.pool.Get()
	defer conn.Close()
	schema, err := redigo.Int(conn.Do("HGET", AccountKey(42), protos.FieldDBAccount_SchemaVersion))
	if err != nil {
		t.Fatalf("读取 SchemaVersion: %v", err)
	}
	if schema != int(protos.DBSchemaVersion_DB_SCHEMA_VERSION_CURRENT) {
		t.Fatalf("SchemaVersion = %d，期望 current", schema)
	}

	if err := store.Delete(ctx, 42); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, err := store.Get(ctx, 42); err != nil || ok {
		t.Fatalf("Delete 后应不存在: ok=%v err=%v", ok, err)
	}
}

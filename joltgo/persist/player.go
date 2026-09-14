package persist

import (
	"context"
	"fmt"
	"sort"

	"github.com/gomodule/redigo/redis"
	playerpb "joltgo/persist/protos/player"
)

const (
	playerBagNamespace    uint32 = uint32(playerpb.REDBKey_UserBagDB)
	playerWalletNamespace uint32 = uint32(playerpb.REDBKey_UserWalletDB)
)

type PlayerWallet struct {
	Coins int64
}

type PlayerItem struct {
	ItemID     string
	Quantity   int64
	AcquiredAt int64
}

type PlayerBag struct {
	Items                 []PlayerItem
	EquippedPrimaryWeapon string
}

type PlayerPersistence interface {
	GetWallet(context.Context, uint64) (PlayerWallet, bool, error)
	SaveWallet(context.Context, uint64, PlayerWallet) error
	AddCoins(context.Context, uint64, int64) error
	DeleteWallet(context.Context, uint64) error
	GetBag(context.Context, uint64) (PlayerBag, bool, error)
	SaveBag(context.Context, uint64, PlayerBag) error
	DeleteBag(context.Context, uint64) error
}

type PlayerStore struct {
	pool *redis.Pool
}

var _ PlayerPersistence = (*PlayerStore)(nil)

func NewPlayerStore(pool *redis.Pool) *PlayerStore {
	return &PlayerStore{pool: pool}
}

func PlayerWalletKey(id uint64) string {
	return fmt.Sprintf("REDB#%d:%d:%d", playerWalletNamespace, id, 0)
}

func PlayerBagKey(id uint64) string {
	return fmt.Sprintf("REDB#%d:%d:%d", playerBagNamespace, id, 0)
}

func (s *PlayerStore) SaveWallet(ctx context.Context, id uint64, wallet PlayerWallet) error {
	conn, err := s.pool.GetContext(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	row := &playerpb.DBUserWallet{
		Coins:         wallet.Coins,
		SchemaVersion: playerpb.DBSchemaVersion_DB_SCHEMA_VERSION_CURRENT,
	}
	return row.SetFields(conn, playerWalletNamespace, id, 0)
}

func (s *PlayerStore) AddCoins(ctx context.Context, id uint64, delta int64) error {
	conn, err := s.pool.GetContext(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Do("HINCRBY", PlayerWalletKey(id), playerpb.FieldDBUserWallet_Coins, delta)
	return err
}

func (s *PlayerStore) GetWallet(ctx context.Context, id uint64) (PlayerWallet, bool, error) {
	conn, err := s.pool.GetContext(ctx)
	if err != nil {
		return PlayerWallet{}, false, err
	}
	defer conn.Close()
	exists, err := redis.Bool(conn.Do("EXISTS", PlayerWalletKey(id)))
	if err != nil || !exists {
		return PlayerWallet{}, false, err
	}
	var row playerpb.DBUserWallet
	if err := row.GetFields(conn, playerWalletNamespace, id, 0); err != nil {
		return PlayerWallet{}, false, err
	}
	return PlayerWallet{Coins: row.Coins}, true, nil
}

func (s *PlayerStore) DeleteWallet(ctx context.Context, id uint64) error {
	conn, err := s.pool.GetContext(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Do("DEL", PlayerWalletKey(id))
	return err
}

func (s *PlayerStore) SaveBag(ctx context.Context, id uint64, bag PlayerBag) error {
	conn, err := s.pool.GetContext(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	items := append([]PlayerItem(nil), bag.Items...)
	sort.Slice(items, func(i, j int) bool { return items[i].ItemID < items[j].ItemID })
	stored := make([]playerpb.DBUserBag_DBItem, 0, len(items))
	for _, item := range items {
		stored = append(stored, playerpb.DBUserBag_DBItem{
			ItemId: item.ItemID, Quantity: item.Quantity, AcquiredAt: item.AcquiredAt,
		})
	}
	row := &playerpb.DBUserBag{
		Items:                 playerpb.DBUserBag_DBItems{Items: stored},
		EquippedPrimaryWeapon: bag.EquippedPrimaryWeapon,
		SchemaVersion:         playerpb.DBSchemaVersion_DB_SCHEMA_VERSION_CURRENT,
	}
	return row.SetFields(conn, playerBagNamespace, id, 0)
}

func (s *PlayerStore) GetBag(ctx context.Context, id uint64) (PlayerBag, bool, error) {
	conn, err := s.pool.GetContext(ctx)
	if err != nil {
		return PlayerBag{}, false, err
	}
	defer conn.Close()
	exists, err := redis.Bool(conn.Do("EXISTS", PlayerBagKey(id)))
	if err != nil || !exists {
		return PlayerBag{}, false, err
	}
	var row playerpb.DBUserBag
	if err := row.GetFields(conn, playerBagNamespace, id, 0); err != nil {
		return PlayerBag{}, false, err
	}
	bag := PlayerBag{EquippedPrimaryWeapon: row.EquippedPrimaryWeapon}
	for _, item := range row.Items.Items {
		bag.Items = append(bag.Items, PlayerItem{
			ItemID: item.ItemId, Quantity: item.Quantity, AcquiredAt: item.AcquiredAt,
		})
	}
	return bag, true, nil
}

func (s *PlayerStore) DeleteBag(ctx context.Context, id uint64) error {
	conn, err := s.pool.GetContext(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Do("DEL", PlayerBagKey(id))
	return err
}

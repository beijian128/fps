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
	// playerProfileNamespace 是玩家档案计数的 REDBKey。历史 List 不用它
	// （List 不是 Hash 行，见 PlayerHistoryKey）。
	playerProfileNamespace uint32 = uint32(playerpb.REDBKey_UserProfileDB)
	// historyLimit 是每名玩家保留的最近对局数（新在前）。
	historyLimit = 20
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

// PlayerStats 是玩家档案里的累计统计（等级由 XP 派生，不落库）。
type PlayerStats struct {
	XP      int64
	Kills   int64
	Deaths  int64
	Matches int64
	Wins    int64
	Losses  int64
}

// PlayerStatsDelta 是一次对局结算要累加的增量。
type PlayerStatsDelta struct {
	XP      int64
	Kills   int64
	Deaths  int64
	Matches int64
	Wins    int64
	Losses  int64
}

// PlayerMatchRecord 是历史里的单场战绩（对手名在记账时解析成快照）。
type PlayerMatchRecord struct {
	MatchID         string
	Won             bool
	Kills           int32
	Deaths          int32
	OpponentKills   int32
	DurationSeconds int32
	OpponentName    string
	EndedAt         int64
}

type PlayerPersistence interface {
	GetWallet(context.Context, uint64) (PlayerWallet, bool, error)
	SaveWallet(context.Context, uint64, PlayerWallet) error
	AddCoins(context.Context, uint64, int64) error
	DeleteWallet(context.Context, uint64) error
	GetBag(context.Context, uint64) (PlayerBag, bool, error)
	SaveBag(context.Context, uint64, PlayerBag) error
	DeleteBag(context.Context, uint64) error
	GetStats(context.Context, uint64) (PlayerStats, bool, error)
	SaveStats(context.Context, uint64, PlayerStats) error
	AddStats(context.Context, uint64, PlayerStatsDelta) error
	AppendMatchRecord(context.Context, uint64, PlayerMatchRecord) error
	ListMatchRecords(context.Context, uint64, int) ([]PlayerMatchRecord, error)
	UsernameByID(context.Context, uint64) (string, bool, error)
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

func PlayerProfileKey(id uint64) string {
	return fmt.Sprintf("REDB#%d:%d:%d", playerProfileNamespace, id, 0)
}

// PlayerHistoryKey 是历史 List 的键：它不归 protoc-gen-redis 管（List 不是 Hash 行），
// 所以不套 REDB# 命名。
func PlayerHistoryKey(id uint64) string {
	return fmt.Sprintf("playerhist:%d", id)
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

// SaveStats 建档（或整体覆写档案计数）。等级不落库，由 XP 派生。
func (s *PlayerStore) SaveStats(ctx context.Context, id uint64, stats PlayerStats) error {
	conn, err := s.pool.GetContext(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	row := &playerpb.DBUserProfile{
		Xp:            stats.XP,
		Kills:         int32(stats.Kills),
		Deaths:        int32(stats.Deaths),
		Matches:       int32(stats.Matches),
		Wins:          int32(stats.Wins),
		Losses:        int32(stats.Losses),
		SchemaVersion: playerpb.DBSchemaVersion_DB_SCHEMA_VERSION_CURRENT,
	}
	return row.SetFields(conn, playerProfileNamespace, id, 0)
}

// AddStats 用一条 MULTI/EXEC 累加六项计数：这里没有跨命令的读-改-写不变量，
// 纯计数累加不需要分布式锁（对比钱包扣款）。
func (s *PlayerStore) AddStats(ctx context.Context, id uint64, delta PlayerStatsDelta) error {
	conn, err := s.pool.GetContext(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	key := PlayerProfileKey(id)
	increments := []struct {
		field playerpb.FieldDBUserProfile
		delta int64
	}{
		{playerpb.FieldDBUserProfile_Xp, delta.XP},
		{playerpb.FieldDBUserProfile_Kills, delta.Kills},
		{playerpb.FieldDBUserProfile_Deaths, delta.Deaths},
		{playerpb.FieldDBUserProfile_Matches, delta.Matches},
		{playerpb.FieldDBUserProfile_Wins, delta.Wins},
		{playerpb.FieldDBUserProfile_Losses, delta.Losses},
	}
	if err := conn.Send("MULTI"); err != nil {
		return err
	}
	for _, inc := range increments {
		if err := conn.Send("HINCRBY", key, inc.field, inc.delta); err != nil {
			return err
		}
	}
	_, err = conn.Do("EXEC")
	return err
}

// GetStats 读取档案计数。第二个返回值为 false 表示还没建档。
func (s *PlayerStore) GetStats(ctx context.Context, id uint64) (PlayerStats, bool, error) {
	conn, err := s.pool.GetContext(ctx)
	if err != nil {
		return PlayerStats{}, false, err
	}
	defer conn.Close()
	exists, err := redis.Bool(conn.Do("EXISTS", PlayerProfileKey(id)))
	if err != nil || !exists {
		return PlayerStats{}, false, err
	}
	var row playerpb.DBUserProfile
	if err := row.GetFields(conn, playerProfileNamespace, id, 0); err != nil {
		return PlayerStats{}, false, err
	}
	return PlayerStats{
		XP:      row.Xp,
		Kills:   int64(row.Kills),
		Deaths:  int64(row.Deaths),
		Matches: int64(row.Matches),
		Wins:    int64(row.Wins),
		Losses:  int64(row.Losses),
	}, true, nil
}

// AppendMatchRecord 把一场战绩推到历史头部，并只保留最近 historyLimit 条。
// LPUSH + LTRIM 在一条 MULTI/EXEC 里，不会留下「推了但没截断」的中间态。
func (s *PlayerStore) AppendMatchRecord(ctx context.Context, id uint64, rec PlayerMatchRecord) error {
	conn, err := s.pool.GetContext(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	row := &playerpb.DBMatchRecord{
		MatchId:         rec.MatchID,
		Won:             rec.Won,
		Kills:           rec.Kills,
		Deaths:          rec.Deaths,
		OpponentKills:   rec.OpponentKills,
		DurationSeconds: rec.DurationSeconds,
		OpponentName:    rec.OpponentName,
		EndedAt:         rec.EndedAt,
	}
	blob, err := row.MarshalRedisProto()
	if err != nil {
		return err
	}
	key := PlayerHistoryKey(id)
	if err := conn.Send("MULTI"); err != nil {
		return err
	}
	if err := conn.Send("LPUSH", key, blob); err != nil {
		return err
	}
	if err := conn.Send("LTRIM", key, 0, historyLimit-1); err != nil {
		return err
	}
	_, err = conn.Do("EXEC")
	return err
}

// ListMatchRecords 按新在前的顺序读最近战绩。无历史返回空切片与 nil。
func (s *PlayerStore) ListMatchRecords(ctx context.Context, id uint64, limit int) ([]PlayerMatchRecord, error) {
	conn, err := s.pool.GetContext(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if limit <= 0 || limit > historyLimit {
		limit = historyLimit
	}
	blobs, err := redis.ByteSlices(conn.Do("LRANGE", PlayerHistoryKey(id), 0, limit-1))
	if err != nil {
		return nil, err
	}
	out := make([]PlayerMatchRecord, 0, len(blobs))
	for _, blob := range blobs {
		var row playerpb.DBMatchRecord
		if err := row.UnmarshalRedisProto(blob); err != nil {
			return nil, err
		}
		out = append(out, PlayerMatchRecord{
			MatchID:         row.MatchId,
			Won:             row.Won,
			Kills:           row.Kills,
			Deaths:          row.Deaths,
			OpponentKills:   row.OpponentKills,
			DurationSeconds: row.DurationSeconds,
			OpponentName:    row.OpponentName,
			EndedAt:         row.EndedAt,
		})
	}
	return out, nil
}

// UsernameByID 只读 account 服务的账号 Hash 取用户名：logic 从不写这个键，
// 只借用一次显示名（见 spec §4.2）。读不到就返回 ok=false，不当作错误。
func (s *PlayerStore) UsernameByID(ctx context.Context, id uint64) (string, bool, error) {
	conn, err := s.pool.GetContext(ctx)
	if err != nil {
		return "", false, err
	}
	defer conn.Close()
	name, err := redis.String(conn.Do("HGET", AccountKey(id), accountUsernameField))
	if err == redis.ErrNil {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return name, true, nil
}

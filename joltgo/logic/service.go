package logic

import (
	"context"
	"errors"
	"log"
	"strconv"
	"time"

	"joltgo/persist"
)

const (
	ReasonBadQuantity      = "bad_quantity"
	ReasonItemNotFound     = "item_not_found"
	ReasonInsufficientFund = "insufficient_funds"
	ReasonNotOwned         = "not_owned"
	ReasonNotEquippable    = "not_equippable"
	ReasonBusy             = "busy"
	ReasonUnauthenticated  = "unauthenticated"
	ReasonProfileMissing   = "profile_missing"
	ReasonInternal         = "internal"
	// ReasonNotEnoughPlayers 表示结算上报收到了，但槽位不齐（没有两名真实玩家），
	// 按规则不入账。兜底单人局已删除，这条是防御。
	ReasonNotEnoughPlayers = "not_enough_players"
)

type ReasonError struct {
	Reason string
}

func (e *ReasonError) Error() string { return e.Reason }

func reason(code string) error { return &ReasonError{Reason: code} }

func ReasonOf(err error) string {
	if err == nil {
		return ""
	}
	var target *ReasonError
	if errors.As(err, &target) {
		return target.Reason
	}
	return ReasonInternal
}

type Store interface {
	GetWallet(context.Context, uint64) (persist.PlayerWallet, bool, error)
	SaveWallet(context.Context, uint64, persist.PlayerWallet) error
	AddCoins(context.Context, uint64, int64) error
	DeleteWallet(context.Context, uint64) error
	GetBag(context.Context, uint64) (persist.PlayerBag, bool, error)
	SaveBag(context.Context, uint64, persist.PlayerBag) error
	DeleteBag(context.Context, uint64) error
	// 玩家档案（累计战绩）与最近对局历史
	GetStats(context.Context, uint64) (persist.PlayerStats, bool, error)
	SaveStats(context.Context, uint64, persist.PlayerStats) error
	AddStats(context.Context, uint64, persist.PlayerStatsDelta) error
	AppendMatchRecord(context.Context, uint64, persist.PlayerMatchRecord) error
	ListMatchRecords(context.Context, uint64, int) ([]persist.PlayerMatchRecord, error)
	UsernameByID(context.Context, uint64) (string, bool, error)
}

type Lock interface {
	Lock(context.Context) error
	Unlock(context.Context) error
	IsLost() bool
}

type LockFactory interface {
	New(accountID uint64) Lock
}

type ShopItem struct {
	ItemID        string
	DisplayName   string
	Price         int64
	EquipSlot     string
	OwnedQuantity int64
}

type State struct {
	Coins                 int64
	Items                 []ShopItem
	EquippedPrimaryWeapon string
}

type Service struct {
	store   Store
	catalog Catalog
	locks   LockFactory
	now     func() time.Time
}

func NewService(store Store, catalog Catalog, locks LockFactory) *Service {
	return &Service{store: store, catalog: catalog, locks: locks, now: time.Now}
}

func parseAccountID(raw string) (uint64, error) {
	if raw == "" {
		return 0, reason(ReasonUnauthenticated)
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		return 0, reason(ReasonUnauthenticated)
	}
	return id, nil
}

func (s *Service) EnsureProfile(ctx context.Context, accountID string) error {
	id, err := parseAccountID(accountID)
	if err != nil {
		return err
	}
	_, hasWallet, err := s.store.GetWallet(ctx, id)
	if err != nil {
		return err
	}
	_, hasBag, err := s.store.GetBag(ctx, id)
	if err != nil {
		return err
	}
	if hasWallet && hasBag {
		// 存量账号（redis-data 永不清空）只有钱包与背包，没有档案行：登录建档时
		// 顺带补一份零值档案，个人页就不会读到 profile_missing。
		if _, hasStats, err := s.store.GetStats(ctx, id); err != nil {
			return err
		} else if !hasStats {
			if err := s.store.SaveStats(ctx, id, persist.PlayerStats{}); err != nil {
				return err
			}
		}
		return nil
	}

	return s.withAccountLock(ctx, id, func(lock Lock) error {
		_, walletExists, err := s.store.GetWallet(ctx, id)
		if err != nil {
			return err
		}
		_, bagExists, err := s.store.GetBag(ctx, id)
		if err != nil {
			return err
		}
		if walletExists && bagExists {
			return nil
		}

		createdWallet := false
		if !walletExists {
			if lock.IsLost() {
				return errLockLost
			}
			if err := s.store.SaveWallet(ctx, id, persist.PlayerWallet{Coins: 1000}); err != nil {
				return err
			}
			createdWallet = true
		}
		if !bagExists {
			if lock.IsLost() {
				return errLockLost
			}
			if err := s.store.SaveBag(ctx, id, persist.PlayerBag{}); err != nil {
				if createdWallet {
					if lock.IsLost() {
						log.Printf("logic: skip wallet compensation for account %d after lock loss", id)
					} else if delErr := s.store.DeleteWallet(ctx, id); delErr != nil {
						log.Printf("logic: compensate wallet for account %d: %v", id, delErr)
					}
				}
				return err
			}
		}
		if _, statsExists, err := s.store.GetStats(ctx, id); err != nil {
			return err
		} else if !statsExists {
			if lock.IsLost() {
				return errLockLost
			}
			if err := s.store.SaveStats(ctx, id, persist.PlayerStats{}); err != nil {
				return err
			}
		}
		return nil
	})
}

func addItem(bag persist.PlayerBag, itemID string, quantity, acquiredAt int64) persist.PlayerBag {
	out := persist.PlayerBag{
		Items:                 append([]persist.PlayerItem(nil), bag.Items...),
		EquippedPrimaryWeapon: bag.EquippedPrimaryWeapon,
	}
	for i := range out.Items {
		if out.Items[i].ItemID == itemID {
			out.Items[i].Quantity += quantity
			return out
		}
	}
	out.Items = append(out.Items, persist.PlayerItem{
		ItemID: itemID, Quantity: quantity, AcquiredAt: acquiredAt,
	})
	return out
}

func hasItem(bag persist.PlayerBag, itemID string) bool {
	for _, item := range bag.Items {
		if item.ItemID == itemID && item.Quantity > 0 {
			return true
		}
	}
	return false
}

func (s *Service) compensateWallet(ctx context.Context, accountID uint64, delta int64) {
	compCtx, cancel := context.WithTimeout(context.Background(), compensateTimeout)
	defer cancel()
	if err := s.store.AddCoins(compCtx, accountID, delta); err != nil {
		log.Printf("logic: compensate wallet for account %d failed: %v", accountID, err)
	}
}

func (s *Service) Purchase(ctx context.Context, accountID, itemID string, quantity int32) (State, error) {
	id, err := parseAccountID(accountID)
	if err != nil {
		return State{}, err
	}
	if quantity < 1 || quantity > 99 {
		return State{}, reason(ReasonBadQuantity)
	}
	def, ok := s.catalog.Lookup(itemID)
	if !ok {
		return State{}, reason(ReasonItemNotFound)
	}

	wallet, ok, err := s.store.GetWallet(ctx, id)
	if err != nil {
		return State{}, err
	}
	if !ok {
		return State{}, reason(ReasonProfileMissing)
	}
	total := def.Price * int64(quantity)
	if wallet.Coins < total {
		return State{}, reason(ReasonInsufficientFund)
	}

	var out State
	committed := false
	err = s.withAccountLock(ctx, id, func(lock Lock) error {
		currentWallet, ok, err := s.store.GetWallet(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return reason(ReasonProfileMissing)
		}
		bag, ok, err := s.store.GetBag(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return reason(ReasonProfileMissing)
		}
		if currentWallet.Coins < total {
			return reason(ReasonInsufficientFund)
		}

		nextWallet := persist.PlayerWallet{Coins: currentWallet.Coins - total}
		if lock.IsLost() {
			return errLockLost
		}
		if err := s.store.SaveWallet(ctx, id, nextWallet); err != nil {
			return err
		}

		nextBag := addItem(bag, def.ID, int64(quantity), s.now().Unix())
		if lock.IsLost() {
			s.compensateWallet(ctx, id, total)
			return errLockLost
		}
		if err := s.store.SaveBag(ctx, id, nextBag); err != nil {
			s.compensateWallet(ctx, id, total)
			return err
		}
		committed = true
		out = s.stateFrom(nextWallet, nextBag)
		return nil
	})
	if err != nil {
		if committed && errors.Is(err, errLockLost) {
			return out, nil
		}
		return State{}, err
	}
	return out, nil
}

func (s *Service) State(ctx context.Context, accountID string) (State, error) {
	id, err := parseAccountID(accountID)
	if err != nil {
		return State{}, err
	}
	wallet, ok, err := s.store.GetWallet(ctx, id)
	if err != nil {
		return State{}, err
	}
	if !ok {
		return State{}, reason(ReasonProfileMissing)
	}
	bag, ok, err := s.store.GetBag(ctx, id)
	if err != nil {
		return State{}, err
	}
	if !ok {
		return State{}, reason(ReasonProfileMissing)
	}
	return s.stateFrom(wallet, bag), nil
}

func (s *Service) stateFrom(wallet persist.PlayerWallet, bag persist.PlayerBag) State {
	quantities := make(map[string]int64, len(bag.Items))
	for _, item := range bag.Items {
		if item.Quantity > 0 {
			quantities[item.ItemID] += item.Quantity
		}
	}
	defs := s.catalog.Definitions()
	items := make([]ShopItem, 0, len(defs))
	for _, def := range defs {
		items = append(items, ShopItem{
			ItemID:        def.ID,
			DisplayName:   def.DisplayName,
			Price:         def.Price,
			EquipSlot:     def.EquipSlot,
			OwnedQuantity: quantities[def.ID],
		})
	}
	return State{
		Coins:                 wallet.Coins,
		Items:                 items,
		EquippedPrimaryWeapon: bag.EquippedPrimaryWeapon,
	}
}

func (s *Service) Equip(ctx context.Context, accountID, itemID string) (State, error) {
	id, err := parseAccountID(accountID)
	if err != nil {
		return State{}, err
	}
	wallet, ok, err := s.store.GetWallet(ctx, id)
	if err != nil {
		return State{}, err
	}
	if !ok {
		return State{}, reason(ReasonProfileMissing)
	}
	bag, ok, err := s.store.GetBag(ctx, id)
	if err != nil {
		return State{}, err
	}
	if !ok {
		return State{}, reason(ReasonProfileMissing)
	}

	if itemID == "" {
		if bag.EquippedPrimaryWeapon == "" {
			return s.stateFrom(wallet, bag), nil
		}
	} else {
		def, found := s.catalog.Lookup(itemID)
		if !found {
			return State{}, reason(ReasonItemNotFound)
		}
		if def.EquipSlot == "" {
			return State{}, reason(ReasonNotEquippable)
		}
		if !hasItem(bag, itemID) {
			return State{}, reason(ReasonNotOwned)
		}
		if bag.EquippedPrimaryWeapon == itemID {
			return s.stateFrom(wallet, bag), nil
		}
	}

	var out State
	err = s.withAccountLock(ctx, id, func(lock Lock) error {
		currentWallet, ok, err := s.store.GetWallet(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return reason(ReasonProfileMissing)
		}
		currentBag, ok, err := s.store.GetBag(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return reason(ReasonProfileMissing)
		}

		if itemID == "" {
			if currentBag.EquippedPrimaryWeapon == "" {
				out = s.stateFrom(currentWallet, currentBag)
				return nil
			}
			currentBag.EquippedPrimaryWeapon = ""
		} else {
			def, found := s.catalog.Lookup(itemID)
			if !found {
				return reason(ReasonItemNotFound)
			}
			if def.EquipSlot == "" {
				return reason(ReasonNotEquippable)
			}
			if !hasItem(currentBag, itemID) {
				return reason(ReasonNotOwned)
			}
			if currentBag.EquippedPrimaryWeapon == itemID {
				out = s.stateFrom(currentWallet, currentBag)
				return nil
			}
			currentBag.EquippedPrimaryWeapon = itemID
		}

		if lock.IsLost() {
			return errLockLost
		}
		if err := s.store.SaveBag(ctx, id, currentBag); err != nil {
			return err
		}
		out = s.stateFrom(currentWallet, currentBag)
		return nil
	})
	if err != nil {
		return State{}, err
	}
	return out, nil
}

// Profile 是个人信息页要的一份档案快照：累计统计 + 最近对局历史。
type Profile struct {
	XP      int64
	Kills   int32
	Deaths  int32
	Matches int32
	Wins    int32
	Losses  int32
	Recent  []persist.PlayerMatchRecord
}

// MatchSlot 是一局里单个槽位的战绩（下标即 player_idx）。
type MatchSlot struct {
	UID    string
	Kills  int32
	Deaths int32
	// Left = true：这位玩家中途放弃了对局（uid 仍然是他的账号 ID）。
	// 与「空槽位」（UID=="" 且 Left=false）是两回事：前者说明这**本来就是一场双人局**、
	// 只是有一位不参与结算；后者说明这个槽位压根没有人。
	Left bool
}

// MatchResult 是一局的结算结果，由 game 在对局结束时上报。
type MatchResult struct {
	MatchID         string
	Slots           []MatchSlot
	WinnerSlot      int32
	DurationSeconds int32
}

// Profile 读取玩家档案。存量账号没有档案行（redis-data 永不清空，老账号一定缺），
// 所以缺行要**就地补建零值档案**再返回，而不是报 profile_missing。
func (s *Service) Profile(ctx context.Context, accountID string) (Profile, error) {
	id, err := parseAccountID(accountID)
	if err != nil {
		return Profile{}, err
	}
	stats, ok, err := s.store.GetStats(ctx, id)
	if err != nil {
		return Profile{}, err
	}
	if !ok {
		stats = persist.PlayerStats{}
		if err := s.store.SaveStats(ctx, id, stats); err != nil {
			return Profile{}, err
		}
	}
	recent, err := s.store.ListMatchRecords(ctx, id, 20)
	if err != nil {
		return Profile{}, err
	}
	return Profile{
		XP:      stats.XP,
		Kills:   int32(stats.Kills),
		Deaths:  int32(stats.Deaths),
		Matches: int32(stats.Matches),
		Wins:    int32(stats.Wins),
		Losses:  int32(stats.Losses),
		Recent:  recent,
	}, nil
}

// RecordMatch 把一局结果记进**仍在场**玩家的档案与历史，返回是否真的入账。
//
// 入账条件：每个槽位要么有真实 uid（在场），要么带着 left 标记（中途放弃了对局）。
// 放弃者本人跳过（spec：放弃对局的人不再参与这场对局的结算），对手照常入账 ——
// 他确实打完了一局，不能因为对手跑了就白打。反过来，**空槽位而没有 left 标记**的
// 说明这压根不是一场双人局（练习/调试入口那种），照旧不入账：兜底单人局已删除，
// 这条是防御，不至于把「刷木桩」记成胜率。
//
// 上报方不重试，所以这里任何一步失败都只记日志（没有补偿机会），不把错误抛回去。
func (s *Service) RecordMatch(ctx context.Context, res MatchResult) (bool, error) {
	if len(res.Slots) < 2 {
		return false, nil
	}
	present := 0
	for slot := range res.Slots {
		if res.Slots[slot].UID == "" {
			if !res.Slots[slot].Left {
				return false, nil // 空槽位且不是「放弃」= 这一局本来就不是两个人的
			}
			continue
		}
		if !res.Slots[slot].Left {
			present++
		}
	}
	if present == 0 {
		// 双方都放弃了对局：没有人的档案要更新（也算「未入账」，不是错误）。
		return false, nil
	}
	if int(res.WinnerSlot) < 0 || int(res.WinnerSlot) >= len(res.Slots) {
		return false, reason(ReasonInternal)
	}

	for slot := range res.Slots {
		me := res.Slots[slot]
		if me.Left || me.UID == "" {
			continue // 放弃对局的玩家不进结算（不计战绩、不写历史）
		}
		opp := res.Slots[1-slot]
		id, err := parseAccountID(me.UID)
		if err != nil {
			return false, err
		}
		won := int32(slot) == res.WinnerSlot
		delta := persist.PlayerStatsDelta{
			XP:      MatchXP(won, me.Kills),
			Kills:   int64(me.Kills),
			Deaths:  int64(me.Deaths),
			Matches: 1,
		}
		if won {
			delta.Wins = 1
		} else {
			delta.Losses = 1
		}
		if err := s.store.AddStats(ctx, id, delta); err != nil {
			log.Printf("logic: record match %s stats for %s failed: %v", res.MatchID, me.UID, err)
			continue
		}
		opponentName := ""
		if oppID, err := parseAccountID(opp.UID); err == nil {
			if name, found, err := s.store.UsernameByID(ctx, oppID); err != nil {
				log.Printf("logic: resolve username for %s failed: %v", opp.UID, err)
			} else if found {
				opponentName = name
			}
		}
		rec := persist.PlayerMatchRecord{
			MatchID:         res.MatchID,
			Won:             won,
			Kills:           me.Kills,
			Deaths:          me.Deaths,
			OpponentKills:   opp.Kills,
			DurationSeconds: res.DurationSeconds,
			OpponentName:    opponentName,
			EndedAt:         s.now().Unix(),
		}
		if err := s.store.AppendMatchRecord(ctx, id, rec); err != nil {
			log.Printf("logic: append match history for %s failed: %v", me.UID, err)
		}
	}
	return true, nil
}

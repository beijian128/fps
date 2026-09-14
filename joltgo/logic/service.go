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
	DeleteWallet(context.Context, uint64) error
	GetBag(context.Context, uint64) (persist.PlayerBag, bool, error)
	SaveBag(context.Context, uint64, persist.PlayerBag) error
	DeleteBag(context.Context, uint64) error
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
					if delErr := s.store.DeleteWallet(ctx, id); delErr != nil {
						log.Printf("logic: compensate wallet for account %d: %v", id, delErr)
					}
				}
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

func (s *Service) compensateWallet(ctx context.Context, accountID uint64, wallet persist.PlayerWallet) {
	compCtx, cancel := context.WithTimeout(context.Background(), compensateTimeout)
	defer cancel()
	if err := s.store.SaveWallet(compCtx, accountID, wallet); err != nil {
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
			s.compensateWallet(ctx, id, currentWallet)
			return errLockLost
		}
		if err := s.store.SaveBag(ctx, id, nextBag); err != nil {
			s.compensateWallet(ctx, id, currentWallet)
			return err
		}
		if lock.IsLost() {
			return errLockLost
		}
		out = s.stateFrom(nextWallet, nextBag)
		return nil
	})
	if err != nil {
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

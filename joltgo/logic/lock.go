package logic

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/beijian128/distlock"
	"github.com/redis/go-redis/v9"
)

const (
	lockTTL           = 10 * time.Second
	lockRetry         = 10 * time.Millisecond
	lockWait          = 2 * time.Second
	unlockTimeout     = 1 * time.Second
	compensateTimeout = time.Second
)

func AccountLockKey(accountID uint64) string {
	return fmt.Sprintf("user:lock:%d", accountID)
}

type RedisLockFactory struct {
	client redis.UniversalClient
}

func NewRedisLockFactory(client redis.UniversalClient) *RedisLockFactory {
	return &RedisLockFactory{client: client}
}

func (f *RedisLockFactory) New(accountID uint64) Lock {
	key := AccountLockKey(accountID)
	return distlock.New(f.client, key,
		distlock.WithTTL(lockTTL),
		distlock.WithRetryInterval(lockRetry),
		distlock.WithOnLost(func() { log.Printf("logic: lock %s lost", key) }),
	)
}

var errLockLost = errors.New("logic: lock lost")

func (s *Service) withAccountLock(ctx context.Context, accountID uint64, fn func(Lock) error) error {
	lock := s.locks.New(accountID)
	lockCtx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	if err := lock.Lock(lockCtx); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return reason(ReasonBusy)
		}
		return err
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), unlockTimeout)
		defer cancel()
		if err := lock.Unlock(unlockCtx); err != nil && !errors.Is(err, distlock.ErrNotOwned) {
			log.Printf("logic: unlock account %d: %v", accountID, err)
		}
	}()
	if lock.IsLost() {
		return errLockLost
	}
	if err := fn(lock); err != nil {
		return err
	}
	if lock.IsLost() {
		return errLockLost
	}
	return nil
}

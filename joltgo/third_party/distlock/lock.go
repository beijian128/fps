// Package distlock 实现了一个基于 Redis 的分布式锁。
//
// 特性：
//   - 原子加锁/释放：全部通过 Lua 脚本在 Redis 端原子执行，无竞态
//   - 可重入：同一实例重复加锁为计数，需同样次数解锁
//   - 看门狗：持有期间自动续期，业务未结束锁不会过期
//   - 安全释放：校验持有者 token 后才删除，绝不误删他人锁
//   - 阻塞获取：Lock(ctx) 支持超时与取消
//
// 用法：
//
//	l := distlock.New(client, "order:123:lock", distlock.WithTTL(30*time.Second))
//	if err := l.Lock(ctx); err != nil { ... }
//	defer l.Unlock(ctx)
package distlock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// defaultTTL 锁的默认过期时间。看门狗会持续续期，
	// 该值同时是"持有者彻底宕机后锁多久自动释放"的上限。
	defaultTTL = 30 * time.Second
	// defaultRetryInterval Lock 轮询尝试加锁的间隔。
	defaultRetryInterval = 50 * time.Millisecond
	// renewTimeout 看门狗单次续期调用允许的最长耗时。
	renewTimeout = 5 * time.Second
)

// ErrNotOwned 表示当前实例并不持有该锁（从未获取、已释放、已丢失或被他人获取）。
var ErrNotOwned = errors.New("distlock: lock is not held by this instance")

// Lock 是一个 Redis 分布式锁实例。
//
// 每个实例持有唯一 token：不同实例之间互斥；同一实例的重复加锁为可重入。
// 锁在 Redis 中存储为 hash：field 为 token，value 为当前持有深度。
// 所有字段的读写由 mu 保护，看门狗 goroutine 与业务 goroutine 通过
// stopCh 和 mu 协作，保证 Lock 实例可被多个 goroutine 并发使用。
type Lock struct {
	client redis.UniversalClient
	key    string
	token  string

	ttl           time.Duration // 锁的过期时间，看门狗定期续期
	retryInterval time.Duration // Lock 轮询间隔
	watchdog      bool          // 是否开启自动续期
	onLost        func()        // 锁丢失（续期失败）时的回调，最多调用一次

	mu      sync.Mutex
	depth   int  // 本地记录的持有深度，与 Redis 中的计数保持一致
	lost    bool // 锁已被判定丢失
	stopCh  chan struct{}
	lostOne sync.Once
}

// New 创建一个锁实例。key 为锁名，同一把锁的 key 必须一致。
func New(client redis.UniversalClient, key string, opts ...Option) *Lock {
	if client == nil {
		panic("distlock: nil redis client")
	}
	l := &Lock{
		client:        client,
		key:           key,
		token:         newToken(),
		ttl:           defaultTTL,
		retryInterval: defaultRetryInterval,
		watchdog:      true,
	}
	for _, o := range opts {
		o(l)
	}
	return l
}

// TryLock 尝试获取一次锁，不等待。返回 true 表示获取成功，
// false 表示锁正被其他实例持有。
func (l *Lock) TryLock(ctx context.Context) (bool, error) {
	ok, err := l.acquire(ctx)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	l.mu.Lock()
	l.depth++
	first := l.depth == 1
	l.mu.Unlock()
	if first {
		l.startWatchdog()
	}
	return true, nil
}

// Lock 阻塞获取锁，直到成功或 ctx 被取消/超时。
// 加锁失败（锁被占用）会按 retryInterval 轮询重试；
// Redis 自身报错（如网络故障）则直接返回，不重试。
func (l *Lock) Lock(ctx context.Context) error {
	for {
		ok, err := l.TryLock(ctx)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		timer := time.NewTimer(l.retryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Unlock 释放锁。可重入时递减计数，深度归零才真正删除。
// 若返回 ErrNotOwned，说明锁已不在本实例名下（已过期、已被他人获取），
// 此时无需也不应再调用 Unlock。
func (l *Lock) Unlock(ctx context.Context) error {
	l.mu.Lock()
	if l.depth <= 0 {
		l.mu.Unlock()
		return ErrNotOwned
	}
	l.depth--
	last := l.depth == 0
	l.mu.Unlock()

	if last {
		l.stopWatchdog()
	}
	// 每次解锁都调用释放脚本递减 Redis 计数，保证本地深度与 Redis 计数同步；
	// 脚本返回 releasedPart（可重入递减后仍持有）同样视为成功。
	res, err := l.release(ctx)
	if err != nil {
		// 释放命令失败：看门狗已停止，锁会在 TTL 后自动过期，不会死锁。
		return err
	}
	switch res {
	case releasedFully, releasedPart:
		return nil
	default:
		return ErrNotOwned
	}
}

// Key 返回锁名。
func (l *Lock) Key() string { return l.key }

// Token 返回本实例的持有者标识，可用于日志排查和业务侧 fencing token 校验。
func (l *Lock) Token() string { return l.token }

// IsHeld 返回本地视角下是否仍持有该锁。
func (l *Lock) IsHeld() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.depth > 0
}

// IsLost 返回锁是否已被判定丢失（看门狗续期发现锁不存在）。
func (l *Lock) IsLost() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lost
}

// startWatchdog 启动自动续期 goroutine。仅在首次持锁时调用。
func (l *Lock) startWatchdog() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.watchdog || l.stopCh != nil {
		return
	}
	stop := make(chan struct{})
	l.stopCh = stop
	go l.watchLoop(stop)
}

// stopWatchdog 停止续期 goroutine。锁被完全释放或丢失时调用。
func (l *Lock) stopWatchdog() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopCh == nil {
		return
	}
	close(l.stopCh)
	l.stopCh = nil
}

// watchLoop 按 ttl/3 的间隔续期，保证单次续期失败也不会在
// 下一轮续期前过期。锁在 Redis 端消失（丢失）时触发 markLost 后退出。
func (l *Lock) watchLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(l.ttl / 3)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), renewTimeout)
		ok, err := l.renew(ctx)
		cancel()
		if err != nil {
			// 网络抖动等瞬时错误：下一轮继续尝试，不中断续期。
			continue
		}
		if !ok {
			// 续期失败可能恰逢用户主动释放（释放命令已删除 key），
			// 此时不视为丢失，直接退出即可。
			l.mu.Lock()
			held := l.depth > 0 && l.stopCh == stop
			l.mu.Unlock()
			if !held {
				return
			}
			l.markLost()
			return
		}
	}
}

// markLost 标记锁丢失：清零本地状态并通知 onLost 回调。
func (l *Lock) markLost() {
	l.lostOne.Do(func() {
		l.mu.Lock()
		if l.stopCh != nil {
			close(l.stopCh)
			l.stopCh = nil
		}
		l.lost = true
		l.depth = 0
		l.mu.Unlock()
		if l.onLost != nil {
			l.onLost()
		}
	})
}

// newToken 生成唯一持有者标识（128 位随机数，十六进制）。
func newToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("distlock: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}

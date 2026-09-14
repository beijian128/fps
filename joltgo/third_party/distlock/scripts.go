package distlock

import (
	"context"

	"github.com/redis/go-redis/v9"
)

// 释放锁脚本的返回码。
const (
	releasedFully = 1  // 深度归零，key 已删除
	releasedPart  = 0  // 可重入递减后仍持有
	notOwned      = -1 // key 不存在或非本实例持有
)

// acquireLua 原子加锁：
//   - key 不存在：写入 token=1 并设置过期时间，获得锁
//   - key 存在且 field 为本实例 token：深度 +1（可重入），刷新过期时间
//   - 否则：锁被他人持有，返回 0
//
// 整个脚本在 Redis 端原子执行，不存在"检查与写入之间被插队"的竞态。
const acquireLua = `
if redis.call('exists', KEYS[1]) == 0 then
	redis.call('hset', KEYS[1], ARGV[1], 1)
	redis.call('pexpire', KEYS[1], ARGV[2])
	return 1
elseif redis.call('hexists', KEYS[1], ARGV[1]) == 1 then
	redis.call('hincrby', KEYS[1], ARGV[1], 1)
	redis.call('pexpire', KEYS[1], ARGV[2])
	return 1
else
	return 0
end
`

// releaseLua 原子释放：深度减一，归零才删除 key。
// 只操作本实例 token 对应的 field，天然防止误删他人锁。
const releaseLua = `
if redis.call('hexists', KEYS[1], ARGV[1]) == 1 then
	local n = redis.call('hincrby', KEYS[1], ARGV[1], -1)
	if n <= 0 then
		redis.call('del', KEYS[1])
		return 1
	end
	redis.call('pexpire', KEYS[1], ARGV[2])
	return 0
else
	return -1
end
`

// renewLua 原子续期：仅当锁仍在本实例名下时刷新过期时间。
// 若锁已丢失（过期/被删），返回 0，看门狗据此判定丢失。
const renewLua = `
if redis.call('hexists', KEYS[1], ARGV[1]) == 1 then
	redis.call('pexpire', KEYS[1], ARGV[2])
	return 1
else
	return 0
end
`

var (
	acquireScript = redis.NewScript(acquireLua)
	releaseScript = redis.NewScript(releaseLua)
	renewScript   = redis.NewScript(renewLua)
)

func (l *Lock) acquire(ctx context.Context) (bool, error) {
	n, err := acquireScript.Run(ctx, l.client, []string{l.key}, l.token, l.ttl.Milliseconds()).Int()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func (l *Lock) release(ctx context.Context) (int, error) {
	return releaseScript.Run(ctx, l.client, []string{l.key}, l.token, l.ttl.Milliseconds()).Int()
}

func (l *Lock) renew(ctx context.Context) (bool, error) {
	n, err := renewScript.Run(ctx, l.client, []string{l.key}, l.token, l.ttl.Milliseconds()).Int()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

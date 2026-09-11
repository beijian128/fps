// queue.go 是配对队列：ZSET(member=uid, score=入队毫秒时间戳)。
//
// 为什么在 Redis 而不是进程内切片：多个 match 节点各有一份队列的话，两个玩家
// 大概率落到不同节点，各自队列永远凑不满 2 人，双双等满兜底超时、各开一局。
// 放进 Redis 后所有 match 节点共享同一个队列，节点本身无状态、可水平扩容。
//
// 为什么用 ZSET 而不是 LIST：按 uid 去重（重连再 ZADD 就是更新 score，天然的
// 「挤掉旧的那条」）与按等待时长排序（score 就是入队时间）都是免费的。
//
// 时间戳一律取 **Redis 服务端时间**（脚本里的 redis.call('TIME')），不用各节点
// 的 time.Now()：否则「等了 10 秒」的定义会随节点时钟偏移而变 —— 一个快 15 秒
// 的节点会把刚入队的人直接拿去做单人开局。多节点共享同一个时钟是这里的关键。
package match

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// queueKey 是配对队列的键。
const queueKey = "match:queue"

// enqueueScript 入队并返回入队时刻（毫秒，取自 Redis 服务端时钟）。
// 同一个 uid 重复入队只更新 score，即**重新计时** —— 排队期间断线重连会被视为
// 重新排队，这是有意的（他确实刚刚才回来）。
var enqueueScript = redis.NewScript(`
local t = redis.call('TIME')
local ms = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
redis.call('ZADD', KEYS[1], ms, ARGV[1])
return ms
`)

// pairScript 原子地取出最早的两个排队者。
//
// 必须先判人数再 ZPOPMIN：不足 2 人时直接 ZPOPMIN 会把人白白弹出队列，
// 弹出来又凑不齐只能丢回去 —— 丢回等于重置等待时间，反复发生会让排队者
// 永远等不到开局。
var pairScript = redis.NewScript(`
if redis.call('ZCARD', KEYS[1]) < 2 then return {} end
return redis.call('ZPOPMIN', KEYS[1], 2)
`)

// staleScript 原子地取出「最早且已等待超过 ARGV[1] 毫秒」的那一个（单人兜底）。
//
// 用 ZRANGEBYSCORE + ZREM 而不是 ZPOPMIN：要取的是**最早且已超时**的，
// 而不是单纯最早的 —— 后者会把一个刚入队的人拿去单人开局。
var staleScript = redis.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local cutoff = now - tonumber(ARGV[1])
local r = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', cutoff, 'LIMIT', 0, 1)
if #r == 0 then return nil end
redis.call('ZREM', KEYS[1], r[1])
return r[1]
`)

// Queue 是 Redis 上的配对队列。
type Queue struct {
	rdb *redis.Client
}

// NewQueue 构造队列。
func NewQueue(rdb *redis.Client) *Queue { return &Queue{rdb: rdb} }

// Enqueue 入队（时间戳用 Redis 服务端时钟）。
func (q *Queue) Enqueue(ctx context.Context, uid string) error {
	return enqueueScript.Run(ctx, q.rdb, []string{queueKey}, uid).Err()
}

// PopPair 原子取出最早的两个排队者。返回空切片表示当前不足 2 人。
func (q *Queue) PopPair(ctx context.Context) ([]string, error) {
	res, err := pairScript.Run(ctx, q.rdb, []string{queueKey}).Result()
	if err != nil {
		return nil, err
	}
	items, ok := res.([]interface{})
	if !ok || len(items) == 0 {
		return nil, nil
	}
	// ZPOPMIN 在 Lua 里返回扁平的 [member, score, member, score, ...]，
	// 所以每隔一个取。
	uids := make([]string, 0, len(items)/2)
	for i := 0; i < len(items); i += 2 {
		if s, ok := items[i].(string); ok {
			uids = append(uids, s)
		}
	}
	return uids, nil
}

// PopStale 原子取出「最早且已等待超过 timeout」的那一个排队者（单人兜底）。
// 返回空串表示当前没有超时的排队者。
func (q *Queue) PopStale(ctx context.Context, timeout time.Duration) (string, error) {
	res, err := staleScript.Run(ctx, q.rdb, []string{queueKey}, timeout.Milliseconds()).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	s, _ := res.(string)
	return s, nil
}

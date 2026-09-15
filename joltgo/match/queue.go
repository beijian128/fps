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
	"log"

	"github.com/redis/go-redis/v9"
)

// queueKey 是配对队列的键。
const queueKey = "match:queue"

// enqueueScript 入队，score 取 Redis 服务端时钟（毫秒）。
//
// **NX**：同一 uid 重复入队是空操作，不刷新 score。客户端的 match.join 在等待期每
// 15 秒静默重发一次（防「服务端静默丢了我」），覆盖式 ZADD 会把这 15 秒的节奏写进
// 入队时间，服务端算出来的「已等待」就永远在 0–15 秒之间跳；改 NX 后重发幂等，
// 等待时长在断线重连后也保持连续。取消匹配走 ZREM，所以重新排队会拿到新时间戳。
//
// 必须显式 `return 1`：Lua 脚本没有返回值时，服务端应答是 nil bulk，go-redis
// 会把它报成 redis.Nil，于是「正常返回」与「真出错」在调用方看来一模一样。
// 而 Enqueue 的错误判断是四条重新入队路径的承重逻辑（Redis/NATS 抖动时必须把
// 玩家放回队列，绝不能静默丢掉），错判的后果正是玩家无声消失。
var enqueueScript = redis.NewScript(`
local t = redis.call('TIME')
local ms = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
redis.call('ZADD', KEYS[1], 'NX', ms, ARGV[1])
return 1
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

// QueueEntry 是队列里一员的等待状态。
type QueueEntry struct {
	UID           string
	WaitedSeconds int32
}

// Snapshot 返回队列总人数与逐人等待时长（按入队时间升序）。
//
// 时间基准用 Redis 服务端时钟，与入队保持一致 —— 多节点共享同一个时钟，
// 否则「等了 8 秒」的定义会随节点时钟偏移而变。
func (q *Queue) Snapshot(ctx context.Context) (int, []QueueEntry, error) {
	members, err := q.rdb.ZRangeWithScores(ctx, queueKey, 0, -1).Result()
	if err != nil {
		return 0, nil, err
	}
	nowMillis, err := q.serverMillis(ctx)
	if err != nil {
		return 0, nil, err
	}
	entries := make([]QueueEntry, 0, len(members))
	for _, z := range members {
		uid, ok := z.Member.(string)
		if !ok {
			continue
		}
		waited := nowMillis - int64(z.Score)
		if waited < 0 {
			waited = 0
		}
		entries = append(entries, QueueEntry{UID: uid, WaitedSeconds: int32(waited / 1000)})
	}
	return len(entries), entries, nil
}

// serverMillis 读 Redis 服务端时钟（毫秒）。等待时长的定义必须与入队用同一个时钟。
func (q *Queue) serverMillis(ctx context.Context) (int64, error) {
	t, err := q.rdb.Time(ctx).Result()
	if err != nil {
		return 0, err
	}
	return t.UnixMilli(), nil
}

// Remove 把 uid 移出队列，返回是否真的移除了（false = 本来就不在队列里，可能是刚被
// 别人配对走，调用方应据此去查回局）。
func (q *Queue) Remove(ctx context.Context, uid string) (bool, error) {
	n, err := q.rdb.ZRem(ctx, queueKey, uid).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// QueueBots 把 n 个机器人加入队列，返回实际成功入队的数量。
//
// 机器人与真人走**同一条**队列、同一个 Lua 脚本：它是队列里的普通成员，去重与
// 按等待时长排序的语义完全一致。uid 由 newUID 提供（调用方决定怎么生成），
// 本方法只负责「生成 + 入队 + 数成功了几个」。
//
// 单个入队失败只记日志、不中断：调用方（GM 页面）没有补偿机会，返回的数量必须
// 诚实 —— 说入队了 3 个实际只有 2 个，比直接报错更难排查。
// 入队时间戳仍由脚本里的 Redis 服务端时钟决定，机器人也不例外。
func (q *Queue) QueueBots(ctx context.Context, n int, newUID func() string) (int, error) {
	if n <= 0 || newUID == nil {
		return 0, nil
	}
	queued := 0
	for i := 0; i < n; i++ {
		uid := newUID()
		if err := q.Enqueue(ctx, uid); err != nil {
			log.Printf("match: enqueue bot %s failed: %v", uid, err)
			continue
		}
		queued++
	}
	return queued, nil
}

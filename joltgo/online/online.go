// Package online 维护「账号当前在线于哪个 gate」这一条登记。
//
// 三个角色用它：gate 在会话绑定/断开时写，account 在顶号时读（定点踢旧连接），
// match 在开局前读（定点请该 gate 写会话数据）。
//
// 它是 best-effort 的：读不到只会降级（少踢一次 / 该玩家被当作掉线剔除），
// 正确性由凭证轮换兜底 —— 被顶掉的客户端拿着已作废的 token，重连也 resume 不回来。
package online

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// TTL 给得很宽（与凭证同量级）。陈旧条目无害：它指向一个已经没有该会话的 gate，
// 定点踢过去会得到 ErrSessionNotFound 并被忽略。真正有害的是「有会话却没登记」——
// 那会让顶号踢不掉、match 的探活把活人当死人，所以宁可留久一点。
const TTL = 24 * time.Hour

// Store 是会话归属的读写层。
type Store struct {
	rdb *redis.Client
}

// NewStore 构造 Store。
func NewStore(rdb *redis.Client) *Store { return &Store{rdb: rdb} }

func key(accountID string) string { return "online:" + accountID }

// Set 登记账号当前在线的 gate 节点（覆盖写：换设备登录就是换个 gate）。
func (s *Store) Set(ctx context.Context, accountID, gateID string) error {
	return s.rdb.Set(ctx, key(accountID), gateID, TTL).Err()
}

// Clear 清除登记（连接断开时调用）。
func (s *Store) Clear(ctx context.Context, accountID string) error {
	return s.rdb.Del(ctx, key(accountID)).Err()
}

// Gate 返回账号当前在线的 gate 节点 id。第二个返回值语义上是「找到了吗」，
// 这里用空串表示未知：调用方对「没登记」与「登记为空」的处理完全一样。
func (s *Store) Gate(ctx context.Context, accountID string) (string, error) {
	v, err := s.rdb.Get(ctx, key(accountID)).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return v, nil
}

package account

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// 键空间与参数。集中在这里，避免键名散落在各处拼字符串。
const (
	keySeq = "acct:seq"

	keySessTTL = 7 * 24 * time.Hour // 凭证有效期（每次 resume 续满）
	keyRateTTL = time.Minute        // 限流窗口

	// placeholderTTL 是占名占位符的存活时间。占名是两步的（先占位、再回填
	// accountID），若中途失败且补偿的 DEL 也失败（多数时候是同一个原因：
	// Redis 断连），占位符会留在那里 —— 这个名字就再也注册不了也别想登录
	// （SETNX 说它存在，LookupByName 说它不存在）。带 TTL 就能自愈。
	placeholderTTL = 30 * time.Second

	// RateLimit 是每个用户名在 keyRateTTL 窗口内的最大尝试次数。
	//
	// 按**用户名**而不是 IP：account 服务拿不到客户端 IP —— 后端 agent 是
	// pitaya 的 Remote，RemoteAddr() 返回 nil，会话上也没有 frontendID 的
	// getter。按用户名限流其实比按 IP 更抗分布式暴力破解（换 IP 绕不开），
	// 代价是攻击者可以持续刷某个用户名、把他挡在门外（比「账号锁定」轻得多：
	// 只有一分钟窗口，且不影响已登录的会话）。
	RateLimit = 10
)

func keyName(username string) string { return "acct:name:" + NormalizeUsername(username) }
func keyAcct(id string) string       { return "acct:" + id }
func keySess(token string) string    { return "sess:" + token }
func keySessAcct(id string) string   { return "sess:acct:" + id }
func keyRate(username string) string { return "rl:user:" + NormalizeUsername(username) }

// ErrNameTaken 表示用户名已被占用。
var ErrNameTaken = errors.New("account: name taken")

// Account 是一个账号的持久数据（凭证不入此结构，见 keySess）。
type Account struct {
	ID        string
	Username  string // 保留用户输入的原始大小写，供 UI 显示
	PassHash  string // bcrypt
	CreatedAt int64
}

// Store 是账号数据的 Redis 读写层：只做键的存取，不做规则判断
// （格式校验、错误码映射、顶号决策都在 component.go）。
type Store struct {
	rdb *redis.Client
}

// NewStore 构造 Store。
func NewStore(rdb *redis.Client) *Store { return &Store{rdb: rdb} }

// Create 占名并创建账号，返回分配到的 accountID（十进制字符串）。
//
// 用 SETNX 占名而不是「先 GET 再 SET」：多节点/多请求并发下只有 SETNX 能保证
// 只有一个成功。占名键先写空串、拿到 id 后再回填 —— 中间态的空值在
// LookupByName 里被当作「不存在」，不会被误当成有效账号。
//
// 任一步失败都回滚占名，否则会留下「名字被占了、却没有任何账号」的僵尸名。
func (s *Store) Create(ctx context.Context, username, passHash string) (string, error) {
	nameKey := keyName(username)
	ok, err := s.rdb.SetNX(ctx, nameKey, "", placeholderTTL).Result()
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrNameTaken
	}

	n, err := s.rdb.Incr(ctx, keySeq).Result()
	if err != nil {
		_ = s.rdb.Del(ctx, nameKey).Err()
		return "", err
	}
	id := strconv.FormatInt(n, 10)

	if err := s.rdb.HSet(ctx, keyAcct(id), map[string]any{
		"username":   username,
		"pass_hash":  passHash,
		"created_at": time.Now().Unix(),
	}).Err(); err != nil {
		_ = s.rdb.Del(ctx, nameKey).Err()
		return "", err
	}

	if err := s.rdb.Set(ctx, nameKey, id, 0).Err(); err != nil {
		_ = s.rdb.Del(ctx, nameKey).Err()
		_ = s.rdb.Del(ctx, keyAcct(id)).Err()
		return "", err
	}
	return id, nil
}

// LookupByName 按用户名查 accountID（大小写不敏感）。第二个返回值表示是否存在。
func (s *Store) LookupByName(ctx context.Context, username string) (string, bool, error) {
	id, err := s.rdb.Get(ctx, keyName(username)).Result()
	if err == redis.Nil {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if id == "" {
		return "", false, nil // 占名事务进行中
	}
	return id, true, nil
}

// GetAccount 读账号数据。第二个返回值表示是否存在。
func (s *Store) GetAccount(ctx context.Context, id string) (Account, bool, error) {
	m, err := s.rdb.HGetAll(ctx, keyAcct(id)).Result()
	if err != nil {
		return Account{}, false, err
	}
	if len(m) == 0 {
		return Account{}, false, nil
	}
	created, _ := strconv.ParseInt(m["created_at"], 10, 64)
	return Account{
		ID:        id,
		Username:  m["username"],
		PassHash:  m["pass_hash"],
		CreatedAt: created,
	}, true, nil
}

// IssueToken 签发新凭证，并让该账号的旧凭证立即失效（单会话强制）。
//
// 「轮换」是顶号的**权威**手段：被顶掉的客户端拿着已删除的 token，即使重连
// 也 resume 不回来、只能回到登录面板。定点踢（见 online 包）只负责让它及时
// 闭嘴，不负责正确性。
func (s *Store) IssueToken(ctx context.Context, accountID string) (string, error) {
	old, err := s.rdb.Get(ctx, keySessAcct(accountID)).Result()
	if err != nil && err != redis.Nil {
		return "", err
	}

	token, err := NewToken()
	if err != nil {
		return "", err
	}

	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, keySess(token), accountID, keySessTTL)
	pipe.Set(ctx, keySessAcct(accountID), token, keySessTTL)
	if old != "" {
		pipe.Del(ctx, keySess(old))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return "", err
	}
	return token, nil
}

// ResolveToken 查凭证对应的 accountID 并续期（活跃用户凭证不过期）。
// 第二个返回值表示凭证是否有效。
func (s *Store) ResolveToken(ctx context.Context, token string) (string, bool, error) {
	id, err := s.rdb.Get(ctx, keySess(token)).Result()
	if err == redis.Nil {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	// 续期失败不当作认证失败：凭证本身是有效的，只是 TTL 没续上。
	//
	// 两个键都要续：只续 sess:{token} 的话，活跃账号的 sess:acct:{id} 指针会在
	// 第 7 天过期，下一次 IssueToken 就读不到旧 token、跳过删除 —— 轮换
	// （单会话强制的权威手段）从此静默失效。
	pipe := s.rdb.TxPipeline()
	pipe.Expire(ctx, keySess(token), keySessTTL)
	pipe.Expire(ctx, keySessAcct(id), keySessTTL)
	_, _ = pipe.Exec(ctx)
	return id, true, nil
}

// CurrentToken 返回账号当前的凭证（没有则返回空串）。
//
// 用于「同一连接上重复登录」的幂等返回：那时会话已经绑定，不能再次轮换 ——
// 那会把上一次刚签发、客户端正在使用的凭证删掉。
func (s *Store) CurrentToken(ctx context.Context, accountID string) (string, error) {
	tok, err := s.rdb.Get(ctx, keySessAcct(accountID)).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return tok, nil
}

// RevokeToken 删除凭证（登出）。幂等：凭证不存在也算成功。
func (s *Store) RevokeToken(ctx context.Context, token string) error {
	id, err := s.rdb.Get(ctx, keySess(token)).Result()
	if err == redis.Nil {
		return nil
	}
	if err != nil {
		return err
	}
	pipe := s.rdb.TxPipeline()
	pipe.Del(ctx, keySess(token))
	pipe.Del(ctx, keySessAcct(id))
	_, err = pipe.Exec(ctx)
	return err
}

// Allow 是登录限流：同一用户名在 keyRateTTL 窗口内最多 RateLimit 次。
// 返回 false 表示超限，调用方应回 rate_limited 而不是继续校验密码。
func (s *Store) Allow(ctx context.Context, username string) (bool, error) {
	k := keyRate(username)
	n, err := s.rdb.Incr(ctx, k).Result()
	if err != nil {
		return false, err
	}
	if n == 1 {
		if err := s.rdb.Expire(ctx, k, keyRateTTL).Err(); err != nil {
			return false, err
		}
	}
	return n <= RateLimit, nil
}

package account

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/beijian128/distlock"
	"github.com/redis/go-redis/v9"
	"joltgo/persist"
)

// 键空间与参数。集中在这里，避免键名散落在各处拼字符串。
const (
	keySeq        = "acct:seq"
	keySessPrefix = "sess:" // 凭证键 sess:{token} 的前缀（Lua 脚本要用它拼旧键）

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

	// 一次注册提交（占名、分配 ID、写账号 Hash、落名字映射）是一个多命令
	// 业务事务。单机 Redis 不会穿插执行，但多个 account 节点会并发；用分布式
	// 锁把同名注册串行化。TTL 只覆盖进程崩溃后的自动恢复窗口，看门狗会续期。
	createLockTTL      = 10 * time.Second
	createLockRetry    = 10 * time.Millisecond
	createUnlockBudget = time.Second
)

func keyName(username string) string { return "acct:name:" + NormalizeUsername(username) }
func keySess(token string) string    { return keySessPrefix + token }
func keySessAcct(id string) string   { return "sess:acct:" + id }
func keyRate(username string) string { return "rl:user:" + NormalizeUsername(username) }

func keyCreateLock(username string) string {
	return "acct:lock:create:" + NormalizeUsername(username)
}

// issueTokenScript 原子地完成一次凭证轮换：读旧指针 → 写新凭证 → 换指针 → 删旧凭证。
//
// **必须原子**。拆成「先 GET 旧指针，再 pipeline 写新凭证/换指针/删旧 token」时，
// 同一账号的两个并发登录会读到同一个旧指针、各自删掉它，于是**两个 token 都活着**
// —— 而指针只指向其中一个，另一个从此再也不会被任何一次轮换删掉（孤儿凭证永久
// 有效）。轮换是单会话强制的权威机制，这个窗口直接把它作废。Redis 串行执行脚本，
// 把「读旧值 + 换指针 + 删旧值」合成一次操作就不存在这个窗口。
//
// KEYS[1] = sess:acct:{accountID}（指针）  KEYS[2] = sess:{newToken}
// ARGV[1] = newToken  ARGV[2] = TTL 秒  ARGV[3] = accountID  ARGV[4] = 凭证键前缀
//
// 前缀要经 ARGV 传进来：旧凭证的键名（sess:{old}）在调用前是不知道的，而脚本里
// 不能拼字符串字面量。本项目是单实例 Redis，不涉及 Cluster 的跨槽位限制。
// 结尾必须 return 一个非 nil 值：脚本无返回值时 go-redis 会把这次 EVAL 读成
// redis.Nil（"no value"），调用方会把每一次成功轮换都当成失败。
const issueTokenScript = `
local old = redis.call('GET', KEYS[1])
redis.call('SET', KEYS[2], ARGV[3], 'EX', ARGV[2])
redis.call('SET', KEYS[1], ARGV[1], 'EX', ARGV[2])
if old then
  redis.call('DEL', ARGV[4] .. old)
end
return 1
`

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
	rdb      *redis.Client
	accounts persist.AccountPersistence
}

// NewStore 构造 Store。accounts 是 protoc-gen-redis 生成的账号 Hash 仓储；
// 凭证仍由 rdb 上的 sess:* 原子脚本管理。
func NewStore(rdb *redis.Client, accounts persist.AccountPersistence) *Store {
	return &Store{rdb: rdb, accounts: accounts}
}

// Create 占名并创建账号，返回分配到的 accountID（十进制字符串）。
//
// 分布式锁把同名注册串行化；SETNX 仍保留为最终的占名权威，锁只是为了把
// 「占名 → 分配 ID → 写 Hash → 落名字映射」这组多命令提交保护成一个临界区。
// 如果进程在临界区崩溃，锁的 TTL/看门狗会释放锁，占名键也带 TTL 自愈。
func (s *Store) Create(ctx context.Context, username, passHash string) (string, error) {
	lockKey := keyCreateLock(username)
	lock := distlock.New(s.rdb, lockKey,
		distlock.WithTTL(createLockTTL),
		distlock.WithRetryInterval(createLockRetry),
		distlock.WithOnLost(func() {
			log.Printf("account: create lock %s lost", lockKey)
		}),
	)
	if err := lock.Lock(ctx); err != nil {
		return "", fmt.Errorf("account: acquire create lock: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), createUnlockBudget)
		defer cancel()
		if err := lock.Unlock(unlockCtx); err != nil && !errors.Is(err, distlock.ErrNotOwned) {
			log.Printf("account: release create lock %s: %v", lockKey, err)
		}
	}()

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

	if err := s.accounts.Save(ctx, uint64(n), username, passHash, time.Now().Unix()); err != nil {
		_ = s.rdb.Del(ctx, nameKey).Err()
		_ = s.accounts.Delete(ctx, uint64(n))
		return "", err
	}

	if err := s.rdb.Set(ctx, nameKey, id, 0).Err(); err != nil {
		_ = s.rdb.Del(ctx, nameKey).Err()
		_ = s.accounts.Delete(ctx, uint64(n))
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

// GetAccount 从 protoc-gen-redis 生成的账号 Hash 读取数据。第二个返回值
// 表示账号是否存在。
func (s *Store) GetAccount(ctx context.Context, id string) (Account, bool, error) {
	idNum, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return Account{}, false, nil
	}

	row, ok, err := s.accounts.Get(ctx, idNum)
	if err != nil {
		return Account{}, false, err
	}
	if !ok {
		return Account{}, false, nil
	}
	return Account{
		ID:        id,
		Username:  row.Username,
		PassHash:  row.PassHash,
		CreatedAt: row.CreatedAt,
	}, true, nil
}

// IssueToken 签发新凭证，并让该账号的旧凭证立即失效（单会话强制）。
//
// 「轮换」是顶号的**权威**手段：被顶掉的客户端拿着已删除的 token，即使重连
// 也 resume 不回来、只能回到登录面板。定点踢（见 online 包）只负责让它及时
// 闭嘴，不负责正确性。
func (s *Store) IssueToken(ctx context.Context, accountID string) (string, error) {
	token, err := NewToken()
	if err != nil {
		return "", err
	}
	// 换指针 + 删旧凭证必须在**一次**脚本里完成，见 issueTokenScript 的说明。
	if err := s.rdb.Eval(ctx, issueTokenScript,
		[]string{keySessAcct(accountID), keySess(token)},
		token,
		strconv.FormatInt(int64(keySessTTL/time.Second), 10),
		accountID,
		keySessPrefix,
	).Err(); err != nil {
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
	if _, err := pipe.Exec(ctx); err != nil {
		// 续期失败不当作认证失败（凭证本身是有效的），但**不能静默**：只续上
		// sess:{token} 而没续上指针时，活跃账号的 sess:acct:{id} 会先过期，下一次
		// IssueToken 就读不到旧 token、跳过删除 —— 轮换（单会话强制的权威手段）
		// 从那一刻起静默失效，而症状（旧凭证一直有效）要到几天后才显形，没有这条
		// 日志就只能靠猜。
		log.Printf("account: renew ttl for account %s failed: %v", id, err)
	}
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

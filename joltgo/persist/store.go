// Package persist 是服务端结构化持久数据的写入边界。
//
// 数据模型由 persist/protos/*.proto 通过 protoc-gen-redis 生成；本包只做
// 连接/上下文/存在性判断与领域类型转换，不把生成代码泄漏给业务包。
package persist

import (
	"context"
	"fmt"

	"github.com/gomodule/redigo/redis"
	protos "joltgo/persist/protos"
)

const (
	// accountNamespace 是 DBAccount 的 REDBKey。生成器的 key 格式为
	// acct:<namespace>:<accountID>:0，便于将来按业务空间隔离。
	accountNamespace uint32 = 1
	accountKeyFormat        = "acct:%d:%d:%d"

	// accountUsernameField 是账号 Hash 里用户名字段的编号。字段号一律取自
	// 生成码，不手写数字（生成码改了字段顺序就会错位）。
	accountUsernameField = protos.FieldDBAccount_Username
)

// AccountRecord 是业务层看到的账号持久化记录；生成类型不会越过本包。
type AccountRecord struct {
	Username  string
	PassHash  string
	CreatedAt int64
}

// AccountPersistence 是账号持久化依赖的窄接口，便于业务层单测故障分支。
type AccountPersistence interface {
	Save(ctx context.Context, id uint64, username, passHash string, createdAt int64) error
	Get(ctx context.Context, id uint64) (AccountRecord, bool, error)
	Delete(ctx context.Context, id uint64) error
}

// AccountStore 持久化账号本体（用户名、bcrypt 哈希、创建时间）。
//
// 凭证 sess:* 有自己的原子轮换路径，不属于本 Store。
type AccountStore struct {
	pool *redis.Pool
}

var _ AccountPersistence = (*AccountStore)(nil)

// NewAccountStore 构造账号持久化仓储。pool 由调用方拥有并负责 Close。
func NewAccountStore(pool *redis.Pool) *AccountStore {
	return &AccountStore{pool: pool}
}

// AccountKey 返回账号 Hash 的 Redis key。导出用于存储层测试与诊断，
// 业务逻辑应优先调用 Get/Save/Delete。
func AccountKey(id uint64) string {
	return fmt.Sprintf(accountKeyFormat, accountNamespace, id, 0)
}

// Save 原子写入账号的全部字段。
func (s *AccountStore) Save(ctx context.Context, id uint64, username, passHash string, createdAt int64) error {
	conn, err := s.pool.GetContext(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	row := &protos.DBAccount{
		Username:      username,
		PassHash:      passHash,
		CreatedAt:     createdAt,
		SchemaVersion: protos.DBSchemaVersion_DB_SCHEMA_VERSION_CURRENT,
	}
	return row.SetFields(conn, accountNamespace, id, 0)
}

// Get 读取账号。第二个返回值为 false 表示 Hash 不存在。
func (s *AccountStore) Get(ctx context.Context, id uint64) (AccountRecord, bool, error) {
	var row protos.DBAccount

	conn, err := s.pool.GetContext(ctx)
	if err != nil {
		return AccountRecord{}, false, err
	}
	defer conn.Close()

	exists, err := redis.Bool(conn.Do("EXISTS", AccountKey(id)))
	if err != nil {
		return AccountRecord{}, false, err
	}
	if !exists {
		return AccountRecord{}, false, nil
	}
	if err := row.GetFields(conn, accountNamespace, id, 0); err != nil {
		return AccountRecord{}, false, err
	}
	return AccountRecord{
		Username:  row.Username,
		PassHash:  row.PassHash,
		CreatedAt: row.CreatedAt,
	}, true, nil
}

// Delete 删除账号 Hash。只用于注册失败时的补偿；已分配账号 ID 不复用。
func (s *AccountStore) Delete(ctx context.Context, id uint64) error {
	conn, err := s.pool.GetContext(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Do("DEL", AccountKey(id))
	return err
}

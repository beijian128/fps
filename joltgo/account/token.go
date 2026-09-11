// Package account 实现账号体系：注册、登录、凭证恢复。
//
// 本文件只有纯函数（无 I/O）：凭证生成与校验、密码哈希与校验、用户名密码的
// 格式规则。Redis 读写在同包的 store.go，pitaya handler 在 component.go。
package account

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// 用户名与密码的格式上下限。用户名允许字母/数字/下划线；密码只限长度 ——
// 不强制复杂度（demo 定位，避免造出「必须含数字」这类无意义规则把人挡在门外）。
const (
	MinUsernameLen = 3
	MaxUsernameLen = 16
	MinPasswordLen = 6
	MaxPasswordLen = 64

	// TokenBytes 是凭证的随机字节数（base64url 无填充后 43 字符）。
	TokenBytes = 32
)

var (
	// ErrBadUsername 表示用户名不符合格式要求。
	ErrBadUsername = errors.New("account: bad username")
	// ErrBadPassword 表示密码不符合长度要求。
	ErrBadPassword = errors.New("account: bad password")
	// ErrBadToken 表示凭证字符串形态非法。
	ErrBadToken = errors.New("account: bad token")
)

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_]{3,16}$`)

// NewToken 生成一个新的会话凭证：32 字节 crypto/rand，base64url 无填充。
// 用不透明随机串而不是 JWT：可随时吊销（DEL 一个键）、无密钥管理、泄露面小，
// 与「状态都在 Redis」的取向一致。
func NewToken() (string, error) {
	b := make([]byte, TokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ValidateToken 校验凭证字符串的形态（长度与字符集）。
// 这不是安全校验（真伪由 Redis 查表决定），只是避免拿垃圾串去打 Redis。
func ValidateToken(t string) error {
	b, err := base64.RawURLEncoding.DecodeString(t)
	if err != nil || len(b) != TokenBytes {
		return ErrBadToken
	}
	return nil
}

// HashPassword 用 bcrypt 哈希密码（DefaultCost，约 50ms/次）。
func HashPassword(pw string) (string, error) {
	if err := ValidatePassword(pw); err != nil {
		return "", err
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// CheckPassword 校验密码是否匹配哈希。
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// ValidateUsername 校验用户名格式：3-16 位字母/数字/下划线。
func ValidateUsername(u string) error {
	if !usernameRe.MatchString(u) {
		return ErrBadUsername
	}
	return nil
}

// ValidatePassword 校验密码长度：6-64 位。
func ValidatePassword(pw string) error {
	if len(pw) < MinPasswordLen || len(pw) > MaxPasswordLen {
		return ErrBadPassword
	}
	return nil
}

// NormalizeUsername 返回用户名的规范化形式，用于占名与查询（大小写不敏感，
// 防「Alice」和「alice」被当成两个账号抢注）。展示用的原始大小写存在账号 Hash 里。
func NormalizeUsername(u string) string {
	return strings.ToLower(u)
}

package account

import (
	"strings"
	"testing"
)

func TestNewTokenIsRandomAndWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		tok, err := NewToken()
		if err != nil {
			t.Fatalf("NewToken 不该报错: %v", err)
		}
		if err := ValidateToken(tok); err != nil {
			t.Fatalf("刚生成的 token 应通过校验，得到 %q: %v", tok, err)
		}
		if seen[tok] {
			t.Fatalf("token 重复了: %q", tok)
		}
		seen[tok] = true
	}
}

func TestValidateTokenRejectsGarbage(t *testing.T) {
	bad := []string{
		"",
		"short",
		strings.Repeat("a", 42), // 合法 base64url，但解码只有 31 字节
		strings.Repeat("a", 44), // 解码 33 字节
		strings.Repeat("!", 43), // 非法字符
	}
	for _, s := range bad {
		if err := ValidateToken(s); err == nil {
			t.Fatalf("%q 应被判为非法 token", s)
		}
	}
}

// 边界：43 个 base64url 字符解码正好是 32 字节，必须被接受 ——
// 别把它写成「非法」用例（那会让上面的测试以错误理由通过）。
func TestValidateTokenAcceptsExactly32Bytes(t *testing.T) {
	s := strings.Repeat("a", 43)
	if err := ValidateToken(s); err != nil {
		t.Fatalf("43 字符 = 32 字节，应是合法形态，得到 %v", err)
	}
}

func TestHashPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("hunter2")
	if err != nil {
		t.Fatalf("HashPassword 不该报错: %v", err)
	}
	if h == "hunter2" {
		t.Fatal("哈希不能等于明文")
	}
	if !CheckPassword(h, "hunter2") {
		t.Fatal("正确密码应校验通过")
	}
	if CheckPassword(h, "hunter3") {
		t.Fatal("错误密码不应通过")
	}
	// bcrypt 加了随机盐，同一密码两次哈希必须不同。
	h2, _ := HashPassword("hunter2")
	if h == h2 {
		t.Fatal("两次哈希相同说明没有加盐")
	}
}

func TestHashPasswordRejectsBadLength(t *testing.T) {
	for _, pw := range []string{"", "12345", strings.Repeat("x", 65)} {
		if _, err := HashPassword(pw); err == nil {
			t.Fatalf("%d 位的密码应被拒绝", len(pw))
		}
	}
}

func TestValidateUsername(t *testing.T) {
	ok := []string{"abc", "a_1", "User123", strings.Repeat("a", 16)}
	for _, u := range ok {
		if err := ValidateUsername(u); err != nil {
			t.Fatalf("%q 应是合法用户名: %v", u, err)
		}
	}
	bad := []string{"", "ab", strings.Repeat("a", 17), "a b", "a-b", "用户名", "a.b"}
	for _, u := range bad {
		if err := ValidateUsername(u); err == nil {
			t.Fatalf("%q 应被判为非法用户名", u)
		}
	}
}

func TestNormalizeUsername(t *testing.T) {
	if got := NormalizeUsername("Alice"); got != "alice" {
		t.Fatalf("应转小写，得到 %q", got)
	}
	if got := NormalizeUsername("a_b_1"); got != "a_b_1" {
		t.Fatalf("已是小写应原样返回，得到 %q", got)
	}
}

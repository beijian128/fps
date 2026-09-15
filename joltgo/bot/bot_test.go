package bot

import "testing"

func TestNewAlwaysCarriesPrefix(t *testing.T) {
	if got := New("abc123"); got != "bot:abc123" {
		t.Fatalf("New = %q, want %q", got, "bot:abc123")
	}
	if !Is(New("abc123")) {
		t.Fatal("New 造出来的 uid 必须被 Is 认成机器人")
	}
}

func TestIsAcceptsOnlyBotPrefix(t *testing.T) {
	cases := []struct {
		uid  string
		want bool
	}{
		{"bot:1", true},
		{"bot:4EoTb9pQ", true},
		{"bot:", true},    // 空后缀也算机器人 uid：判据只看身份来源，不看内容
		{"", false},
		{"1", false},      // 真人的十进制 accountID
		{"10001", false},  // 同上
		{"bot", false},    // 少了分隔符
		{"bots:1", false}, // 前缀相似但不是
		{" bot:1", false}, // 不 TrimSpace
		{"BOT:1", false},  // 大小写敏感
		{"xbot:1", false}, // 不在开头
	}
	for _, c := range cases {
		if got := Is(c.uid); got != c.want {
			t.Errorf("Is(%q) = %v, want %v", c.uid, got, c.want)
		}
	}
}

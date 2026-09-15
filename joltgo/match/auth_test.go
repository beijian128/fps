package match

import (
	"context"
	"testing"
)

func TestAdminKeyAllowedRequiresExactMatch(t *testing.T) {
	cases := []struct {
		secret, provided string
		want             bool
	}{
		{"s3cret", "s3cret", true},
		{"s3cret", "s3cret ", false},
		{"s3cret", "S3CRET", false},
		{"s3cret", "", false},
		{"", "s3cret", false}, // 服务端没配密钥时一律拒绝（不是「放行」）
		{"", "", false},
	}
	for _, c := range cases {
		if got := adminKeyAllowed(c.secret, c.provided); got != c.want {
			t.Errorf("adminKeyAllowed(%q,%q) = %v, want %v", c.secret, c.provided, got, c.want)
		}
	}
}

func TestIsClientCallOnlyTrueWithSession(t *testing.T) {
	if isClientCall(context.Background(), &joinTestApp{}) {
		t.Fatal("没有会话的 ctx 应判定为后端调用")
	}
	app := &joinTestApp{sess: &joinTestSession{uid: "7"}}
	if !isClientCall(context.Background(), app) {
		t.Fatal("带会话的 ctx 应判定为客户端调用")
	}
}

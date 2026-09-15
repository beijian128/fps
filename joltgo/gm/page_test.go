package gm

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// pageBody 取一次首页 HTML。
func pageBody(t *testing.T) string {
	t.Helper()
	h, _, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET / = %d", w.Code)
	}
	return w.Body.String()
}

func TestPageContainsBothFormsAndKeyField(t *testing.T) {
	body := pageBody(t)
	for _, want := range []string{
		`id="gmkey"`,   // 密钥输入框
		`id="target"`,  // 发钱目标（用户名或 accountID）
		`id="delta"`,   // 发钱数量
		`id="coinbtn"`, // 发钱按钮
		`id="botcount"`,
		`id="botbtn"`,
		`id="result"`,   // 结果区
		"/api/coins",    // 两个接口都要被页面调用
		"/api/bots",
		"localStorage", // 密钥要能记住，刷新不用重填
	} {
		if !strings.Contains(body, want) {
			t.Errorf("页面缺少 %s", want)
		}
	}
}

func TestPageSendsBearerHeader(t *testing.T) {
	if body := pageBody(t); !strings.Contains(body, "Bearer") {
		t.Error("页面必须用 Authorization: Bearer 发密钥，否则所有请求都会 401")
	}
}

func TestPageExplainsBotBehaviour(t *testing.T) {
	body := pageBody(t)
	// 加机器人这条操作的结果不在页面上可见（要另开客户端才知道），
	// 所以页面必须至少说清「它会怎么表现」，否则用户不知道自己做成了没有。
	if !strings.Contains(body, "不动") {
		t.Error("页面要说明机器人进对局后全程不动")
	}
}

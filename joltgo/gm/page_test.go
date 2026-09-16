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

func TestPageHasLoginForm(t *testing.T) {
	body := pageBody(t)
	for _, want := range []string{
		`id="user"`,     // 账号
		`id="password"`, // 密码
		`id="loginbtn"`, // 登录按钮
		"/api/login",
		`id="logoutbtn"`, // 退出
		"/api/logout",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("页面缺少 %s", want)
		}
	}
}

func TestPageContainsBothForms(t *testing.T) {
	body := pageBody(t)
	for _, want := range []string{
		`id="target"`,  // 发钱目标（用户名或 accountID）
		`id="delta"`,   // 发钱数量
		`id="coinbtn"`, // 发钱按钮
		`id="botcount"`,
		`id="botbtn"`,
		`id="result"`, // 结果区
		"/api/coins",
		"/api/bots",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("页面缺少 %s", want)
		}
	}
}

func TestPageSendsSessionCookieNotBearer(t *testing.T) {
	// 会话走 HttpOnly cookie（浏览器自动带），页面脚本既不读也不存任何凭证。
	body := pageBody(t)
	if !strings.Contains(body, "credentials") {
		t.Error("页面请求必须带上同源 cookie（credentials），否则所有 /api/* 都会 401")
	}
	if strings.Contains(body, "localStorage") {
		t.Error("页面不该把任何凭证存进 localStorage（旧的密钥方案才那么做）")
	}
	if strings.Contains(body, "Authorization") {
		t.Error("页面不该再发 Authorization 头（已改为会话 cookie）")
	}
}

func TestPageExplainsBotBehaviour(t *testing.T) {
	body := pageBody(t)
	// 加机器人这条操作的结果在页面上看不见（要另开客户端才知道），
	// 所以页面必须至少说清「它会怎么表现」。
	if !strings.Contains(body, "不动") {
		t.Error("页面要说明机器人进对局后全程不动")
	}
}

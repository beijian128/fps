package gm

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// login 发一次登录请求。
func login(t *testing.T, h *Handler, user, pass string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"user":"` + user + `","password":"` + pass + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Router().ServeHTTP(w, req)
	return w
}

// cookieOf 从响应里取出指定名字的 cookie（没有则失败）。
func cookieOf(t *testing.T, w *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("响应里没有 cookie %s", name)
	return nil
}

func TestLoginSetsCookieAndGrantsAPI(t *testing.T) {
	h, coins, _ := newTestHandler(t)
	w := login(t, h, testUser, testPass)
	if w.Code != http.StatusOK {
		t.Fatalf("登录 = %d，body=%s", w.Code, w.Body.String())
	}
	c := cookieOf(t, w, sessionCookie)
	if !c.HttpOnly {
		t.Error("会话 cookie 应该是 HttpOnly")
	}

	w2 := postJSON(t, h.Router(), "/api/coins", `{"target":"alice","delta":5}`, c.Value)
	if w2.Code != http.StatusOK {
		t.Fatalf("带 cookie 的 /api/coins = %d，body=%s", w2.Code, w2.Body.String())
	}
	if coins.gotID != "10001" {
		t.Fatalf("下游应收到 10001，得到 %q", coins.gotID)
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	h, coins, _ := newTestHandler(t)
	w := login(t, h, testUser, "nope")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("密码错误 = %d，期望 401", w.Code)
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("失败时不该下发 cookie")
	}
	if coins.gotID != "" {
		t.Fatal("登录失败的请求不该触达下游")
	}
}

func TestLoginRejectsWrongUser(t *testing.T) {
	h, _, _ := newTestHandler(t)
	if w := login(t, h, "root", testPass); w.Code != http.StatusUnauthorized {
		t.Fatalf("账号错误 = %d，期望 401", w.Code)
	}
}

func TestAPIsRequireCookie(t *testing.T) {
	h, coins, bots := newTestHandler(t)
	if w := postJSON(t, h.Router(), "/api/coins", `{"target":"alice","delta":1}`, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("无 cookie 访问 /api/coins = %d，期望 401", w.Code)
	}
	if w := postJSON(t, h.Router(), "/api/bots", `{"count":1}`, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("无 cookie 访问 /api/bots = %d，期望 401", w.Code)
	}
	if coins.gotID != "" || bots.gotCount != 0 {
		t.Fatal("未鉴权的请求不该触达下游")
	}
}

func TestForgedCookieIsRejected(t *testing.T) {
	h, _, _ := newTestHandler(t)
	// 自造一个形状合法（user|expiry|mac）但签名不对的 cookie。
	w := postJSON(t, h.Router(), "/api/bots", `{"count":1}`, "admin|9999999999|deadbeef")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("伪造 cookie = %d，期望 401", w.Code)
	}
}

func TestExpiredCookieIsRejected(t *testing.T) {
	h, _, _ := newTestHandler(t)
	expired := h.signSession(testUser, time.Now().Add(-time.Hour))
	if w := postJSON(t, h.Router(), "/api/bots", `{"count":1}`, expired); w.Code != http.StatusUnauthorized {
		t.Fatalf("过期 cookie = %d，期望 401", w.Code)
	}
}

func TestLogoutClearsCookie(t *testing.T) {
	h, _, _ := newTestHandler(t)
	w := login(t, h, testUser, testPass)
	c := cookieOf(t, w, sessionCookie)

	req := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: c.Value})
	got := httptest.NewRecorder()
	h.Router().ServeHTTP(got, req)
	if got.Code != http.StatusOK {
		t.Fatalf("登出 = %d", got.Code)
	}
	if out := cookieOf(t, got, sessionCookie); out.MaxAge >= 0 {
		t.Fatalf("登出应清掉 cookie（MaxAge<0），得到 %d", out.MaxAge)
	}
}

func TestLoginRateLimit(t *testing.T) {
	h, _, _ := newTestHandler(t)
	for i := 0; i < maxLoginAttempts; i++ {
		if w := login(t, h, testUser, "wrong"); w.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次失败登录 = %d，期望 401", i+1, w.Code)
		}
	}
	// 达到阈值之后，即使密码正确也应被限速挡下。
	if w := login(t, h, testUser, testPass); w.Code != http.StatusTooManyRequests {
		t.Fatalf("限速后 = %d，期望 429", w.Code)
	}
}

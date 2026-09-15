package gm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	redigo "github.com/gomodule/redigo/redis"
	"github.com/redis/go-redis/v9"
	"joltgo/account"
	"joltgo/persist"
)

type stubCoins struct {
	gotID    string
	gotDelta int64
	coins    int64
	err      error
}

func (s *stubCoins) GrantCoins(_ context.Context, accountID string, delta int64) (int64, error) {
	s.gotID, s.gotDelta = accountID, delta
	return s.coins, s.err
}

type stubBots struct {
	gotCount int32
	enqueued int32
	err      error
}

func (s *stubBots) AddBots(_ context.Context, count int32) (int32, error) {
	s.gotCount = count
	if s.err != nil {
		return 0, s.err
	}
	return s.enqueued, nil
}

// newTestHandler 用真 account.Store（跑在 miniredis 上）而不是 stub 解析器：
// 「用户名 → accountID」这条路径的坑全在规范化与键名里，用真的才有意义。
func newTestHandler(t *testing.T) (*Handler, *stubCoins, *stubBots) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	mr := miniredis.RunT(t)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	pool := &redigo.Pool{
		MaxIdle: 2,
		Dial:    func() (redigo.Conn, error) { return redigo.Dial("tcp", mr.Addr()) },
	}
	t.Cleanup(func() { _ = pool.Close() })

	// 造一个已注册账号：账号 Hash + 名字映射（两者都要，缺一个就查不到）。
	accounts := persist.NewAccountStore(pool)
	if err := accounts.Save(context.Background(), 10001, "alice", "hash", 1); err != nil {
		t.Fatal(err)
	}
	if err := mr.Set("acct:name:alice", "10001"); err != nil {
		t.Fatal(err)
	}

	coins := &stubCoins{coins: 1500}
	bots := &stubBots{enqueued: 1}
	handler := NewHandler(coins, bots, account.NewStore(rdb, accounts), "s3cret")
	return handler, coins, bots
}

func postJSON(t *testing.T, router *gin.Engine, path, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestIndexServesHTMLWithoutAuth(t *testing.T) {
	h, _, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET / = %d，期望 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "<html") {
		t.Fatal("GET / 应返回 HTML 页面")
	}
}

func TestCoinsRejectsMissingKey(t *testing.T) {
	h, coins, _ := newTestHandler(t)
	w := postJSON(t, h.Router(), "/api/coins", `{"target":"alice","delta":500}`, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("缺密钥 = %d，期望 401", w.Code)
	}
	if coins.gotID != "" {
		t.Fatal("鉴权失败的请求不该触达下游服务")
	}
}

func TestCoinsRejectsWrongKey(t *testing.T) {
	h, coins, _ := newTestHandler(t)
	w := postJSON(t, h.Router(), "/api/coins", `{"target":"alice","delta":500}`, "wrong")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("错密钥 = %d，期望 401", w.Code)
	}
	if coins.gotID != "" {
		t.Fatal("鉴权失败的请求不该触达下游服务")
	}
}

func TestCoinsByUsername(t *testing.T) {
	h, coins, _ := newTestHandler(t)
	w := postJSON(t, h.Router(), "/api/coins", `{"target":"alice","delta":500}`, "s3cret")
	if w.Code != http.StatusOK {
		t.Fatalf("= %d，body=%s", w.Code, w.Body.String())
	}
	if coins.gotID != "10001" || coins.gotDelta != 500 {
		t.Fatalf("下游收到 id=%q delta=%d，期望 10001 / 500", coins.gotID, coins.gotDelta)
	}
	var body struct {
		Ok    bool  `json:"ok"`
		Coins int64 `json:"coins"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Ok || body.Coins != 1500 {
		t.Fatalf("body=%+v，期望 ok 且 1500", body)
	}
}

func TestCoinsByUsernameIsCaseInsensitive(t *testing.T) {
	h, coins, _ := newTestHandler(t)
	w := postJSON(t, h.Router(), "/api/coins", `{"target":"ALICE","delta":1}`, "s3cret")
	if w.Code != http.StatusOK {
		t.Fatalf("= %d，body=%s", w.Code, w.Body.String())
	}
	if coins.gotID != "10001" {
		t.Fatalf("大小写不敏感的用户名应解析到 10001，得到 %q", coins.gotID)
	}
}

func TestCoinsByAccountID(t *testing.T) {
	h, coins, _ := newTestHandler(t)
	w := postJSON(t, h.Router(), "/api/coins", `{"target":"10001","delta":500}`, "s3cret")
	if w.Code != http.StatusOK {
		t.Fatalf("= %d，body=%s", w.Code, w.Body.String())
	}
	if coins.gotID != "10001" {
		t.Fatalf("纯十进制应直接当 accountID，得到 %q", coins.gotID)
	}
}

func TestCoinsUnknownUsername(t *testing.T) {
	h, coins, _ := newTestHandler(t)
	w := postJSON(t, h.Router(), "/api/coins", `{"target":"nobody","delta":1}`, "s3cret")
	if w.Code != http.StatusNotFound {
		t.Fatalf("未知用户名 = %d，期望 404", w.Code)
	}
	if coins.gotID != "" {
		t.Fatal("解析失败不该调下游")
	}
}

func TestCoinsRejectsZeroDelta(t *testing.T) {
	h, _, _ := newTestHandler(t)
	w := postJSON(t, h.Router(), "/api/coins", `{"target":"alice","delta":0}`, "s3cret")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("delta=0 = %d，期望 400", w.Code)
	}
}

func TestCoinsRejectsEmptyTarget(t *testing.T) {
	h, _, _ := newTestHandler(t)
	w := postJSON(t, h.Router(), "/api/coins", `{"target":"  ","delta":5}`, "s3cret")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("空目标 = %d，期望 400", w.Code)
	}
}

func TestCoinsUpstreamFailure(t *testing.T) {
	h, coins, _ := newTestHandler(t)
	coins.err = errors.New("rpc down")
	w := postJSON(t, h.Router(), "/api/coins", `{"target":"alice","delta":5}`, "s3cret")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("上游失败 = %d，期望 503", w.Code)
	}
}

func TestBotsRejectsMissingKey(t *testing.T) {
	h, _, bots := newTestHandler(t)
	w := postJSON(t, h.Router(), "/api/bots", `{"count":1}`, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("缺密钥 = %d，期望 401", w.Code)
	}
	if bots.gotCount != 0 {
		t.Fatal("鉴权失败的请求不该触达下游")
	}
}

func TestBotsAddsRequestedCount(t *testing.T) {
	h, _, bots := newTestHandler(t)
	w := postJSON(t, h.Router(), "/api/bots", `{"count":3}`, "s3cret")
	if w.Code != http.StatusOK {
		t.Fatalf("= %d，body=%s", w.Code, w.Body.String())
	}
	if bots.gotCount != 3 {
		t.Fatalf("下游收到 count=%d，期望 3", bots.gotCount)
	}
}

func TestBotsRejectsNonPositiveCount(t *testing.T) {
	h, _, bots := newTestHandler(t)
	for _, body := range []string{`{"count":0}`, `{"count":-2}`} {
		if w := postJSON(t, h.Router(), "/api/bots", body, "s3cret"); w.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d，期望 400", body, w.Code)
		}
	}
	if bots.gotCount != 0 {
		t.Fatal("非法 count 不该调下游")
	}
}

func TestBotsUpstreamFailure(t *testing.T) {
	h, _, bots := newTestHandler(t)
	bots.err = errors.New("rpc down")
	w := postJSON(t, h.Router(), "/api/bots", `{"count":1}`, "s3cret")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("上游失败 = %d，期望 503", w.Code)
	}
}

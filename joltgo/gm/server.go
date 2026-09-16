// Package gm 是 GM 服务：一个只对运维可见的 HTTP 入口（Gin）+ 内嵌 Web 操作页。
//
// 它不进玩家协议：gate 的路由表里没有 gm.*，它自己也不监听任何 pitaya 端口、
// 不注册 handler / remote。它只做两件事 —— 用账号密码把住控制台入口（谁能用这台
// 工具），然后把指令转成后端 RPC 交给拥有该状态的服务去执行（match 管队列、
// logic 管钱包）。因此本包**不碰** match:queue 的键名，也不碰钱包 Hash。
//
// 注意分工：这里挡的是「谁能打开控制台」。**客户端可达性不靠本包** —— 那由 gate 的
// 转发白名单保证（客户端根本发不到 match.match.addbots / logic.logic.grantcoins）。
package gm

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	// sessionCookie 是 GM 控制台的会话 cookie 名。
	sessionCookie = "gm_session"
	// sessionTTL 是会话有效期。无状态签名 cookie，没有服务端会话存储。
	sessionTTL = 8 * time.Hour
	// 登录失败限速：同一 IP 连续失败 maxLoginAttempts 次后，
	// loginBlockWindow 内一律拒绝（本地工具，不做更复杂的）。
	maxLoginAttempts = 5
	loginBlockWindow = time.Minute
)

// CoinsService 是「加/扣金币」这一个能力；由 gm.RPC 实现（转成后端 RPC 调 logic）。
//
// 定义成窄接口而不是直接调 RPC：HTTP 层因此可以用 stub 测，不用起整个集群。
type CoinsService interface {
	GrantCoins(ctx context.Context, accountID string, delta int64) (int64, error)
}

// AddBotsService 是「往匹配队列塞机器人」这一个能力；由 gm.RPC 实现。
//
// 注意它接的是**业务参数**（数量），不是 proto 消息 —— HTTP 层因此不需要知道
// 有 AddBotsMsg 这回事，stub 测试也好写。
type AddBotsService interface {
	AddBots(ctx context.Context, count int32) (int32, error)
}

// UsernameResolver 把用户名解析成 accountID（大小写不敏感）。
// 由 account.Store 实现；单列一个接口是为了让 HTTP 层可测。
type UsernameResolver interface {
	LookupByName(ctx context.Context, username string) (string, bool, error)
}

// Handler 持有 GM 控制台的依赖与登录配置。
type Handler struct {
	coins    CoinsService
	bots     AddBotsService
	accounts UsernameResolver
	user     string
	pass     string
	secret   []byte // cookie 签名密钥
	page     []byte

	loginMu   sync.Mutex
	loginFail map[string]loginAttempts
}

type loginAttempts struct {
	count int
	last  time.Time
}

// NewHandler 构造 GM 处理器。secret 为空时回落到 pass（本地开发够用）。
//
// pass 为空表示这个进程没配控制台密码 —— 调用方（main.go）应当在启动时就拒绝这种
// 配置；这里对空密码的登录请求一律回 401，作为第二重保险。
func NewHandler(coins CoinsService, bots AddBotsService, accounts UsernameResolver, user, pass, secret string) *Handler {
	if secret == "" {
		secret = pass
	}
	return &Handler{
		coins:     coins,
		bots:      bots,
		accounts:  accounts,
		user:      user,
		pass:      pass,
		secret:    []byte(secret),
		page:      indexPage,
		loginFail: map[string]loginAttempts{},
	}
}

// Router 组装 Gin 路由。页面与登录接口放行（页面本身不含数据），
// 其余 /api/* 一律过会话中间件。
func (h *Handler) Router() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.GET("/", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", h.page)
	})
	r.POST("/api/login", h.postLogin)
	r.POST("/api/logout", h.postLogout)
	api := r.Group("/api", h.requireSession)
	api.POST("/coins", h.postCoins)
	api.POST("/bots", h.postBots)
	return r
}

// signSession 生成 `user|expiryUnix|hmac` 形式的会话值。
func (h *Handler) signSession(user string, expiry time.Time) string {
	payload := fmt.Sprintf("%s|%d", user, expiry.Unix())
	mac := hmac.New(sha256.New, h.secret)
	mac.Write([]byte(payload))
	return payload + "|" + hex.EncodeToString(mac.Sum(nil))
}

// verifySession 校验签名与过期时间，返回会话里的用户名。
func (h *Handler) verifySession(value string) (string, bool) {
	parts := strings.Split(value, "|")
	if len(parts) != 3 {
		return "", false
	}
	payload := parts[0] + "|" + parts[1]
	mac := hmac.New(sha256.New, h.secret)
	mac.Write([]byte(payload))
	want := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(want), []byte(parts[2])) != 1 {
		return "", false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", false
	}
	return parts[0], true
}

// requireSession 是受保护接口的中间件。
func (h *Handler) requireSession(c *gin.Context) {
	v, err := c.Cookie(sessionCookie)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"ok": false, "reason": "unauthorized"})
		return
	}
	user, ok := h.verifySession(v)
	if !ok || user != h.user {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"ok": false, "reason": "unauthorized"})
		return
	}
}

// blocked 报告这个 IP 是否因为连续失败登录过多而被暂时拒绝。
func (h *Handler) blocked(c *gin.Context) bool {
	ip := c.ClientIP()
	h.loginMu.Lock()
	defer h.loginMu.Unlock()
	att, ok := h.loginFail[ip]
	if !ok {
		return false
	}
	if time.Since(att.last) > loginBlockWindow {
		delete(h.loginFail, ip)
		return false
	}
	return att.count >= maxLoginAttempts
}

func (h *Handler) noteFailure(c *gin.Context) {
	ip := c.ClientIP()
	h.loginMu.Lock()
	defer h.loginMu.Unlock()
	att := h.loginFail[ip]
	if time.Since(att.last) > loginBlockWindow {
		att.count = 0
	}
	att.count++
	att.last = time.Now()
	h.loginFail[ip] = att
}

func (h *Handler) clearFailures(c *gin.Context) {
	h.loginMu.Lock()
	defer h.loginMu.Unlock()
	delete(h.loginFail, c.ClientIP())
}

// postLogin 校验账号密码并下发签名 cookie。
func (h *Handler) postLogin(c *gin.Context) {
	if h.pass == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"ok": false, "reason": "unauthorized"})
		return
	}
	if h.blocked(c) {
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"ok": false, "reason": "too_many_attempts"})
		return
	}
	var req struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"ok": false, "reason": "bad_request"})
		return
	}
	userOK := subtle.ConstantTimeCompare([]byte(req.User), []byte(h.user)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(req.Password), []byte(h.pass)) == 1
	if !userOK || !passOK {
		h.noteFailure(c)
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"ok": false, "reason": "bad_credentials"})
		return
	}
	h.clearFailures(c)
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     sessionCookie,
		Value:    h.signSession(h.user, time.Now().Add(sessionTTL)),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// postLogout 清掉 cookie。无状态会话，服务端没有东西要清。
func (h *Handler) postLogout(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// postCoins 处理「给玩家发钱」。
//
// target 是纯十进制就按 accountID 处理，否则按用户名解析 —— 运维手上拿到的
// 通常是玩家吆喝的那串字，而 accountID 在排查时更好用，两条都支持。
func (h *Handler) postCoins(c *gin.Context) {
	var req struct {
		Target string `json:"target"`
		Delta  int64  `json:"delta"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Target) == "" || req.Delta == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "reason": "bad_request"})
		return
	}

	accountID, err := h.resolveAccountID(c.Request.Context(), strings.TrimSpace(req.Target))
	if err != nil {
		if errors.Is(err, errPlayerNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"ok": false, "reason": "player_not_found"})
			return
		}
		log.Printf("gm: resolve target %q failed: %v", req.Target, err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"ok": false, "reason": "upstream_failed"})
		return
	}

	coins, err := h.coins.GrantCoins(c.Request.Context(), accountID, req.Delta)
	if err != nil {
		// 下游拒绝（密钥不符 / 账号不存在）与集群不可达都归到 503：GM 页面只需要
		// 知道「这次没成」，detail 在 gm.log 与后端的日志里。
		log.Printf("gm: grant coins to %s failed: %v", accountID, err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"ok": false, "reason": "upstream_failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "account_id": accountID, "coins": coins})
}

// postBots 处理「往匹配队列塞机器人」。
func (h *Handler) postBots(c *gin.Context) {
	var req struct {
		Count int32 `json:"count"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Count <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "reason": "bad_request"})
		return
	}
	enqueued, err := h.bots.AddBots(c.Request.Context(), req.Count)
	if err != nil {
		log.Printf("gm: add bots failed: %v", err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"ok": false, "reason": "upstream_failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "enqueued": enqueued})
}

var errPlayerNotFound = errors.New("player not found")

// resolveAccountID 把页面上的目标解析成 accountID：纯十进制直接用，否则按用户名查。
//
// 用户名解析必须走 account.Store.LookupByName —— 它内部用 NormalizeUsername 规范化
// （用户名大小写不敏感），自己拼 "acct:name:" 会在「页面填 Alice、注册的是 alice」
// 时查不到，表现为「明明有这个人却报 player_not_found」。
func (h *Handler) resolveAccountID(ctx context.Context, target string) (string, error) {
	if _, err := strconv.ParseUint(target, 10, 64); err == nil {
		return target, nil
	}
	id, found, err := h.accounts.LookupByName(ctx, target)
	if err != nil {
		return "", err
	}
	if !found {
		return "", errPlayerNotFound
	}
	return id, nil
}

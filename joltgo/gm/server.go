// Package gm 是 GM 服务：一个只对运维可见的 HTTP 入口（Gin）+ 内嵌 Web 操作页。
//
// 它不进玩家协议：gate 的路由表里没有 gm.*，它自己也不监听任何 pitaya 端口、
// 不注册 handler / remote。它只做两件事 —— 校验管理密钥，然后把指令转成后端 RPC
// 交给拥有该状态的服务去执行（match 管队列、logic 管钱包）。因此本包**不碰**
// match:queue 的键名，也不碰钱包 Hash。
package gm

import (
	"context"
	"crypto/subtle"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// CoinsService 是「加/扣金币」这一个能力；由 gm.RPC 实现（转成后端 RPC 调 logic）。
//
// 定义成窄接口而不是直接调 RPC：HTTP 层因此可以用 stub 测，不用起整个集群。
type CoinsService interface {
	GrantCoins(ctx context.Context, accountID string, delta int64) (int64, error)
}

// AddBotsService 是「往匹配队列塞机器人」这一个能力；由 gm.RPC 实现。
//
// 注意它接的是**业务参数**（数量），不是 proto 消息：密钥由 gm.RPC 在构造时固定、
// 由它注入到 RPC 请求里，HTTP 层根本不需要知道有 admin_key 这回事。
type AddBotsService interface {
	AddBots(ctx context.Context, count int32) (int32, error)
}

// UsernameResolver 把用户名解析成 accountID（大小写不敏感）。
// 由 account.Store 实现；单列一个接口是为了让 HTTP 层可测。
type UsernameResolver interface {
	LookupByName(ctx context.Context, username string) (string, bool, error)
}

// Handler 持有 GM 页面的依赖。
type Handler struct {
	coins    CoinsService
	bots     AddBotsService
	accounts UsernameResolver
	secret   string
	page     []byte
}

// NewHandler 构造 GM 处理器。secret 为空时所有 /api/* 都返回 401。
func NewHandler(coins CoinsService, bots AddBotsService, accounts UsernameResolver, secret string) *Handler {
	return &Handler{coins: coins, bots: bots, accounts: accounts, secret: secret, page: indexPage}
}

// Router 组装 Gin 路由。页面放行（它本身不含数据），/api/* 一律过鉴权中间件。
func (h *Handler) Router() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.GET("/", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", h.page)
	})
	api := r.Group("/api", h.requireKey)
	api.POST("/coins", h.postCoins)
	api.POST("/bots", h.postBots)
	return r
}

// requireKey 校验 Authorization: Bearer <key>，比对走常数时间。
//
// 空密钥 = 不提供服务（而不是「谁都能进」）：这与 match / logic 两侧
// adminKeyAllowed 的空密钥处置一致。
func (h *Handler) requireKey(c *gin.Context) {
	token := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	if h.secret == "" || subtle.ConstantTimeCompare([]byte(h.secret), []byte(token)) != 1 {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"ok": false, "reason": "unauthorized"})
	}
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

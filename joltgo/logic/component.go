package logic

import (
	"context"
	"crypto/subtle"
	"log"
	"sync/atomic"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/component"
	"joltgo/game/protos"
)

type Component struct {
	component.Base
	app     pitaya.Pitaya
	service *Service

	// secret 是 GM 管理指令的共享密钥（来自 -gmkey）。为空 = 不提供管理入口，
	// 因此 adminKeyAllowed 对空密钥一律拒绝。
	//
	// 用 atomic.Value 而不是裸字段：它由 main.go 在 app.Start() 之前写入、
	// 由 RPC handler goroutine 读取，两者之间没有 happens-before（pitaya 的 RPC
	// 走 NATS 回调），裸字段在这里是数据竞争。match 侧同款。
	secret atomic.Value // string
}

func NewComponent(app pitaya.Pitaya, service *Service) *Component {
	return &Component{app: app, service: service}
}

// NewComponentWithSecret 构造带 GM 管理入口的组件。secret 为空表示不提供该入口。
func NewComponentWithSecret(app pitaya.Pitaya, service *Service, secret string) *Component {
	c := NewComponent(app, service)
	c.UpdateSecret(secret)
	return c
}

// UpdateSecret 更新管理密钥。
func (c *Component) UpdateSecret(secret string) { c.secret.Store(secret) }

func (c *Component) adminSecret() string {
	v, _ := c.secret.Load().(string)
	return v
}

// adminKeyAllowed 报告请求携带的管理密钥是否等于本节点配置的密钥。
// 空密钥一律拒绝（没配密钥 = 不提供服务）；比对走常数时间。
//
// 与 match/auth.go 的同名函数逐字一致：两个包各持有一份，不为 12 行函数引入
// 跨包依赖。改一处要一起改。
func adminKeyAllowed(secret string, provided string) bool {
	if secret == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(secret), []byte(provided)) == 1
}

// GrantCoins 是远端 RPC handler（route "logic.logic.grantcoins"）：GM 请求给账号发钱。
//
// 两道入口检查与 match.QueueBots 相同，缺一不可：
//
//  1. **拒绝带会话的调用**。gate 把 logic.* 按前缀转给 logic 节点，所以客户端发出的
//     logic.logic.grantcoins 会真的到达这里（RPCType_Sys，ctx 里带 Remote 会话）；
//     而后端 gm 用 app.RPCTo 发的（RPCType_User）不带会话。route 名不是权限。
//  2. **校验共享密钥**。任何后端都能调这条 route，密钥是第二道闸。
func (c *Component) GrantCoins(ctx context.Context, msg *protos.GrantCoinsMsg) (*protos.GrantCoinsReply, error) {
	if s := c.app.GetSessionFromCtx(ctx); s != nil {
		log.Printf("logic: grantcoins from client rejected (uid=%s)", s.UID())
		return &protos.GrantCoinsReply{Ok: false, Reason: ReasonForbidden}, nil
	}
	if !adminKeyAllowed(c.adminSecret(), msg.GetAdminKey()) {
		log.Printf("logic: grantcoins rejected: bad admin key")
		return &protos.GrantCoinsReply{Ok: false, Reason: ReasonForbidden}, nil
	}

	coins, err := c.service.GrantCoins(ctx, msg.GetAccountId(), msg.GetDelta())
	if err != nil {
		return &protos.GrantCoinsReply{Ok: false, Reason: ReasonOf(err)}, nil
	}
	return &protos.GrantCoinsReply{Ok: true, Coins: coins}, nil
}

func (c *Component) boundAccount(ctx context.Context) string {
	if s := c.app.GetSessionFromCtx(ctx); s != nil {
		return s.UID()
	}
	return ""
}

func stateReply(state State, err error) *protos.LogicStateReply {
	if err != nil {
		return &protos.LogicStateReply{Ok: false, Reason: ReasonOf(err)}
	}
	reply := &protos.LogicStateReply{
		Ok:                    true,
		Coins:                 state.Coins,
		EquippedPrimaryWeapon: state.EquippedPrimaryWeapon,
		Items:                 make([]*protos.LogicShopItem, 0, len(state.Items)),
	}
	for _, item := range state.Items {
		reply.Items = append(reply.Items, &protos.LogicShopItem{
			ItemId:        item.ItemID,
			DisplayName:   item.DisplayName,
			Price:         item.Price,
			EquipSlot:     item.EquipSlot,
			OwnedQuantity: item.OwnedQuantity,
		})
	}
	return reply
}

func (c *Component) State(ctx context.Context, _ *protos.LogicStateMsg) (*protos.LogicStateReply, error) {
	state, err := c.service.State(ctx, c.boundAccount(ctx))
	return stateReply(state, err), nil
}

func (c *Component) Purchase(ctx context.Context, msg *protos.PurchaseMsg) (*protos.LogicStateReply, error) {
	state, err := c.service.Purchase(ctx, c.boundAccount(ctx), msg.ItemId, msg.Quantity)
	return stateReply(state, err), nil
}

func (c *Component) Equip(ctx context.Context, msg *protos.EquipMsg) (*protos.LogicStateReply, error) {
	state, err := c.service.Equip(ctx, c.boundAccount(ctx), msg.ItemId)
	return stateReply(state, err), nil
}

// Profile 是客户端请求 handler（route "logic.profile"）：个人档案 + 最近对局历史。
// 身份只来自会话绑定，客户端不能自报 uid。
func (c *Component) Profile(ctx context.Context, _ *protos.PlayerProfileMsg) (*protos.PlayerProfileReply, error) {
	profile, err := c.service.Profile(ctx, c.boundAccount(ctx))
	if err != nil {
		return &protos.PlayerProfileReply{Ok: false, Reason: ReasonOf(err)}, nil
	}
	reply := &protos.PlayerProfileReply{
		Ok:             true,
		Level:          Level(profile.XP),
		Xp:             profile.XP,
		XpIntoLevel:    XPIntoLevel(profile.XP),
		XpForNextLevel: XPForNextLevel(),
		Kills:          profile.Kills,
		Deaths:         profile.Deaths,
		Matches:        profile.Matches,
		Wins:           profile.Wins,
		Losses:         profile.Losses,
		RecentMatches:  make([]*protos.MatchRecord, 0, len(profile.Recent)),
	}
	for _, rec := range profile.Recent {
		reply.RecentMatches = append(reply.RecentMatches, &protos.MatchRecord{
			MatchId:         rec.MatchID,
			Won:             rec.Won,
			Kills:           rec.Kills,
			Deaths:          rec.Deaths,
			OpponentKills:   rec.OpponentKills,
			DurationSeconds: rec.DurationSeconds,
			OpponentName:    rec.OpponentName,
			EndedAt:         rec.EndedAt,
		})
	}
	return reply, nil
}

package logic

import (
	"context"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/component"
	"joltgo/game/protos"
	gatepb "joltgo/gate/protos"
)

type Component struct {
	component.Base
	app     pitaya.Pitaya
	service *Service
}

// Logic 是 logic 节点注册给 pitaya 的**唯一组件**：把两个方法集合并到一起。
//
// 为什么要合并：pitaya 的 remote 表按**服务名**注册（见内置 service/remote.go 的
// Register：同名会报 "remote: service already defined"），而 handler 与 remote 又必须
// 用同一个服务名 "logic"。所以一个节点只能注册一个 "logic" 组件，它必须同时提供：
//
//   - **客户端经 gate 转发的 handler**（Component 的方法）：State / Purchase / Equip /
//     Profile / GrantCoins
//   - **服务之间的 remote**（Remote 的方法）：Online（account 登录时调）/
//     RecordMatch（game 结算时调）
//
// 用嵌入而不是手写转发：方法被提升后，pitaya 的反射仍然能按原名收录（route 名不变）。
type Logic struct {
	// Base 由组合类型自己持有，**不能**只靠嵌入两个都带 Base 的结构：
	// 那样 AfterInit / Init 等方法会有两个提升来源，Go 报 ambiguous selector。
	component.Base
	*Component
	*Remote
}

// New 构造 logic 节点唯一的组件实例。
func New(app pitaya.Pitaya, service *Service) *Logic {
	return &Logic{
		Component: NewComponent(app, service),
		Remote:    NewRemote(service),
	}
}

func NewComponent(app pitaya.Pitaya, service *Service) *Component {
	return &Component{app: app, service: service}
}

// GrantCoins 是远端 RPC handler（route "logic.logic.grantcoins"）：GM 请求给账号发钱。
//
// ⚠️ 它**同时**在 remotes 表与客户端可达的 handlers 表里，这是 pitaya 的收录规则决定的，
// 拆组件也躲不掉：`isHandlerMethod` 只要求「两三个入参（ctx + 指针）、两个返回值
// （指针 + error）」，而这个签名恰好如此（见内置 component/method.go）。ExtractHandler
// 也没有任何排除机制。
//
// 所以**客户端可达性由 gate 的转发白名单负责**：gate 只放行
// gate/protos/gate.proto 里定义过的 route（见 joltgo/gate/routes.go 的 allowedRoutes），
// 而 logic.logic.grantcoins 不在清单里 —— 客户端发它会在 gate 被拒（通用 route not found）。
// 集群内部进程本就能调它，那不是鉴权能解决的问题。
//
// 参数校验与「账号不存在不隐式建档」仍然在 Service.GrantCoins 里。
func (c *Component) GrantCoins(ctx context.Context, msg *protos.GrantCoinsMsg) (*protos.GrantCoinsReply, error) {
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

func stateReply(state State, err error) *gatepb.LogicStateReply {
	if err != nil {
		return &gatepb.LogicStateReply{Ok: false, Reason: ReasonOf(err)}
	}
	reply := &gatepb.LogicStateReply{
		Ok:                    true,
		Coins:                 state.Coins,
		EquippedPrimaryWeapon: state.EquippedPrimaryWeapon,
		Items:                 make([]*gatepb.LogicShopItem, 0, len(state.Items)),
	}
	for _, item := range state.Items {
		reply.Items = append(reply.Items, &gatepb.LogicShopItem{
			ItemId:        item.ItemID,
			DisplayName:   item.DisplayName,
			Price:         item.Price,
			EquipSlot:     item.EquipSlot,
			OwnedQuantity: item.OwnedQuantity,
		})
	}
	return reply
}

func (c *Component) State(ctx context.Context, _ *gatepb.LogicStateMsg) (*gatepb.LogicStateReply, error) {
	state, err := c.service.State(ctx, c.boundAccount(ctx))
	return stateReply(state, err), nil
}

func (c *Component) Purchase(ctx context.Context, msg *gatepb.PurchaseMsg) (*gatepb.LogicStateReply, error) {
	state, err := c.service.Purchase(ctx, c.boundAccount(ctx), msg.ItemId, msg.Quantity)
	return stateReply(state, err), nil
}

func (c *Component) Equip(ctx context.Context, msg *gatepb.EquipMsg) (*gatepb.LogicStateReply, error) {
	state, err := c.service.Equip(ctx, c.boundAccount(ctx), msg.ItemId)
	return stateReply(state, err), nil
}

// Profile 是客户端请求 handler（route "logic.profile"）：个人档案 + 最近对局历史。
// 身份只来自会话绑定，客户端不能自报 uid。
func (c *Component) Profile(ctx context.Context, _ *gatepb.PlayerProfileMsg) (*gatepb.PlayerProfileReply, error) {
	profile, err := c.service.Profile(ctx, c.boundAccount(ctx))
	if err != nil {
		return &gatepb.PlayerProfileReply{Ok: false, Reason: ReasonOf(err)}, nil
	}
	reply := &gatepb.PlayerProfileReply{
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
		RecentMatches:  make([]*gatepb.MatchRecord, 0, len(profile.Recent)),
	}
	for _, rec := range profile.Recent {
		reply.RecentMatches = append(reply.RecentMatches, &gatepb.MatchRecord{
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

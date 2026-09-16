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

func NewComponent(app pitaya.Pitaya, service *Service) *Component {
	return &Component{app: app, service: service}
}

// GrantCoins 是远端 RPC handler（route "logic.logic.grantcoins"）：GM 请求给账号发钱。
//
// 不做调用方鉴权（2026-09-16 的明确取舍）：客户端发不到这条 route —— 它不在 gate 的
// 转发白名单里（见 joltgo/gate/routes.go 的 allowedRoutes），而 gate 是客户端唯一的
// 入口。集群内部进程本就能调它，那不是鉴权能解决的问题。
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

package logic

import (
	"context"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/component"
	"joltgo/game/protos"
)

type Component struct {
	component.Base
	app     pitaya.Pitaya
	service *Service
}

func NewComponent(app pitaya.Pitaya, service *Service) *Component {
	return &Component{app: app, service: service}
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

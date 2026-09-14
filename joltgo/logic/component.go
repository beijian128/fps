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

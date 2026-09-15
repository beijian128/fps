package logic

import (
	"context"

	"github.com/topfreegames/pitaya/v3/pkg/component"
	"joltgo/game/protos"
)

type Remote struct {
	component.Base
	service *Service
}

func NewRemote(service *Service) *Remote {
	return &Remote{service: service}
}

func (r *Remote) Online(ctx context.Context, msg *protos.UserOnlineMsg) (*protos.UserOnlineReply, error) {
	if err := r.service.EnsureProfile(ctx, msg.AccountId); err != nil {
		return &protos.UserOnlineReply{Ok: false, Reason: ReasonOf(err)}, nil
	}
	return &protos.UserOnlineReply{Ok: true}, nil
}

// RecordMatch 是服务间 remote（route "logic.recordmatch"）：game 在一局分出胜负时上报。
//
// 必须是 remote 而不是 handler：game 用 app.RPC 打过来，走 RPCType_User → remotes 表。
func (r *Remote) RecordMatch(ctx context.Context, msg *protos.RecordMatchMsg) (*protos.RecordMatchReply, error) {
	slots := make([]MatchSlot, 0, len(msg.Slots))
	for _, slot := range msg.Slots {
		if slot == nil {
			slots = append(slots, MatchSlot{})
			continue
		}
		slots = append(slots, MatchSlot{UID: slot.Uid, Kills: slot.Kills, Deaths: slot.Deaths})
	}
	applied, err := r.service.RecordMatch(ctx, MatchResult{
		MatchID:         msg.MatchId,
		Slots:           slots,
		WinnerSlot:      msg.WinnerSlot,
		DurationSeconds: msg.DurationSeconds,
	})
	if err != nil {
		return &protos.RecordMatchReply{Ok: false, Reason: ReasonOf(err)}, nil
	}
	if !applied {
		return &protos.RecordMatchReply{Ok: true, Applied: false, Reason: ReasonNotEnoughPlayers}, nil
	}
	return &protos.RecordMatchReply{Ok: true, Applied: true}, nil
}

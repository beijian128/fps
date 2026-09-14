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

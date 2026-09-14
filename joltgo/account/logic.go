package account

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"joltgo/game/protos"
)

const logicOnlineRoute = "logic.logic.online"

type LogicNotifier interface {
	NotifyOnline(context.Context, string) error
}

type logicNotifier struct {
	app pitaya.Pitaya
}

func NewLogicNotifier(app pitaya.Pitaya) LogicNotifier {
	return &logicNotifier{app: app}
}

func (n *logicNotifier) NotifyOnline(ctx context.Context, accountID string) error {
	servers, err := n.app.GetServersByType("logic")
	if err != nil {
		return err
	}
	if len(servers) == 0 {
		return errors.New("account: no logic server available")
	}
	ids := make([]string, 0, len(servers))
	for id := range servers {
		ids = append(ids, id)
	}
	target := ids[rand.IntN(len(ids))]
	reply := &protos.UserOnlineReply{}
	if err := n.app.RPCTo(ctx, target, logicOnlineRoute, reply, &protos.UserOnlineMsg{
		AccountId: accountID,
	}); err != nil {
		return err
	}
	if !reply.Ok {
		return fmt.Errorf("account: logic online rejected: %s", reply.Reason)
	}
	return nil
}

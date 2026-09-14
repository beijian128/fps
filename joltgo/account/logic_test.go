package account

import (
	"context"
	"errors"
	"testing"

	"github.com/golang/protobuf/proto"
	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/cluster"
	"joltgo/game/protos"
)

type notifierCall struct {
	serverID string
	route    string
}

type notifierApp struct {
	pitaya.Pitaya
	servers       map[string]*cluster.Server
	srvErr        error
	rpcErr        error
	calls         []notifierCall
	ok            bool
	requestedType string
}

func (a *notifierApp) GetServersByType(serverType string) (map[string]*cluster.Server, error) {
	a.requestedType = serverType
	return a.servers, a.srvErr
}

func (a *notifierApp) RPCTo(_ context.Context, serverID, route string, reply proto.Message, arg proto.Message) error {
	a.calls = append(a.calls, notifierCall{serverID: serverID, route: route})
	if a.rpcErr != nil {
		return a.rpcErr
	}
	msg, ok := arg.(*protos.UserOnlineMsg)
	if !ok || msg.AccountId != "7" {
		return errors.New("unexpected user online message")
	}
	out := reply.(*protos.UserOnlineReply)
	out.Ok = a.ok
	if !a.ok {
		out.Reason = ReasonInternal
	}
	return nil
}

func TestLogicNotifierCallsRandomLogicNode(t *testing.T) {
	app := &notifierApp{
		servers: map[string]*cluster.Server{"logic-a": {ID: "logic-a"}},
		ok:      true,
	}
	if err := NewLogicNotifier(app).NotifyOnline(context.Background(), "7"); err != nil {
		t.Fatalf("NotifyOnline: %v", err)
	}
	if app.requestedType != "logic" {
		t.Fatalf("GetServersByType serverType=%q", app.requestedType)
	}
	if len(app.calls) != 1 || app.calls[0].serverID != "logic-a" || app.calls[0].route != "logic.logic.online" {
		t.Fatalf("calls=%+v", app.calls)
	}
}

func TestLogicNotifierRejectsMissingOrFailedLogic(t *testing.T) {
	if err := NewLogicNotifier(&notifierApp{}).NotifyOnline(context.Background(), "7"); err == nil {
		t.Fatal("无 logic 节点应失败")
	}
	app := &notifierApp{
		servers: map[string]*cluster.Server{"logic-a": {ID: "logic-a"}},
		ok:      false,
	}
	if err := NewLogicNotifier(app).NotifyOnline(context.Background(), "7"); err == nil {
		t.Fatal("logic 返回 ok=false 应失败")
	}
}

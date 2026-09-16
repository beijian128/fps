package gate

import (
	"context"
	"testing"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/cluster"
	"github.com/topfreegames/pitaya/v3/pkg/route"
	"github.com/topfreegames/pitaya/v3/pkg/router"
)

type routeRecordingApp struct {
	pitaya.Pitaya
	routes []string
}

func (a *routeRecordingApp) AddRoute(name string, _ router.RoutingFunc) error {
	a.routes = append(a.routes, name)
	return nil
}

func TestConfigureAddsLogicRoute(t *testing.T) {
	app := &routeRecordingApp{}
	if err := Configure(app); err != nil {
		t.Fatal(err)
	}
	want := []string{"account", "match", "logic", "game"}
	if len(app.routes) != len(want) {
		t.Fatalf("routes=%v", app.routes)
	}
	for i := range want {
		if app.routes[i] != want[i] {
			t.Fatalf("routes=%v", app.routes)
		}
	}
}

func TestRouteRandom(t *testing.T) {
	servers := map[string]*cluster.Server{
		"logic-a": {ID: "logic-a"},
		"logic-b": {ID: "logic-b"},
	}
	// 必须传一条白名单内的 route：路由函数现在先过白名单（见 routes.go），
	// 传 nil 会被判成 route not found。
	rt, err := route.Decode("logic.logic.state")
	if err != nil {
		t.Fatal(err)
	}
	got, err := routeRandom(context.Background(), rt, nil, servers)
	if err != nil || got == nil || (got.ID != "logic-a" && got.ID != "logic-b") {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if _, err := routeRandom(context.Background(), rt, nil, nil); err == nil {
		t.Fatal("无节点应报错")
	}
}

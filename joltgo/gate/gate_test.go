package gate

import (
	"context"
	"testing"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/cluster"
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
	got, err := routeRandom(context.Background(), nil, nil, servers)
	if err != nil || got == nil || (got.ID != "logic-a" && got.ID != "logic-b") {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if _, err := routeRandom(context.Background(), nil, nil, nil); err == nil {
		t.Fatal("无节点应报错")
	}
}

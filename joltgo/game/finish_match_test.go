package game

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/golang/protobuf/proto"
	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"joltgo/game/protos"
	"joltgo/sim"
)

// recordingApp 只实现本任务用到的两个方法：推送与 RPC。
type recordingApp struct {
	pitaya.Pitaya
	mu       sync.Mutex
	pushed   []string
	pushArg  []interface{}
	pushUIDs [][]string
	rpcRoute []string
	rpcArg   []interface{}
}

func (a *recordingApp) SendPushToUsers(route string, v interface{}, uids []string, frontendType string) ([]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pushed = append(a.pushed, route)
	a.pushArg = append(a.pushArg, v)
	a.pushUIDs = append(a.pushUIDs, uids)
	return nil, nil
}

func (a *recordingApp) RPC(ctx context.Context, routeStr string, reply proto.Message, arg proto.Message) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rpcRoute = append(a.rpcRoute, routeStr)
	a.rpcArg = append(a.rpcArg, arg)
	return nil
}

func (a *recordingApp) waitRPCs(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		got := len(a.rpcRoute)
		a.mu.Unlock()
		if got >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等 %d 次 RPC 超时", want)
}

func TestFinishMatchPushesThenReportsThenExits(t *testing.T) {
	app := &recordingApp{}
	exited := 0
	inst := &Instance{
		app:     app,
		matchID: "m1",
		uids:    []string{"7", "8"},
		stop:    make(chan struct{}),
		onExit:  func() { exited++ },
	}

	inst.finishMatch(sim.MatchOutcome{
		WinnerSlot:      1,
		Kills:           [sim.MaxPlayers]int32{3, 10},
		Deaths:          [sim.MaxPlayers]int32{10, 3},
		DurationSeconds: 84,
	})

	if len(app.pushed) != 1 || app.pushed[0] != endedRoute {
		t.Fatalf("应先推一条 %s，得到 %v", endedRoute, app.pushed)
	}
	ended, ok := app.pushArg[0].(*protos.MatchEnded)
	if !ok {
		t.Fatalf("onMatchEnded 载荷类型不对: %T", app.pushArg[0])
	}
	if ended.MatchId != "m1" || ended.WinnerSlot != 1 || ended.DurationSeconds != 84 {
		t.Fatalf("onMatchEnded 载荷不对: %+v", ended)
	}
	if len(ended.Slots) != 2 || ended.Slots[0].Uid != "7" || ended.Slots[1].Kills != 10 {
		t.Fatalf("onMatchEnded 的槽位数据不对: %+v", ended.Slots)
	}
	if len(app.pushUIDs[0]) != 2 || app.pushUIDs[0][0] != "7" {
		t.Fatalf("onMatchEnded 应推给两个玩家，得到 %v", app.pushUIDs[0])
	}

	app.waitRPCs(t, 1)
	if app.rpcRoute[0] != recordMatchRoute {
		t.Fatalf("上报 route = %s，期望 %s", app.rpcRoute[0], recordMatchRoute)
	}
	record, ok := app.rpcArg[0].(*protos.RecordMatchMsg)
	if !ok {
		t.Fatalf("上报载荷类型不对: %T", app.rpcArg[0])
	}
	if record.MatchId != "m1" || record.WinnerSlot != 1 || record.DurationSeconds != 84 {
		t.Fatalf("上报载荷不对: %+v", record)
	}
	if len(record.Slots) != 2 || record.Slots[0].Uid != "7" || record.Slots[0].Deaths != 10 || record.Slots[1].Uid != "8" {
		t.Fatalf("上报槽位数据不对: %+v", record.Slots)
	}

	select {
	case <-inst.stop:
	default:
		t.Fatal("实例必须被停止（否则 goroutine 不会退出）")
	}
	if exited != 1 {
		t.Fatalf("onExit 应恰好调用一次，得到 %d", exited)
	}
}

// 空槽位（单人局）也要照发：是否入账由 logic 判定。
func TestFinishMatchToleratesMissingSlot(t *testing.T) {
	app := &recordingApp{}
	inst := &Instance{
		app:     app,
		matchID: "solo",
		uids:    []string{"7"},
		stop:    make(chan struct{}),
	}
	inst.finishMatch(sim.MatchOutcome{WinnerSlot: 0, DurationSeconds: 10})
	app.waitRPCs(t, 1)

	record := app.rpcArg[0].(*protos.RecordMatchMsg)
	if len(record.Slots) != sim.MaxPlayers {
		t.Fatalf("槽位应补齐到 %d 个，得到 %d", sim.MaxPlayers, len(record.Slots))
	}
	if record.Slots[0].Uid != "7" || record.Slots[1].Uid != "" {
		t.Fatalf("空槽位必须是空串 uid，得到 %+v", record.Slots)
	}
	if len(app.pushUIDs[0]) != 1 {
		t.Fatalf("推送目标应只含在座玩家，得到 %v", app.pushUIDs[0])
	}
}

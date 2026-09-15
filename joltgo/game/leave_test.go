package game

// 放弃对局（release）的服务端语义测试。
//
// 这一组测试钉住的是 spec 里最容易做错的那句话：**放弃对局不是终结对局实例**，
// 而是把当前玩家从这一局里释放出来、并且不再参与这一局的结算。所以这里同时断言
// 「他不再收帧 / 注册表里没有他了」与「实例还在跑 / 对手那一局不受影响」。

import (
	"context"
	"testing"

	"joltgo/game/protos"
	"joltgo/sim"
)

// newIdleInstance 手搓一个**不启 goroutine** 的实例：Release 把命令投进 cmds，
// 测试自己取出来执行。断言因此是确定性的，不需要等实例 goroutine 跑完再回头读字段
// （那样读的是另一个 goroutine 写的状态，既慢又数据竞争）。
func newIdleInstance(uids []string) *Instance {
	return &Instance{
		app:     &recordingApp{},
		matchID: "m1",
		uids:    uids,
		cmds:    make(chan func(), 8),
		stop:    make(chan struct{}),
	}
}

// runPendingCommand 取出并执行实例命令队列里那一条命令（Release 投的）。
func runPendingCommand(t *testing.T, inst *Instance) {
	t.Helper()
	select {
	case cmd := <-inst.cmds:
		cmd()
	default:
		t.Fatal("Release 应该往实例命令队列里投一条命令")
	}
}

func instanceStopped(inst *Instance) bool {
	select {
	case <-inst.stop:
		return true
	default:
		return false
	}
}

// 释放一个槽位：他不再收帧、结算带 left 标记，而**实例照常跑**（对手还在局里）。
func TestReleaseKeepsInstanceForTheOtherPlayer(t *testing.T) {
	inst := newIdleInstance([]string{"7", "8"})

	inst.Release(1)
	runPendingCommand(t, inst)

	if !inst.left[1] {
		t.Fatal("被释放的槽位应标记为已离场")
	}
	if got := inst.presentUIDs(); len(got) != 1 || got[0] != "7" {
		t.Fatalf("在座玩家应只剩槽位 0，得到 %v", got)
	}
	if instanceStopped(inst) {
		t.Fatal("还有人在局里时不该终结实例 —— 放弃对局不是终止对局")
	}

	ended := inst.matchEnded(sim.MatchOutcome{WinnerSlot: 0, DurationSeconds: 60})
	if len(ended.Slots) != 2 {
		t.Fatalf("结算仍应带上两个槽位（放弃的那位用 left 标记区分），得到 %d", len(ended.Slots))
	}
	if ended.Slots[1].Uid != "8" || !ended.Slots[1].Left {
		t.Fatalf("放弃的槽位应保留 uid 且带 left=true，得到 %+v", ended.Slots[1])
	}
	if ended.Slots[0].Left {
		t.Fatalf("在座的那位不该带 left 标记: %+v", ended.Slots[0])
	}
}

// 释放最后一个在场玩家：实例已经没有接收者，立刻回收（与空闲回收同一条退出路径）。
func TestReleaseLastPlayerStopsInstance(t *testing.T) {
	inst := newIdleInstance([]string{"7"})
	exited := 0
	inst.onExit = func() { exited++ }

	inst.Release(0)
	runPendingCommand(t, inst)

	if !instanceStopped(inst) {
		t.Fatal("一个人都不剩时应终结实例（否则只剩一条空转的 goroutine）")
	}
	if exited != 1 {
		t.Fatalf("onExit 应恰好调用一次（从 uid→实例表里摘掉），得到 %d", exited)
	}
}

// 越界槽位（uid 名单比 MaxPlayers 短）不能 panic。
func TestReleaseIgnoresOutOfRangeSlot(t *testing.T) {
	inst := newIdleInstance([]string{"7"})

	inst.Release(3)

	if inst.left[0] || instanceStopped(inst) {
		t.Fatal("越界的槽位号应被忽略")
	}
	if got := inst.presentUIDs(); len(got) != 1 || got[0] != "7" {
		t.Fatalf("越界的槽位号不该动任何状态，得到 %v", got)
	}
}

// 客户端直接发 game.game.leave 必须被拒 —— 否则任何人都能拿别人的 uid 把他从
// 对局里踢出去（这个入口没有别的身份判据）。
func TestLeaveRejectsClientSession(t *testing.T) {
	c := New(&fakeApp{sess: clientSession(t)})
	inst := newIdleInstance([]string{"7", "8"})
	c.instances = map[string]*Instance{"m1": inst}
	c.uidToInst = map[string]*Instance{"7": inst}
	c.uidToIndex = map[string]int{"7": 0}

	reply, err := c.Leave(context.Background(), &protos.LeaveMsg{Uid: "7"})
	if err != nil {
		t.Fatalf("查询类接口被拒时不该报 Go error: %v", err)
	}
	if reply.Ok {
		t.Fatal("客户端发来的 leave 不能报告成功")
	}
	if c.uidToInst["7"] != inst {
		t.Fatal("被拒的 leave 不许动注册表")
	}
	if inst.left[0] {
		t.Fatal("被拒的 leave 不许改实例状态")
	}
}

// 后端 RPC（match 转发）释放成功：返回值、注册表、实例三处都要变。
func TestLeaveUnregistersUIDAndReleasesSlot(t *testing.T) {
	c := New(&fakeApp{sess: nil})
	inst := newIdleInstance([]string{"7", "8"})
	c.instances = map[string]*Instance{"m1": inst}
	c.uidToInst = map[string]*Instance{"7": inst, "8": inst}
	c.uidToIndex = map[string]int{"7": 0, "8": 1}

	reply, err := c.Leave(context.Background(), &protos.LeaveMsg{Uid: "7"})
	if err != nil || !reply.Ok {
		t.Fatalf("后端 RPC 应放行: %+v err=%v", reply, err)
	}
	if _, ok := c.uidToInst["7"]; ok {
		t.Fatal("释放后 uid→实例 映射必须摘掉，否则他还会被回局查询领回这一局")
	}
	if _, ok := c.uidToIndex["7"]; ok {
		t.Fatal("释放后 uid→槽位 映射必须摘掉")
	}
	if c.uidToInst["8"] != inst {
		t.Fatal("对手的映射不该受影响")
	}
	runPendingCommand(t, inst)
	if !inst.left[0] {
		t.Fatal("释放应落到他被分配的槽位上")
	}
	if got := inst.presentUIDs(); len(got) != 1 || got[0] != "8" {
		t.Fatalf("释放后只剩对手在场，得到 %v", got)
	}

	rejoin, err := c.Rejoin(context.Background(), &protos.RejoinMsg{Token: "7"})
	if err != nil || rejoin.Found {
		t.Fatalf("释放后不该再查到他的对局（他会去开新的一局）: %+v err=%v", rejoin, err)
	}
	if c.instances["m1"] != inst {
		t.Fatal("实例本身必须留着 —— 对手还在打这一局")
	}
}

// 查不到这个 uid 的实例（已经释放过 / 已经打完）：ok=false，且不动任何状态。
func TestLeaveReportsNotFoundWithoutInstance(t *testing.T) {
	c := New(&fakeApp{sess: nil})

	reply, err := c.Leave(context.Background(), &protos.LeaveMsg{Uid: "404"})
	if err != nil {
		t.Fatalf("Leave 不该报错: %v", err)
	}
	if reply.Ok {
		t.Fatal("没有实例时应回 ok=false")
	}
}

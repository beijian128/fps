package game

// Instance 是一个对局实例：一个匹配（matchId）对应一条 goroutine 独占驱动一个
// sim.Simulation。核心不变量：
//
//   - **单线程所有，无锁**：只有 run() 这条 goroutine 访问 sim（Step/ApplyInput/
//     Shoot/Reset/Shapshot），输入经命令 channel 投递、由同一 goroutine 顺序执行，
//     因此 sim 内部不需要任何互斥锁。
//   - 20 Hz tick 由同一条 goroutine 驱动，每 tick 把快照经 pitaya 推给局内玩家。
//
// 生命周期：Create（remote RPC）构造并 Start → Stop 在 game 组件 Shutdown 时调用。

import (
	"log"
	"time"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"joltgo/physics"
	"joltgo/sim"
)

// Instance 对局实例。
type Instance struct {
	app     pitaya.Pitaya
	sim     *sim.Simulation
	matchID string
	uids    []string // 玩家 uid，下标即 player_idx（槽位 0/1）

	cmds chan func() // 命令队列：输入/射击/重置（由 run goroutine 顺序消费）
	stop chan struct{}
}

// NewInstance 构造对局实例（物理世界在 Start 时创建）。
func NewInstance(app pitaya.Pitaya, matchID string, uids []string) *Instance {
	return &Instance{
		app:     app,
		sim:     sim.New(physics.New()),
		matchID: matchID,
		uids:    uids,
		cmds:    make(chan func(), 128),
		stop:    make(chan struct{}),
	}
}

// Start 创建物理世界并启动对局 goroutine。
func (i *Instance) Start() {
	i.sim.Init()
	go i.run()
}

// Stop 停止对局 goroutine 并释放物理世界。
func (i *Instance) Stop() {
	close(i.stop)
}

// run 是实例唯一的执行 goroutine：顺序消费命令 + 20 Hz tick + 广播快照。
func (i *Instance) run() {
	defer i.sim.Shutdown()
	ticker := time.NewTicker(time.Second / 20)
	defer ticker.Stop()
	for {
		select {
		case cmd := <-i.cmds:
			cmd()
		case <-ticker.C:
			i.sim.Step()
			i.broadcast()
		case <-i.stop:
			return
		}
	}
}

// enqueue 把命令投进实例 goroutine 的命令队列（线程安全，供 RPC handler 调用）。
// 实例停止后投递会静默失败（select 走 stop 分支）。
func (i *Instance) enqueue(cmd func()) {
	select {
	case i.cmds <- cmd:
	case <-i.stop:
	}
}

// broadcast 把当前快照（转成 protobuf）推给局内所有玩家（经 gate 前端）。
func (i *Instance) broadcast() {
	snap := toSnapshot(i.sim.Snapshot())
	if _, err := i.app.SendPushToUsers(snapRoute, snap, i.uids, frontendType); err != nil {
		log.Printf("instance %s broadcast: %v", i.matchID, err)
	}
}

// ApplyInput 玩家输入（playerIdx 由 game 组件按 uid 映射，yaw 为水平朝向弧度）。
func (i *Instance) ApplyInput(playerIdx int, move [2]float32, yaw float32, jump bool) {
	i.enqueue(func() { i.sim.ApplyInput(playerIdx, move, yaw, jump) })
}

// Shoot 发射弹丸（origin/dir 为枪口与朝向，归一化在 sim 内完成）。
func (i *Instance) Shoot(origin, dir [3]float32) {
	i.enqueue(func() { i.sim.Shoot(origin, dir) })
}

// Reset 重建对局场景。
func (i *Instance) Reset() {
	i.enqueue(func() { i.sim.Reset() })
}

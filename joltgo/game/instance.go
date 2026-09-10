package game

// Instance 是一个对局实例：一个匹配（matchId）对应一条 goroutine 独占驱动一个
// sim.Simulation。核心不变量：
//
//   - **单线程所有，无锁**：只有 run() 这条 goroutine 访问 sim（Step/ApplyInput/
//     Shoot/Reset/DrainFrame/FullFrame），输入经命令 channel 投递、由同一 goroutine
//     顺序执行，因此 sim 内部不需要任何互斥锁。
//   - 20 Hz tick 由同一条 goroutine 驱动，每 tick 先把 replication store 里的本帧
//     增量取走（DrainFrame，恰好调用一次、负责清脏），再按槽位下发：登记了重连 /
//     resync 的槽位（pendingFull）收全量帧，其余槽位收增量帧。
//
// 生命周期：Create（remote RPC）构造并 Start → Stop 由 game 组件 Shutdown 调用，
// 也会在**空闲自退**时由实例自己调用；stopOnce 保证两条路径都安全。

import (
	"log"
	"sync"
	"time"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"joltgo/physics"
	"joltgo/sim"
)

// instanceIdleTimeout 是「所有槽位都无上行消息」多久之后结束实例。
// 远大于客户端 1s 重连 + 2.5s 看门狗，正常重连不会误杀。
const instanceIdleTimeout = 60 * time.Second

// Instance 对局实例。
type Instance struct {
	app     pitaya.Pitaya
	sim     *sim.Simulation
	matchID string
	uids    []string // 玩家 uid，下标即 player_idx（槽位 0/1）

	pendingFull [sim.MaxPlayers]bool // 本 tick 需要下发全量的槽位（重连 / resync）

	lastSeen [sim.MaxPlayers]time.Time // 各槽位最近一次上行时间（仅 run goroutine 读写）
	onExit   func()                    // 实例自行退出时的回调（由 game 组件设置）
	stopOnce sync.Once

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
	startedAt := time.Now()
	for slot := range i.lastSeen {
		i.lastSeen[slot] = startedAt
	}
	i.sim.Init()
	go i.run()
}

// Stop 停止对局 goroutine 并释放物理世界。
func (i *Instance) Stop() {
	i.stopOnce.Do(func() { close(i.stop) })
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
			if i.idleExpired() {
				log.Printf("instance %s: %v 无玩家上行，结束对局", i.matchID, instanceIdleTimeout)
				// 关掉 stop：退出后没有 goroutine 再消费 cmds，正在并发的
				// RPC handler 若还持有实例指针，enqueue 会卡在写满的 channel 上。
				// 用 defer 而不是直接调用，是为了 onExit 万一 panic 也一定会关。
				defer i.Stop()
				if i.onExit != nil {
					i.onExit()
				}
				return
			}
		case <-i.stop:
			return
		}
	}
}

// idleExpired 报告所有槽位是否都已超过 instanceIdleTimeout 没有上行消息。
// 用「最近一次收到上行」而不是 pitaya 的会话状态判断在线：后者跨服务不可见，
// 前者是实例本就持有的信息。
func (i *Instance) idleExpired() bool {
	for slot := range i.uids {
		if time.Since(i.lastSeen[slot]) < instanceIdleTimeout {
			return false
		}
	}
	return true
}

// touch 刷新某个槽位的在线时间（由 run goroutine 调用）。
func (i *Instance) touch(slot int) {
	if slot >= 0 && slot < len(i.lastSeen) {
		i.lastSeen[slot] = time.Now()
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

// broadcast 把本 tick 的增量推给局内玩家；被标记为待全量的槽位改推全量帧。
// DrainFrame 每 tick 必须恰好调用一次（它负责清脏），所以先取帧再决定收件人。
func (i *Instance) broadcast() {
	delta := i.sim.DrainFrame()

	var deltaUIDs, fullUIDs []string
	for slot, uid := range i.uids {
		if i.pendingFull[slot] {
			i.pendingFull[slot] = false
			fullUIDs = append(fullUIDs, uid)
		} else {
			deltaUIDs = append(deltaUIDs, uid)
		}
	}

	if len(deltaUIDs) > 0 {
		if _, err := i.app.SendPushToUsers(frameRoute, toFrame(delta), deltaUIDs, frontendType); err != nil {
			log.Printf("instance %s 广播增量: %v", i.matchID, err)
		}
	}
	if len(fullUIDs) > 0 {
		full := i.sim.FullFrame()
		if _, err := i.app.SendPushToUsers(frameRoute, toFrame(full), fullUIDs, frontendType); err != nil {
			log.Printf("instance %s 下发全量: %v", i.matchID, err)
		}
	}
}

// RequestFull 把某个槽位的下一帧标为全量（客户端 resync / 重连时调用）。
func (i *Instance) RequestFull(slot int) {
	if slot < 0 || slot >= len(i.uids) {
		return
	}
	i.enqueue(func() {
		i.touch(slot)
		i.pendingFull[slot] = true
	})
}

// MatchID 返回对局 id（match 服务回局查询时用）。
func (i *Instance) MatchID() string { return i.matchID }

// ApplyInput 玩家输入（playerIdx 由 game 组件按 uid 映射，yaw 为水平朝向弧度）。
func (i *Instance) ApplyInput(playerIdx int, move [2]float32, yaw float32, jump bool) {
	i.enqueue(func() {
		i.touch(playerIdx)
		i.sim.ApplyInput(playerIdx, move, yaw, jump)
	})
}

// Shoot 发射弹丸（origin/dir 为枪口与朝向，归一化在 sim 内完成）。
func (i *Instance) Shoot(origin, dir [3]float32) {
	i.enqueue(func() { i.sim.Shoot(origin, dir) })
}

// Reset 重建对局场景。
func (i *Instance) Reset() {
	i.enqueue(func() { i.sim.Reset() })
}

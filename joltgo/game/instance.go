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
	"context"
	"log"
	"sync"
	"time"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"joltgo/bot"
	"joltgo/game/protos"
	"joltgo/physics"
	"joltgo/sim"
)

// instanceIdleTimeout 是「所有槽位都无上行消息」多久之后结束实例。
//
// 30 分钟同时是掉线回局窗口：服务端权威、对局不因掉线暂停，玩家在这段时间内 resume
// 回来仍能通过 tryRejoin 接回原局。远大于客户端 1s 重连 + 2.5s 看门狗，正常重连不会
// 误杀。这条超时只对「谁都没打死谁」的局生效 —— 判出胜负的局会在结算时立刻终结。
const instanceIdleTimeout = 30 * time.Minute

// recordMatchTimeout 是上报战绩的 RPC 超时（在独立 goroutine 里发送，不阻塞 tick）。
const recordMatchTimeout = 2 * time.Second

// Instance 对局实例。
type Instance struct {
	app     pitaya.Pitaya
	sim     *sim.Simulation
	matchID string
	uids    []string // 玩家 uid，下标即 player_idx（槽位 0/1）

	pendingFull [sim.MaxPlayers]bool // 本 tick 需要下发全量的槽位（重连 / resync）
	// left 标记「这个槽位的玩家已经放弃了对局」（见 Release）。它与「空槽位」是两回事：
	// uid 仍然是他的账号 ID（对局开始时就是两个人），只是这个人从此不再收帧、不再被
	// 结算。对局本身照常推进 —— 对手那一局不因为有人放弃而中断。
	left [sim.MaxPlayers]bool

	// bots 标记「这个槽位是机器人」。它与 left 是两件事：left 的语义是「本来有这个
	// 人，他中途放弃了」（logic 靠它区分「放弃」与「空槽位」），把机器人标成 left
	// 会让结算日志出现「机器人在中途放弃」这种假话。
	//
	// 机器人的效果是：不收帧、不在推送目标里、不进实例注册表、不参与回局 ——
	// 也就是「没有客户端的槽位」。结算侧不需要特殊处理：它的 SlotResult.Uid 本来就是
	// 空串，logic 的 RecordMatch 已经会跳过空槽位。
	bots [sim.MaxPlayers]bool

	lastSeen [sim.MaxPlayers]time.Time // 各槽位最近一次上行时间（仅 run goroutine 读写）
	onExit   func()                    // 实例自行退出时的回调（由 game 组件设置）
	stopOnce sync.Once

	cmds chan func() // 命令队列：输入/射击/重置（由 run goroutine 顺序消费）
	stop chan struct{}
}

// NewInstance 构造对局实例（物理世界在 Start 时创建）。
func NewInstance(app pitaya.Pitaya, matchID string, uids []string) *Instance {
	inst := &Instance{
		app:     app,
		sim:     sim.New(physics.New()),
		matchID: matchID,
		uids:    uids,
		cmds:    make(chan func(), 128),
		stop:    make(chan struct{}),
	}
	// 机器人 uid 前缀是唯一的判据（见 joltgo/bot）：match 也是用它决定
	// 「跳过在线探活」的，两边的认识必须一致 —— 前缀一旦不一致，这里就会给
	// 一个真人不收帧、或者给机器人推帧。
	for slot, uid := range inst.uids {
		if slot < sim.MaxPlayers {
			inst.bots[slot] = bot.Is(uid)
		}
	}
	return inst
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
			if out, ok := i.sim.DrainOutcome(); ok {
				// 上面那次 broadcast 很关键：含 Game.Winner 的帧必须在 onMatchEnded 之前出去。
				i.finishMatch(out)
				return
			}
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

// finishMatch 是一局结束后的收尾，只能由实例 goroutine 调用：先推送（此时本 tick 的
// 增量帧已经 broadcast 过），再异步上报，最后停止实例并摘注册表。
//
// 与空闲回收那条退出路径等价（`defer i.Stop()` + `onExit()` + return）：两件事都做，
// 少做一件都会留下问题 —— 不 Stop 则 goroutine 不退出，不 onExit 则 uid→实例表永久
// 留着死实例，玩家点「再来一局」会被 pushMatched 塞回一个不再有帧的对局。
func (i *Instance) finishMatch(out sim.MatchOutcome) {
	if uids := i.presentUIDs(); len(uids) > 0 {
		if _, err := i.app.SendPushToUsers(endedRoute, i.matchEnded(out), uids, frontendType); err != nil {
			log.Printf("instance %s: push %s failed: %v", i.matchID, endedRoute, err)
		}
	}
	go i.reportMatch(out)
	i.Stop()
	if i.onExit != nil {
		i.onExit()
	}
}

// presentUIDs 返回在座玩家的 uid（跳过空槽位与已经放弃对局的槽位）。
func (i *Instance) presentUIDs() []string {
	out := make([]string, 0, len(i.uids))
	for slot, uid := range i.uids {
		if uid != "" && !i.left[slot] && !i.bots[slot] {
			out = append(out, uid)
		}
	}
	return out
}

// Release 把一个玩家从本实例里释放出来（客户端选择「放弃对局」）。
//
// 语义（spec）：**只释放这一个玩家**，不终结实例 ——
//   - 他从此不再收增量/全量帧（broadcast 跳过，presentUIDs 也不含他），
//   - 结算时他的槽位带上 left 标记，logic 据此把他排除在战绩之外，
//   - onMatchEnded 也不推给他（他已经不在这个对局里了）。
//
// 实例本身继续跑：对手还在局里，他那一局照常打到分出胜负。槽位（也就是那个静止的
// 角色）留在世界里的意义正在于此 —— 对手仍然能把这一局打完、拿到正常的结算。
//
// 命令投进实例 goroutine（唯一的写者），所以 left/pendingFull 不需要加锁。
func (i *Instance) Release(slot int) {
	if slot < 0 || slot >= len(i.uids) {
		return
	}
	i.enqueue(func() {
		i.left[slot] = true
		// 被释放的槽位不该再收全量帧（它已经不收任何帧了）。
		i.pendingFull[slot] = false
		if len(i.presentUIDs()) == 0 {
			// 最后一个玩家也走了：实例已经没有任何接收者，留它只是空转一条 goroutine
			// 和一个 Jolt 世界。与空闲回收那条路径等价（Stop + onExit + return）。
			defer i.Stop()
			if i.onExit != nil {
				i.onExit()
			}
		}
	})
}

func (i *Instance) uidAt(slot int) string {
	if slot < 0 || slot >= len(i.uids) {
		return ""
	}
	return i.uids[slot]
}

func (i *Instance) matchEnded(out sim.MatchOutcome) *protos.MatchEnded {
	msg := &protos.MatchEnded{
		MatchId:         i.matchID,
		WinnerSlot:      int32(out.WinnerSlot),
		DurationSeconds: out.DurationSeconds,
		Slots:           make([]*protos.SlotResult, 0, sim.MaxPlayers),
	}
	for slot := 0; slot < sim.MaxPlayers; slot++ {
		msg.Slots = append(msg.Slots, &protos.SlotResult{
			Uid:    i.uidAt(slot),
			Kills:  out.Kills[slot],
			Deaths: out.Deaths[slot],
			Left:   i.left[slot],
		})
	}
	return msg
}

// reportMatch 在独立 goroutine 里上报战绩：**绝不能在实例 goroutine 里同步发** ——
// pitaya 的 RPC 默认 5 秒超时，卡住就是 100 个 tick 停摆、客户端看门狗立刻判定掉线。
// 用 RPC（不是 RPCTo）：RPCType_User 走 router 的 default route，从 game 节点可直接
// 选到一个 logic 节点，不需要额外 AddRoute。失败只记日志：不重试、不去重。
func (i *Instance) reportMatch(out sim.MatchOutcome) {
	ctx, cancel := context.WithTimeout(context.Background(), recordMatchTimeout)
	defer cancel()
	msg := &protos.RecordMatchMsg{
		MatchId:         i.matchID,
		WinnerSlot:      int32(out.WinnerSlot),
		DurationSeconds: out.DurationSeconds,
		Slots:           make([]*protos.SlotResult, 0, sim.MaxPlayers),
	}
	for slot := 0; slot < sim.MaxPlayers; slot++ {
		msg.Slots = append(msg.Slots, &protos.SlotResult{
			Uid:    i.uidAt(slot),
			Kills:  out.Kills[slot],
			Deaths: out.Deaths[slot],
			Left:   i.left[slot],
		})
	}
	reply := &protos.RecordMatchReply{}
	if err := i.app.RPC(ctx, recordMatchRoute, reply, msg); err != nil {
		log.Printf("instance %s: report match failed: %v", i.matchID, err)
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
		if uid == "" || i.left[slot] || i.bots[slot] {
			// 空槽位与「已放弃对局」的玩家都不收帧：前者没人，后者已经从这个对局里
			// 释放出去了（给他推帧等于把他继续留在这一局里）。机器人也没有客户端，
			// 给它推帧既没人收，也白走一趟 NATS 用户频道。
			continue
		}
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

// Shoot 发射弹丸（playerIdx 是发射者槽位，origin/dir 为枪口与朝向，
// 归一化在 sim 内完成）。归属由服务端按会话查出，不信客户端上报的任何身份字段。
func (i *Instance) Shoot(playerIdx int, origin, dir [3]float32) {
	i.enqueue(func() { i.sim.Shoot(playerIdx, origin, dir) })
}

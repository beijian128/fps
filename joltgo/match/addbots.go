package match

import (
	"context"
	"log"

	"github.com/nats-io/nuid"
	"joltgo/bot"
	"joltgo/game/protos"
)

// maxBotsPerRequest 是一次 GM 指令最多入队的机器人数。
//
// 有上限不是为了保护队列（ZSET 塞几千个也没事），而是为了让「页面填错数字」的后果
// 可控：每个机器人后面都可能变成一个真的对局实例（一个 Jolt 世界 + 一条 goroutine），
// 而这一步是不可撤销的。超过就截断，并把实际入队数量回给页面。
const maxBotsPerRequest = 8

// AddBots 是远端 RPC handler（route "match.match.addbots"）：GM 请求往匹配队列里
// 塞 N 个机器人凑人数。
//
// **方法名就是 route**：pitaya 按「服务名 + 小写方法名」推出 route（见内置
// component/suitableRemoteMethods），所以这里必须叫 AddBots 而不是别的名字 ——
// 叫 QueueBots 的话 route 会变成 match.queuebots，gm 那边按 match.addbots 调就会
// 拿到 "route not found"。这条约定在本项目里是一致的：Join → match.join、
// Pending → match.pending。
//
// **不做调用方鉴权**（2026-09-16 的明确取舍）：客户端发不到这条 route —— 它不在
// gate 的转发白名单里（见 joltgo/gate/routes.go 的 allowedRoutes），而 gate 是客户端
// 唯一的入口；集群内部进程本就能调它，那不是鉴权能解决的问题。
//
// 入队语义留在本包：gm 不碰 match:queue、也不构造 bot uid —— 队列的写入口只有
// Queue.Enqueue 这一条路径。
func (c *Component) AddBots(ctx context.Context, msg *protos.AddBotsMsg) (*protos.AddBotsReply, error) {
	count := int(msg.GetCount())
	if count <= 0 {
		return &protos.AddBotsReply{Ok: true, Reason: "noop"}, nil
	}
	if count > maxBotsPerRequest {
		count = maxBotsPerRequest
	}

	// nuid 是本仓库已有的直接依赖（match 用它生成 matchId），一串单调随机串，
	// 天然不重复，因此不需要 Redis 计数器。
	queued, err := c.queue.QueueBots(ctx, count, func() string { return bot.New(nuid.New().Next()) })
	if err != nil {
		log.Printf("match: addbots failed: %v", err)
		return &protos.AddBotsReply{Ok: false, Reason: "internal"}, nil
	}
	log.Printf("match: enqueued %d bots (requested %d)", queued, count)

	// 不等下一秒的 ticker：机器人通常立刻就能跟一个正在排队的真人配上。
	c.tryMatch(ctx)
	return &protos.AddBotsReply{Ok: true, Enqueued: int32(queued)}, nil
}

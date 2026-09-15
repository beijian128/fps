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

// QueueBots 是远端 RPC handler（route "match.match.addbots"）：GM 请求往匹配队列里
// 塞 N 个机器人凑人数。
//
// 两道入口检查（见 auth.go）：拒绝带会话的调用（客户端经 gate 发来的）、
// 校验共享密钥（任何后端都能调这条 route）。
//
// 入队语义留在本包：gm 不碰 match:queue、也不构造 bot uid —— 队列的写入口只有
// Queue.Enqueue 这一条路径。
func (c *Component) QueueBots(ctx context.Context, msg *protos.AddBotsMsg) (*protos.AddBotsReply, error) {
	if isClientCall(ctx, c.app) {
		log.Printf("match: addbots from client rejected")
		return &protos.AddBotsReply{Ok: false, Reason: "forbidden"}, nil
	}
	if !adminKeyAllowed(c.adminSecret(), msg.GetAdminKey()) {
		log.Printf("match: addbots rejected: bad admin key")
		return &protos.AddBotsReply{Ok: false, Reason: "forbidden"}, nil
	}

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

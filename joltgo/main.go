package main

// 分布式服务端入口：单二进制按 -type 启动四种角色（gate / account / match / game），
// pitaya Cluster 模式（etcd 服务发现 + NATS RPC），共享状态放 Redis。
//
//   - gate（frontend）：与客户端直连（WS），把业务消息路由到后端，
//     并登记「账号在哪个 gate 在线」（见 gate/session.go）
//   - account（backend）：账号注册/登录/凭证恢复。唯一碰密码与 Redis 账号数据的角色
//   - match（backend）：对局匹配，队列在 Redis（多节点共享）
//   - game（backend）：游戏逻辑，每个对局一个 goroutine 顺序执行、无锁
//
// 启动顺序：先起 etcd + nats-server + redis-server（见 deploy/），再起四个进程：
//
//	joltgo.exe -type gate    -port 8080
//	joltgo.exe -type account
//	joltgo.exe -type match
//	joltgo.exe -type game
//
// 优雅退出由 pitaya 的 app.Start() 内部处理（SIGINT/SIGTERM → shutdownComponents）。

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/acceptor"
	"github.com/topfreegames/pitaya/v3/pkg/component"
	"github.com/topfreegames/pitaya/v3/pkg/config"
	"github.com/topfreegames/pitaya/v3/pkg/groups"
	"joltgo/account"
	"joltgo/game"
	"joltgo/gate"
	"joltgo/kv"
	"joltgo/match"
	"joltgo/online"
)

func main() {
	svType := flag.String("type", "gate", "server type: gate | account | match | game")
	redisAddr := flag.String("redis", kv.DefaultAddr, "redis address (host:port)")
	flag.Parse()

	cfg := config.NewDefaultPitayaConfig()
	// protobuf serializer（serializertype=2），消息类型见 game/protos/game.proto；
	// 关消息压缩：客户端用纯 GDScript 解码，不引入 gzip。
	cfg.SerializerType = 2
	cfg.Handler.Messages.Compression = false

	isFrontend := *svType == "gate"
	builder := pitaya.NewDefaultBuilder(
		isFrontend,
		*svType,
		pitaya.Cluster, // 集群模式：etcd 服务发现 + NATS RPC
		map[string]string{},
		*cfg,
	)
	builder.Groups = groups.NewMemoryGroupService(cfg.Groups.Memory)

	if isFrontend {
		builder.AddAcceptor(acceptor.NewWSAcceptor(":8080"))
	}

	log.Fatalf("启动失败: %v", run(svType, builder, *redisAddr))
}

// run 组装并启动指定角色的服务。抽成函数是为了让 flag 解析与 defer 清理分离 ——
// main 里 log.Fatal 会跳过 defer，Redis 连接必须在这里关。
func run(svType *string, builder *pitaya.Builder, redisAddr string) error {
	// Redis 是三个角色的共享依赖（gate 写会话归属、account 存取账号与凭证、
	// match 存排队队列），但不是每个角色都必须在启动时连上：gate 的归属登记是
	// best-effort，连不上只降级；account/match 没有 Redis 则无法工作。
	// 这里统一「连不上就启动失败」——早失败比运行中途才暴露好排查。
	rdb, err := kv.Open(context.Background(), redisAddr)
	if err != nil {
		return err
	}
	defer rdb.Close()

	app := builder.Build()

	switch *svType {
	case "gate":
		if err := gate.Configure(app); err != nil {
			return err
		}
		// 会话归属：绑定后写 online:{uid} → 本节点，断开时清除。
		gate.RegisterSessionHooks(builder.SessionPool, app.GetServerID(), online.NewStore(rdb))
		app.Register(gate.NewSessionComponent(app, builder.SessionPool),
			component.WithName("gate"),
			component.WithNameFunc(strings.ToLower),
		)

	case "account":
		app.Register(account.New(app, account.NewStore(rdb), online.NewStore(rdb)),
			component.WithName("account"),
			component.WithNameFunc(strings.ToLower),
		)

	case "match":
		// TODO(Task 7): 改成 match.New(app, match.NewQueue(rdb), online.NewStore(rdb))
		// —— Redis 队列与探活还没实现，此处先保持旧的内存队列构造。
		app.Register(match.New(app),
			component.WithName("match"),
			component.WithNameFunc(strings.ToLower),
		)

	case "game":
		// 同一个组件实例同时注册为 handler（客户端经 gate 路由来的
		// game.cmd / game.resync，RPCType_Sys）与 remote（match 服务 RPCTo 来的
		// game.create / game.rejoin，RPCType_User）——两者的实例注册表必须共享。
		comp := game.New(app)
		app.Register(comp,
			component.WithName("game"),
			component.WithNameFunc(strings.ToLower),
		)
		app.RegisterRemote(comp,
			component.WithName("game"),
			component.WithNameFunc(strings.ToLower),
		)

	default:
		return fmt.Errorf("unknown server type %q (want gate|account|match|game)", *svType)
	}

	app.Start()
	return nil
}

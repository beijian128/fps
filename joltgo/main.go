package main

// 分布式服务端入口：单二进制按 -type 启动三种角色（gate / match / game），
// pitaya Cluster 模式（etcd 服务发现 + NATS RPC）。
//
//   - gate（frontend）：与客户端直连（WS），把业务消息路由到 match/game 后端
//   - match（backend）：对局匹配，配对后 RPC 通知 game 创建实例，并推结果给客户端
//   - game（backend）：游戏逻辑，每个对局一个 goroutine 顺序执行、无锁
//
// 启动顺序：先起 etcd + nats-server（见 deploy/），再起三个进程（或三个终端）：
//
//	joltgo.exe -type gate -port 8080
//	joltgo.exe -type match
//	joltgo.exe -type game
//
// 优雅退出由 pitaya 的 app.Start() 内部处理（SIGINT/SIGTERM → shutdownComponents
// 回调 game.Component.Shutdown 停掉所有对局实例并释放物理世界）。

import (
	"flag"
	"log"
	"strings"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/acceptor"
	"github.com/topfreegames/pitaya/v3/pkg/component"
	"github.com/topfreegames/pitaya/v3/pkg/config"
	"github.com/topfreegames/pitaya/v3/pkg/groups"
	"joltgo/game"
	"joltgo/gate"
	"joltgo/match"
)

func main() {
	svType := flag.String("type", "gate", "server type: gate | match | game")
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

	app := builder.Build()

	switch *svType {
	case "gate":
		if err := gate.Configure(app); err != nil {
			log.Fatalf("configure gate routes: %v", err)
		}
	case "match":
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
		log.Fatalf("unknown server type %q (want gate|match|game)", *svType)
	}

	app.Start()
}

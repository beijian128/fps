package main

// 分布式服务端入口：单二进制按 -type 启动六种角色（gate / account / logic / match / game / gm），
// pitaya Cluster 模式（etcd 服务发现 + NATS RPC），共享状态放 Redis。
//
//   - gate（frontend）：与客户端直连（WS），把业务消息路由到后端，
//     并登记「账号在哪个 gate 在线」（见 gate/session.go）
//   - account（backend）：账号注册/登录/凭证恢复。唯一碰密码与 Redis 账号数据的角色
//   - logic（backend）：玩家档案、钱包与库存，按会话账号提供商店与装备操作
//   - match（backend）：对局匹配，队列在 Redis（多节点共享）
//   - game（backend）：游戏逻辑，每个对局一个 goroutine 顺序执行、无锁
//   - gm（backend）：GM 指令入口。自己起一个 Gin HTTP 端口 + 内嵌 Web 操作页
//     （不对外开放、不进 gate 路由表、不监听任何 pitaya 端口），只**主动**发后端
//     RPC：match.match.addbots（往队列塞机器人）/ logic.logic.grantcoins（发钱）。
//
// 启动顺序：先起 etcd + nats-server + redis-server（见 deploy/），再起六个进程：
//
//	joltgo.exe -type gate
//	joltgo.exe -type account
//	joltgo.exe -type logic
//	joltgo.exe -type match
//	joltgo.exe -type game
//	joltgo.exe -type gm -gmkey <secret>
//
// flag：-type / -redis / -gmaddr（gm 的 HTTP 端口，默认 :8082）/ -gmkey（GM 管理密钥，
// 也可用环境变量 GM_KEY；gm 没配密钥就拒绝启动）。gate 的 WS 端口 8080 仍写死在 run 里。
//
// 优雅退出由 pitaya 的 app.Start() 内部处理（SIGINT/SIGTERM → shutdownComponents）。

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"

	"github.com/redis/go-redis/v9"
	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/acceptor"
	"github.com/topfreegames/pitaya/v3/pkg/component"
	"github.com/topfreegames/pitaya/v3/pkg/config"
	"github.com/topfreegames/pitaya/v3/pkg/groups"
	"joltgo/account"
	"joltgo/game"
	"joltgo/gate"
	"joltgo/gm"
	"joltgo/kv"
	"joltgo/logic"
	"joltgo/match"
	"joltgo/online"
	"joltgo/persist"
)

func main() {
	svType := flag.String("type", "gate", "server type: gate | account | logic | match | game | gm")
	redisAddr := flag.String("redis", kv.DefaultAddr, "redis address (host:port)")
	gmAddr := flag.String("gmaddr", ":8082", "gm http listen address (type=gm)")
	gmUser := flag.String("gmuser", "admin", "gm console account (type=gm)")
	gmPass := flag.String("gmpass", "", "gm console password (type=gm); empty refuses to start")
	gmSecret := flag.String("gmsecret", "", "gm session cookie signing key (type=gm); falls back to -gmpass")
	flag.Parse()

	// GM 控制台的登录凭据。客户端可达性**不靠它** —— 那由 gate 的转发白名单保证
	// （见 gate/routes.go）：客户端根本发不到 match.addbots / logic.grantcoins。
	// 这里的账号密码只保护 GM 页面本身（谁能用这台控制台）。
	if *svType == "gm" && *gmPass == "" {
		// 默认拒绝服务比默认开放安全：忘记配置的后果是服务起不来，
		// 而不是「谁都能登控制台」。
		log.Fatalf("-type gm 需要 -gmpass（GM 控制台密码）")
	}

	cfg := config.NewDefaultPitayaConfig()
	// protobuf serializer（serializertype=2）；消息定义按归属分三份：
	// 客户端请求在 gate/protos/gate.proto，推送按发送方在 match/protos 与 game/protos。
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

	// app.Start() 收到 SIGINT/SIGTERM 时是**正常返回**的，run 也就返回 nil。
	// 无条件 log.Fatalf 会把每一次优雅退出都打成「启动失败: <nil>」并以 1 退出，
	// 部署脚本与冒烟测试会读到不存在的失败。
	if err := run(svType, builder, *redisAddr, *gmAddr, *gmUser, *gmPass, *gmSecret); err != nil {
		log.Fatalf("启动失败: %v", err)
	}
}

// run 组装并启动指定角色的服务。抽成函数是为了让 flag 解析与 defer 清理分离 ——
// main 里 log.Fatal 会跳过 defer，Redis 连接必须在这里关。
func run(svType *string, builder *pitaya.Builder, redisAddr, gmAddr, gmUser, gmPass, gmSecret string) error {
	// Redis 是 gate / account / logic / match 四个角色的共享依赖（gate 写会话归属、
	// account 存取账号与凭证、logic 存玩家档案、match 存排队队列）。game 不碰 Redis，就不给它开连接 ——
	// 否则 Redis 一挂，连纯计算的 game 节点都起不来。
	// 对需要它的四个角色统一「连不上就启动失败」——早失败比运行中途才暴露好排查
	// （gate 的归属登记虽是 best-effort、运行期写失败只降级，但地址配错仍应在启动时炸）。
	var rdb *redis.Client
	if *svType != "game" {
		var err error
		rdb, err = kv.Open(context.Background(), redisAddr)
		if err != nil {
			return err
		}
		defer rdb.Close()
	}

	app := builder.Build()

	switch *svType {
	case "gate":
		if err := gate.Configure(app); err != nil {
			return err
		}
		// 会话归属：绑定后写 online:{uid} → 本节点，断开时清除。
		gate.RegisterSessionHooks(builder.SessionPool, app.GetServerID(), online.NewStore(rdb))
		// 必须用 RegisterRemote，不能用 Register：match 是用 app.RPCTo 调过来的，
		// 而 RPCTo 走 RPCType_User → handleRPCUser → remotes 表，那张表**只由
		// RegisterRemote 填充**。注册成 handler 的话路由在 remotes 里找不到，
		// match 的 bindgame 会拿 ErrNotFoundCode（见 service/remote.go:246/254）。
		// 反过来这也正好关掉了攻击面：客户端发的 gate.gate.bindgame 会被路由到
		// handler 池、找不到而报错，压根到不了这里。
		app.RegisterRemote(gate.NewSessionComponent(app, builder.SessionPool),
			component.WithName("gate"),
			component.WithNameFunc(strings.ToLower),
		)

	case "account":
		// protoc-gen-redis 生成代码以 redigo.Conn 为接口；账号本体写 Hash，
		// 凭证仍由上面 rdb 上的 Lua 原子轮换管理。连接池与进程同生命周期。
		accountPool, err := kv.OpenRedigo(context.Background(), redisAddr)
		if err != nil {
			return err
		}
		defer accountPool.Close()
		app.Register(account.New(app,
			account.NewStore(rdb, persist.NewAccountStore(accountPool)),
			online.NewStore(rdb),
			account.NewLogicNotifier(app),
		), component.WithName("account"), component.WithNameFunc(strings.ToLower))

	case "logic":
		playerPool, err := kv.OpenRedigo(context.Background(), redisAddr)
		if err != nil {
			return err
		}
		defer playerPool.Close()
		service := logic.NewService(
			persist.NewPlayerStore(playerPool),
			logic.MustDefaultCatalog(),
			logic.NewRedisLockFactory(rdb),
		)
		// handler 与 remote 必须是**同一个组件实例**：GrantCoins（GM 发钱）要能被
		// RPCTo 调到，而状态只有一份。
		logicComp := logic.NewComponent(app, service)
		app.Register(logicComp,
			component.WithName("logic"),
			component.WithNameFunc(strings.ToLower),
		)
		app.RegisterRemote(logicComp,
			component.WithName("logic"),
			component.WithNameFunc(strings.ToLower),
		)

	case "match":
		// 队列在 Redis（多个 match 节点共享同一条队列），开局前用 online 登记
		// 定位每个玩家所属的 gate 并请它写会话数据（顺带探活）。
		// 同 logic：handler 与 remote 共用一个实例（AddBots 是 GM 用 RPCTo 调的）。
		matchComp := match.New(app, match.NewQueue(rdb), online.NewStore(rdb))
		app.Register(matchComp,
			component.WithName("match"),
			component.WithNameFunc(strings.ToLower),
		)
		app.RegisterRemote(matchComp,
			component.WithName("match"),
			component.WithNameFunc(strings.ToLower),
		)

	case "gm":
		// gm 是 backend：注册进 etcd 只为「主动发后端 RPC」，不注册任何 handler /
		// remote，也不加 acceptor —— 它没有任何 pitaya 监听端口，管理流量走 Gin。
		//
		// 它需要 Redis 是为了「用户名 → accountID」（只读账号 Hash，不写）。
		accountPool, err := kv.OpenRedigo(context.Background(), redisAddr)
		if err != nil {
			return err
		}
		defer accountPool.Close()

		rpc := gm.NewRPC(app)
		handler := gm.NewHandler(rpc, rpc,
			account.NewStore(rdb, persist.NewAccountStore(accountPool)), gmUser, gmPass, gmSecret)
		go func() {
			// Run 是阻塞的：放进 goroutine，让 app.Start() 继续处理信号与优雅退出。
			if err := handler.Router().Run(gmAddr); err != nil {
				log.Fatalf("gm http 启动失败: %v", err)
			}
		}()
		log.Printf("gm: http 监听 %s", gmAddr)

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
		return fmt.Errorf("unknown server type %q (want gate|account|logic|match|game|gm)", *svType)
	}

	app.Start()
	return nil
}

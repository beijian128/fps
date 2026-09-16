# deploy —— 本地集群基础设施

分布式服务端（gate / account / logic / match / game）需要三个外部中间件：

- **etcd**：服务发现（各服务注册自己，互相发现）—— 监听 `localhost:2379`
- **NATS**：服务间 RPC 总线 —— 监听 `localhost:4222`
- **Redis**：共享状态（账号与凭证、会话归属、匹配队列、钱包与背包）—— 监听 `localhost:6379`

前两个是 pitaya Cluster 模式的硬依赖（etcd 做服务发现，NATS 做 RPC），Redis 是本项目自己的
共享存储。生产环境用容器/K8s 部署；本地调试可直接用本目录下编译好的 `etcd.exe` /
`nats-server.exe` / `redis-server.exe`。

> ⚠️ **etcd-data 每次清空，redis-data 永不清空。** 两者的对待方式**相反**，把 etcd 的习惯
> 套到 Redis 上会把所有账号与局外进度删光。理由见下方「etcd-data 清、redis-data 不清」。

## 一键启动（PowerShell）

```powershell
.\start-infra.ps1          # 起 etcd + nats + redis（先停旧 etcd 并清空 etcd-data；nats/redis 已在跑则沿用）
.\start-all.ps1            # start-infra + gate / account / logic / match / game / gm 六进程
.\stop-infra.ps1           # 全部停掉（等到端口真正释放再返回；不删任何数据目录）
```

或手动（同样要先清空 etcd 数据目录，原因见下方「etcd-data 清、redis-data 不清」）：

```powershell
if (Test-Path .\etcd-data) { Remove-Item -Recurse -Force .\etcd-data }   # 需先确认 etcd 没在跑

.\etcd.exe --data-dir .\etcd-data `
  --listen-client-urls http://localhost:2379 `
  --advertise-client-urls http://localhost:2379

.\nats-server.exe -p 4222

# redis-data 不要删。--appendonly yes 让账号在 Redis 重启后仍然存在。
.\redis-server.exe --port 6379 --dir .\redis-data --appendonly yes
```

## 获取二进制（如需重新下载）

- **nats-server**（v2.x）：
  ```bash
  go install github.com/nats-io/nats-server/v2@v2.12.2
  ```
  产物在 `$GOPATH/bin/nats-server.exe`。

- **etcd**（v3.5.x，Windows）：从 GitHub releases 下载
  `etcd-v3.5.14-windows-amd64.zip`（github.com 直连需走代理，见仓库 github-ops 说明），
  解压取 `etcd.exe`。

- **Redis**（8.x，Windows / msys2 构建）：本目录里的 `redis-server.exe` 取自
  `Redis-8.10.1-Windows-x64-msys2-with-Service`。它是 msys2 编译的，**依赖同目录的
  运行库**，缺一个就报「找不到 XXX.dll」而静默退不起来，所以一并放在这里：
  `msys-2.0.dll` / `msys-crypto-3.dll` / `msys-ssl-3.dll` / `msys-gcc_s-seh-1.dll` /
  `msys-stdc++-6.dll`。另外附带 `redis-cli.exe`，排查账号数据时用得上（例如
  `.\redis-cli.exe keys 'acct:*'`）。

## docker-compose（可选）

本机无 docker 时用上面的二进制方式。有 docker 时可用：

```yaml
# docker-compose.yml
services:
  etcd:
    image: quay.io/coreos/etcd:v3.5.14
    command: etcd --listen-client-urls http://0.0.0.0:2379 --advertise-client-urls http://etcd:2379
    ports: ["2379:2379"]
  nats:
    image: nats:2.12
    ports: ["4222:4222"]
  redis:
    image: redis:8
    command: redis-server --appendonly yes
    ports: ["6379:6379"]
    volumes: ["redis-data:/data"]
volumes:
  redis-data:
```

## 启动服务端（六个进程）

先起基础设施，再起六个服务（单二进制按 `-type` 区分角色）：

```powershell
cd joltgo
.\joltgo.exe -type gate      # frontend，监听 ws://localhost:8080
.\joltgo.exe -type account   # 账号注册/登录/凭证恢复
.\joltgo.exe -type logic     # 玩家档案 / 钱包 / 背包 / 商城 / 装备
.\joltgo.exe -type match     # 匹配服务
.\joltgo.exe -type game      # 游戏逻辑（对局实例）
.\joltgo.exe -type gm -gmpass <pass>            # GM：http://localhost:8082 + Web 操作页（账号 admin）
```

GM 控制台用**账号密码**登录：账号默认 `admin`（`-gmuser`），密码来自 `-gmpass`，
**没配密码 `gm` 就拒绝启动**；`-gmsecret` 是会话 cookie 的签名密钥（留空回落到密码）。
`start-all.ps1` 默认用 `local-dev-pass`（可用 `-gmPass <pass>` 或环境变量 `GM_PASS` 覆盖）。

**别的角色不需要任何 GM 相关配置**：客户端能不能打到管理指令，由 gate 的转发白名单保证
（`joltgo/gate/routes.go` 的 `allowedRoutes`，只放行 `gate/protos/gate.proto` 里定义过的 route）。
所以想关掉 GM 入口，只要不起 `gm` 进程即可。

Redis 地址由 `-redis`（默认 `localhost:6379`）指定。`gate` / `account` / `logic` / `match` / `gm` 启动时会
Ping 一次 Redis，**连不上就直接退出**（早失败比运行中途才暴露好排查）；`game` 是纯计算节点，
不碰 Redis，也就不会被 Redis 故障拖着起不来。因此 `start-infra.ps1` 起完 redis 会等端口真的
在监听再返回 —— 不等的话后面四个进程会稳定地「起来一下就没了」。

每个进程各自注册到 etcd，通过 NATS 互相 RPC。`start-all.ps1` 把六个角色的日志分别写到
`gate.log` / `account.log` / `logic.log` / `match.log` / `game.log` / `gm.log` —— **pitaya 的日志走 stderr 而不是
stdout**，所以这六个对应的是 `-RedirectStandardError`（stdout 另存为同名的 `*.out.log`，基本
为空；两个流不能指向同一个文件，各自从头写会互相覆盖）。排查问题时 `tail -f deploy/game.log`
即可；日志是 debug 级别、涨得很快（几分钟就 ~10 MB）且不轮转，长时间跑记得清一下。

GM 页面在 <http://localhost:8082/>：用 `admin` + `-gmpass` 登录后，就能给账号发钱（用户名或
accountID 都行）、往匹配队列里塞机器人。会话是 HMAC 签名的无状态 cookie（HttpOnly，8 小时），
登录失败按 IP 限速。GM 操作**不审计**，只写 `gm.log`。

## etcd-data 清、redis-data 不清

这两个目录的处理方式相反，别把其中一边的习惯套到另一边：

| 目录 | 内容 | 启动时 | 清掉的后果 |
| --- | --- | --- | --- |
| `etcd-data` | 服务注册（瞬时状态） | **每次清空** | 无害，各进程会重新注册 |
| `redis-data` | 账号、密码哈希、凭证、会话归属、玩家钱包与背包 | **永不清空** | 所有账号与局外进度消失，谁都登不进来 |

`redis-server` 用 `--appendonly yes` 启动（AOF）。不开的话默认只有 RDB 定时快照，
`stop-infra.ps1` 那种强杀会丢掉最近几分钟的注册 —— 表现为「刚注册的账号重启后登不上，
报 bad_credentials」。`stop-infra.ps1` 也不删任何数据目录。

### 本机已装 Redis 服务时

有些开发机上装了 Windows 服务版 Redis（服务名 `Redis`，由 `RedisService.exe` 看着
`redis-server`），它开机就占着 6379。这种情况下：

- `start-infra.ps1` 走「已在监听就沿用」分支，**不会**再起 `deploy/redis-server.exe`，
  账号数据落在那个服务自己的数据目录里而不是 `deploy/redis-data`；
- `stop-infra.ps1` 杀掉 `redis-server` 之后服务会立刻把它拉起来（非管理员连
  `Stop-Service Redis` 都做不到），所以脚本对 redis 只做 best-effort，**不**因为它还活着
  而报错 —— 只有 etcd / nats / joltgo 才是硬性要求退出的（etcd 尤其，它的数据目录要被清空）。

正确性上没有区别（Redis 数据目录我们本来就从不清空），但排查「账号去哪了」的时候要知道
数据可能不在 `deploy/redis-data`。想强制用本目录这份，先 `Stop-Service Redis`（需管理员）
再跑 `start-infra.ps1`。

### 为什么每次启动都清空 etcd-data

etcd 的租约倒计时**只在 etcd 进程运行期间走**：重启时它把租约按 checkpoint 恢复到后端里存的
剩余 TTL，并从「恢复那一刻」重新倒计时，停机时间不计入。隔离实验（deploy 里的 etcd v3.5.14，
独立端口 + 临时数据目录）实测：

| 时刻 | 事件 | 结果 |
| --- | --- | --- |
| 21:46:18 | 写入者进程退出（模拟强杀），租约还剩 18s | 键在，ttl 18s |
| 21:46:20 | 杀掉 etcd，关闭 66s（远超 18s） | — |
| 21:47:26 | 重启 etcd | 键**还在**，ttl 被重置为 19s |
| 21:47:36 | 再杀、关 12s、再启 | 键**还在**，ttl 18s（此时主人已死 78s） |
| 21:48:17 | 连续运行满一个 TTL | 才真正消失 |

所以「进程被强杀后 ~60s 才过期」只在 **etcd 不重启** 时成立。而 `stop-infra.ps1` →
`start-all.ps1` 这种整套重启，残留会一直活到 etcd 回来之后再满一个 TTL；在 TTL 内反复重启
还会反复续命。这是有实际危害的：`match.startMatch` 从 `GetServersByType("game")` 取 map 的
第一个元素且不校验可达性，选中残留节点时 RPC 失败，而玩家此刻**已经被移出队列**（配对
是先出队再开局），于是既不在队列里也收不到 `onMatched`，客户端永久卡在匹配等待
（单人兜底已删除，不会再有第二条兜底路径；玩家可以自己取消后重新排队）。

etcd 里只有服务注册这类临时数据，所以本地每次启动直接推倒重来，`start-infra.ps1` 会先停掉
旧 etcd（不停掉的话新 etcd 会因端口被占而静默退出，清空就白做了）再清目录。生产环境请改用
另外两条：把心跳 TTL 从默认 60s 调小，并给选节点逻辑加重试与回退（见 `match/match.go`）。

## Redis 中的玩家局外数据

除账号与会话数据外，logic 使用 `persist/protos/player/` 生成的独立 Redis Hash：`REDB#1:<accountID>:0` 保存背包与装备，`REDB#2:<accountID>:0` 保存钱包金币，`REDB#3:<accountID>:0` 保存玩家档案（xp / 击杀 / 死亡 / 场次 / 胜负）。最近 20 场对局历史是 Redis List `playerhist:<accountID>`（新的在前，`LPUSH` + `LTRIM 0 19`）—— 它是 List 不是 Hash 行，所以键名不套 `REDB#` 前缀。`redis-data` **不要清空**；删除它不仅会删除账号，也会删除玩家的金币、物品、装备进度与战绩历史。

`logic` 是无状态节点。客户端经 gate 随机访问 logic；account 在登录/注册/resume 的成功路径中先通过 `logic.logic.online` 确保新账号拥有初始 1000 金币的钱包和空背包，RPC 失败则中止登录且不轮换 token、不绑定会话。购买不是请求幂等操作，客户端不得自动重试；具体错误码和并发/补偿边界见 `../../docs/API.md` 与 `../../docs/ARCHITECTURE.md`。

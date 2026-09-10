# deploy —— 本地集群基础设施

分布式服务端（gate / match / game）需要两个外部中间件：

- **etcd**：服务发现（各服务注册自己，互相发现）—— 监听 `localhost:2379`
- **NATS**：服务间 RPC 总线 —— 监听 `localhost:4222`

pitaya Cluster 模式的硬依赖（etcd 做服务发现，NATS 做 RPC）。生产环境用容器/K8s 部署；
本地调试可直接用本目录下编译好的 `etcd.exe` 与 `nats-server.exe`。

## 一键启动（PowerShell）

```powershell
.\start-infra.ps1          # 起 etcd + nats（先停旧 etcd 并清空 etcd-data；nats 已在跑则沿用）
.\stop-infra.ps1           # 全部停掉（等到端口真正释放再返回）
```

或手动（同样要先清空数据目录，原因见下方「为什么每次启动都清空 etcd-data」）：

```powershell
if (Test-Path .\etcd-data) { Remove-Item -Recurse -Force .\etcd-data }   # 需先确认 etcd 没在跑

.\etcd.exe --data-dir .\etcd-data `
  --listen-client-urls http://localhost:2379 `
  --advertise-client-urls http://localhost:2379

.\nats-server.exe -p 4222
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
```

## 启动服务端（三个进程）

先起基础设施，再起三个服务（单二进制按 `-type` 区分角色）：

```powershell
cd joltgo
.\joltgo.exe -type gate    # frontend，监听 ws://localhost:8080
.\joltgo.exe -type match   # 匹配服务
.\joltgo.exe -type game    # 游戏逻辑（对局实例）
```

每个进程各自注册到 etcd，通过 NATS 互相 RPC。`start-all.ps1` 把三个角色的日志分别写到
`gate.log` / `match.log` / `game.log` —— **pitaya 的日志走 stderr 而不是 stdout**，所以这三个
对应的是 `-RedirectStandardError`（stdout 另存为同名的 `*.out.log`，基本为空；两个流不能指向
同一个文件，各自从头写会互相覆盖）。排查问题时 `tail -f deploy/game.log` 即可；日志是 debug
级别、涨得很快（几分钟就 ~10 MB）且不轮转，长时间跑记得清一下。

## 为什么每次启动都清空 etcd-data

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
第一个元素且不校验可达性，选中残留节点时 RPC 失败，而玩家此刻**已经被移出队列**（配对与
10s 兜底两条路径都是先出队），于是既不在队列里也收不到 `onMatched`，客户端永久卡在匹配等待。

etcd 里只有服务注册这类临时数据，所以本地每次启动直接推倒重来，`start-infra.ps1` 会先停掉
旧 etcd（不停掉的话新 etcd 会因端口被占而静默退出，清空就白做了）再清目录。生产环境请改用
另外两条：把心跳 TTL 从默认 60s 调小，并给选节点逻辑加重试与回退（见 `match/match.go`）。

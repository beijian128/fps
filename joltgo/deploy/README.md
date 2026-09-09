# deploy —— 本地集群基础设施

分布式服务端（gate / match / game）需要两个外部中间件：

- **etcd**：服务发现（各服务注册自己，互相发现）—— 监听 `localhost:2379`
- **NATS**：服务间 RPC 总线 —— 监听 `localhost:4222`

pitaya Cluster 模式的硬依赖（etcd 做服务发现，NATS 做 RPC）。生产环境用容器/K8s 部署；
本地调试可直接用本目录下编译好的 `etcd.exe` 与 `nats-server.exe`。

## 一键启动（PowerShell）

```powershell
.\start-infra.ps1          # 后台起 etcd + nats
.\stop-infra.ps1           # 全部停掉
```

或手动：

```powershell
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

每个进程各自注册到 etcd，通过 NATS 互相 RPC。服务发现采用 60s 租约：进程被强杀后
旧租约要等约 60s 才过期，期间会短暂出现在服务列表里（这是 etcd 语义，不是 bug）。

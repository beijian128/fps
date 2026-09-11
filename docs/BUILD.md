# 构建与环境

## 环境要求

| 组件 | 版本 | 说明 |
|---|---|---|
| Windows | 10/11 x64 | 当前构建脚本仅支持 Windows |
| MSYS2 | 最新 | 需要 UCRT64 环境 |
| GCC/G++ | 16.x（UCRT64） | `mingw-w64-ucrt-x86_64-gcc` |
| CMake | 4.x | `mingw-w64-ucrt-x86_64-cmake` |
| Ninja | 1.x | `mingw-w64-ucrt-x86_64-ninja` |
| GNU Make | 4.x | `mingw-w64-ucrt-x86_64-make` |
| Go | 1.26+ | cgo 需要 GCC |
| protoc | 3.5+ | 仅在改动 `.proto` 后重新生成 Go 码时需要 |
| protoc-gen-go | 与 `google.golang.org/protobuf` 匹配 | `go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.34.2` |
| etcd | 3.5.x | 服务发现；本机二进制由 `joltgo/deploy/` 提供 |
| nats-server | 2.x | 服务间 RPC 总线；本机二进制由 `joltgo/deploy/` 提供 |
| Redis | 8.x | 共享状态（账号/凭证/会话归属/匹配队列）；本机二进制由 `joltgo/deploy/` 提供 |

> 后三项只在**运行**时需要，构建不依赖它们。三个二进制（以及 Redis 的 msys2 运行库
> DLL）都已放在 `joltgo/deploy/`，`start-infra.ps1` 一把起齐，详见
> [deploy/README.md](../joltgo/deploy/README.md)。

MSYS2 假定安装在 `C:\msys64`（`build.ps1` 中有硬编码，换路径需同步修改）。

安装 UCRT64 工具链：

```bash
pacman -S --needed \
  mingw-w64-ucrt-x86_64-gcc \
  mingw-w64-ucrt-x86_64-cmake \
  mingw-w64-ucrt-x86_64-ninja \
  mingw-w64-ucrt-x86_64-make
```

## Jolt Physics 源码

本仓库不包含 Jolt Physics，需在仓库根目录（`fps/`）单独克隆：

```bash
git clone https://github.com/jrouwe/JoltPhysics.git
```

克隆后的目录关系必须满足 `joltgo/../JoltPhysics` 即 `fps/JoltPhysics`，
因为 `joltgo/CMakeLists.txt` 通过相对路径引用它。

## pitaya 框架（已内置）

服务端使用 [pitaya](https://github.com/topfreegames/pitaya) v3 作为框架基建层，
源码已内置在 `joltgo/third_party/pitaya/`（`pkg/` + `go.mod` + `go.sum` + `LICENSE`），
通过 `joltgo/go.mod` 的 `replace` 指令指向本地目录：

```
replace github.com/topfreegames/pitaya/v3 => ./third_party/pitaya
```

- **无需单独克隆** pitaya；其传递依赖（etcd/grpc/nats/otel 等）由 Go 模块代理拉取，
  需网络可达（本项目用 `GOPROXY=goproxy.cn,direct`）
- 若改动/升级内置的 pitaya，重跑 `go mod tidy` 同步 `go.sum`；二进制体积较大是
  pitaya 的固有代价
- **Cluster 模式**：gate/account/match/game 四服务经 etcd（服务发现）+ nats-server（RPC）
  通信，账号/凭证/会话归属/匹配队列这些共享状态放 Redis，本地运行见
  `joltgo/deploy/README.md`

## protobuf 消息（game/protos/）

服务端与客户端之间的业务消息用 protobuf，schema 在 `joltgo/game/protos/game.proto`，
生成码 `game/protos/game.pb.go` 已提交进仓库。**只有在改动 `.proto` 时才需要重新生成**：

```bash
cd joltgo
protoc --go_out=. --go_opt=paths=source_relative -I . game/protos/game.proto
```

需要 `protoc` 与 `protoc-gen-go`（见上方环境要求表）。生成后客户端
`godot_client/scripts/fps_client.gd` 里的手写 wire 编解码也要同步改。

## 一键构建

```powershell
cd joltgo
.\build.ps1
```

`build.ps1` 依次执行：

1. **编译包装层 DLL**：调用 MSYS2 bash，把 PATH 切到 `/ucrt64/bin`，运行
   `cmake -S . -B build -G Ninja -DCMAKE_BUILD_TYPE=Release`，然后
   `cmake --build build --target jolt_c`。Jolt 在此作为子项目被编译为静态库
   `libJolt.a`，再与 `wrapper/jolt_c.cpp` 链接成 `libjolt_c.dll`。
2. **编译 Go 程序**：设置 `CC`/`CXX` 为 UCRT64 的 gcc/g++，`go build -o joltgo.exe .`。
   首次构建会先拉取 pitaya 依赖树（较慢）。
3. **拷贝 DLL**：把 `build/libjolt_c.dll` 复制到 exe 旁。

## 构建产物

```text
joltgo/
├── joltgo.exe          # Go 可执行文件
├── libjolt_c.dll       # C 包装层动态库（运行时需要，放在 exe 旁）
└── build/              # CMake 生成目录（gitignored）
    ├── libjolt_c.dll
    ├── libjolt_c.dll.a # 导入库
    └── Jolt/libJolt.a  # Jolt 静态库
```

`libjolt_c.dll` 已静态链接 C++ 运行库，运行只需 `joltgo.exe` + `libjolt_c.dll` 两个文件。

## 运行

服务端是分布式四服务，先起基础设施再起四个角色：

```powershell
# 1. 起 etcd + nats-server + redis-server（服务发现 + RPC 总线 + 共享状态）
cd joltgo\deploy
.\start-all.ps1          # 内含 start-infra + 四进程；或手动起

# 手动起四个服务（同一二进制按 -type 区分）：
cd joltgo
.\joltgo.exe -type gate      # frontend，监听 ws://localhost:8080
.\joltgo.exe -type account   # 账号注册/登录/凭证恢复
.\joltgo.exe -type match     # 匹配服务
.\joltgo.exe -type game      # 游戏逻辑（对局实例）
```

**只有两个 flag**（gate 的 WS 端口 8080 目前写死在 `main.go` 里，没有 `-port`）：

| flag | 默认值 | 说明 |
|---|---|---|
| `-type` | `gate` | 角色：`gate` / `account` / `match` / `game`，其它值直接报错退出 |
| `-redis` | `localhost:6379` | Redis 地址。`gate` / `account` / `match` 启动时 Ping 一次，**连不上就退出**；`game` 不碰 Redis，这个 flag 对它无效 |

gate 监听 <ws://localhost:8080/>，用 Godot 客户端连接游玩（见根目录 README）。
停服务用 `deploy\stop-infra.ps1`（它不删任何数据目录）。

## 常见问题

| 现象 | 原因与处理 |
|---|---|
| `JPH::AssertFailed` 未定义引用 | 包装层编译时缺 `NDEBUG`，误开了 asserts。保持用 CMake 的 `add_subdirectory`，不要手写编译命令 |
| `libJolt.a: file format not recognized` | Jolt 开了 LTO 而你的目标没开。用 CMake 继承同一套 IPO 设置，或统一关闭 `INTERPROCEDURAL_OPTIMIZATION` |
| 重建时 `Copy-Item` 报「文件被另一进程占用」 | `joltgo.exe` 还在运行、锁住了 DLL。先结束该进程再构建 |
| `gcc/g++` 找不到 Jolt 头文件 | 包装层只应通过 CMake 编译；`go build` 时 cgo 只需 `-I wrapper`，不要手动引 Jolt 头 |
| 运行时缺 `libstdc++-6.dll` 等 | 说明 DLL 没静态链接运行库，检查 CMake 里 MINGW 分支的 `-static` 是否被改动 |
| `no server type chosen for sending RPC` | RPC route 不是三段式（应为 `server.service.method`，如 `game.game.create`） |
| `nats: no responders available for request` | 目标服务未注册/已死；或 etcd 里残留了强杀进程的旧租约（60s 过期），`GetServersByType` 挑到了死节点 |
| gate/account/match 起来一下就退出 | 连不上 Redis（三者启动时都会 Ping）。确认 `redis-server` 在 6379 上真的在监听，或用 `-redis` 指对地址。`game` 不碰 Redis，所以只有那三个会挂 |
| 登录报 `bad_credentials`，但账号刚注册过 | Redis 数据没了。`redis-data` **永不清空**（这一点和每次都清空的 `etcd-data` 相反），并且必须开 `--appendonly yes`，否则强杀会丢掉最近几分钟的写入 |
| 客户端一直停在「正在匹配…」 | 没登录就发了 `match.join`：会话没有 uid，服务端静默忽略（只在 match 日志里留一行）。也可能是 etcd 残留旧 game 节点，见上一条 |
| 启动后服务端世界在动（步数增长） | 正常：实例创建后 20 Hz 模拟 tick 无条件运行，无论客户端是否在线 |

## 手动构建（不使用 build.ps1）

```bash
# 在 MSYS2 UCRT64 shell 中
cd /path/to/joltgo
cmake -S . -B build -G Ninja -DCMAKE_BUILD_TYPE=Release
cmake --build build --target jolt_c --parallel
```

```powershell
# PowerShell
$env:CC='C:\msys64\ucrt64\bin\gcc.exe'
$env:CXX='C:\msys64\ucrt64\bin\g++.exe'
go build -o joltgo.exe .
Copy-Item build\libjolt_c.dll -Destination . -Force
```

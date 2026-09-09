# joltgo

本目录是服务端：Go 服务（pitaya 框架 + ECS 模拟 + Jolt C 包装层），分布式三服务
gate / match / game（单二进制按 `-type` 区分）。客户端在仓库根目录的 `godot_client/`。

项目概述、快速开始、架构、构建、API 与开发维护，请见仓库根目录文档：

- [README.md](../README.md)
- [docs/ARCHITECTURE.md](../docs/ARCHITECTURE.md)
- [docs/BUILD.md](../docs/BUILD.md)
- [docs/API.md](../docs/API.md)
- [docs/DEVELOPMENT.md](../docs/DEVELOPMENT.md)

快速运行：

```powershell
cd joltgo
.\build.ps1
cd deploy
.\start-all.ps1   # 起 etcd + nats + gate/match/game 三进程，然后用 Godot 打开 ../godot_client
```

# joltgo

本目录是服务端：Go 服务（ECS 模拟 + WebSocket）+ Jolt C 包装层。
客户端在仓库根目录的 `godot_client/`（Godot 4）。

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
.\joltgo.exe   # 监听 ws://localhost:8080，然后用 Godot 打开 ../godot_client
```

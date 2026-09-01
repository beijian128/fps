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

```powershell
cd joltgo
.\joltgo.exe
```

服务监听 <http://localhost:8080>（端口在 `main.go` 中硬编码为 `:8080`）。

## 常见问题

| 现象 | 原因与处理 |
|---|---|
| `JPH::AssertFailed` 未定义引用 | 包装层编译时缺 `NDEBUG`，误开了 asserts。保持用 CMake 的 `add_subdirectory`，不要手写编译命令 |
| `libJolt.a: file format not recognized` | Jolt 开了 LTO 而你的目标没开。用 CMake 继承同一套 IPO 设置，或统一关闭 `INTERPROCEDURAL_OPTIMIZATION` |
| 重建时 `Copy-Item` 报「文件被另一进程占用」 | `joltgo.exe` 还在运行、锁住了 DLL。先结束该进程再构建 |
| `gcc/g++` 找不到 Jolt 头文件 | 包装层只应通过 CMake 编译；`go build` 时 cgo 只需 `-I wrapper`，不要手动引 Jolt 头 |
| 运行时缺 `libstdc++-6.dll` 等 | 说明 DLL 没静态链接运行库，检查 CMake 里 MINGW 分支的 `-static` 是否被改动 |
| 启动后游戏不动、开始界面敌人不追 | 正常：物理只在鼠标锁定后才推进，防止开始前被打 |

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

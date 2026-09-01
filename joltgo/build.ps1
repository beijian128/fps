# Build the Jolt C wrapper DLL and the Go program, then place the DLL beside the executable.

$ErrorActionPreference = 'Stop'

$root = $PSScriptRoot
$bash = 'C:\msys64\usr\bin\bash.exe'
$rootUnix = $root -replace '\\', '/'

# 1. Build the C wrapper DLL. Jolt is pulled in as a CMake subproject, so the
#    instruction-set flags and compile definitions always match the static library.
$bashCmd = "export PATH=/ucrt64/bin:/usr/bin:`$PATH; cd '$rootUnix'; cmake -S . -B build -G Ninja -DCMAKE_BUILD_TYPE=Release && cmake --build build --target jolt_c --parallel"
& $bash -lc $bashCmd
if ($LASTEXITCODE -ne 0) { throw 'wrapper DLL build failed' }

# 2. Build the Go program using the matching UCRT64 GCC toolchain.
$env:CC = 'C:\msys64\ucrt64\bin\gcc.exe'
$env:CXX = 'C:\msys64\ucrt64\bin\g++.exe'
go build -o joltgo.exe .
if ($LASTEXITCODE -ne 0) { throw 'go build failed' }

# 3. Put the DLL beside the executable so it can be found at run time.
Copy-Item -LiteralPath (Join-Path $root 'build\libjolt_c.dll') -Destination (Join-Path $root 'libjolt_c.dll') -Force

Write-Host 'Built joltgo.exe'

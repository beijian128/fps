# 一键启动整个分布式服务端：先起 etcd + nats，再起 gate / match / game 三进程。
# 每个进程把日志写到 deploy/ 下的 log 文件。
$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot

& (Join-Path $root 'start-infra.ps1')

$exe = Join-Path $root '..\joltgo.exe'

# pitaya 的日志走 stderr 而不是 stdout，所以真正有内容的是 <role>.log；stdout 另存为.
# <role>.out.log —— 两者不能指向同一个文件，Start-Process 会各自从头写而互相覆盖.
Start-Process -FilePath $exe -ArgumentList @('-type', 'gate')  -WorkingDirectory (Split-Path $exe) -RedirectStandardOutput (Join-Path $root 'gate.out.log')  -RedirectStandardError (Join-Path $root 'gate.log')  -WindowStyle Hidden
Start-Process -FilePath $exe -ArgumentList @('-type', 'match') -WorkingDirectory (Split-Path $exe) -RedirectStandardOutput (Join-Path $root 'match.out.log') -RedirectStandardError (Join-Path $root 'match.log') -WindowStyle Hidden
Start-Process -FilePath $exe -ArgumentList @('-type', 'game')  -WorkingDirectory (Split-Path $exe) -RedirectStandardOutput (Join-Path $root 'game.out.log')  -RedirectStandardError (Join-Path $root 'game.log')  -WindowStyle Hidden

Write-Host 'started gate (ws://localhost:8080) + match + game'

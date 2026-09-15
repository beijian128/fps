# 一键启动整个分布式服务端：先起 etcd + nats + redis，再起 gate / account / logic / match / game / gm 六进程。
# 每个进程把日志写到 deploy/ 下的 log 文件。
#
# GM 管理密钥（-gmkey）**四个进程都要传**：gm 用它鉴权页面请求，
# account / logic / match 用它校验 gm 发来的后端 RPC。
#   - 只给 gm 传、忘了给 logic / match 传：后端拿到空密钥，按「空密钥一律拒绝」
#     把每一条 GM 指令都挡掉（页面只会看到 503）。
#   - 只给 logic / match 传、忘了给 gm 传：gm 直接拒绝启动。
# 所以这里统一用同一个 $gmKey 起它们。换密钥用 -gmKey <secret>，或先设环境变量 GM_KEY。
param(
    [string]$gmKey = $env:GM_KEY
)
if (-not $gmKey) { $gmKey = 'local-dev-key' }

$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot

& (Join-Path $root 'start-infra.ps1')

$exe = Join-Path $root '..\joltgo.exe'

# pitaya 的日志走 stderr 而不是 stdout，所以真正有内容的是 <role>.log；stdout 另存为.
# <role>.out.log —— 两者不能指向同一个文件，Start-Process 会各自从头写而互相覆盖.
Start-Process -FilePath $exe -ArgumentList @('-type', 'gate')  -WorkingDirectory (Split-Path $exe) -RedirectStandardOutput (Join-Path $root 'gate.out.log')  -RedirectStandardError (Join-Path $root 'gate.log')  -WindowStyle Hidden
Start-Process -FilePath $exe -ArgumentList @('-type', 'account') -WorkingDirectory (Split-Path $exe) -RedirectStandardOutput (Join-Path $root 'account.out.log') -RedirectStandardError (Join-Path $root 'account.log') -WindowStyle Hidden
Start-Process -FilePath $exe -ArgumentList @('-type', 'logic', '-gmkey', $gmKey) -WorkingDirectory (Split-Path $exe) -RedirectStandardOutput (Join-Path $root 'logic.out.log') -RedirectStandardError (Join-Path $root 'logic.log') -WindowStyle Hidden
Start-Process -FilePath $exe -ArgumentList @('-type', 'match', '-gmkey', $gmKey) -WorkingDirectory (Split-Path $exe) -RedirectStandardOutput (Join-Path $root 'match.out.log') -RedirectStandardError (Join-Path $root 'match.log') -WindowStyle Hidden
Start-Process -FilePath $exe -ArgumentList @('-type', 'game')  -WorkingDirectory (Split-Path $exe) -RedirectStandardOutput (Join-Path $root 'game.out.log')  -RedirectStandardError (Join-Path $root 'game.log')  -WindowStyle Hidden
Start-Process -FilePath $exe -ArgumentList @('-type', 'gm', '-gmkey', $gmKey) -WorkingDirectory (Split-Path $exe) -RedirectStandardOutput (Join-Path $root 'gm.out.log') -RedirectStandardError (Join-Path $root 'gm.log') -WindowStyle Hidden

Write-Host 'started gate (ws://localhost:8080) + account + logic + match + game + gm (http://localhost:8082)'

# 一键启动整个分布式服务端：先起 etcd + nats + redis，再起 gate / account / logic / match / game / gm 六进程。
# 每个进程把日志写到 deploy/ 下的 log 文件。
#
# GM 控制台的登录凭据：账号默认 admin，密码由 -gmpass 给（没给就不启动 gm）。
#
# 别的角色**不需要任何 GM 相关配置**：客户端能不能打到管理指令由 gate 的转发白名单
# 保证（见 joltgo/gate/routes.go），而不是靠共享密钥。
# 换密码用 -gmPass <pass>，或先设环境变量 GM_PASS。
param(
    [string]$gmPass = $env:GM_PASS
)
if (-not $gmPass) { $gmPass = 'local-dev-pass' }

$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot

& (Join-Path $root 'start-infra.ps1')

$exe = Join-Path $root '..\joltgo.exe'

# pitaya 的日志走 stderr 而不是 stdout，所以真正有内容的是 <role>.log；stdout 另存为.
# <role>.out.log —— 两者不能指向同一个文件，Start-Process 会各自从头写而互相覆盖.
Start-Process -FilePath $exe -ArgumentList @('-type', 'gate')  -WorkingDirectory (Split-Path $exe) -RedirectStandardOutput (Join-Path $root 'gate.out.log')  -RedirectStandardError (Join-Path $root 'gate.log')  -WindowStyle Hidden
Start-Process -FilePath $exe -ArgumentList @('-type', 'account') -WorkingDirectory (Split-Path $exe) -RedirectStandardOutput (Join-Path $root 'account.out.log') -RedirectStandardError (Join-Path $root 'account.log') -WindowStyle Hidden
Start-Process -FilePath $exe -ArgumentList @('-type', 'logic') -WorkingDirectory (Split-Path $exe) -RedirectStandardOutput (Join-Path $root 'logic.out.log') -RedirectStandardError (Join-Path $root 'logic.log') -WindowStyle Hidden
Start-Process -FilePath $exe -ArgumentList @('-type', 'match') -WorkingDirectory (Split-Path $exe) -RedirectStandardOutput (Join-Path $root 'match.out.log') -RedirectStandardError (Join-Path $root 'match.log') -WindowStyle Hidden
Start-Process -FilePath $exe -ArgumentList @('-type', 'game')  -WorkingDirectory (Split-Path $exe) -RedirectStandardOutput (Join-Path $root 'game.out.log')  -RedirectStandardError (Join-Path $root 'game.log')  -WindowStyle Hidden
Start-Process -FilePath $exe -ArgumentList @('-type', 'gm', '-gmpass', $gmPass) -WorkingDirectory (Split-Path $exe) -RedirectStandardOutput (Join-Path $root 'gm.out.log') -RedirectStandardError (Join-Path $root 'gm.log') -WindowStyle Hidden

Write-Host 'started gate (ws://localhost:8080) + account + logic + match + game + gm (http://localhost:8082, admin/' -NoNewline
Write-Host "$gmPass)"

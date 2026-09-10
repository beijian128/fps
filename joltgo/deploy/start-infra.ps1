# 后台启动 etcd + nats-server（分布式服务端的本地基础设施）.
#
# etcd-data 每次启动前清空：etcd 的租约倒计时只在 etcd 运行期间走，重启时会把上一轮的.
# 孤儿租约按 checkpoint 恢复并重新计时，于是「整套重启」后服务发现里会出现残留节点.
# 实测数据与影响见 deploy/README.md 的「为什么每次启动都清空 etcd-data」.
$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
$dataDir = Join-Path $root 'etcd-data'
$natsPort = 4222

# 旧 etcd 不停掉，新 etcd 会因端口被占而退出，而 -WindowStyle Hidden 看不到报错.
# 结果是旧 etcd 继续带着残留键服务 —— 清空也就白做了，所以这里必须先把旧实例停干净.
Stop-Process -Name 'etcd' -ErrorAction SilentlyContinue
$deadline = (Get-Date).AddSeconds(5)
while ((Get-Process -Name 'etcd' -ErrorAction SilentlyContinue) -and (Get-Date) -lt $deadline) {
  Start-Sleep -Milliseconds 100
}
if (Get-Process -Name 'etcd' -ErrorAction SilentlyContinue) {
  throw 'etcd did not exit within 5s, cannot clear etcd-data -- run .\stop-infra.ps1 first'
}

if (Test-Path $dataDir) {
  Remove-Item -Path $dataDir -Recurse -Force
  Write-Host "cleared $dataDir"
}

Start-Process -FilePath (Join-Path $root 'etcd.exe') -ArgumentList @(
  '--data-dir', $dataDir,
  '--listen-client-urls', 'http://localhost:2379',
  '--advertise-client-urls', 'http://localhost:2379'
) -WindowStyle Hidden
Write-Host 'started etcd (localhost:2379, empty data dir)'

# nats 没有需要清空的状态，已在跑就直接沿用：再起一个只会因端口被占而退出，而.
# -WindowStyle Hidden 把这个报错藏起来，看起来就像「已经起来了」.
if (Get-NetTCPConnection -LocalPort $natsPort -State Listen -ErrorAction SilentlyContinue) {
  Write-Host "nats-server already listening on $natsPort, kept as is"
} else {
  Start-Process -FilePath (Join-Path $root 'nats-server.exe') -ArgumentList @('-p', "$natsPort") -WindowStyle Hidden
  Write-Host "started nats-server (localhost:$natsPort)"
}

# 停止本地集群基础设施与所有服务端进程。
#
# 必须等进程真正退出再返回：start-infra 靠 2379/4222 是否已被占用来决定起不起新进程，
# 上一轮还没释放端口就返回，新集群会「跳过」启动却落空 —— 表现为服务连不上 nats.
$ErrorActionPreference = 'Stop'
$names = @('joltgo', 'nats-server', 'etcd')

foreach ($n in $names) {
  Stop-Process -Name $n -ErrorAction SilentlyContinue
}

$deadline = (Get-Date).AddSeconds(10)
do {
  $alive = @($names | Where-Object { Get-Process -Name $_ -ErrorAction SilentlyContinue })
  if ($alive.Count -eq 0) { break }
  Start-Sleep -Milliseconds 100
} while ((Get-Date) -lt $deadline)

$alive = @($names | Where-Object { Get-Process -Name $_ -ErrorAction SilentlyContinue })
if ($alive.Count -gt 0) {
  $stuck = ($alive | ForEach-Object { $_.Name }) -join ', '
  throw "still running after 10s: $stuck -- kill them manually before starting again"
}

foreach ($port in @(2379, 4222)) {
  if (Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue) {
    Write-Warning "port $port is still listening after shutdown -- start-infra will reuse whatever holds it"
  }
}

Write-Host 'stopped joltgo / nats-server / etcd (ports released)'

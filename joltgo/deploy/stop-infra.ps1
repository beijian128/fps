# 停止本地集群基础设施与所有服务端进程。
#
# 必须等进程真正退出再返回：start-infra 靠 2379/4222/6379 是否已被占用来决定起不起新进程，
# 上一轮还没释放端口就返回，新集群会「跳过」启动却落空 —— 表现为服务连不上 nats.
#
# 停 redis 不会丢账号：开了 AOF，数据在 deploy/redis-data 里；本脚本也**不删**该目录.
$ErrorActionPreference = 'Stop'

# 必须真正退出的进程。etcd 尤其是：start-infra 会清空它的数据目录，旧实例不死就白清.
$hardNames = @('joltgo', 'nats-server', 'etcd')

# redis 只是「尽量停掉」。本机可能装了 Windows 服务版 Redis（服务名 Redis，由.
# RedisService.exe 看着 redis-server），杀掉它会被服务立刻拉起来，非管理员还停不了.
# 这不影响正确性：redis 的数据目录我们从不清空，start-infra 见到 6379 已被监听就沿用.
# 它 —— 与 nats 同样的处理。硬性要求它退出只会让本脚本在这类机器上永远失败.
$softNames = @('redis-server')

foreach ($n in ($hardNames + $softNames)) {
  Stop-Process -Name $n -ErrorAction SilentlyContinue
}

$deadline = (Get-Date).AddSeconds(10)
do {
  $alive = @($hardNames | Where-Object { Get-Process -Name $_ -ErrorAction SilentlyContinue })
  if ($alive.Count -eq 0) { break }
  Start-Sleep -Milliseconds 100
} while ((Get-Date) -lt $deadline)

# $alive 里是**进程名字符串**（Where-Object 过滤的是 $hardNames），不是进程对象 ——.
# 早先这里写的是 $_.Name，字符串没有该属性，报错信息里的名字列表恒为空.
$alive = @($hardNames | Where-Object { Get-Process -Name $_ -ErrorAction SilentlyContinue })
if ($alive.Count -gt 0) {
  $stuck = $alive -join ', '
  throw "still running after 10s: $stuck -- kill them manually before starting again"
}

if (Get-Process -Name 'redis-server' -ErrorAction SilentlyContinue) {
  Write-Warning 'redis-server is still running (a Windows service likely supervises it) -- start-infra will reuse it'
}

foreach ($port in @(2379, 4222, 6379)) {
  if (Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue) {
    Write-Warning "port $port is still listening after shutdown -- start-infra will reuse whatever holds it"
  }
}

Write-Host 'stopped joltgo / nats-server / etcd (redis best-effort; ports released)'

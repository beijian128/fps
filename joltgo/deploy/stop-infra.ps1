# 停止本地集群基础设施与所有服务端进程。
Stop-Process -Name 'joltgo' -ErrorAction SilentlyContinue
Stop-Process -Name 'nats-server' -ErrorAction SilentlyContinue
Stop-Process -Name 'etcd' -ErrorAction SilentlyContinue
Write-Host 'stopped joltgo / nats-server / etcd'

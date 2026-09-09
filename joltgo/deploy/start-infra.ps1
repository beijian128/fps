# 后台启动 etcd + nats-server（分布式服务端的本地基础设施）。
$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot

Start-Process -FilePath (Join-Path $root 'etcd.exe') -ArgumentList @(
  '--data-dir', (Join-Path $root 'etcd-data'),
  '--listen-client-urls', 'http://localhost:2379',
  '--advertise-client-urls', 'http://localhost:2379'
) -WindowStyle Hidden

Start-Process -FilePath (Join-Path $root 'nats-server.exe') -ArgumentList @('-p', '4222') -WindowStyle Hidden

Write-Host 'started etcd (localhost:2379) + nats-server (localhost:4222)'

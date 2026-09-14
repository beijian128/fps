# Regenerate the Redis Hash persistence code from persist/protos.
#
# The checked-in .redis.go is a generated artifact. Keep the plugin version
# pinned so a local regeneration is reproducible.

$ErrorActionPreference = 'Stop'

$root = $PSScriptRoot
$protoDir = Join-Path $root 'persist\protos'
$buildDir = Join-Path $root 'build'
$plugin = Join-Path $buildDir 'protoc-gen-redis.exe'
$pluginVersion = 'github.com/beijian128/protoc-gen-redis@v0.0.0-20260820071949-745ac5a22d3f'

New-Item -ItemType Directory -Force $buildDir | Out-Null
$env:GOBIN = $buildDir
go install $pluginVersion

& protoc `
    -I $protoDir `
    "--plugin=protoc-gen-redis=$plugin" `
    "--redis_out=$protoDir" `
    '--redis_opt=paths=source_relative,key_format=acct:%d:%d:%d' `
    'account.proto'
if ($LASTEXITCODE -ne 0) { throw 'protoc-gen-redis account failed' }

& protoc `
    -I $protoDir `
    "--plugin=protoc-gen-redis=$plugin" `
    "--redis_out=$protoDir" `
    '--redis_opt=paths=source_relative,key_format=REDB#%d:%d:%d' `
    'player/player.proto'
if ($LASTEXITCODE -ne 0) { throw 'protoc-gen-redis player failed' }

$generated = @(
    (Join-Path $protoDir 'account.redis.go'),
    (Join-Path $protoDir 'player\player.redis.go')
)
gofmt -w $generated
if ($LASTEXITCODE -ne 0) { throw 'gofmt failed' }

Write-Host "Generated $($generated -join ', ')"

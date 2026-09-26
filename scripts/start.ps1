# ChargePilot 一键启动 (Windows PowerShell)
# 用法: .\scripts\start.ps1

$ErrorActionPreference = "Stop"
Set-Location (Split-Path -Parent $PSScriptRoot)

if (-not (Test-Path ".env")) {
    Write-Error ".env not found. 请先 cp .env.example .env 并填写真实值"
    exit 1
}

Get-Content .env | ForEach-Object {
    if ($_ -match '^([^#][^=]+)=(.*)$') {
        Set-Item -Path "Env:$($Matches[1])" -Value $Matches[2]
    }
}

if (-not $Env:JWT_SECRET -or $Env:JWT_SECRET.Length -lt 32) {
    Write-Error "JWT_SECRET 太短或未设置"
    exit 1
}

Write-Host "[1/4] 校验 docker-compose.yml ..."
docker compose config --quiet

Write-Host "[2/4] 构建服务镜像 ..."
docker compose build --no-cache

Write-Host "[3/4] 启动 ..."
docker compose up -d

Write-Host "[4/4] 等待 mysql/redis 健康检查 ..."
$svcs = @("chargepilot-mysql", "chargepilot-redis-cache", "chargepilot-redis-stream")
foreach ($svc in $svcs) {
    Write-Host "  waiting for $svc ..."
    while ($true) {
        $status = docker compose ps $svc 2>$null | Select-String "healthy"
        if ($status) { break }
        Start-Sleep 2
    }
}

Write-Host ""
Write-Host "==================================================="
Write-Host " ChargePilot 启动成功"
Write-Host " PC 后台:  http://localhost/admin/"
Write-Host " 健康检查: http://localhost/api/v1/health"
Write-Host "==================================================="
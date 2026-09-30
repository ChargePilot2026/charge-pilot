param([switch]$ResetDevelopmentData)
$ErrorActionPreference = 'Stop'
if (-not $ResetDevelopmentData) { throw 'Pass -ResetDevelopmentData to back up and rebuild the five local development schemas.' }
$root = (Resolve-Path "$PSScriptRoot/..").Path
$container = 'chargepilot-dev-mysql-1'
$project = docker inspect $container --format '{{ index .Config.Labels "com.docker.compose.project" }}'
if ($LASTEXITCODE -ne 0 -or $project.Trim() -ne 'chargepilot-dev') { throw 'Expected local chargepilot-dev MySQL container.' }
$backupDir = Join-Path $root '.tmp'
New-Item -ItemType Directory -Path $backupDir -Force | Out-Null
$backup = Join-Path $backupDir ("dev-db-before-reset-" + (Get-Date -Format 'yyyyMMdd-HHmmss') + '.sql')
docker compose -f "$root/compose.dev.yaml" stop central gateway worker
if ($LASTEXITCODE -ne 0) { throw 'Unable to stop backend services.' }
docker exec $container sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysqldump -u root --single-transaction --routines --triggers --databases gateway_db user_db admin_db billing_db worker_db > /tmp/chargepilot-before-reset.sql'
if ($LASTEXITCODE -ne 0) { throw 'Backup failed; schemas were not reset.' }
docker cp "${container}:/tmp/chargepilot-before-reset.sql" $backup
if ($LASTEXITCODE -ne 0 -or (Get-Item -LiteralPath $backup).Length -eq 0) { throw 'Backup copy failed; schemas were not reset.' }
$sql = 'DROP DATABASE gateway_db; CREATE DATABASE gateway_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci; DROP DATABASE user_db; CREATE DATABASE user_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci; DROP DATABASE admin_db; CREATE DATABASE admin_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci; DROP DATABASE billing_db; CREATE DATABASE billing_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci; DROP DATABASE worker_db; CREATE DATABASE worker_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;'
$sql | docker exec -i $container sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -u root'
if ($LASTEXITCODE -ne 0) { throw 'Reset failed; backup exists, keep services stopped.' }
# These Redis containers are dedicated to this development project. Remove old
# sessions and jobs which refer to business rows discarded with the schemas.
foreach ($redis in @('chargepilot-dev-redis-cache-1','chargepilot-dev-redis-stream-1')) {
  docker exec $redis redis-cli FLUSHDB
  if ($LASTEXITCODE -ne 0) { throw "Unable to reset project Redis: $redis" }
}
docker compose -f "$root/compose.dev.yaml" run --rm migrate
if ($LASTEXITCODE -ne 0) { throw 'Init failed; keep services stopped and inspect the backup.' }
docker compose -f "$root/compose.dev.yaml" up -d central gateway worker admin-web
if ($LASTEXITCODE -ne 0) { throw 'Unable to restart the development stack.' }
Write-Host "Development schemas rebuilt. Backup: $backup"

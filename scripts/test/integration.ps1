# Requires the local Compose network; uses disposable MySQL and Redis.
$ErrorActionPreference='Stop'
$repo=(Resolve-Path "$PSScriptRoot/../..").Path
$testTag='chargepilot-db-tests-'+[guid]::NewGuid().ToString('N').Substring(0,8)
$mysql="${testTag}-mysql"
$redis="${testTag}-redis"
docker network inspect chargepilot-dev_default *> $null
if ($LASTEXITCODE -ne 0) { throw 'Start the local Compose stack once to create its development network.' }
try {
  docker run -d --name $mysql --network chargepilot-dev_default -e MYSQL_ROOT_PASSWORD=integration-only mysql:8.4
  if ($LASTEXITCODE -ne 0) { throw 'Unable to create isolated MySQL' }
  docker run -d --name $redis --network chargepilot-dev_default redis:8-alpine
  if ($LASTEXITCODE -ne 0) { throw 'Unable to create isolated Redis' }
  $ready=$false
  $ErrorActionPreference='Continue'
  for ($attempt=0; $attempt -lt 60; $attempt++) {
    docker exec -e MYSQL_PWD=integration-only $mysql mysql -h 127.0.0.1 -uroot -e 'SELECT 1' 2>$null | Out-Null
    if ($LASTEXITCODE -eq 0) {$ready=$true;break}
    Start-Sleep -Seconds 1
  }
  $ErrorActionPreference='Stop'
  if (-not $ready) {throw 'MySQL did not become ready'}
  docker exec -e MYSQL_PWD=integration-only $mysql mysql -uroot -e 'CREATE DATABASE gateway_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci; CREATE DATABASE central_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci; CREATE DATABASE worker_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;'
  if ($LASTEXITCODE -ne 0) { throw 'Unable to create test schemas' }
  $centralUrl="mysql://root:integration-only@${mysql}:3306/central_db"
  $gatewayUrl="mysql://root:integration-only@${mysql}:3306/gateway_db"
  $workerUrl="mysql://root:integration-only@${mysql}:3306/worker_db"
  docker compose -f "$repo/compose.dev.yaml" run --rm --no-deps -T -e "DATABASE_URL_CENTRAL=$centralUrl" -e "DATABASE_URL_GATEWAY=$gatewayUrl" -e "DATABASE_URL_WORKER=$workerUrl" migrate
  if ($LASTEXITCODE -ne 0) {throw 'New initialization failed'}
  docker compose -f "$repo/compose.dev.yaml" run --rm --no-deps -T -e "TEST_ADMIN_DATABASE_URL=$centralUrl" -e "TEST_USER_DATABASE_URL=$centralUrl" -e "TEST_BILLING_DATABASE_URL=$centralUrl" -e "TEST_GATEWAY_DATABASE_URL=$gatewayUrl" -e "TEST_WORKER_DATABASE_URL=$workerUrl" -e "TEST_REDIS_URL=redis://${redis}:6379/0" -e "TEST_STREAM_REDIS_URL=redis://${redis}:6379/1" central go test -count=1 -p 1 -timeout 5m ./...
  if ($LASTEXITCODE -ne 0) {throw 'Database integration tests failed'}
} finally {
  docker rm -fv $mysql $redis | Out-Null
}

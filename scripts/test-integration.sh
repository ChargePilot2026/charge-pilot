#!/usr/bin/env bash
# 用一套隔离、用完即弃的 MySQL 与 Redis 跑全部 Go 集成测试。
set -euo pipefail
cd "$(dirname "$0")/.."
name="chargepilot-test-$$"
cleanup() { docker rm -fv "$name-mysql" "$name-redis" >/dev/null 2>&1 || true; }
trap cleanup EXIT INT TERM
docker run --rm -d --name "$name-mysql" -e MYSQL_ROOT_PASSWORD=integration-only -p 127.0.0.1::3306 mysql:8.4 >/dev/null
docker run --rm -d --name "$name-redis" -p 127.0.0.1::6379 redis:8-alpine >/dev/null
for attempt in $(seq 1 90); do
  if docker exec -e MYSQL_PWD=integration-only "$name-mysql" mysql -h 127.0.0.1 -uroot -e 'SELECT 1' >/dev/null 2>&1; then break; fi
  if [[ "$attempt" == 90 ]]; then echo 'MySQL readiness timed out' >&2; exit 1; fi
  sleep 1
done
mysql_port=$(docker port "$name-mysql" 3306/tcp | sed 's/.*://')
redis_port=$(docker port "$name-redis" 6379/tcp | sed 's/.*://')
for schema in gateway user admin billing worker; do
  docker exec -e MYSQL_PWD=integration-only "$name-mysql" mysql -h 127.0.0.1 -uroot -e "CREATE DATABASE ${schema}_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"
  # macOS 自带的是 bash 3.2，没有 ${var^^}；改用 tr 转大写，
  # 这样脚本在 CI 镜像和原版 macOS shell 上都能跑。
  upper=$(printf '%s' "$schema" | tr '[:lower:]' '[:upper:]')
  key="DATABASE_URL_${upper}"
  export "$key=mysql://root:integration-only@127.0.0.1:$mysql_port/${schema}_db"
  export "TEST_${upper}_DATABASE_URL=${!key}"
done
export TEST_REDIS_URL="redis://127.0.0.1:$redis_port/0"
export TEST_STREAM_REDIS_URL="redis://127.0.0.1:$redis_port/1"
go run ./cmd/migrate --schema all
go test -count=1 -p 1 -timeout 5m "$@" ./...

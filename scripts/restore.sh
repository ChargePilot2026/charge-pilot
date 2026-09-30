#!/usr/bin/env bash
# 把 scripts/backup.sh 产出的备份恢复到配置好的各个库。
# 破坏性是刻意设计的：先 DROP 每个库，恢复才不会和陈旧数据悄悄合并。
# 除非 RESTORE_CONFIRM 等于它即将替换掉的库名清单，否则拒绝执行。
#
# 用法：
#   RESTORE_CONFIRM="user admin billing worker gateway" \
#     scripts/restore.sh backups/chargepilot-20260929T000000Z.sql.gz
set -euo pipefail
cd "$(dirname "$0")/.."

archive="${1:-}"
if [[ -z "$archive" || ! -f "$archive" ]]; then
  echo "usage: RESTORE_CONFIRM=\"<schemas>\" scripts/restore.sh <archive.sql.gz>" >&2
  exit 2
fi

host="${MYSQL_HOST:-127.0.0.1}"
port="${MYSQL_PORT:-3306}"
user="${MYSQL_USER:-root}"

# 归档才是「要替换掉什么」的准绳，操作者必须确认的正是这几个库。
targets="$(gzip -dc "$archive" | sed -n 's/^-- schemas: //p' | head -1)"
if [[ -z "$targets" ]]; then
  echo "archive does not declare which schemas it contains: $archive" >&2
  exit 1
fi
if [[ "${RESTORE_CONFIRM:-}" != "$targets" ]]; then
  echo "refusing to restore without confirmation." >&2
  echo "archive contains: $targets" >&2
  echo "set RESTORE_CONFIRM=\"$targets\" to proceed (this DROPS those schemas)" >&2
  exit 1
fi

if command -v mysql >/dev/null 2>&1; then
  mysql=(mysql -h"$host" -P"$port" -u"$user")
elif [[ -n "${MYSQL_CLIENT_CONTAINER:-}" ]]; then
  mysql=(docker exec -i -e MYSQL_PWD="$MYSQL_PASSWORD" "$MYSQL_CLIENT_CONTAINER" mysql -h127.0.0.1 -u"$user")
else
  echo "no mysql client available; set MYSQL_CLIENT_CONTAINER to the database container" >&2
  exit 1
fi
for schema in $targets; do
  # 只有 DROP 再重建，才能保证恢复后的数据与归档一致，而不是叠在原有数据之上。
  "${mysql[@]}" -e \
    "DROP DATABASE IF EXISTS \`${schema}\`; CREATE DATABASE \`${schema}\` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
done

if ! gzip -dc "$archive" | "${mysql[@]}"; then
  echo "restore failed while applying the archive; the schemas are partially loaded" >&2
  exit 1
fi

# 恢复后必须重跑 Goose：迁移历史本身就存在数据库里，跳过会让迁移没被记录。
# 连接串取自环境变量，所以只在配置齐全时才执行。
if command -v go >/dev/null 2>&1 && [[ -n "${DATABASE_URL_USER:-}" ]]; then
  echo "re-applying migrations after restore"
  go run ./cmd/migrate --schema all
else
  echo "skipping migration replay: set DATABASE_URL_* to re-apply migrations" >&2
fi

for schema in $targets; do
  tables="$("${mysql[@]}" -N -e \
    "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA='${schema}'")"
  echo "restored ${schema}: ${tables} tables"
done

# 各服务在启动时读取配置与引导状态。恢复把管理后台的表清空后，在重启之前
# 没有人能登录，所以这里明确提示，而不是让人撞上无声的锁死。
echo ""
echo "restart the services so they reload the restored data:"
echo "  docker compose restart central gateway worker   # development stack"
echo "  docker compose -f docker-compose.yml up -d    # production stack"

#!/usr/bin/env bash
# Restore a backup produced by scripts/backup.sh into the configured schemas.
# This is destructive by design: it drops each schema first so a restore cannot
# silently merge with stale rows. It refuses to run unless RESTORE_CONFIRM is set
# to the schema list it is about to replace.
#
# Usage:
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

# The archive is the source of truth for what gets replaced; the operator must
# confirm exactly those schemas.
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
  # Dropping and recreating is the only way to guarantee the restored data
  # matches the archive rather than layering on top of whatever was there.
  "${mysql[@]}" -e \
    "DROP DATABASE IF EXISTS \`${schema}\`; CREATE DATABASE \`${schema}\` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
done

if ! gzip -dc "$archive" | "${mysql[@]}"; then
  echo "restore failed while applying the archive; the schemas are partially loaded" >&2
  exit 1
fi

# Goose has to be re-run after a restore because the schema history lives in the
# database itself; skipping it would leave migrations unrecorded. The connection
# URLs come from the environment, so this only runs when they are configured.
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

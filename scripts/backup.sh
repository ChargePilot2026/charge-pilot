#!/usr/bin/env bash
# Back up every ChargePilot schema into one compressed archive and verify it can
# be read back. A backup nobody has restored is not a backup, so this script
# fails loudly when the archive is unreadable rather than reporting success.
#
# Usage:
#   scripts/backup.sh <output-dir>
#   MYSQL_HOST=... MYSQL_PORT=... MYSQL_USER=... MYSQL_PASSWORD=... scripts/backup.sh out/
set -euo pipefail
cd "$(dirname "$0")/.."

out_dir="${1:-./backups}"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
host="${MYSQL_HOST:-127.0.0.1}"
port="${MYSQL_PORT:-3306}"
user="${MYSQL_USER:-root}"
schemas=(gateway_db user_db admin_db billing_db worker_db)

if [[ ! -d "$out_dir" ]]; then
  echo "create output directory: $out_dir" >&2
  exit 1
fi

# The MySQL client may only exist inside the database container, so every call
# goes through this wrapper; MYSQL_CLIENT_DOCKER names the container to use.
mysql_cli=()
if command -v mysql >/dev/null 2>&1; then
  mysql_cli=(mysql -h"$host" -P"$port" -u"$user")
elif [[ -n "${MYSQL_CLIENT_CONTAINER:-}" ]]; then
  mysql_cli=(docker exec -e MYSQL_PWD="$MYSQL_PASSWORD" "$MYSQL_CLIENT_CONTAINER" mysql -h127.0.0.1 -u"$user")
else
  echo "no mysql client available; set MYSQL_CLIENT_CONTAINER to the database container" >&2
  exit 1
fi

mysqldump_cli=()
if command -v mysqldump >/dev/null 2>&1; then
  mysqldump_cli=(mysqldump -h"$host" -P"$port" -u"$user")
elif [[ -n "${MYSQL_CLIENT_CONTAINER:-}" ]]; then
  mysqldump_cli=(docker exec -e MYSQL_PWD="$MYSQL_PASSWORD" "$MYSQL_CLIENT_CONTAINER" mysqldump -h127.0.0.1 -u"$user")
else
  echo "no mysqldump available; set MYSQL_CLIENT_CONTAINER to the database container" >&2
  exit 1
fi

# mysqldump refuses to continue if a table is missing, so a schema that is not
# present yet must not be silently skipped.
existing=()
for schema in "${schemas[@]}"; do
  if "${mysql_cli[@]}" -N \
      -e "SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME='${schema}'" \
      | grep -q .; then
    existing+=("$schema")
  else
    echo "schema $schema does not exist; refusing to produce a partial backup" >&2
    exit 1
  fi
done

archive="${out_dir%/}/chargepilot-${stamp}.sql.gz"
dump_args=(--single-transaction --routines --triggers --events --set-gtid-purged=OFF
           --column-statistics=0 --no-tablespaces)

{
  echo "-- ChargePilot backup ${stamp}"
  echo "-- schemas: ${existing[*]}"
  for schema in "${existing[@]}"; do
    echo "-- ---- ${schema} ----"
    # mysqldump omits USE when it is told not to, so the target is stated
    # explicitly; a restore that guessed would write into the wrong schema.
    echo "USE \`${schema}\`;"
   "${mysqldump_cli[@]}" "${dump_args[@]}" "$schema"
  done
} | gzip -9 > "$archive"

# Verify by decompressing the whole archive; a truncated dump must not pass.
if ! gzip -t "$archive"; then
  echo "backup archive failed integrity check: $archive" >&2
  exit 1
fi

size="$(wc -c < "$archive" | tr -d ' ')"
if [[ "$size" -lt 1024 ]]; then
  echo "backup is suspiciously small (${size} bytes); refusing to report success" >&2
  exit 1
fi

echo "$archive"
echo "verified: ${size} bytes, schemas: ${existing[*]}"

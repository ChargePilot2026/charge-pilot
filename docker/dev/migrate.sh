#!/bin/sh
# P4 migration 接管：把全部迁移逐库执行一遍。
#
# 写在文件里而不是 compose 的 command 字段，是为了避开两重坑：
#   1. compose 会先做一次 `$$` → `$` 的转义，折叠标量里的 `$$1` 会变成 `$1`，
#      而 `sh` 里 `$1` 是位置参数 —— 空循环时没人发现，命令一旦真正传参就立刻失效；
#   2. 5 个 schema × (库名 + 基准表名 + DATABASE_URL) 挤在一个 YAML 字符串里，
#      引号层级多到无法肉眼校验。
# 脚本形式可以正常 review，也方便本地单独执行验证。
set -eu

MYSQL_HOST="${MYSQL_HOST:-mysql}"
MYSQL_PORT="${MYSQL_PORT:-3306}"
MYSQL_USER="${MYSQL_USER:-chargepilot}"
MYSQL_PASSWORD_VALUE="${MYSQL_PASSWORD_VALUE:-chargepilot_dev}"

# 基准表必须取各 schema `0001_init.sql` 里**必然存在**的一张表：
# migrate-baseline 用它区分「存量库（需要 baseline 登记）」与
# 「全新库（应当全量执行）」。选错会导致其中一条分支被跳过。
run_schema() {
    db="$1"
    probe="$2"
    echo "--- migrate: ${db} (probe=${probe}) ---"
    DATABASE_URL="mysql://${MYSQL_USER}:${MYSQL_PASSWORD_VALUE}@${MYSQL_HOST}:${MYSQL_PORT}/${db}" \
        cargo run --release -q -p migrate-baseline -- \
        --source "migrations/${db}" --probe "${probe}"
}

run_schema gateway_db vendor
run_schema user_db 'user'
run_schema admin_db admin_user_role
run_schema billing_db settlement
run_schema worker_db scheduled_task

echo '全部 schema 的迁移执行完成'

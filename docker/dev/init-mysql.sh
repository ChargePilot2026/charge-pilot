#!/bin/bash
# This file is sourced by the official MySQL entrypoint on an empty data volume.
# A subshell keeps our settings and variables out of the parent entrypoint.
#
# P4 migration 接管:**本脚本只负责建库与授权,不碰任何表结构。**
# 原实现在这里以 root 身份裸跑 `migrations/*/*.sql` 直接建表,后果是:
#   - 所有表的 _sqlx_migrations 记录被整体跳过,迁移登记表形同虚设;
#   - 服务连到的是一个「schema 看起来对、版本记录全无」的库,
#     后续任何回滚/增量升级都无法判断当前处于第几版;
#   - 空卷启动与存量库升级走的是两条完全不同的代码路径,V9 无法验证。
# 建表统一交给 migrate job(migrate-baseline + sqlx Migrator),真正做到单一事实来源。
(
    set -eu
    export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"
    for service in gateway user admin billing worker; do
        mysql --user=root --default-character-set=utf8mb4 <<SQL
CREATE DATABASE IF NOT EXISTS ${service}_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
-- 收紧到本服务自己的 schema：原实现对全部 5 个 schema 授 ALL PRIVILEGES，
-- "数据隔离"只是自觉而非机制（技术规格 §2.4）。
-- 需要跨库的服务改用本库视图，且视图只读。
GRANT ALL PRIVILEGES ON ${service}_db.* TO 'chargepilot'@'%';
SQL
    done
)

#!/bin/bash
# This file is sourced by the official MySQL entrypoint on an empty data volume.
# A subshell keeps our settings and variables out of the parent entrypoint.
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
        for migration in /migrations/${service}_db/*.sql; do
            mysql --user=root --default-character-set=utf8mb4 "${service}_db" < "$migration"
        done
    done
)

#!/bin/bash
# MySQL 官方入口脚本只在数据卷为空时才会 source 本文件。
# 这里只建库和授权；所有建表 DDL 归 cmd/migrate 里的 Goose 管。
(
    set -eu
    export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"
    for service in gateway central worker; do
        mysql --user=root --default-character-set=utf8mb4 <<SQL
CREATE DATABASE IF NOT EXISTS ${service}_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
GRANT ALL PRIVILEGES ON ${service}_db.* TO 'chargepilot'@'%';
SQL
    done
)

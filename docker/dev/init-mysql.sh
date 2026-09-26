#!/bin/bash
# This file is sourced by the official MySQL entrypoint on an empty data volume.
# A subshell keeps our settings and variables out of the parent entrypoint.
(
    set -eu
    export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"
    for service in gateway user admin billing worker; do
        mysql --user=root --default-character-set=utf8mb4 <<SQL
CREATE DATABASE IF NOT EXISTS ${service}_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
GRANT ALL PRIVILEGES ON ${service}_db.* TO 'chargepilot'@'%';
SQL
        mysql --user=root --default-character-set=utf8mb4 "${service}_db" \
            < "/migrations/${service}_db/0001_init.sql"
    done
)

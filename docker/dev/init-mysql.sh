#!/bin/bash
# MySQL official entrypoint sources this only on an empty data volume.
# Create schemas and grants; Goose in cmd/migrate owns all table DDL.
(
    set -eu
    export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"
    for service in gateway user admin billing worker; do
        mysql --user=root --default-character-set=utf8mb4 <<SQL
CREATE DATABASE IF NOT EXISTS ${service}_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
GRANT ALL PRIVILEGES ON ${service}_db.* TO 'chargepilot'@'%';
SQL
    done
)

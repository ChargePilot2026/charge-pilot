-- +goose Up
CREATE TABLE charge_port_lock (
    port_code VARCHAR(64) NOT NULL PRIMARY KEY
) ENGINE=InnoDB;

-- +goose Down
DROP TABLE charge_port_lock;

-- +goose Up
CREATE TABLE charge_billing_cutoff (
 charge_order_id BIGINT UNSIGNED PRIMARY KEY,
 cutoff_at DATETIME(3) NOT NULL,
 reason VARCHAR(64) NOT NULL,
 electric_cents BIGINT DEFAULT NULL COMMENT '预算耗尽时冻结的实收电费',
 service_cents BIGINT DEFAULT NULL COMMENT '预算耗尽时冻结的实收服务费',
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
) ENGINE=InnoDB;
CREATE TABLE charge_manual_settlement (
 charge_order_id BIGINT UNSIGNED PRIMARY KEY,
 request_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL UNIQUE,
 actor_id BIGINT UNSIGNED NOT NULL,
 electric_cents BIGINT NOT NULL,
 service_cents BIGINT NOT NULL,
 reason VARCHAR(1000) NOT NULL,
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
) ENGINE=InnoDB;

-- +goose Down
DROP TABLE charge_manual_settlement;
DROP TABLE charge_billing_cutoff;

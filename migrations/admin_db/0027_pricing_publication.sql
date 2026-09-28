-- +goose NO TRANSACTION
-- +goose Up
CREATE TABLE pricing_publication (
 request_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 actor_id BIGINT UNSIGNED NOT NULL,
 payload_hash CHAR(64) CHARACTER SET ascii NOT NULL,
 rule_id BIGINT UNSIGNED NOT NULL,
 version INT UNSIGNED NOT NULL,
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 UNIQUE KEY uk_rule (rule_id)
) ENGINE=InnoDB;
INSERT IGNORE INTO permission(code,name,module) VALUES ('pricing.rule.update','停用计费规则','pricing');
INSERT IGNORE INTO role_permission(role_id,permission_id)
 SELECT r.id,p.id FROM role r CROSS JOIN permission p WHERE r.code='customer_admin' AND r.deleted_at IS NULL AND p.code='pricing.rule.update';

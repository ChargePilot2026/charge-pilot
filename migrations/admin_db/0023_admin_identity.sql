-- +goose NO TRANSACTION
-- +goose Up
-- NULL in the old composite unique keys allowed duplicate active identities.
ALTER TABLE admin_user_role ADD COLUMN active_username VARCHAR(64)
  GENERATED ALWAYS AS (IF(deleted_at IS NULL, username, NULL)) STORED,
  ADD UNIQUE KEY uk_active_username (active_username);
ALTER TABLE role ADD COLUMN active_code VARCHAR(64)
  GENERATED ALWAYS AS (IF(deleted_at IS NULL, code, NULL)) STORED,
  ADD UNIQUE KEY uk_active_code (active_code);
INSERT INTO role (code,name,is_builtin) VALUES ('customer_admin','客户管理员',1)
  ON DUPLICATE KEY UPDATE code=VALUES(code);
INSERT IGNORE INTO role_permission (role_id,permission_id)
 SELECT r.id,p.id FROM role r CROSS JOIN permission p
 WHERE r.code='customer_admin' AND r.deleted_at IS NULL;

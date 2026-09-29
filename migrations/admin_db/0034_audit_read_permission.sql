-- +goose NO TRANSACTION
-- +goose Up

-- The audit trail was write-only: every operator action was recorded but there
-- was no permission, endpoint or page to read it back. Reading the trail is
-- deliberately a separate permission from acting, because it exposes who did
-- what across every module including money.
INSERT INTO permission (code, name, module, description) VALUES
  ('audit.read', '查看审计日志', 'audit', '查看操作审计日志与操作前后快照');

INSERT INTO role_permission (role_id, permission_id)
SELECT r.id, p.id FROM role r
JOIN permission p ON p.code = 'audit.read'
WHERE r.code IN ('customer_admin', 'customer_ops') AND r.deleted_at IS NULL;

-- +goose Down
DELETE rp FROM role_permission rp
  JOIN permission p ON p.id = rp.permission_id
  WHERE p.code = 'audit.read';
DELETE FROM permission WHERE code = 'audit.read';

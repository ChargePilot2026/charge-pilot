-- +goose NO TRANSACTION
-- +goose Up
INSERT IGNORE INTO permission (code, name, module) VALUES
 ('vendor.read', '查看厂商', 'vendor'),
 ('vendor.create', '新建厂商', 'vendor'),
 ('vendor.update', '编辑及启停厂商', 'vendor');

-- 厂商是设备开通的前置资料，授权与已有站点/设备运营权限保持一致。
INSERT IGNORE INTO role_permission (role_id, permission_id)
SELECT r.id, p.id FROM role r JOIN permission p ON p.module = 'vendor'
WHERE r.code IN ('customer_admin', 'customer_ops', 'dev_admin') AND r.deleted_at IS NULL;

-- +goose Down
DELETE rp FROM role_permission rp JOIN permission p ON p.id = rp.permission_id
WHERE p.code IN ('vendor.read', 'vendor.create', 'vendor.update');
DELETE FROM permission WHERE code IN ('vendor.read', 'vendor.create', 'vendor.update');

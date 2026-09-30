-- +goose NO TRANSACTION
-- +goose Up
INSERT IGNORE INTO permission (code, name, module, description) VALUES
('device.operate', '启用禁用设备', 'device', '切换设备运营状态，不断开连接或停止已有订单');
INSERT IGNORE INTO role_permission (role_id, permission_id)
SELECT r.id, p.id FROM role r JOIN permission p ON p.code = 'device.operate'
WHERE r.code IN ('customer_admin', 'customer_ops') AND r.deleted_at IS NULL;

-- +goose Down
DELETE rp FROM role_permission rp JOIN permission p ON p.id = rp.permission_id WHERE p.code = 'device.operate';
DELETE FROM permission WHERE code = 'device.operate';

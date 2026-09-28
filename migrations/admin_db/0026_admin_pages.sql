-- +goose NO TRANSACTION
-- +goose Up
INSERT IGNORE INTO permission (code,name,module) VALUES
 ('admin_user.read','查看管理员','admin_user'),('alert.read','查看告警','alert'),
 ('announcement.read','查看公告','announcement'),('customer_service.read','查看客服','customer_service'),
 ('webhook.read','查看 Webhook','webhook'),('ota.read','查看 OTA','ota'),
 ('pricing.read','查看计费规则','pricing'),('finance.read','查看财务记录','finance'),
 ('whitelabel.read','查看白标','whitelabel'),('coupon.read','查看优惠券','coupon'),
 ('feedback.read','查看反馈','feedback'),('fault.read','查看故障','fault'),
 ('device.import','导入设备','device');
INSERT INTO role (code,name,is_builtin) VALUES
 ('customer_ops','运营',1),('customer_cs','客服',1),('customer_finance','财务',1)
 ON DUPLICATE KEY UPDATE code=VALUES(code);
INSERT IGNORE INTO role_permission(role_id,permission_id)
 SELECT r.id,p.id FROM role r CROSS JOIN permission p WHERE r.code='customer_admin' AND r.deleted_at IS NULL;
INSERT IGNORE INTO role_permission(role_id,permission_id)
 SELECT r.id,p.id FROM role r CROSS JOIN permission p WHERE r.deleted_at IS NULL AND (
 (r.code='customer_ops' AND p.module IN ('station','device','coupon','announcement','customer_service','alert','webhook','ota','dashboard')) OR
 (r.code='customer_cs' AND p.code IN ('dashboard.read','order.read','feedback.read','feedback.reply','fault.read','fault.dispatch','fault.resolve','admin_user.read','customer_service.read')) OR
 (r.code='customer_finance' AND (p.module IN ('finance','invoice') OR p.code IN ('dashboard.read','order.read','order.refund.create'))));

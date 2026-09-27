INSERT IGNORE INTO permission (code,name,module,description)
VALUES ('dashboard.read','查看运营仪表盘','dashboard','查看充电订单、结算金额与待处理告警汇总');

INSERT IGNORE INTO role_permission (role_id,permission_id)
SELECT r.id,p.id FROM role r JOIN permission p ON p.code='dashboard.read'
WHERE r.code IN ('customer_admin','customer_ops','dev_admin') AND r.deleted_at IS NULL;

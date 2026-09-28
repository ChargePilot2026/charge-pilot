-- +goose NO TRANSACTION
-- +goose Up
INSERT IGNORE INTO station_code_identity (code) SELECT DISTINCT code FROM station;
INSERT IGNORE INTO permission (code,name,module) VALUES ('order.read','查看充电订单','order');
INSERT IGNORE INTO role_permission (role_id,permission_id)
 SELECT r.id,p.id FROM role r JOIN permission p ON p.code='order.read'
 WHERE r.code='customer_admin' AND r.deleted_at IS NULL;

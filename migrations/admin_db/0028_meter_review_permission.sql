-- +goose Up
INSERT IGNORE INTO permission(code,name,module) VALUES('billing.meter.review','实际计量双人核实','billing');
INSERT IGNORE INTO role_permission(role_id,permission_id)
 SELECT r.id,p.id FROM role r CROSS JOIN permission p WHERE r.code='customer_finance' AND r.deleted_at IS NULL AND p.code='billing.meter.review';

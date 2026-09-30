-- +goose Up
-- No conversion of the independent legacy template/offer pools. Existing
-- rows remain available for inspection; new APIs only accept complete schemes.
ALTER TABLE device_meta ADD COLUMN execution_capabilities JSON DEFAULT NULL
 COMMENT '经验证的时长、电量、在线卡、加时与事件识别能力';
INSERT IGNORE INTO permission(code,name,module) VALUES ('online_card.manage','管理在线卡绑定与状态','user');
INSERT IGNORE INTO role_permission(role_id,permission_id)
 SELECT r.id,p.id FROM role r CROSS JOIN permission p WHERE r.code='customer_admin' AND r.deleted_at IS NULL AND p.code='online_card.manage';

-- +goose Down
DELETE rp FROM role_permission rp JOIN permission p ON p.id=rp.permission_id WHERE p.code='online_card.manage';
DELETE FROM permission WHERE code='online_card.manage';
ALTER TABLE device_meta DROP COLUMN execution_capabilities;

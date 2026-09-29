-- +goose NO TRANSACTION
-- +goose Up

-- Activity rules are configured by operations, not by hand-written SQL, so the
-- campaign window and budget can be changed while the system is running. They
-- reuse the coupon permissions because an activity is a way of handing out a
-- coupon: an operator who may create coupons may create the rules that grant
-- them, and one who may only read coupons can see which campaigns are running.
-- module is NOT NULL with no default, so every column must be named.
INSERT INTO permission (code, name, module, description) VALUES
  ('coupon.activity.read', '查看活动规则', 'coupon', '查看优惠券活动规则与发放统计'),
  ('coupon.activity.manage', '管理活动规则', 'coupon', '创建、调整与停用优惠券活动规则');

-- Roles that already manage coupons get the matching activity permissions.
INSERT INTO role_permission (role_id, permission_id)
SELECT r.id, p.id FROM role r
JOIN permission p ON p.code IN ('coupon.activity.read', 'coupon.activity.manage')
WHERE r.code = 'customer_admin' AND r.deleted_at IS NULL;

INSERT INTO role_permission (role_id, permission_id)
SELECT r.id, p.id FROM role r
JOIN permission p ON p.code = 'coupon.activity.read'
WHERE r.code = 'customer_ops' AND r.deleted_at IS NULL;

-- +goose Down
DELETE rp FROM role_permission rp
  JOIN permission p ON p.id = rp.permission_id
  WHERE p.code IN ('coupon.activity.read', 'coupon.activity.manage');
DELETE FROM permission WHERE code IN ('coupon.activity.read', 'coupon.activity.manage');

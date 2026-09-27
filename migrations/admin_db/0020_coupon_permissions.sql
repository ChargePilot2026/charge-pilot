INSERT IGNORE INTO permission (code,name,module,description) VALUES
 ('coupon.read','查看优惠券','coupon','查询优惠券模板和发放统计'),
 ('coupon.create','创建优惠券','coupon','创建优惠券模板'),
 ('coupon.update','编辑优惠券','coupon','修改优惠券名称、状态和结束时间'),
 ('coupon.delete','删除优惠券','coupon','软删除优惠券模板'),
 ('coupon.grant','发放优惠券','coupon','向指定用户发放优惠券');

INSERT IGNORE INTO role_permission (role_id,permission_id)
SELECT r.id,p.id FROM role r JOIN permission p ON p.code IN ('coupon.read','coupon.create','coupon.update','coupon.delete','coupon.grant')
WHERE r.code IN ('customer_admin','customer_ops','dev_admin') AND r.deleted_at IS NULL;

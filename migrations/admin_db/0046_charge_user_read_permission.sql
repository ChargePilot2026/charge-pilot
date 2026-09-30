-- +goose NO TRANSACTION
-- +goose Up

-- The back office could see admin accounts and charge orders, but there was no
-- way to get from either to the charging user behind them. Every user-facing
-- identifier in the admin console was a bare numeric user_id: a support agent
-- reading a complaint had no name, no contact number and no way to search for
-- the person.
--
-- This adds the read permission for that list. The code is charge_user.read
-- rather than user.read on purpose -- the user_db table is called `user`, but
-- `user.read` sits one word away from the existing `admin_user.read`, and in
-- this console the two would be read side by side in Require() calls. The extra
-- qualifier costs a few characters and removes the ambiguity permanently.
--
-- Note what this permission actually grants. The list decrypts phone_enc and
-- shows the number in full, because searching for a customer by phone is the
-- primary way support finds them. That means this one permission is the whole
-- access-control boundary around every charging user's phone number: holding
-- it is equivalent to holding a phone book. Keep the grant narrow, and treat
-- the grant itself as an audited event.
INSERT IGNORE INTO permission (code, name, module, description) VALUES
('charge_user.read', '查看充电用户', 'charge_user', '查看充电用户列表与档案,含完整手机号——持有本权限等同于持有全部充电用户手机号,授权需谨慎');

-- Support is the role that has to identify a customer from a phone call or a
-- complaint; the customer administrator needs it to audit an account. Finance
-- and operations work from orders and settlement, not from user profiles, so
-- they are left without it.
INSERT IGNORE INTO role_permission (role_id, permission_id)
SELECT r.id, p.id FROM role r JOIN permission p ON p.code = 'charge_user.read'
WHERE r.code IN ('customer_admin', 'customer_cs') AND r.deleted_at IS NULL;

-- +goose Down
DELETE rp FROM role_permission rp JOIN permission p ON p.id = rp.permission_id
WHERE p.code = 'charge_user.read';
DELETE FROM permission WHERE code = 'charge_user.read';

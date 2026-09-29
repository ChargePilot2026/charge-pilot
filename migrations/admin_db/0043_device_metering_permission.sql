-- +goose NO TRANSACTION
-- +goose Up

-- What a board can report is a property of the hardware, so declaring it is
-- part of bringing a board into service, not part of pricing it.
--
-- The separation matters: the capability check refuses to publish a metered
-- tariff onto a pile that cannot measure, and that check is only worth
-- anything if the role allowed to publish a tariff is not also the role
-- allowed to certify the hardware. Granting this to whoever holds
-- device.import — the roles that physically onboard boards — keeps the two
-- apart, and an operator who can set a price cannot simply assert that a
-- board supports kWh.
INSERT IGNORE INTO permission (code, name, module, description) VALUES
('device.metering', '声明设备计量能力', 'device', '登记设备是否上报电量与分段功率，未声明按不支持处理');

-- The roles that bring boards in are the ones that know what arrived, so they
-- are the ones who declare what it can report.
INSERT IGNORE INTO role_permission (role_id, permission_id)
SELECT r.id, p.id FROM role r JOIN permission p ON p.code = 'device.metering'
WHERE r.code IN ('customer_admin', 'customer_ops') AND r.deleted_at IS NULL;

-- +goose Down
DELETE rp FROM role_permission rp JOIN permission p ON p.id = rp.permission_id
WHERE p.code = 'device.metering';
DELETE FROM permission WHERE code = 'device.metering';

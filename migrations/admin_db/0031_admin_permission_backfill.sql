-- +goose NO TRANSACTION
-- +goose Up

-- The built-in customer administrator must be able to perform every operation a
-- lesser role can, otherwise the escalation guard on account creation would make
-- some roles impossible to grant even to the platform owner.
INSERT IGNORE INTO role_permission(role_id, permission_id)
 SELECT r.id, p.id FROM role r CROSS JOIN permission p
 WHERE r.code = 'customer_admin' AND r.deleted_at IS NULL
   AND p.code IN (
     'billing.meter.review',
     'finance.withdraw.create',
     'finance.withdraw.review',
     'finance.split_template.create',
     'finance.split_party.create',
     'export.create',
     'admin_user.update',
     'admin_user.delete',
     'admin_user.reset_password',
     'alert.rule.create',
     'alert.rule.update',
     'alert.rule.delete',
     'alert.subscription.create',
     'alert.risk_config.update',
     'ota.package.create',
     'ota.package.delete',
     'ota.schedule.create',
     'ota.schedule.trigger',
     'webhook.create'
   );

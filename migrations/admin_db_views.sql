-- ============================================
-- ChargePilot · admin_db 视图补全
-- 文档: docs/db/billing.md (withdraw_request) + docs/db/admin.md (membership_card)
-- 背景: admin 服务直连 admin_db,但 services/admin/src/billing.rs 的
--   withdraw_list/withdraw_create/withdraw_review 与 services/admin/src/api/membership.rs
--   的 list 各自使用 FROM withdraw_request / FROM membership_card(无 schema 前缀)。
--   物理表实际归属 billing_db / user_db,因此 admin_db 需要两张 VIEW 转发。
-- 范围: 仅 SELECT 场景(列表接口)。写入接口本论已禁用或未实现:
--   - withdraw_create: 仍走 INSERT INTO withdraw_request(View 可写,这里保留可写语义)
--   - membership create: 已显式返回 ServiceUnavailable,不在本迁移范围内
-- ============================================

SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci;

DROP VIEW IF EXISTS `withdraw_request`;
CREATE VIEW `withdraw_request` AS
  SELECT * FROM `billing_db`.`withdraw_request`;

DROP VIEW IF EXISTS `membership_card`;
CREATE VIEW `membership_card` AS
  SELECT * FROM `user_db`.`membership_card`;
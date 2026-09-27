-- D1:管理端写接口的全量操作权限矩阵
--
-- 背景:此前 admin 的写接口(角色/账号/密码重置/计费规则/分账模板等)没有任何
-- 操作授权校验,任一登录态管理员都能调用。`split_party_create` 尤其危险——
-- billing/src/api.rs 实际读取该模板分账,向比例已合计 10000 的模板追加正比例
-- 参与方会使后续分账因比例校验失败而停止。
--
-- 既有权限码(station.* / device.import / finance.refund.* / order.refund.* /
-- coupon.* / alert 之外的 fault.* / feedback.* / whitelabel.* / invoice.* /
-- dashboard.read)沿用不改;本迁移补齐其余写入口所需的操作码。

INSERT IGNORE INTO permission (code,name,module,description) VALUES
 -- 管理员账号
 ('admin_user.create','创建后台账号','admin_user','创建后台管理员账号'),
 ('admin_user.update','编辑后台账号','admin_user','修改账号资料、角色归属与状态'),
 ('admin_user.delete','删除后台账号','admin_user','软删除后台管理员账号'),
 ('admin_user.reset_password','重置后台账号密码','admin_user','重置指定后台账号的登录密码'),
 -- 角色与权限
 ('role.create','创建角色','role','创建角色'),
 ('role.update','编辑角色权限','role','修改角色信息与权限集合'),
 ('role.delete','删除角色','role','软删除角色'),
 -- 计费规则与模板
 ('pricing.rule.create','创建计费规则','pricing','创建计费规则(直接改变计费输入)'),
 ('pricing.template.create','创建计费模板','pricing','创建计费模板'),
 -- 分账(影响 billing 实际分账结算)
 ('finance.split_template.create','创建分账模板','finance','创建分账模板'),
 ('finance.split_party.create','维护分账参与方','finance','向分账模板增删参与方、比例与收款信息'),
 -- 提现
 ('finance.withdraw.create','发起提现申请','finance','创建提现申请'),
 ('finance.withdraw.review','审核提现申请','finance','审批或驳回提现申请'),
 -- 告警
 ('alert.ack','确认告警','alert','确认/忽略告警事件'),
 ('alert.rule.create','创建告警规则','alert','创建告警规则'),
 ('alert.rule.update','编辑告警规则','alert','修改告警规则'),
 ('alert.rule.delete','删除告警规则','alert','软删除告警规则'),
 ('alert.subscription.create','创建告警订阅','alert','创建告警订阅'),
 ('alert.risk_config.update','修改风控配置','alert','修改风控阈值配置'),
 -- 会员卡
 ('membership.create','创建会员卡模板','membership','创建会员卡模板'),
 -- 设置与 OTA
 ('settings.ota.update','修改 OTA 配置','settings','修改 OTA 全局配置'),
 ('ota.package.create','创建 OTA 固件包','ota','上传并创建 OTA 固件包'),
 ('ota.package.delete','删除 OTA 固件包','ota','软删除 OTA 固件包'),
 ('ota.schedule.create','创建 OTA 升级计划','ota','创建 OTA 升级计划'),
 ('ota.schedule.trigger','触发 OTA 升级计划','ota','立即触发 OTA 升级计划'),
 -- 公告
 ('announcement.create','创建公告','announcement','创建公告'),
 ('announcement.update','编辑公告','announcement','修改公告'),
 ('announcement.delete','删除公告','announcement','软删除公告'),
 -- 客服配置
 ('customer_service.create','创建客服配置','customer_service','创建客服入口配置'),
 ('customer_service.update','编辑客服配置','customer_service','修改客服入口配置'),
 ('customer_service.delete','删除客服配置','customer_service','软删除客服入口配置'),
 -- 客服工单
 ('fault.resolve','处理设备故障','fault','标记设备故障已处理'),
 -- Webhook
 ('webhook.create','创建 Webhook 订阅','webhook','创建 Webhook 订阅'),
 ('webhook.update','编辑 Webhook 订阅','webhook','修改 Webhook 订阅'),
 ('webhook.delete','删除 Webhook 订阅','webhook','软删除 Webhook 订阅'),
 -- 导出
 ('export.create','创建导出任务','export','创建数据导出任务');

-- 默认授权给既有管理角色。新增的敏感域(账号/角色/分账/定价)只给
-- customer_admin 与 dev_admin,运营与客服角色不授予。
INSERT IGNORE INTO role_permission (role_id,permission_id)
SELECT r.id, p.id
FROM role r JOIN permission p
  ON p.code IN (
    -- 运营/客服也需要的常规配置
    'alert.ack','alert.rule.create','alert.rule.update','alert.rule.delete',
    'alert.subscription.create','alert.risk_config.update',
    'announcement.create','announcement.update','announcement.delete',
    'customer_service.create','customer_service.update','customer_service.delete',
    'fault.resolve',
    'webhook.create','webhook.update','webhook.delete',
    'ota.package.create','ota.package.delete','ota.schedule.create','ota.schedule.trigger',
    'settings.ota.update','membership.create','export.create'
  )
WHERE r.code IN ('customer_admin','customer_ops','customer_cs','dev_admin')
  AND r.deleted_at IS NULL;

-- 高权限域:仅管理员与开发角色
INSERT IGNORE INTO role_permission (role_id,permission_id)
SELECT r.id, p.id
FROM role r JOIN permission p
  ON p.code IN (
    'admin_user.create','admin_user.update','admin_user.delete','admin_user.reset_password',
    'role.create','role.update','role.delete',
    'pricing.rule.create','pricing.template.create',
    'finance.split_template.create','finance.split_party.create',
    'finance.withdraw.create','finance.withdraw.review'
  )
WHERE r.code IN ('customer_admin','dev_admin')
  AND r.deleted_at IS NULL;

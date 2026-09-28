-- +goose NO TRANSACTION
-- +goose Up

-- Keep old rows for audit/recovery, but seed the supported singleton ID from
-- the latest configuration. Future application writes only update ID 1.
INSERT INTO whitelabel_config
  (id,name,logo_url,mini_program_name,mini_program_appid,theme_color,contact_phone,about_text,config_json)
SELECT 1,name,logo_url,mini_program_name,mini_program_appid,theme_color,contact_phone,about_text,config_json
FROM (
  SELECT name,logo_url,mini_program_name,mini_program_appid,theme_color,contact_phone,about_text,config_json
  FROM whitelabel_config ORDER BY id DESC LIMIT 1
) AS latest
ON DUPLICATE KEY UPDATE
  name=VALUES(name),logo_url=VALUES(logo_url),mini_program_name=VALUES(mini_program_name),
  mini_program_appid=VALUES(mini_program_appid),theme_color=VALUES(theme_color),
  contact_phone=VALUES(contact_phone),about_text=VALUES(about_text),config_json=VALUES(config_json);

INSERT IGNORE INTO permission (code,name,module,description) VALUES
('whitelabel.read','查看白标配置','settings','查看租户品牌和联系信息'),
('whitelabel.update','更新白标配置','settings','更新租户品牌、服务入口和展示信息');

INSERT IGNORE INTO role_permission (role_id,permission_id)
SELECT r.id,p.id FROM role r JOIN permission p ON p.code='whitelabel.read'
WHERE r.code IN ('customer_admin','customer_ops') AND r.deleted_at IS NULL;

INSERT IGNORE INTO role_permission (role_id,permission_id)
SELECT r.id,p.id FROM role r JOIN permission p ON p.code='whitelabel.update'
WHERE r.code='customer_admin' AND r.deleted_at IS NULL;

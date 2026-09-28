-- +goose NO TRANSACTION
-- +goose Up

-- MFA enrolment needs room for a base32 TOTP secret. The column already existed
-- as a 64-character placeholder, which is exactly the size RFC 4226 needs for a
-- 160-bit key, so only the semantics are documented here and the index that
-- makes "who still has MFA pending" answerable is added.
ALTER TABLE admin_user_role
  MODIFY COLUMN `mfa_secret` VARCHAR(64) NULL COMMENT 'TOTP 密钥(base32，启用前为待确认状态)';

-- Operations needs to find accounts whose enrolment is pending, so a secret
-- that was never confirmed does not linger as an unused factor.
ALTER TABLE admin_user_role
  ADD KEY idx_mfa_state (mfa_enabled, mfa_secret);

-- Station-scoped data scope: an empty scope means the account sees everything
-- its role permits, so NULL is the unrestricted default.
CREATE TABLE IF NOT EXISTS admin_data_scope (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  admin_user_id BIGINT UNSIGNED NOT NULL,
  scope_type ENUM('station','vendor') NOT NULL,
  scope_id BIGINT UNSIGNED NOT NULL,
  created_by BIGINT UNSIGNED NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uk_user_scope (admin_user_id, scope_type, scope_id),
  KEY idx_user (admin_user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='后台账号数据范围';

-- Field-level masking rules, keyed by resource and field so a role can hide
-- sensitive columns without a bespoke query per screen.
CREATE TABLE IF NOT EXISTS admin_field_mask (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  role_id BIGINT UNSIGNED NOT NULL,
  resource VARCHAR(64) NOT NULL,
  field VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uk_role_resource_field (role_id, resource, field)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='角色字段脱敏规则';

-- Asynchronous exports keep large reads off the request path and give operators
-- a download that expires instead of living forever.
CREATE TABLE IF NOT EXISTS export_task (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  task_no VARCHAR(64) NOT NULL,
  resource VARCHAR(64) NOT NULL,
  status ENUM('pending','running','completed','failed','expired') NOT NULL DEFAULT 'pending',
  requested_by BIGINT UNSIGNED NOT NULL,
  filter_json JSON NULL,
  row_count INT UNSIGNED NOT NULL DEFAULT 0,
  file_path VARCHAR(512) DEFAULT NULL,
  error_msg VARCHAR(255) DEFAULT NULL,
  expires_at DATETIME(3) DEFAULT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  completed_at DATETIME(3) DEFAULT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_task_no (task_no),
  KEY idx_status (status, expires_at),
  KEY idx_requester (requested_by, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='导出任务';

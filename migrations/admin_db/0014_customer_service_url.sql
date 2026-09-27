-- WeChat customer-service entry URLs exceed the legacy path field's 64 chars.
ALTER TABLE customer_service_config
  MODIFY COLUMN path VARCHAR(512) DEFAULT NULL COMMENT '微信客服入口 URL';

-- +goose NO TRANSACTION
-- +goose Up

-- D16:人工定价兜底单
--
-- 背景:
--   跨分时电价订单此前 100% 无法计费,且报错文案是「需审核」——
--   **没有任何对应流程**,运维拿到这句话无从下手。
--
--   现在计费失败会落一条本表,把「无法自动计费」变成**可查的落点**:
--   失败原因写进 `reason`,运维据此人工定价。
--
--   触发场景(见 `services/billing/src/services/fee.rs`):
--     1. 跨电价订单但 `meter.segments` 为空 —— gateway 未能从
--        telemetry 推导分段(设备未上报 meter_kwh / 采样不足 /
--        读数回退 / 段数超限)
--     2. `Σsegments.energy_wh != meter.charged_wh` —— 推导与设备
--        上报的总电量互相矛盾
--
--   表不存在时计费代码只 `warn!` 不报错 —— 兜底记账失败不应阻断
--   计费主流程。

CREATE TABLE IF NOT EXISTS `manual_fee_review` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `charge_order_id` BIGINT UNSIGNED NOT NULL,
  `order_no` VARCHAR(64) NOT NULL,
  `reason` VARCHAR(255) NOT NULL COMMENT '无法自动计费的原因(供人工定价参考)',
  `energy_wh` BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '设备上报的总电量',
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `created_month` VARCHAR(7) NOT NULL COMMENT 'YYYY-MM,供月度归档扫描',
  PRIMARY KEY (`id`),
  -- 同一订单重复重试(投递重放/人工重跑)只留一条,避免刷单
  UNIQUE KEY `uk_order` (`charge_order_id`),
  KEY `idx_month` (`created_month`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='人工定价兜底单(D16)';

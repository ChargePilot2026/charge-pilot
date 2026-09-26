CREATE TABLE IF NOT EXISTS fee_delivery (
  charge_order_id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
  payload_json JSON NOT NULL,
  delivered TINYINT(1) NOT NULL DEFAULT 0,
  attempts INT UNSIGNED NOT NULL DEFAULT 0,
  scheduled_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  delivered_at DATETIME(3) NULL,
  KEY idx_due (delivered,scheduled_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Resume fees committed before delivery was introduced.
INSERT IGNORE INTO fee_delivery (charge_order_id,payload_json)
SELECT r.charge_order_id,JSON_OBJECT('calculation_no',f.calculation_no,'source',r.source_json,
 'electric_cents',f.electric_cents,'service_cents',f.service_cents,'total_cents',f.total_cents)
FROM fee_receipt r JOIN fee_calculation f ON f.id=r.calculation_id
 AND f.calculation_no=r.calculation_no AND f.charge_order_id=r.charge_order_id;

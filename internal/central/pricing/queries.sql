-- name: ActiveStationRule :one
SELECT r.id, r.station_id, r.mode, r.time_of_use_json,
       r.service_fee_cents_per_kwh, r.service_fee_cents_per_min,
       r.min_charge_cents, r.version
FROM pricing_rule AS r
JOIN station AS s ON s.id = r.station_id
WHERE r.station_id = ? AND s.status = 'active' AND s.deleted_at IS NULL
  AND r.status = 'active' AND r.deleted_at IS NULL
  AND (r.effective_from IS NULL OR r.effective_from <= NOW(3))
  AND (r.effective_to IS NULL OR r.effective_to > NOW(3))
ORDER BY r.version DESC, r.id DESC LIMIT 1;

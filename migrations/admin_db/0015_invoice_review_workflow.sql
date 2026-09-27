-- Keep one current queue state per user-owned invoice; retain the newest state
-- if earlier deployments recorded multiple rows before enforcing uniqueness.
INSERT INTO audit_log(actor_id,actor_name,module,action,target_type,target_id,before_json,after_json,created_month)
SELECT 0,'system-migration','finance','invoice.queue_deduplicate','invoice_review',
       CAST(older.invoice_request_id AS CHAR),
       JSON_OBJECT('archived_row_id',older.id,'review_status',older.review_status,
                   'reviewed_by',older.reviewed_by,'reviewed_at',older.reviewed_at,
                   'reject_reason',older.reject_reason),
       JSON_OBJECT('retained_row_id',newer.id),DATE_FORMAT(UTC_DATE(),'%Y-%m-01')
FROM invoice_review older
JOIN invoice_review newer
  ON newer.invoice_request_id=older.invoice_request_id AND newer.id>older.id;

DELETE older FROM invoice_review older
JOIN invoice_review newer
  ON newer.invoice_request_id = older.invoice_request_id
 AND newer.id > older.id;

ALTER TABLE invoice_review
  ADD UNIQUE KEY `uk_invoice_review_request` (`invoice_request_id`);

INSERT IGNORE INTO permission (code, name, module)
VALUES ('invoice.review', '审核发票', 'finance');

INSERT IGNORE INTO role_permission (role_id, permission_id)
SELECT r.id, p.id
FROM role r
JOIN permission p ON p.code = 'invoice.review'
WHERE r.code = 'customer_finance' AND r.deleted_at IS NULL;

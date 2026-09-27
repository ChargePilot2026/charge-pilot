INSERT IGNORE INTO permission (code, name, module, description) VALUES
('feedback.read', '查看用户评价与投诉', 'customer_service', '查看用户提交的评价、投诉和建议'),
('feedback.reply', '回复与关闭用户反馈', 'customer_service', '回复或关闭用户反馈'),
('fault.read', '查看设备报修', 'inspection', '查看用户和巡检提交的设备报修'),
('fault.dispatch', '派单与处理设备报修', 'inspection', '指派巡检人员并更新报修处理状态');

INSERT IGNORE INTO role_permission (role_id, permission_id)
SELECT r.id, p.id FROM role r JOIN permission p ON p.code IN ('feedback.read','feedback.reply')
WHERE r.code IN ('customer_cs','customer_ops') AND r.deleted_at IS NULL;

INSERT IGNORE INTO role_permission (role_id, permission_id)
SELECT r.id, p.id FROM role r JOIN permission p ON p.code IN ('fault.read','fault.dispatch')
WHERE r.code IN ('customer_inspection','customer_ops') AND r.deleted_at IS NULL;

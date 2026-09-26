$ErrorActionPreference='Stop'
$taskCompose=Join-Path (Split-Path -Parent $PSScriptRoot) 'compose.dev.yaml'
$tag='manual_'+[guid]::NewGuid().ToString('N').Substring(0,12)
function Sql([string]$q){
 $result=$q|docker compose -f $taskCompose exec -T mysql sh -c 'export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"; exec mysql -uroot --batch --skip-column-names admin_db'
 if($LASTEXITCODE -ne 0){throw 'Manual refund fixture SQL failed'};return $result
}
function Create($body,[int]$status){
 $r=Invoke-WebRequest "http://127.0.0.1:8082/api/v1/admin/orders/$cid/refunds" -Headers $headers -Method Post -ContentType application/json -Body ($body|ConvertTo-Json) -SkipHttpErrorCheck
 if($r.StatusCode -ne $status){throw "Expected $status, got $($r.StatusCode): $($r.Content)"};return ($r.Content|ConvertFrom-Json).data
}
try {
 $role=Sql "INSERT INTO role(code,name) VALUES ('customer_finance','$tag'); SELECT LAST_INSERT_ID();"
 Sql "INSERT INTO role_permission(role_id,permission_id) SELECT $role,id FROM permission WHERE code IN ('order.read','order.refund.review'); INSERT INTO admin_user_role(username,password_hash,role_id,status) SELECT '$tag',password_hash,$role,'active' FROM admin_user_role WHERE username='admin' AND deleted_at IS NULL LIMIT 1;"|Out-Null
 $login=Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:8082/api/v1/admin/auth/login' -ContentType application/json -Body (@{username=$tag;password='DevAdmin2026!'}|ConvertTo-Json)
 $headers=@{Authorization="Bearer $($login.data.token)"}
 $uid=Sql "INSERT INTO user_db.user(openid) VALUES ('$tag'); SELECT LAST_INSERT_ID();"
 $cid=Sql "INSERT INTO user_db.charge_order(order_no,user_id,device_id,port_no,status,created_month) VALUES ('$tag',$uid,'$tag',1,'failed',DATE_FORMAT(UTC_DATE(),'%Y-%m-01')); SELECT LAST_INSERT_ID();"
 $paymentId=Sql "INSERT INTO user_db.payment_order(order_no,user_id,biz_type,biz_id,pay_method,total_cents,paid_cents,status,wechat_transaction_id,created_month) VALUES ('$tag',$uid,'charge',$cid,'wechat',200,200,'paid','TESTONLY123',DATE_FORMAT(UTC_DATE(),'%Y-%m-01')); SELECT LAST_INSERT_ID();"
 Sql "UPDATE user_db.charge_order SET payment_order_id=$paymentId WHERE id=$cid;"|Out-Null
 $body=@{request_id=[guid]::NewGuid().ToString();amount_cents=100;reason='manual refund integration test'}
 Create $body 403|Out-Null
 Sql "INSERT INTO role_permission(role_id,permission_id) SELECT $role,id FROM permission WHERE code='order.refund.create';"|Out-Null
 Sql "UPDATE user_db.charge_order SET status='charging' WHERE id=$cid;"|Out-Null
 Create $body 409|Out-Null
 Sql "UPDATE user_db.charge_order SET status='failed' WHERE id=$cid;"|Out-Null
 $first=Create $body 200
 $repeat=Create $body 200
 if(!$first.created -or $repeat.refund_no -ne $first.refund_no){throw 'Creation replay changed refund'}
 $body.amount_cents=50;Create $body 409|Out-Null
 $body.request_id=[guid]::NewGuid().ToString();$body.amount_cents=150;Create $body 409|Out-Null
 $body.amount_cents=100;$second=Create $body 200
 if($second.refund_no -eq $first.refund_no){throw 'New request reused old refund'}
 $body.request_id=[guid]::NewGuid().ToString();$body.amount_cents=1;Create $body 409|Out-Null
 $count=Sql "SELECT COUNT(*) FROM user_db.refund_record WHERE payment_order_id=$paymentId;"
 if([int]$count -ne 2){throw 'Failed requests left orphan refunds'}
 $count=Sql "SELECT COUNT(*) FROM user_db.refund_review WHERE refund_record_id IN (SELECT id FROM user_db.refund_record WHERE payment_order_id=$paymentId) AND second_signer IS NULL;"
 if([int]$count -ne 2){throw 'First signatures missing'}
 $count=Sql "SELECT COUNT(*) FROM user_db.event_outbox WHERE JSON_UNQUOTE(JSON_EXTRACT(envelope_json,'$.payload.refund_no')) IN ('$($first.refund_no)','$($second.refund_no)');"
 if([int]$count -ne 0){throw 'Creation prematurely scheduled payment'}
 $detail=(Invoke-RestMethod "http://127.0.0.1:8082/api/v1/admin/orders/$cid" -Headers $headers).data
 if(!$detail.refund_applicant_id){throw 'Authorized order detail hides refund entry'}
 Sql "DELETE rp FROM role_permission rp JOIN permission p ON p.id=rp.permission_id WHERE rp.role_id=$role AND p.code='order.refund.create';"|Out-Null
 Create $body 403|Out-Null
 $detail=(Invoke-RestMethod "http://127.0.0.1:8082/api/v1/admin/orders/$cid" -Headers $headers).data
 if($detail.refund_applicant_id){throw 'Revoked permission remains visible'}
 Write-Output 'PASS: live permissions, terminal-order restriction, idempotent creation, immutable requests, pending refund limits, rollback and first signature without execution.'
}finally {
 if($cid){Sql "DELETE FROM audit_log WHERE target_type='charge_order' AND target_id='$cid' AND action='refund.create'; DELETE FROM user_db.manual_refund_request WHERE JSON_UNQUOTE(JSON_EXTRACT(payload_json,'$.order_id'))='$cid'; DELETE FROM user_db.refund_review WHERE refund_record_id IN (SELECT id FROM user_db.refund_record WHERE biz_type='charge' AND biz_id=$cid); DELETE FROM user_db.refund_record WHERE biz_type='charge' AND biz_id=$cid; DELETE FROM user_db.charge_event_log WHERE charge_order_id=$cid;"|Out-Null}
 Sql "DELETE FROM user_db.charge_order WHERE order_no='$tag'; DELETE FROM user_db.payment_order WHERE order_no='$tag'; DELETE FROM user_db.user WHERE openid='$tag'; DELETE FROM admin_user_role WHERE username='$tag'; DELETE rp FROM role_permission rp JOIN role r ON r.id=rp.role_id WHERE r.name='$tag'; DELETE FROM role WHERE name='$tag';"|Out-Null
}

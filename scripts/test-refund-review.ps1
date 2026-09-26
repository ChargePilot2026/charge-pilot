param([switch]$Reject)
$ErrorActionPreference='Stop'
$taskCompose=Join-Path (Split-Path -Parent $PSScriptRoot) 'compose.dev.yaml'
$tag='review_'+[guid]::NewGuid().ToString('N').Substring(0,12)
function Sql([string]$q){
 $result=$q|docker compose -f $taskCompose exec -T mysql sh -c 'export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"; exec mysql -uroot --batch --skip-column-names admin_db'
 if($LASTEXITCODE -ne 0){throw 'Review fixture SQL failed'};return $result
}
function Approve($headers,[int]$status,[string]$comment='verified refund'){
 $r=Invoke-WebRequest "http://127.0.0.1:8082/api/v1/admin/billing/refunds/$tag/approve" -Headers $headers -Method Post -ContentType application/json -Body (@{approve_comment=$comment}|ConvertTo-Json) -SkipHttpErrorCheck
 if($r.StatusCode -ne $status){throw "Expected $status, got $($r.StatusCode): $($r.Content)"};return ($r.Content|ConvertFrom-Json).data
}
function Prepare([int]$status){
 $r=Invoke-WebRequest 'http://127.0.0.1:8081/api/v1/internal/refund-records/execution' -Headers $serviceHeaders -Method Post -ContentType application/json -Body (@{refund_no=$tag}|ConvertTo-Json) -SkipHttpErrorCheck
 if($r.StatusCode -ne $status){throw "Execution expected $status, got $($r.StatusCode)"}
}
try {
 $token=docker compose -f $taskCompose exec -T user printenv SERVICE_TOKEN
 $serviceHeaders=@{'X-Service-Token'=$token.Trim()}
 $role=Sql "INSERT INTO role(code,name) VALUES ('customer_finance','$tag'); SELECT LAST_INSERT_ID();"
 Sql "INSERT INTO role_permission(role_id,permission_id) SELECT $role,id FROM permission WHERE code='order.refund.review'; INSERT INTO admin_user_role(username,password_hash,role_id,status) SELECT '${tag}_1',password_hash,$role,'active' FROM admin_user_role WHERE username='admin' AND deleted_at IS NULL LIMIT 1; INSERT INTO admin_user_role(username,password_hash,role_id,status) SELECT '${tag}_2',password_hash,$role,'active' FROM admin_user_role WHERE username='admin' AND deleted_at IS NULL LIMIT 1;"|Out-Null
 $headers=@();foreach($i in 1..2){$login=Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:8082/api/v1/admin/auth/login' -ContentType application/json -Body (@{username="${tag}_$i";password='DevAdmin2026!'}|ConvertTo-Json);$headers+=@{Authorization="Bearer $($login.data.token)"}}
 $uid=Sql "INSERT INTO user_db.user(openid) VALUES ('$tag'); SELECT LAST_INSERT_ID();"
 $cid=Sql "INSERT INTO user_db.charge_order(order_no,user_id,device_id,port_no,status,created_month) VALUES ('$tag',$uid,'$tag',1,'failed',DATE_FORMAT(UTC_DATE(),'%Y-%m-01')); SELECT LAST_INSERT_ID();"
 $paymentId=Sql "INSERT INTO user_db.payment_order(order_no,user_id,biz_type,biz_id,pay_method,total_cents,paid_cents,status,wechat_transaction_id,created_month) VALUES ('$tag',$uid,'charge',$cid,'wechat',200,200,'paid','TESTONLY123',DATE_FORMAT(UTC_DATE(),'%Y-%m-01')); SELECT LAST_INSERT_ID();"
 Sql "UPDATE user_db.charge_order SET payment_order_id=$paymentId WHERE id=$cid; INSERT INTO user_db.refund_record(refund_no,payment_order_id,user_id,biz_type,biz_id,refund_cents,reason,status,created_month) VALUES ('$tag',$paymentId,$uid,'charge',$cid,50,'manual review fixture','pending',DATE_FORMAT(UTC_DATE(),'%Y-%m-01'));"|Out-Null
 Prepare 409
 $first=Approve $headers[0] 200
 if($first.review_status -ne 'awaiting_second'){throw 'First approval released refund'}
 $again=Approve $headers[0] 200
 if($again.review_status -ne 'awaiting_second'){throw 'Same actor counted twice'}
 Prepare 409
 Approve $headers[0] 409 'changed opinion'|Out-Null
 if($Reject){
  foreach($attempt in 1..2){
   $r=Invoke-WebRequest "http://127.0.0.1:8082/api/v1/admin/billing/refunds/$tag/reject" -Headers $headers[1] -Method Post -ContentType application/json -Body '{"reason":"verified rejection"}' -SkipHttpErrorCheck
   if($r.StatusCode -ne 200 -or ($r.Content|ConvertFrom-Json).data.review_status -ne 'rejected'){throw "Rejection failed: $($r.Content)"}
  }
  Approve $headers[0] 409|Out-Null
  Approve $headers[1] 409|Out-Null
  Prepare 409
  $count=Sql "SELECT COUNT(*) FROM user_db.refund_rejection WHERE refund_record_id IN (SELECT id FROM user_db.refund_record WHERE refund_no='$tag');"
  if([int]$count -ne 1){throw 'Rejection duplicated receipt'}
  Sql "INSERT INTO user_db.refund_record(refund_no,payment_order_id,user_id,biz_type,biz_id,refund_cents,reason,status,created_month) VALUES ('${tag}_next',$paymentId,$uid,'charge',$cid,200,'replacement after rejection','pending',DATE_FORMAT(UTC_DATE(),'%Y-%m-01'));"|Out-Null
  $r=Invoke-WebRequest "http://127.0.0.1:8082/api/v1/admin/billing/refunds/${tag}_next/approve" -Headers $headers[0] -Method Post -ContentType application/json -Body '{"approve_comment":"replacement verified"}' -SkipHttpErrorCheck
  if($r.StatusCode -ne 200){throw "Rejected amount still reserved: $($r.Content)"}
  $count=Sql "SELECT COUNT(*) FROM user_db.event_outbox WHERE JSON_UNQUOTE(JSON_EXTRACT(envelope_json,'$.payload.refund_no')) IN ('$tag','${tag}_next');"
  if([int]$count -ne 0){throw 'Rejection or first signature released refund'}
  Write-Output 'PASS: idempotent rejection, approval/execution blocked, no execution event and full payment amount available for replacement review.'
  return
 }
 Sql "UPDATE user_db.refund_record SET refund_cents=60 WHERE refund_no='$tag';"|Out-Null
 Approve $headers[1] 409|Out-Null
 Sql "UPDATE user_db.refund_record SET refund_cents=50 WHERE refund_no='$tag'; UPDATE admin_user_role SET status='disabled' WHERE username='${tag}_1';"|Out-Null
 Approve $headers[1] 403|Out-Null
 Sql "UPDATE admin_user_role SET status='active' WHERE username='${tag}_1';"|Out-Null
 $second=Approve $headers[1] 200
 if($second.review_status -ne 'approved' -or $second.first_signer -eq $second.second_signer){throw 'Distinct signers missing'}
 Approve $headers[1] 200|Out-Null
 $count=Sql "SELECT COUNT(*) FROM user_db.event_outbox WHERE JSON_UNQUOTE(JSON_EXTRACT(envelope_json,'$.payload.refund_no'))='$tag';"
 if([int]$count -ne 1){throw 'Approval duplicated execution event'}
 Prepare 200
 Write-Output 'PASS: separate finance approvals, same-actor replay, snapshot binding, revoked first signer, one durable event and execution gate.'
}finally {
 Sql "DELETE FROM audit_log WHERE target_id IN ('$tag','${tag}_next') AND action IN ('refund.reject','refund.approve'); DELETE FROM user_db.refund_rejection WHERE refund_record_id IN (SELECT id FROM user_db.refund_record WHERE refund_no='$tag'); DELETE FROM user_db.refund_review WHERE refund_record_id IN (SELECT id FROM user_db.refund_record WHERE refund_no='${tag}_next'); DELETE FROM user_db.refund_record WHERE refund_no='${tag}_next';"|Out-Null
 Sql "DELETE FROM refund_task WHERE refund_no='$tag'; DELETE FROM audit_log WHERE target_id='$tag' AND action='refund.approve'; DELETE FROM user_db.refund_execution WHERE refund_record_id IN (SELECT id FROM user_db.refund_record WHERE refund_no='$tag'); DELETE FROM user_db.refund_review WHERE refund_record_id IN (SELECT id FROM user_db.refund_record WHERE refund_no='$tag'); DELETE FROM user_db.refund_record WHERE refund_no='$tag'; DELETE FROM user_db.event_outbox WHERE JSON_UNQUOTE(JSON_EXTRACT(envelope_json,'$.payload.refund_no'))='$tag'; DELETE FROM user_db.charge_event_log WHERE charge_order_id IN (SELECT id FROM user_db.charge_order WHERE order_no='$tag'); DELETE FROM user_db.charge_order WHERE order_no='$tag'; DELETE FROM user_db.payment_order WHERE order_no='$tag'; DELETE FROM user_db.user WHERE openid='$tag'; DELETE FROM admin_user_role WHERE username IN ('${tag}_1','${tag}_2'); DELETE rp FROM role_permission rp JOIN role r ON r.id=rp.role_id WHERE r.name='$tag'; DELETE FROM role WHERE name='$tag';"|Out-Null
}

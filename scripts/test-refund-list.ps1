$ErrorActionPreference='Stop'
$taskCompose=Join-Path (Split-Path -Parent $PSScriptRoot) 'compose.dev.yaml'
$tag='refundread_'+[guid]::NewGuid().ToString('N').Substring(0,12)
function Sql([string]$q){
 $result=$q|docker compose -f $taskCompose exec -T mysql sh -c 'export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"; exec mysql -uroot --batch --skip-column-names admin_db'
 if($LASTEXITCODE -ne 0){throw 'Fixture SQL failed'};return $result
}
function Expect([string]$url,[int]$status){
 $r=Invoke-WebRequest $url -Headers $headers -SkipHttpErrorCheck
 if($r.StatusCode -ne $status){throw "Expected $status, got $($r.StatusCode)"}
 return ($r.Content|ConvertFrom-Json).data
}
function Retry([int]$status,[string]$reason='network checked'){
 $r=Invoke-WebRequest ($base+'/'+$tag+'/retry') -Method Post -Headers $headers -ContentType application/json -Body (@{reason=$reason}|ConvertTo-Json) -SkipHttpErrorCheck
 if($r.StatusCode -ne $status){throw "Retry expected $status, got $($r.StatusCode): $($r.Content)"}
 return ($r.Content|ConvertFrom-Json).data
}
$base='http://127.0.0.1:8082/api/v1/admin/billing/refunds'
try {
 Sql "INSERT INTO role(code,name) VALUES ('$tag','Refund reader test'); INSERT INTO admin_user_role(username,password_hash,role_id,status) SELECT '$tag',a.password_hash,r.id,'active' FROM admin_user_role a JOIN role r ON r.code='$tag' WHERE a.username='admin' AND a.deleted_at IS NULL LIMIT 1; INSERT INTO user_db.refund_record(refund_no,payment_order_id,user_id,biz_type,biz_id,refund_cents,status,created_month) VALUES ('$tag',0,0,'charge',0,123,'failed',DATE_FORMAT(UTC_DATE(),'%Y-%m-01'));"|Out-Null
 $login=Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:8082/api/v1/admin/auth/login' -ContentType application/json -Body (@{username=$tag;password='DevAdmin2026!'}|ConvertTo-Json)
 $headers=@{Authorization="Bearer $($login.data.token)"}
 Expect $base 403|Out-Null
 Sql "INSERT INTO role_permission(role_id,permission_id) SELECT r.id,p.id FROM role r JOIN permission p ON p.code='finance.refund.read' WHERE r.code='$tag';"|Out-Null
 $d=Expect ($base+'?refund_no='+$tag+'&status=failed&page_size=1') 200
 if($d.total -ne 1 -or $d.items[0].refund_cents -ne 123 -or $d.items[0].refund_no -ne $tag){throw 'Real refund data missing'}
 $d=Expect ($base+'?refund_no='+$tag+'&page=2&page_size=1') 200
 if($d.total -ne 1 -or $d.items.Count -ne 0){throw 'Pagination failed'}
 $d=Expect ($base+'?refund_no='+$tag+'&status=success') 200
 if($d.total -ne 0){throw 'Status filtering failed'}
 foreach($q in @('?page=0','?page_size=101','?status=bogus','?refund_no=%25')){Expect ($base+$q) 400|Out-Null}
 Retry 403|Out-Null
 Sql "INSERT INTO role_permission(role_id,permission_id) SELECT r.id,p.id FROM role r JOIN permission p ON p.code='finance.refund.retry' WHERE r.code='$tag';"|Out-Null
 Retry 404|Out-Null
 Sql "INSERT INTO refund_task(refund_no,event_id,stage,last_error,scheduled_at,request_json) VALUES ('$tag','$tag','queued','test failure',DATE_ADD(UTC_TIMESTAMP(3),INTERVAL 1 DAY),JSON_OBJECT('test','immutable'));"|Out-Null
 $d=Expect ($base+'?refund_no='+$tag) 200
 if(!$d.items[0].can_retry -or $d.items[0].task.last_error -ne 'test failure'){throw 'Retry eligibility missing'}
 Retry 400 ' '|Out-Null
 $result=Retry 200
 if(!$result.queued -or $result.refund_no -ne $tag){throw 'Task not rescheduled'}
 $count=Sql "SELECT COUNT(*) FROM audit_log WHERE action='refund.retry' AND target_id='$tag' AND JSON_UNQUOTE(JSON_EXTRACT(after_json,'$.reason'))='network checked';"
 if([int]$count -ne 1){throw 'Retry audit missing'}
 $count=Sql "SELECT COUNT(*) FROM refund_task WHERE refund_no='$tag' AND stage='queued' AND JSON_UNQUOTE(JSON_EXTRACT(request_json,'$.test'))='immutable';"
 if([int]$count -ne 1){throw 'Retry changed immutable task snapshot'}
 foreach($stage in @('done','manual_review')){
  Sql "UPDATE refund_task SET stage='$stage' WHERE refund_no='$tag';"|Out-Null
  Retry 409|Out-Null
 }
 Sql "DELETE rp FROM role_permission rp JOIN role r ON r.id=rp.role_id JOIN permission p ON p.id=rp.permission_id WHERE r.code='$tag' AND p.code='finance.refund.retry';"|Out-Null
 Retry 403|Out-Null
 Sql "UPDATE user_db.refund_record SET deleted_at=UTC_TIMESTAMP() WHERE refund_no='$tag';"|Out-Null
 $d=Expect ($base+'?refund_no='+$tag) 200
 if($d.total -ne 0){throw 'Deleted refund leaked'}
 Sql "DELETE rp FROM role_permission rp JOIN role r ON r.id=rp.role_id WHERE r.code='$tag';"|Out-Null
 Expect $base 403|Out-Null
 Write-Output 'PASS: refund list, live grant/revoke, retry scheduling, immutable snapshots, audit and terminal-state rejection.'
}finally {
 Sql "DELETE FROM refund_task WHERE refund_no='$tag'; DELETE FROM audit_log WHERE target_id='$tag' AND action='refund.retry'; DELETE FROM user_db.refund_record WHERE refund_no='$tag'; DELETE FROM admin_user_role WHERE username='$tag'; DELETE rp FROM role_permission rp JOIN role r ON r.id=rp.role_id WHERE r.code='$tag'; DELETE FROM role WHERE code='$tag';"|Out-Null
}

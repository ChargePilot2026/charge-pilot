$ErrorActionPreference='Stop'
$taskCompose=Join-Path (Split-Path -Parent $PSScriptRoot) 'compose.dev.yaml'
$tag='risk_'+[guid]::NewGuid().ToString('N').Substring(0,12)
$requestId=[guid]::NewGuid().ToString()
function Sql([string]$q){$result=$q|docker compose -f $taskCompose exec -T mysql sh -c 'export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"; exec mysql -uroot --batch --skip-column-names admin_db';if($LASTEXITCODE -ne 0){throw 'Risk fixture SQL failed'};return $result}
function Request([string]$method,[string]$path,$body,[int]$status){
 $args=@{Uri="http://127.0.0.1:8082/api/v1/admin/billing/wallet-risks$path";Method=$method;Headers=$headers;SkipHttpErrorCheck=$true}
 if($body){$args.ContentType='application/json';$args.Body=$body|ConvertTo-Json}
 $r=Invoke-WebRequest @args;if($r.StatusCode -ne $status){throw "Expected $status, got $($r.StatusCode): $($r.Content)"};if($status -eq 200){return ($r.Content|ConvertFrom-Json).data}
}
try{
 $role=Sql "INSERT INTO role(code,name) VALUES ('customer_cs','$tag');SELECT LAST_INSERT_ID();"
 Sql "INSERT INTO admin_user_role(username,password_hash,role_id,status) SELECT '$tag',password_hash,$role,'active' FROM admin_user_role WHERE username='admin' AND deleted_at IS NULL LIMIT 1;"|Out-Null
 $login=Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:8082/api/v1/admin/auth/login' -ContentType application/json -Body (@{username=$tag;password='DevAdmin2026!'}|ConvertTo-Json)
 $headers=@{Authorization="Bearer $($login.data.token)"}
 $uid=Sql "INSERT INTO user_db.user(openid) VALUES ('$tag');SELECT LAST_INSERT_ID();"
 Sql "INSERT INTO user_db.wallet_refund_request(request_id,user_id,amount_cents,reason,response_json) VALUES('$requestId',$uid,100,'fixture',JSON_OBJECT('status','manual_review'));"|Out-Null
 Request GET '' $null 403|Out-Null
 Sql "INSERT INTO role_permission(role_id,permission_id) SELECT $role,id FROM permission WHERE code='finance.wallet_risk.review';"|Out-Null
 $list=Request GET '' $null 200
 if(!($list.items|Where-Object request_id -eq $requestId)){throw 'Pending request missing'}
 Request GET '?page=0' $null 400|Out-Null
 Request POST "/$requestId/review" @{approved=$false;comment=''} 400|Out-Null
 Request POST "/$requestId/review" @{approved=$false;comment='checked';actor_id=1} 422|Out-Null
 $body=@{approved=$false;comment='risk fixture rejected'}
 $first=Request POST "/$requestId/review" $body 200
 $again=Request POST "/$requestId/review" $body 200
 if($first.status -ne 'rejected' -or $again.review.actor_id -ne $first.review.actor_id){throw 'Decision replay changed result'}
 Request POST "/$requestId/review" @{approved=$true;comment='changed decision'} 409|Out-Null
 $list=Request GET '' $null 200
 if($list.items|Where-Object request_id -eq $requestId){throw 'Reviewed request remains pending'}
 $count=Sql "SELECT COUNT(*) FROM user_db.wallet_risk_review WHERE request_id='$requestId';";if([int]$count -ne 1){throw 'Duplicate review'}
 Sql "DELETE FROM role_permission WHERE role_id=$role;"|Out-Null
 Request GET '' $null 403|Out-Null
 Request POST "/$requestId/review" $body 403|Out-Null
 Write-Output 'PASS: live customer-service grants, queue, pagination validation, actor spoof rejection, required comment, idempotent rejection, final decision and revocation.'
}finally{
 Sql "DELETE FROM audit_log WHERE target_type='wallet_refund_request' AND target_id='$requestId';DELETE FROM user_db.wallet_risk_review WHERE request_id='$requestId';DELETE FROM user_db.wallet_refund_request WHERE request_id='$requestId';DELETE FROM user_db.user WHERE openid='$tag';DELETE FROM admin_user_role WHERE username='$tag';DELETE rp FROM role_permission rp JOIN role r ON r.id=rp.role_id WHERE r.name='$tag';DELETE FROM role WHERE name='$tag';"|Out-Null
}

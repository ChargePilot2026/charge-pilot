param(
    [string]$BaseUrl = 'http://127.0.0.1:5173',
    [string]$AdminUsername = 'admin',
    [string]$AdminPassword = 'DevAdmin2026!'
)
$ErrorActionPreference = 'Stop'
$taskRoot = Split-Path -Parent $PSScriptRoot
$taskCompose = Join-Path $taskRoot 'compose.dev.yaml'
$taskTag = 'test_orders_' + [guid]::NewGuid().ToString('N').Substring(0, 12)
$taskReader = $taskTag + '_reader'
$taskUsernameSql = $AdminUsername.Replace("'", "''")
function Invoke-TestSql([string]$Sql) {
    $result = $Sql | docker compose -f $taskCompose exec -T mysql sh -c 'export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"; exec mysql -uroot --batch --skip-column-names --default-character-set=utf8mb4'
    if ($LASTEXITCODE -ne 0) { throw 'Fixture SQL failed' }
    return $result
}
function Assert-That([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw $Message }
}
function Get-Api([string]$Path, [string]$Token) {
    Invoke-RestMethod -Uri ($BaseUrl + $Path) -Headers @{Authorization="Bearer $Token"}
}
function Assert-Status([string]$Path, [string]$Token, [int]$Expected) {
    $actual = 200
    try { Get-Api $Path $Token | Out-Null }
    catch {
        if ($null -eq $_.Exception.Response) { throw }
        $actual = [int]$_.Exception.Response.StatusCode
    }
    Assert-That ($actual -eq $Expected) "$Path returned $actual, expected $Expected"
}
function New-TestUserToken([long]$UserId) {
    if (-not $script:authTestRows) { $script:authTestRows=[Collections.Generic.List[string]]::new(); $script:authTestKeys=[Collections.Generic.List[string]]::new() }
    $authTag='auth_test_'+[guid]::NewGuid().ToString('N')
    $fixtureSql="INSERT IGNORE INTO user_db.user (id,openid) VALUES ($UserId,'$authTag'); SELECT openid FROM user_db.user WHERE id=$UserId;"
    $authOpenid=$fixtureSql | docker compose -f $taskCompose exec -T mysql sh -c 'export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"; exec mysql -uroot --batch --skip-column-names'
    if($LASTEXITCODE -ne 0){throw 'Authentication fixture failed'}
    $authOpenid=($authOpenid | Select-Object -Last 1)
    $script:authTestRows.Add($authTag)
    $sid=[guid]::NewGuid().ToString()
    $authKey="auth:user:session:$sid"; $script:authTestKeys.Add($authKey)
    docker compose -f $taskCompose exec -T redis-cache redis-cli SET $authKey 'test-session' EX 300 | Out-Null
    $secret = docker compose -f $taskCompose exec -T user printenv JWT_SECRET
    $issuer = docker compose -f $taskCompose exec -T user printenv JWT_ISSUER
    if (-not $issuer) { $issuer = 'chargepilot' }
    function Encode([byte[]]$bytes) { [Convert]::ToBase64String($bytes).TrimEnd('=').Replace('+','-').Replace('/','_') }
    $now = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds()
    $header = Encode ([Text.Encoding]::UTF8.GetBytes('{"alg":"HS256","typ":"JWT"}'))
    $payload = @{sub=$authOpenid;sid=$sid;user_id=$UserId;role='user';iat=$now;exp=$now+300;iss=$issuer.Trim()} | ConvertTo-Json -Compress
    $unsigned = $header + '.' + (Encode ([Text.Encoding]::UTF8.GetBytes($payload)))
    $hmac = [Security.Cryptography.HMACSHA256]::new([Text.Encoding]::UTF8.GetBytes($secret.Trim()))
    try { return $unsigned + '.' + (Encode ($hmac.ComputeHash([Text.Encoding]::UTF8.GetBytes($unsigned)))) } finally { $hmac.Dispose() }
}
try {
    $loginBody = @{username=$AdminUsername;password=$AdminPassword} | ConvertTo-Json
    $login = Invoke-RestMethod -Method Post -Uri ($BaseUrl + '/api/v1/admin/auth/login') -ContentType application/json -Body $loginBody
    $token = $login.data.token
    Assert-That (-not [string]::IsNullOrEmpty($token)) 'Admin login failed'
    $ids = Invoke-TestSql @"
INSERT INTO admin_db.station (code,name,longitude,latitude) VALUES ('$taskTag','订单集成测试站点',116.4,39.9);
SET @station=LAST_INSERT_ID();
INSERT INTO admin_db.device_meta (device_id,station_id) VALUES ('$taskTag',@station);
INSERT INTO user_db.charge_order (order_no,user_id,device_id,port_no,status,started_at,ended_at,charged_seconds,charged_kwh,electric_cents,service_cents,total_cents,created_month)
VALUES ('${taskTag}_done',123,'$taskTag',1,'completed',UTC_TIMESTAMP()-INTERVAL 2 HOUR,UTC_TIMESTAMP()-INTERVAL 1 HOUR,3600,0.5200,29,26,55,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'));
SET @order=LAST_INSERT_ID();
INSERT INTO user_db.payment_order (order_no,biz_type,biz_id,user_id,pay_method,total_cents,paid_cents,refunded_cents,status,created_month)
VALUES ('${taskTag}_pay','charge',@order,123,'wechat',100,100,45,'partial_refunded',DATE_FORMAT(UTC_DATE(),'%Y-%m-01'));
SET @payment=LAST_INSERT_ID();
UPDATE user_db.charge_order SET payment_order_id=@payment WHERE id=@order;
INSERT INTO user_db.charge_order (order_no,user_id,device_id,port_no,status,started_at,created_month)
VALUES ('${taskTag}_active',123,'$taskTag',2,'charging',UTC_TIMESTAMP()-INTERVAL 30 MINUTE,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'));
INSERT INTO user_db.charge_order (order_no,user_id,device_id,port_no,status,deleted_at,created_month)
VALUES ('${taskTag}_deleted',123,'$taskTag',3,'failed',UTC_TIMESTAMP(),DATE_FORMAT(UTC_DATE(),'%Y-%m-01'));
SET @deleted=LAST_INSERT_ID();
INSERT INTO admin_db.admin_user_role (username,password_hash,status)
SELECT '$taskReader',password_hash,'active' FROM admin_db.admin_user_role WHERE username='$taskUsernameSql' AND deleted_at IS NULL LIMIT 1;
SELECT @station,@order,@deleted;
"@
    $parts = ($ids | Select-Object -Last 1) -split "`t"
    $stationId, $orderId, $deletedId = $parts
    Invoke-TestSql @"
INSERT INTO user_db.charge_event_log (charge_order_id,event_id,event,actor,detail,occurred_at)
VALUES ($orderId,'${taskTag}_end','ended','device','充电结束',UTC_TIMESTAMP()),
($orderId,'${taskTag}_create','created','user:123','创建订单',UTC_TIMESTAMP()-INTERVAL 2 HOUR);
"@ | Out-Null
    $events = (Get-Api "/api/v1/admin/orders/$orderId/timeline" $token).data
    Assert-That ($events.timeline.Count -eq 2 -and $events.timeline[0].event -eq 'created' -and $events.timeline[1].event -eq 'ended') 'Timeline chronological ordering failed'
    Assert-Status "/api/v1/admin/orders/$deletedId/timeline" $token 404
    $list = (Get-Api "/api/v1/admin/orders?device_id=$taskTag&page=1&page_size=1" $token).data
    Assert-That ($list.total -eq 2 -and $list.items.Count -eq 1) 'Real pagination/soft-delete filtering failed'
    $next = (Get-Api "/api/v1/admin/orders?device_id=$taskTag&page=2&page_size=1" $token).data
    Assert-That ($next.items.Count -eq 1 -and $next.items[0].order_id -ne $list.items[0].order_id) 'Page 2 repeated page 1'
    $done = (Get-Api "/api/v1/admin/orders?device_id=$taskTag&status=finished" $token).data
    Assert-That ($done.total -eq 1 -and $done.items[0].total_fee_cents -eq 55) 'Status/fee mapping failed'
    Assert-That ($done.items[0].electric_fee_cents -eq 29 -and $done.items[0].service_fee_cents -eq 26) 'Price separation failed'
    Assert-That ($done.items[0].station_name -eq '订单集成测试站点') 'Station enrichment failed'
    $station = (Get-Api "/api/v1/admin/orders?station_id=$stationId" $token).data
    Assert-That ($station.total -eq 2) 'Station filter failed'
    $detail = (Get-Api "/api/v1/admin/orders/$orderId" $token).data
    Assert-That ($null -eq $detail.billing.calculation_no -and $detail.billing.settlements.Count -eq 0) 'Unsettled order must not invent billing records'
    Invoke-TestSql @"
INSERT INTO billing_db.fee_calculation (calculation_no,order_no,charge_order_id,user_id,charged_kwh,charged_seconds,electric_cents,service_cents,total_cents,created_month)
VALUES ('$taskTag','${taskTag}_done',$orderId,123,0.52,3600,30,27,57,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'));
SET @fee=LAST_INSERT_ID();
INSERT INTO billing_db.settlement (settlement_no,split_template_id,mode,fee_calculation_id,order_no,total_cents,split_pool_cents,status,created_month)
VALUES ('$taskTag',1,'mode_b',@fee,'${taskTag}_done',57,27,'confirmed',DATE_FORMAT(UTC_DATE(),'%Y-%m-01'));
SET @settlement=LAST_INSERT_ID();
INSERT INTO billing_db.settlement_party_amount (settlement_id,party_id,party_code,party_name,ratio_bp,amount_cents,status)
VALUES (@settlement,1,'platform','平台',6000,16,'paid'),(@settlement,2,'operator','运营商',4000,11,'pending');
"@ | Out-Null
    $detail = (Get-Api "/api/v1/admin/orders/$orderId" $token).data
    Assert-That ($detail.total_fee_cents -eq 57 -and $detail.electric_fee_cents -eq 30 -and $detail.service_fee_cents -eq 27) 'Billing must override stale lifecycle fee snapshots'
    $split = $detail.billing.settlements[0]
    Assert-That ($split.mode -eq 'mode_b' -and $split.split_pool_cents -eq 27 -and $split.status -eq 'confirmed') 'Settlement header failed'
    Assert-That ($split.parties.Count -eq 2 -and $split.parties[0].ratio_bp -eq 6000 -and $split.parties[0].amount_cents -eq 16 -and $split.parties[1].amount_cents -eq 11) 'Settlement allocations failed'
    Assert-That ($detail.order_id -eq $orderId -and $detail.payment_order_no -eq "${taskTag}_pay") 'Order detail path/payment link failed'
    Assert-That ($detail.paid_cents -eq 100 -and $detail.refunded_cents -eq 45 -and $detail.refund_status -eq 'partial_refunded') 'Payment/refund details failed'
    $userToken = New-TestUserToken 123
    $otherToken = New-TestUserToken 987654321
    $userHeaders = @{Authorization="Bearer $userToken"}
    $snapshotBase='http://127.0.0.1:8081/api/v1/user/charge/ongoing/snapshot?order_id='
    $snapshotKeys=@("snapshot:${taskTag}_active","snapshot:${taskTag}_done","snapshot:${taskTag}_deleted")
    $fakeSnapshot='{"order_id":"wrong-order","charge_state":"charging","poll_continue":true,"current_power_w":123.5,"user_id":987654321,"private_field":"must-not-leak"}'
    foreach($key in $snapshotKeys){$fakeSnapshot | docker compose -f $taskCompose exec -T redis-cache redis-cli -x SET $key | Out-Null}
    $snap=(Invoke-RestMethod -Uri ($snapshotBase+"${taskTag}_active") -Headers $userHeaders).data
    Assert-That ($snap.order_no -eq "${taskTag}_active" -and $snap.current_power_w -eq 123.5 -and $snap.telemetry_available -and $null -eq $snap.private_field -and $null -eq $snap.user_id) 'Snapshot identity or private cache fields leaked'
    $numericSnap=(Invoke-RestMethod -Uri ($snapshotBase+$snap.order_id) -Headers $userHeaders).data
    Assert-That ($numericSnap.order_no -eq $snap.order_no) 'Numeric snapshot ID did not resolve canonical cache key'
    $snap=(Invoke-RestMethod -Uri ($snapshotBase+"${taskTag}_done") -Headers $userHeaders).data
    Assert-That ($snap.status -eq 'completed' -and !$snap.poll_continue -and $snap.next_poll_after_ms -eq 0 -and $null -eq $snap.current_power_w -and $snap.elapsed_seconds -eq 3600) 'Stale cache overrode terminal database state'
    foreach($case in @(@{order="${taskTag}_active";token=$otherToken},@{order="${taskTag}_deleted";token=$userToken})){
        $status=200
        try{Invoke-RestMethod -Uri ($snapshotBase+$case.order) -Headers @{Authorization="Bearer $($case.token)"}|Out-Null}catch{$status=[int]$_.Exception.Response.StatusCode}
        Assert-That ($status -eq 404) 'Cached snapshot bypassed ownership or soft-delete check'
    }
    Invoke-TestSql "UPDATE user_db.charge_order SET status='paid' WHERE order_no='${taskTag}_active';"|Out-Null
    $snap=(Invoke-RestMethod -Uri ($snapshotBase+"${taskTag}_active") -Headers $userHeaders).data
    Assert-That ($snap.charge_state -eq 'paid' -and $snap.poll_continue -and !$snap.telemetry_available) 'Paid order falsely reported charging'
    Invoke-TestSql "UPDATE user_db.charge_order SET status='charging' WHERE order_no='${taskTag}_active';"|Out-Null
    docker compose -f $taskCompose exec -T redis-cache redis-cli DEL "snapshot:${taskTag}_active"|Out-Null
    $snap=(Invoke-RestMethod -Uri ($snapshotBase+"${taskTag}_active") -Headers $userHeaders).data
    Assert-That (!$snap.telemetry_available -and $null -eq $snap.current_power_w -and $snap.status -eq 'charging') 'Cache miss fabricated measurements'
    foreach($case in @(@{order="${taskTag}_active";token=$otherToken;expected=404},@{order="${taskTag}_deleted";token=$userToken;expected=404},@{order="${taskTag}_done";token=$userToken;expected=409})){
        $status=200
        try{Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:8081/api/v1/user/charge/stop' -Headers @{Authorization="Bearer $($case.token)"} -ContentType application/json -Body (@{order_no=$case.order}|ConvertTo-Json)|Out-Null}catch{$status=[int]$_.Exception.Response.StatusCode}
        Assert-That ($status -eq $case.expected) 'Invalid stop request reached gateway'
    }
    $nearby = (Invoke-RestMethod -Uri 'http://127.0.0.1:8081/api/v1/user/station/nearby?lat=39.9&lng=116.4&radius_km=1' -Headers $userHeaders).data
    Assert-That (($nearby.items | Where-Object { $_.id -eq $stationId }).Count -eq 1) 'User coordinates were not forwarded or decimal coordinates lost'
    $stationDetail = (Invoke-RestMethod -Uri "http://127.0.0.1:8081/api/v1/user/station/$stationId" -Headers $userHeaders).data
    Assert-That ($stationDetail.longitude -eq 116.4 -and $stationDetail.latitude -eq 39.9) 'Station detail swapped coordinates'
    $dateLineIds = Invoke-TestSql @"
INSERT INTO admin_db.station (code,name,longitude,latitude) VALUES ('${taskTag}_east','跨日界线东',179.99,0);
SET @east=LAST_INSERT_ID();
INSERT INTO admin_db.station (code,name,longitude,latitude) VALUES ('${taskTag}_west','跨日界线西',-179.99,0);
SET @west=LAST_INSERT_ID();
INSERT INTO admin_db.station (code,name,longitude,latitude,status) VALUES ('${taskTag}_closed','未开放',179.99,0,'disabled');
SET @closed=LAST_INSERT_ID();
SELECT @east,@west,@closed;
"@
    $east,$west,$closed = ($dateLineIds | Select-Object -Last 1) -split "`t"
    $nearby = (Invoke-RestMethod -Uri 'http://127.0.0.1:8081/api/v1/user/station/nearby?lat=0&lng=179.99&radius_km=5' -Headers $userHeaders).data
    Assert-That (($nearby.items | Where-Object { $_.id -eq $east -or $_.id -eq $west }).Count -eq 2) 'Spherical distance failed across date line'
    Assert-That (($nearby.items | Where-Object { $_.id -eq $closed }).Count -eq 0) 'Disabled station leaked into nearby results'
    for ($index=1;$index -lt $nearby.items.Count;$index++) { Assert-That ($nearby.items[$index-1].distance_km -le $nearby.items[$index].distance_km) 'Nearby stations are not distance sorted' }
    $userDetail = (Invoke-RestMethod -Uri "http://127.0.0.1:8081/api/v1/user/charge/$orderId" -Headers $userHeaders).data
    Assert-That ($userDetail.order_id -eq $orderId -and $userDetail.total_fee_cents -eq 57 -and $userDetail.paid_fee_cents -eq 100) 'User order detail/authoritative billing failed'
    Assert-That ($null -eq $userDetail.billing -and $null -eq $userDetail.settlements) 'Operator settlement details leaked to user'
    $history = (Invoke-RestMethod -Uri 'http://127.0.0.1:8081/api/v1/user/charge/history?status=finished&page_size=100' -Headers $userHeaders).data
    Assert-That ($history.total -ge 1 -and ($history.items | Where-Object { $_.order_id -eq $orderId }).Count -eq 1) 'User history/status filter failed'
    foreach ($case in @(@{id=$orderId;token=$otherToken},@{id=$deletedId;token=$userToken})) {
        $status=200
        try { Invoke-RestMethod -Uri "http://127.0.0.1:8081/api/v1/user/charge/$($case.id)" -Headers @{Authorization="Bearer $($case.token)"} | Out-Null }
        catch { if ($null -eq $_.Exception.Response) { throw }; $status=[int]$_.Exception.Response.StatusCode }
        Assert-That ($status -eq 404) 'Foreign/soft-deleted user order exposed'
    }
    Assert-Status "/api/v1/admin/orders/$deletedId" $token 404
    Assert-Status '/api/v1/admin/orders?page=0' $token 400
    Assert-Status '/api/v1/admin/orders?page_size=101' $token 400
    Assert-Status '/api/v1/admin/orders?status=unknown' $token 400
    Assert-Status '/api/v1/admin/orders?started_from=2026-09-26T12:00:00Z&started_to=2026-09-25T12:00:00Z' $token 400
    $readerBody = @{username=$taskReader;password=$AdminPassword} | ConvertTo-Json
    $readerLogin = Invoke-RestMethod -Method Post -Uri ($BaseUrl + '/api/v1/admin/auth/login') -ContentType application/json -Body $readerBody
    Assert-Status '/api/v1/admin/orders' $readerLogin.data.token 403
    Assert-Status "/api/v1/admin/orders/$orderId" $readerLogin.data.token 403
    Assert-Status "/api/v1/admin/orders/$orderId/timeline" $readerLogin.data.token 403
    $failedId = Invoke-TestSql @"
INSERT INTO user_db.charge_order (order_no,user_id,device_id,port_no,status,created_month)
VALUES ('${taskTag}_ack',123,'$taskTag',4,'paid',DATE_FORMAT(UTC_DATE(),'%Y-%m-01'));
SELECT LAST_INSERT_ID();
"@
    $failedId = $failedId | Select-Object -Last 1
    $serviceToken = docker compose -f $taskCompose exec -T user printenv SERVICE_TOKEN
    $ackBody = @{order_no="${taskTag}_ack";success=$false;error='测试启动失败'} | ConvertTo-Json
    $ackUrl = "http://127.0.0.1:8081/api/v1/internal/charge-orders/${taskTag}_ack/start-result"
    1..2 | ForEach-Object { Invoke-RestMethod -Method Post -Uri $ackUrl -Headers @{'X-Service-Token'=$serviceToken.Trim()} -ContentType application/json -Body $ackBody | Out-Null }
    $events = (Get-Api "/api/v1/admin/orders/$failedId/timeline" $token).data
    Assert-That ($events.timeline.Count -eq 1 -and $events.timeline[0].event -eq 'start_failed') 'Repeated start result must produce one persisted event'
    $conflictingAck = @{order_no="${taskTag}_ack";success=$true} | ConvertTo-Json
    $ackStatus = 200
    try { Invoke-RestMethod -Method Post -Uri $ackUrl -Headers @{'X-Service-Token'=$serviceToken.Trim()} -ContentType application/json -Body $conflictingAck | Out-Null }
    catch { if ($null -eq $_.Exception.Response) { throw }; $ackStatus = [int]$_.Exception.Response.StatusCode }
    Assert-That ($ackStatus -eq 409) 'Terminal order must reject conflicting start result'
    $failed = (Get-Api "/api/v1/admin/orders/$failedId" $token).data
    Assert-That ($failed.status -eq 'failed') 'Rejected callback changed terminal state'
    Invoke-TestSql @"
INSERT INTO user_db.charge_order (order_no,user_id,device_id,port_no,port_code,status,created_month)
VALUES ('${taskTag}_cancel',123,'$taskTag',7,'${taskTag}:7','pending_payment',DATE_FORMAT(UTC_DATE(),'%Y-%m-01'));
SET @cancel_order=LAST_INSERT_ID();
INSERT INTO user_db.payment_order (order_no,biz_type,biz_id,user_id,pay_method,total_cents,status,created_month)
VALUES ('${taskTag}_cancelpay','charge',@cancel_order,123,'wechat',1234,'initiated',DATE_FORMAT(UTC_DATE(),'%Y-%m-01'));
UPDATE user_db.charge_order SET payment_order_id=LAST_INSERT_ID() WHERE id=@cancel_order;
"@ | Out-Null
    $cancelKey = "charge:hold:port_${taskTag}:7"
    docker compose -f $taskCompose exec -T redis-cache redis-cli SET $cancelKey "123:${taskTag}_cancel" EX 300 | Out-Null
    $cancelBody = @{order_no="${taskTag}_cancel"} | ConvertTo-Json
    $cancelUrl = 'http://127.0.0.1:8081/api/v1/user/scan/cancel'
    $cancelled = Invoke-RestMethod -Uri $cancelUrl -Method Post -Headers $userHeaders -ContentType application/json -Body $cancelBody
    Assert-That ($cancelled.data.cancelled) 'Pending order cancellation failed'
    $remainingHold = docker compose -f $taskCompose exec -T redis-cache redis-cli EXISTS $cancelKey
    Assert-That ($remainingHold -eq '0') 'Cancellation did not release own reservation'
    docker compose -f $taskCompose exec -T redis-cache redis-cli SET $cancelKey '456:new-order' EX 300 | Out-Null
    $repeated = Invoke-RestMethod -Uri $cancelUrl -Method Post -Headers $userHeaders -ContentType application/json -Body $cancelBody
    $newHolder = docker compose -f $taskCompose exec -T redis-cache redis-cli GET $cancelKey
    Assert-That ($repeated.data.cancelled -and $newHolder -eq '456:new-order') 'Repeated cancellation deleted another order reservation'
    $cancelEvents = Invoke-TestSql "SELECT COUNT(*) FROM user_db.charge_event_log e JOIN user_db.charge_order o ON o.id=e.charge_order_id WHERE o.order_no='${taskTag}_cancel' AND e.event='cancelled';"
    Assert-That ($cancelEvents -eq '1') 'Repeated cancellation duplicated lifecycle event'
    Write-Output 'PASS: pagination, filters, billing, splits, payment/refund, validation, permissions, timeline ordering and idempotent start failure.'
} finally {
    foreach($key in $snapshotKeys){docker compose -f $taskCompose exec -T redis-cache redis-cli DEL $key|Out-Null}
    foreach($authKey in $script:authTestKeys){docker compose -f $taskCompose exec -T redis-cache redis-cli DEL $authKey | Out-Null}
    foreach($authTag in $script:authTestRows){
        "DELETE FROM user_db.user WHERE openid='$authTag';" | docker compose -f $taskCompose exec -T mysql sh -c 'export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"; exec mysql -uroot' | Out-Null
    }
    if ($cancelKey) { docker compose -f $taskCompose exec -T redis-cache redis-cli DEL $cancelKey | Out-Null }
    Invoke-TestSql @"
USE billing_db;
DELETE e FROM user_db.charge_event_log e JOIN user_db.charge_order o ON o.id=e.charge_order_id WHERE o.device_id='$taskTag';
DELETE FROM user_db.payment_order WHERE order_no IN ('${taskTag}_pay','${taskTag}_cancelpay');
DELETE p FROM billing_db.settlement_party_amount p JOIN billing_db.settlement s ON s.id=p.settlement_id WHERE s.settlement_no='$taskTag';
DELETE FROM billing_db.settlement WHERE settlement_no='$taskTag';
DELETE FROM billing_db.fee_calculation WHERE calculation_no='$taskTag';
DELETE FROM user_db.charge_order WHERE device_id='$taskTag';
DELETE FROM admin_db.device_meta WHERE device_id='$taskTag';
DELETE FROM admin_db.station WHERE code IN ('$taskTag','${taskTag}_east','${taskTag}_west','${taskTag}_closed');
DELETE FROM admin_db.admin_user_role WHERE username='$taskReader';
"@ | Out-Null
}

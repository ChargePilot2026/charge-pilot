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
    Assert-That ($detail.order_id -eq $orderId -and $detail.payment_order_no -eq "${taskTag}_pay") 'Order detail path/payment link failed'
    Assert-That ($detail.paid_cents -eq 100 -and $detail.refunded_cents -eq 45 -and $detail.refund_status -eq 'partial_refunded') 'Payment/refund details failed'
    Assert-Status "/api/v1/admin/orders/$deletedId" $token 404
    Assert-Status '/api/v1/admin/orders?page=0' $token 400
    Assert-Status '/api/v1/admin/orders?page_size=101' $token 400
    Assert-Status '/api/v1/admin/orders?status=unknown' $token 400
    Assert-Status '/api/v1/admin/orders?started_from=2026-09-26T12:00:00Z&started_to=2026-09-25T12:00:00Z' $token 400
    $readerBody = @{username=$taskReader;password=$AdminPassword} | ConvertTo-Json
    $readerLogin = Invoke-RestMethod -Method Post -Uri ($BaseUrl + '/api/v1/admin/auth/login') -ContentType application/json -Body $readerBody
    Assert-Status '/api/v1/admin/orders' $readerLogin.data.token 403
    Assert-Status "/api/v1/admin/orders/$orderId" $readerLogin.data.token 403
    Write-Output 'PASS: real order pagination, status/station filters, detail, separated fees, payment/refund fields, soft deletion, validation and permissions.'
} finally {
    Invoke-TestSql @"
DELETE FROM user_db.payment_order WHERE order_no='${taskTag}_pay';
DELETE FROM user_db.charge_order WHERE device_id='$taskTag';
DELETE FROM admin_db.device_meta WHERE device_id='$taskTag';
DELETE FROM admin_db.station WHERE code='$taskTag';
DELETE FROM admin_db.admin_user_role WHERE username='$taskReader';
"@ | Out-Null
}

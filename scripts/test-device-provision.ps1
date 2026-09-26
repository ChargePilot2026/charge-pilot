param([string]$BaseUrl = 'http://127.0.0.1:8083')
$ErrorActionPreference = 'Stop'
$taskCompose = Join-Path (Split-Path -Parent $PSScriptRoot) 'compose.dev.yaml'
$tag = 'test_dev_' + [guid]::NewGuid().ToString('N').Substring(0,12)
function Sql([string]$query) {
    $result = $query | docker compose -f $taskCompose exec -T mysql sh -c 'export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"; exec mysql -uroot --batch --skip-column-names --default-character-set=utf8mb4 gateway_db'
    if ($LASTEXITCODE -ne 0) { throw 'Test SQL failed' }
    return $result
}
function Assert([bool]$value, [string]$message) { if (-not $value) { throw $message } }
$serviceToken = docker compose -f $taskCompose exec -T gateway printenv SERVICE_TOKEN
$headers = @{'X-Service-Token'=$serviceToken.Trim()}
function Post([string]$path, $body) {
    Invoke-RestMethod -Uri ($BaseUrl+$path) -Method Post -Headers $headers -ContentType application/json -Body ($body | ConvertTo-Json -Depth 8)
}
function Expect([int]$expected, $body) {
    $status = 200
    try { Post '/api/v1/internal/devices/provision' $body | Out-Null }
    catch { if ($null -eq $_.Exception.Response) { throw }; $status=[int]$_.Exception.Response.StatusCode }
    Assert ($status -eq $expected) "Expected HTTP $expected, got $status"
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
    $vendor = Sql "INSERT INTO vendor (vendor_code,vendor_name,adapter_class) VALUES ('$tag','测试厂商','test'); SELECT LAST_INSERT_ID();"
    $vendor = [long]($vendor | Select-Object -Last 1)
    $device = @{device_id=$tag;vendor_id=$vendor;station_id=1;port_count=3;model='测试型号'}
    $body = @{devices=@($device)}
    $first = (Post '/api/v1/internal/devices/provision' $body).data.items[0]
    Assert ($first.created -and $first.ports.Count -eq 3) 'Device and ports not created'
    $again = (Post '/api/v1/internal/devices/provision' $body).data.items[0]
    Assert (-not $again.created -and $again.ports[0].port_id -eq $first.ports[0].port_id) 'Replay created duplicate ports'
    $port = (Post '/api/v1/internal/scan/resolve' @{code=$first.ports[1].port_code}).data
    Assert ($port.kind -eq 'port' -and $port.port_no -eq 2 -and $port.device_id -eq $tag) 'Imported port cannot be resolved by scan'
    Assert ($port.port_id -eq $first.ports[1].port_code) 'Scan exposed a database id instead of the printed port code'
    $detail = (Post '/api/v1/internal/scan/port' @{port_id=$port.port_id}).data
    Assert ($detail.port_no -eq 2 -and -not $detail.PSObject.Properties['current_order_id']) 'Port read leaked another customer order'
    $deviceScan = (Post '/api/v1/internal/scan/resolve' @{code=$tag}).data
    Assert ($deviceScan.ports.Count -eq 3 -and $deviceScan.ports[0].port_no -eq 1) 'Device scan ports missing or unordered'
    $userToken = New-TestUserToken 123
    $userScan = Invoke-RestMethod -Uri 'http://127.0.0.1:8081/api/v1/user/scan/resolve' -Method Post -Headers @{Authorization="Bearer $userToken"} -ContentType application/json -Body (@{code=$port.port_id}|ConvertTo-Json)
    Assert ($userScan.code -eq 0 -and $userScan.data.port_id -eq $port.port_id -and -not $userScan.data.PSObject.Properties['data']) 'User scan double-wrapped gateway envelope'
    $missingStatus = 200
    try { Invoke-RestMethod -Uri 'http://127.0.0.1:8081/api/v1/user/scan/port' -Method Post -Headers @{Authorization="Bearer $userToken"} -ContentType application/json -Body (@{port_id="${tag}_missing";estimated_kwh="0.500";estimated_minutes=120}|ConvertTo-Json) | Out-Null }
    catch { $missingStatus = [int]$_.Exception.Response.StatusCode }
    Assert ($missingStatus -eq 404) 'User scan did not preserve missing-port status'
    Sql "UPDATE device_port SET deleted_at=NOW() WHERE device_id='$tag' AND port_no=3;" | Out-Null
    Assert ((Post '/api/v1/internal/scan/resolve' @{code=$tag}).data.ports.Count -eq 2) 'Deleted port remained visible'
    Sql "UPDATE device_port SET deleted_at=NULL WHERE device_id='$tag' AND port_no=3;" | Out-Null
    $badStartStatus = 200
    try { Invoke-RestMethod -Uri 'http://127.0.0.1:8081/api/v1/user/scan/start' -Method Post -Headers @{Authorization="Bearer $userToken"} -ContentType application/json -Body (@{port_id="${tag}_missing";estimated_kwh="0.500";estimated_minutes=120}|ConvertTo-Json) | Out-Null }
    catch { $badStartStatus = [int]$_.Exception.Response.StatusCode }
    Assert ($badStartStatus -eq 404) 'Checkout accepted a nonexistent port'
    Sql "UPDATE device_port SET status='charging' WHERE device_id='$tag' AND port_no=2;" | Out-Null
    $busy = Invoke-RestMethod -Uri 'http://127.0.0.1:8081/api/v1/user/scan/start' -Method Post -Headers @{Authorization="Bearer $userToken"} -ContentType application/json -Body (@{port_id=$port.port_id;estimated_kwh="0.500";estimated_minutes=120}|ConvertTo-Json)
    Assert ($busy.code -ne 0) 'Checkout accepted a charging port'
    $orderCount = Sql "SELECT COUNT(*) FROM user_db.charge_order WHERE device_id='$tag' OR port_code IN ('$($port.port_id)','${tag}_missing');"
    Assert ($orderCount -eq '0') 'Rejected checkout created an order'
    $holdCount = docker compose -f $taskCompose exec -T redis-cache redis-cli EXISTS "charge:hold:port_$($port.port_id)" "charge:hold:port_${tag}_missing"
    Assert ($holdCount -eq '0') 'Rejected checkout reserved a port'
    Sql "UPDATE device_port SET status='idle' WHERE device_id='$tag' AND port_no=2;" | Out-Null
    $register = @{device_id=$tag;vendor_id=$vendor;station_id=1;port_count=3;model='测试型号';firmware_version='test-v1';connect_type='tcp';client_ip='127.0.0.1'}
    $session1 = (Post '/api/v1/internal/device/register' $register).data
    $session2 = (Post '/api/v1/internal/device/register' $register).data
    Assert ($session1.registered -and $session2.session_id -gt $session1.session_id) 'Registration did not create distinct sessions'
    $sessionState = Sql "SELECT COUNT(*) FROM device WHERE device_id='$tag'; SELECT COUNT(*) FROM device_session WHERE device_id='$tag' AND ended_at IS NULL; SELECT close_reason FROM device_session WHERE id=$($session1.session_id);"
    Assert ($sessionState[0] -eq '1' -and $sessionState[1] -eq '1' -and $sessionState[2] -eq 're_register') 'Re-registration duplicated device or left old session open'
    $register.device_id="${tag}_unknown"
    Assert ((Post '/api/v1/internal/device/register' $register).code -eq 2001) 'Unknown physical device was provisioned through registration'
    $register.device_id=$tag
    Sql "UPDATE device SET status='disabled' WHERE device_id='$tag';" | Out-Null
    $disabledScan = 200
    try { Post '/api/v1/internal/scan/port' @{port_id=$port.port_id} | Out-Null }
    catch { $disabledScan = [int]$_.Exception.Response.StatusCode }
    Assert ($disabledScan -eq 404) 'Disabled device remained available to scan'

    Assert ((Post '/api/v1/internal/device/register' $register).code -eq 2002) 'Disabled device was allowed to register'
    Sql "UPDATE device SET status='enabled' WHERE device_id='$tag';" | Out-Null
    $tcp = [Net.Sockets.TcpClient]::new()
    try {
        $tcp.Connect('127.0.0.1',9100)
        $tcp.ReceiveTimeout=5000; $tcp.SendTimeout=5000
        $tcpReader = [IO.StreamReader]::new($tcp.GetStream())
        $tcpWriter = [IO.StreamWriter]::new($tcp.GetStream(),[Text.UTF8Encoding]::new($false))
        $tcpWriter.AutoFlush=$true
        $frame = @{device_id=$tag;port_no=1;msg_type='heartbeat';payload=@{};ts=[DateTime]::UtcNow.ToString('o')}
        $tcpWriter.WriteLine(($frame | ConvertTo-Json -Compress))
        $ack = $tcpReader.ReadLine() | ConvertFrom-Json
        Assert ($ack.ack -eq $true) 'Known TCP device heartbeat failed'
        $frame.device_id="${tag}_unknown"
        $tcpWriter.WriteLine(($frame | ConvertTo-Json -Compress))
        $rejected = $tcpReader.ReadLine() | ConvertFrom-Json
        Assert ($rejected.error -eq 'device_identity_mismatch') 'TCP connection changed device identity'
    } finally { $tcp.Dispose() }
    $device.port_count=4; Expect 409 $body; $device.port_count=3
    Expect 400 @{devices=@($device,$device)}
    $device.port_count=0; Expect 400 $body; $device.port_count=3
    # The first row is valid but must roll back when a later row is invalid.
    $valid = @{device_id="${tag}_a";vendor_id=$vendor;station_id=1;port_count=2;model=$null}
    $invalid = @{device_id="${tag}_z";vendor_id=1844674407370955;station_id=1;port_count=2;model=$null}
    Expect 400 @{devices=@($valid,$invalid)}
    $count = Sql "SELECT COUNT(*) FROM device WHERE device_id='${tag}_a'; SELECT COUNT(*) FROM device_provision WHERE device_id='${tag}_a'; SELECT COUNT(*) FROM device_port WHERE device_id='${tag}_a';"
    Assert (($count | Where-Object { $_ -ne '0' }).Count -eq 0) 'Batch error left partial data'
    $parallelBody = @{devices=@($valid)} | ConvertTo-Json -Depth 8
    $parallelUrl = $BaseUrl + '/api/v1/internal/devices/provision'
    $results = 1..2 | ForEach-Object -Parallel {
        Invoke-RestMethod -Uri $using:parallelUrl -Method Post -Headers $using:headers -ContentType application/json -Body $using:parallelBody
    } -ThrottleLimit 2
    Assert ($results.Count -eq 2) 'Concurrent requests did not complete'
    Assert ($results[0].data.items[0].ports[0].port_id -eq $results[1].data.items[0].ports[0].port_id) 'Concurrent retry created different ports'
    $count = Sql "SELECT COUNT(*) FROM device WHERE device_id='${tag}_a'; SELECT COUNT(*) FROM device_port WHERE device_id='${tag}_a';"
    Assert ($count[0] -eq '1' -and $count[1] -eq '2') 'Concurrent retry duplicated database rows'
    $status=200
    try { Invoke-RestMethod -Uri ($BaseUrl+'/api/v1/internal/devices/provision') -Method Post -ContentType application/json -Body ($body | ConvertTo-Json -Depth 8) | Out-Null }
    catch { if ($null -eq $_.Exception.Response) { throw }; $status=[int]$_.Exception.Response.StatusCode }
    Assert ($status -eq 401 -or $status -eq 403) 'Missing service token was accepted'
    $station = Sql "INSERT INTO admin_db.station (code,name,longitude,latitude) VALUES ('$tag','导入测试站点',116.4,39.9); SELECT LAST_INSERT_ID();"
    $station = [long]($station | Select-Object -Last 1)
    $login = Invoke-RestMethod -Uri 'http://127.0.0.1:5173/api/v1/admin/auth/login' -Method Post -ContentType application/json -Body '{"username":"admin","password":"DevAdmin2026!"}'
    $adminHeaders = @{Authorization='Bearer '+$login.data.token}
    $importId = [guid]::NewGuid().ToString()
    $adminBody = @{import_id=$importId;devices=@(@{device_id="${tag}_ui";vendor_id=$vendor;station_id=$station;port_count=2;model=$null})} | ConvertTo-Json -Depth 8
    $job = Invoke-RestMethod -Uri 'http://127.0.0.1:5173/api/v1/admin/device-imports' -Method Post -Headers $adminHeaders -ContentType application/json -Body $adminBody
    Assert ($job.data.status -eq 'completed') ('Admin import failed: '+$job.data.last_error)
    $againJob = Invoke-RestMethod -Uri "http://127.0.0.1:5173/api/v1/admin/device-imports/$importId/retry" -Method Post -Headers $adminHeaders
    Assert ($againJob.data.status -eq 'completed') 'Completed import retry failed'
    $meta = Sql "SELECT COUNT(*) FROM admin_db.device_meta WHERE device_id='${tag}_ui'; SELECT COUNT(*) FROM admin_db.audit_log WHERE target_id='$importId' AND action='import';"
    Assert ($meta[0] -eq '1' -and $meta[1] -eq '1') 'Metadata or exactly-once completion audit failed'
    $failedImportId = [guid]::NewGuid().ToString()
    $failedBody = @{import_id=$failedImportId;devices=@(@{device_id="${tag}_bad";vendor_id=$vendor;station_id=1844674407370955;port_count=2;model=$null})} | ConvertTo-Json -Depth 8
    $failedJob = Invoke-RestMethod -Uri 'http://127.0.0.1:5173/api/v1/admin/device-imports' -Method Post -Headers $adminHeaders -ContentType application/json -Body $failedBody
    Assert ($failedJob.data.status -eq 'failed' -and $failedJob.data.last_error) 'Invalid station did not produce a durable failed job'
    $count = Sql "SELECT COUNT(*) FROM device WHERE device_id='${tag}_bad'; SELECT COUNT(*) FROM admin_db.device_meta WHERE device_id='${tag}_bad';"
    Assert (($count | Where-Object { $_ -ne '0' }).Count -eq 0) 'Invalid station created partial metadata'
    # Simulate a process crash after gateway commits but before admin finishes.
    $recoveryDevice = @{device_id="${tag}_recover";vendor_id=$vendor;station_id=$station;port_count=2;model=$null}
    Post '/api/v1/internal/devices/provision' @{devices=@($recoveryDevice)} | Out-Null
    $recoveryId = [guid]::NewGuid().ToString()
    $revokedId = [guid]::NewGuid().ToString()
    $recoveryJson = (@{devices=@($recoveryDevice)} | ConvertTo-Json -Compress -Depth 8).Replace("'","''")
    $revokedJson = (@{devices=@(@{device_id="${tag}_revoked";vendor_id=$vendor;station_id=$station;port_count=2;model=$null})} | ConvertTo-Json -Compress -Depth 8).Replace("'","''")
    Sql @"
INSERT INTO admin_db.device_import (import_id,actor_id,request_json)
SELECT '$recoveryId',id,'$recoveryJson' FROM admin_db.admin_user_role WHERE username='admin' AND deleted_at IS NULL LIMIT 1;
INSERT INTO admin_db.admin_user_role (username,password_hash,status)
SELECT '${tag}_revoked',password_hash,'disabled' FROM admin_db.admin_user_role WHERE username='admin' AND deleted_at IS NULL LIMIT 1;
SET @actor=LAST_INSERT_ID();
INSERT INTO admin_db.device_import (import_id,actor_id,request_json) VALUES ('$revokedId',@actor,'$revokedJson');
"@ | Out-Null
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    do {
        $states = Sql "SELECT status FROM admin_db.device_import WHERE import_id='$recoveryId'; SELECT status FROM admin_db.device_import WHERE import_id='$revokedId';"
        if ($states[0] -eq 'completed' -and $states[1] -eq 'failed') { break }
        Start-Sleep -Milliseconds 500
    } while ([DateTime]::UtcNow -lt $deadline)
    Assert ($states[0] -eq 'completed' -and $states[1] -eq 'failed') 'Recovery did not complete or permissions were not rechecked'
    $recovered = Sql "SELECT COUNT(*) FROM admin_db.device_meta WHERE device_id='${tag}_recover'; SELECT COUNT(*) FROM device WHERE device_id='${tag}_recover'; SELECT COUNT(*) FROM device WHERE device_id='${tag}_revoked'; SELECT retryable FROM admin_db.device_import WHERE import_id='$revokedId';"
    Assert ($recovered[0] -eq '1' -and $recovered[1] -eq '1' -and $recovered[2] -eq '0' -and $recovered[3] -eq '0') 'Recovery duplicated data or executed revoked job'
    Write-Output 'PASS: onboarding, concurrent replay, rollback, scan, registration sessions, TCP identity, audit, crash recovery and revoked-actor rejection.'
} finally {
    foreach($authKey in $script:authTestKeys){docker compose -f $taskCompose exec -T redis-cache redis-cli DEL $authKey | Out-Null}
    foreach($authTag in $script:authTestRows){
        "DELETE FROM user_db.user WHERE openid='$authTag';" | docker compose -f $taskCompose exec -T mysql sh -c 'export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"; exec mysql -uroot' | Out-Null
    }
    Sql "DELETE FROM device_session WHERE device_id='$tag';" | Out-Null
    if ($recoveryId) { Sql "DELETE FROM admin_db.audit_log WHERE target_id='$recoveryId' AND module='device'; DELETE FROM admin_db.device_import WHERE import_id='$recoveryId';" | Out-Null }
    if ($revokedId) { Sql "DELETE FROM admin_db.device_import WHERE import_id='$revokedId';" | Out-Null }
    Sql "DELETE FROM admin_db.admin_user_role WHERE username='${tag}_revoked'; DELETE FROM admin_db.device_import_identity WHERE device_id IN ('${tag}_recover','${tag}_revoked'); DELETE FROM admin_db.device_meta WHERE device_id IN ('${tag}_recover','${tag}_revoked'); DELETE FROM device_port WHERE device_id IN ('${tag}_recover','${tag}_revoked'); DELETE FROM device WHERE device_id IN ('${tag}_recover','${tag}_revoked'); DELETE FROM device_provision WHERE device_id IN ('${tag}_recover','${tag}_revoked');" | Out-Null
    if ($importId) { Sql "DELETE FROM admin_db.audit_log WHERE target_id='$importId' AND module='device'; DELETE FROM admin_db.device_import WHERE import_id='$importId';" | Out-Null }
    if ($failedImportId) { Sql "DELETE FROM admin_db.device_import WHERE import_id='$failedImportId';" | Out-Null }
    Sql "DELETE FROM admin_db.device_import_identity WHERE device_id IN ('${tag}_ui','${tag}_bad'); DELETE FROM admin_db.device_meta WHERE device_id IN ('${tag}_ui','${tag}_bad'); DELETE FROM admin_db.station WHERE code='$tag'; DELETE FROM device_port WHERE device_id IN ('${tag}_ui','${tag}_bad'); DELETE FROM device WHERE device_id IN ('${tag}_ui','${tag}_bad'); DELETE FROM device_provision WHERE device_id IN ('${tag}_ui','${tag}_bad');" | Out-Null
    Sql "DELETE FROM device_port WHERE device_id IN ('$tag','${tag}_a','${tag}_z'); DELETE FROM device WHERE device_id IN ('$tag','${tag}_a','${tag}_z'); DELETE FROM device_provision WHERE device_id IN ('$tag','${tag}_a','${tag}_z'); DELETE FROM vendor WHERE vendor_code='$tag';" | Out-Null
}

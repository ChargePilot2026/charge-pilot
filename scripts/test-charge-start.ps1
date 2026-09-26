param([string]$Gateway='http://127.0.0.1:8083',[switch]$RestartGateway,[switch]$StopCharging)
$ErrorActionPreference='Stop'
$taskCompose=Join-Path (Split-Path -Parent $PSScriptRoot) 'compose.dev.yaml'
$tag='start_'+[guid]::NewGuid().ToString('N').Substring(0,16)
$published=[Collections.Generic.List[string]]::new()
$orders=[Collections.Generic.List[object]]::new()
function Sql([string]$query) {
    $result=$query | docker compose -f $taskCompose exec -T mysql sh -c 'export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"; exec mysql -uroot --batch --skip-column-names --default-character-set=utf8mb4 gateway_db'
    if($LASTEXITCODE -ne 0){throw 'Fixture SQL failed'}
    return $result
}
function Assert([bool]$ok,[string]$message){if(-not $ok){throw $message}}
function Send-Frame($payload,[int]$port=1,[string]$kind='ack') {
    $frame=@{device_id=$tag;port_no=$port;msg_type=$kind;payload=$payload;ts=[DateTimeOffset]::UtcNow.ToString('o')}|ConvertTo-Json -Compress -Depth 8
    $writer.WriteLine($frame);$writer.Flush()
}
function Read-Command([string]$action,[int]$port) {
    $deadline=[DateTime]::UtcNow.AddSeconds(15)
    while([DateTime]::UtcNow -lt $deadline){
        $pending=$reader.ReadLineAsync()
        if(-not $pending.Wait([TimeSpan]::FromSeconds(15))){throw "No $action command received"}
        $line=$pending.Result;if($null -eq $line){throw 'Device disconnected'}
        $frame=$line|ConvertFrom-Json
        if($frame.msg_type -eq 'cmd'){
            Assert ($frame.payload.command -eq $action -and $frame.port_no -eq $port) 'Unexpected device command'
            return $frame.payload
        }
    }
    throw "No $action command received"
}
function Wait-Result($order,[string]$expected) {
    $deadline=[DateTime]::UtcNow.AddSeconds(20)
    while([DateTime]::UtcNow -lt $deadline){
        $result=Sql "SELECT CONCAT(c.status,':',g.result_reported) FROM user_db.charge_order c JOIN gateway_db.charge_command g ON g.charge_order_id=c.id WHERE c.id=$($order.charge_order_id);"
        if($result -eq "${expected}:1"){return}
        Start-Sleep -Milliseconds 200
    }
    throw "Outcome not confirmed: expected $expected, got $result"
}
function Publish($order) {
    $payload=$order|ConvertTo-Json -Compress
    $event=[guid]::NewGuid().ToString()
    $entry=$payload|docker compose -f $taskCompose exec -T redis-stream redis-cli -x XADD charge_started_stream '*' event_id $event event_type charge_started producer test schema_version 1 occurred_at ([DateTimeOffset]::UtcNow.ToString('o')) payload
    if($LASTEXITCODE -ne 0){throw 'Cannot publish test event'}
    $published.Add($entry.Trim())
}
function New-Order([int]$port,[bool]$hold=$true) {
    $no="${tag}_$port";$code="${tag}:$port"
    $ids=Sql @"
INSERT INTO user_db.charge_order (order_no,user_id,device_id,port_no,port_code,status,created_month)
VALUES ('$no',$uid,'$tag',$port,'$code','paid',DATE_FORMAT(UTC_DATE(),'%Y-%m-01'));
SET @cid=LAST_INSERT_ID();
INSERT INTO user_db.payment_order (order_no,biz_type,biz_id,user_id,pay_method,total_cents,paid_cents,status,created_month)
VALUES ('${no}_pay','charge',@cid,$uid,'wechat',100,100,'paid',DATE_FORMAT(UTC_DATE(),'%Y-%m-01'));
SET @pid=LAST_INSERT_ID();UPDATE user_db.charge_order SET payment_order_id=@pid WHERE id=@cid;
SELECT CONCAT(@cid,':',@pid);
"@
    $ids=($ids|Select-Object -Last 1).Split(':')
    $order=@{charge_order_id=[long]$ids[0];payment_order_id=[long]$ids[1];order_no=$no;user_id=$uid;device_id=$tag;port_no=$port;port_code=$code}
    $orders.Add($order)
    if($hold){docker compose -f $taskCompose exec -T redis-cache redis-cli SET "charge:hold:port_$code" "${uid}:$no" EX 300|Out-Null}
    Publish $order
    return $order
}
try {
    $serviceToken=docker compose -f $taskCompose exec -T gateway printenv SERVICE_TOKEN
    $headers=@{'X-Service-Token'=$serviceToken.Trim()}
    $uid=[long](Sql "INSERT INTO user_db.user (openid) VALUES ('$tag'); SELECT LAST_INSERT_ID();"|Select-Object -Last 1)
    $vendor=[long](Sql "INSERT INTO gateway_db.vendor (vendor_code,vendor_name,adapter_class,protocol) VALUES ('$tag','test','json-line','tcp'); SELECT LAST_INSERT_ID();"|Select-Object -Last 1)
    Sql "INSERT INTO gateway_db.device (device_id,vendor_id,port_count) VALUES ('$tag',$vendor,5);"|Out-Null
    1..5|ForEach-Object {Sql "INSERT INTO gateway_db.device_port (device_id,port_no,port_code) VALUES ('$tag',$_,'${tag}:$_');"|Out-Null}
    $tcp=[Net.Sockets.TcpClient]::new('127.0.0.1',9100)
    $reader=[IO.StreamReader]::new($tcp.GetStream(),[Text.Encoding]::UTF8)
    $writer=[IO.StreamWriter]::new($tcp.GetStream(),[Text.UTF8Encoding]::new($false));$writer.NewLine="`n"
    Send-Frame @{} 1 'heartbeat'
    $hello=$reader.ReadLineAsync();Assert ($hello.Wait([TimeSpan]::FromSeconds(5))) 'Device handshake timed out'
    Assert (($hello.Result|ConvertFrom-Json).ack -eq $true) 'Device handshake failed'

    $happy=New-Order 1
    $start=Read-Command 'START' 1
    Assert ((Sql "SELECT status FROM user_db.charge_order WHERE id=$($happy.charge_order_id);") -eq 'paid') 'Socket write falsely marked order charging'
    $scan=Invoke-RestMethod -Uri "$Gateway/api/v1/internal/scan/port" -Method Post -Headers $headers -ContentType application/json -Body (@{port_id="${tag}:1"}|ConvertTo-Json)
    Assert ($scan.data.status -eq 'reserved') 'Dispatched port remained selectable'
    Send-Frame @{command_id=$start.command_id;command='START';success=$true} 2
    $bad=$reader.ReadLineAsync();Assert ($bad.Wait([TimeSpan]::FromSeconds(5))) 'Bad ACK response timed out'
    Assert (($bad.Result|ConvertFrom-Json).error -eq 'frame_processing_failed') 'Wrong-port ACK accepted'
    Send-Frame @{command_id=$start.command_id;command='START';success=$true} 1
    Wait-Result $happy 'charging'
    Publish $happy
    Send-Frame @{command_id=$start.command_id;command='START';success=$true} 1
    Assert ((Sql "SELECT COUNT(*) FROM gateway_db.charge_command WHERE charge_order_id=$($happy.charge_order_id);") -eq '1') 'Duplicate event created another command'
    Assert ((Sql "SELECT COUNT(*) FROM user_db.active_port_charge WHERE charge_order_id=$($happy.charge_order_id) AND port_id>0;") -eq '1') 'Confirmed charge lacks real port reservation'
    if($StopCharging){
        $refresh='RT_'+[guid]::NewGuid().ToString('N')+[guid]::NewGuid().ToString('N');$sid=[guid]::NewGuid().ToString()
        $identity=@{user_id=$uid;openid=$tag;sid=$sid}|ConvertTo-Json -Compress
        $identity|docker compose -f $taskCompose exec -T redis-cache redis-cli -x SET "auth:user:refresh:$refresh"|Out-Null
        docker compose -f $taskCompose exec -T redis-cache redis-cli SET "auth:user:session:$sid" $refresh EX 300|Out-Null
        $session=(Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:8081/api/v1/public/auth/refresh' -Headers @{Authorization="Bearer $refresh"}).data
        $userHeaders=@{Authorization="Bearer $($session.token)"}
        $stopBody=@{order_no=$happy.order_no}|ConvertTo-Json
        $accepted=(Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:8081/api/v1/user/charge/stop' -Headers $userHeaders -ContentType application/json -Body $stopBody).data
        Assert ($accepted.accepted -and -not $accepted.stopped) 'STOP request claimed immediate physical completion'
        $again=(Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:8081/api/v1/user/charge/stop' -Headers $userHeaders -ContentType application/json -Body $stopBody).data
        Assert ($again.command_id -eq $accepted.command_id) 'Repeated STOP created a different command'
        $stopCommand=Read-Command 'STOP' 1
        Assert ($stopCommand.command_id -eq $accepted.command_id -and $stopCommand.meter_required) 'Wrong metered STOP command dispatched'
        Assert ((Sql "SELECT status FROM user_db.charge_order WHERE id=$($happy.charge_order_id);") -eq 'charging') 'Unacknowledged STOP prematurely ended charge'
        $meter=@{charged_wh=125;charged_seconds=1;ended_at=[DateTimeOffset]::UtcNow.ToString('o')}
        Send-Frame @{command_id=$stopCommand.command_id;command='STOP';success=$true;meter=$meter} 1
        $deadline=[DateTime]::UtcNow.AddSeconds(15);$done=$false
        while([DateTime]::UtcNow -lt $deadline){
            if((Sql "SELECT result_reported FROM gateway_db.charge_stop_command WHERE command_id='$($stopCommand.command_id)';") -eq '1'){$done=$true;break};Start-Sleep -Milliseconds 200
        }
        Assert $done 'Confirmed metered STOP was not persisted'
        $snapshot=(Invoke-RestMethod -Uri "http://127.0.0.1:8081/api/v1/user/charge/ongoing/snapshot?order_id=$($happy.order_no)" -Headers $userHeaders).data
        Assert ($snapshot.status -eq 'completed' -and -not $snapshot.poll_continue -and [decimal]$snapshot.charged_kwh -eq 0.125) 'Final snapshot does not reflect device meter and completion'
        Send-Frame @{command_id=$stopCommand.command_id;command='STOP';success=$true;meter=$meter} 1
        $completed=(Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:8081/api/v1/user/charge/stop' -Headers $userHeaders -ContentType application/json -Body $stopBody).data
        Assert ($completed.stopped -and $completed.command_id -eq $stopCommand.command_id) 'Completed STOP replay lost original result'
        Assert ((Sql "SELECT COUNT(*) FROM gateway_db.event_outbox WHERE event_id='$($stopCommand.command_id)';") -eq '1') 'Duplicate STOP emitted duplicate charge-end events'
        Write-Output 'PASS: authenticated user STOP, pending state, final meter, completed snapshot and idempotent end event.'
    }

    $reject=New-Order 2;$cmd=Read-Command 'START' 2
    Send-Frame @{command_id=$cmd.command_id;command='START';success=$false} 2
    Wait-Result $reject 'failed'
    Assert ((Sql "SELECT COUNT(*) FROM user_db.refund_record WHERE biz_id=$($reject.charge_order_id) AND biz_type='charge' AND refund_cents=100;") -eq '1') 'Device rejection did not create one refund'

    $timeout=New-Order 3;$cmd=Read-Command 'START' 3
    if($RestartGateway){
        $tcp.Dispose()
        docker compose -f $taskCompose restart gateway|Out-Null
        if($LASTEXITCODE -ne 0){throw 'Gateway restart failed'}
        $ready=$false;$deadline=[DateTime]::UtcNow.AddSeconds(30)
        while([DateTime]::UtcNow -lt $deadline){
            try {Invoke-RestMethod -Uri "$Gateway/api/v1/health" -TimeoutSec 2|Out-Null;$ready=$true;break}
            catch {Start-Sleep -Milliseconds 250}
        }
        Assert $ready 'Gateway did not recover after restart'
        $tcp=[Net.Sockets.TcpClient]::new('127.0.0.1',9100)
        $reader=[IO.StreamReader]::new($tcp.GetStream(),[Text.Encoding]::UTF8)
        $writer=[IO.StreamWriter]::new($tcp.GetStream(),[Text.UTF8Encoding]::new($false));$writer.NewLine="`n"
        Send-Frame @{} 1 'heartbeat'
        $hello=$reader.ReadLineAsync();Assert ($hello.Wait([TimeSpan]::FromSeconds(5))) 'Reconnect handshake timed out'
        Assert (($hello.Result|ConvertFrom-Json).ack -eq $true) 'Reconnect handshake failed'
    }
    Sql "UPDATE gateway_db.charge_command SET sent_at=UTC_TIMESTAMP(3)-INTERVAL 21 SECOND WHERE charge_order_id=$($timeout.charge_order_id);"|Out-Null
    $stop=Read-Command 'STOP' 3
    Assert ((Sql "SELECT status FROM user_db.charge_order WHERE id=$($timeout.charge_order_id);") -eq 'paid') 'Timeout falsely asserted device stopped'
    Send-Frame @{command_id=$cmd.command_id;command='START';success=$true} 3
    Send-Frame @{command_id=$stop.command_id;command='STOP';success=$true} 3
    Wait-Result $timeout 'failed'
    Assert ((Sql "SELECT status FROM gateway_db.device_port WHERE device_id='$tag' AND port_no=3 AND current_order_id IS NULL;") -eq 'idle') 'Confirmed STOP did not release port'

    $lost=New-Order 4 $false;Wait-Result $lost 'failed'
    Assert ((Sql "SELECT error FROM gateway_db.charge_command WHERE charge_order_id=$($lost.charge_order_id);") -eq 'reservation_lost') 'Lost logical reservation did not reject startup'
    Sql "UPDATE gateway_db.device_port SET status='charging',current_order_id='existing-test-charge' WHERE device_id='$tag' AND port_no=5;"|Out-Null
    $busy=New-Order 5;Wait-Result $busy 'failed'
    Assert ((Sql "SELECT CONCAT(status,':',current_order_id) FROM gateway_db.device_port WHERE device_id='$tag' AND port_no=5;") -eq 'charging:existing-test-charge') 'Rejected order released an existing port owner'
    Write-Output 'PASS: real TCP dispatch, ACK identity, no simulated success, idempotent replay, device rejection/refund, timeout STOP confirmation and reservation loss.'
} finally {
    foreach($refreshValue in @($refresh,$session.refresh_token)){if($refreshValue){docker compose -f $taskCompose exec -T redis-cache redis-cli DEL "auth:user:refresh:$refreshValue"|Out-Null}}
    if($sid){docker compose -f $taskCompose exec -T redis-cache redis-cli DEL "auth:user:session:$sid"|Out-Null}
    Sql "UPDATE gateway_db.charge_stop_command SET result_reported=TRUE WHERE device_id='$tag';"|Out-Null
    Sql "UPDATE gateway_db.charge_command SET result_reported=TRUE WHERE device_id='$tag';"|Out-Null
    if($tcp){$tcp.Dispose()}
    foreach($order in $orders){
        docker compose -f $taskCompose exec -T redis-cache redis-cli DEL "charge:hold:port_$($order.port_code)" "charge:lock:port_$($order.port_code)"|Out-Null
    }
    foreach($id in $published){docker compose -f $taskCompose exec -T redis-stream redis-cli XACK charge_started_stream gateway-cg $id|Out-Null;docker compose -f $taskCompose exec -T redis-stream redis-cli XDEL charge_started_stream $id|Out-Null}
    Sql @"
DELETE e FROM user_db.event_outbox e JOIN user_db.refund_record r ON JSON_UNQUOTE(JSON_EXTRACT(e.envelope_json,'$.payload.refund_no'))=r.refund_no JOIN user_db.charge_order c ON c.id=r.biz_id WHERE c.device_id='$tag' AND r.biz_type='charge';
DELETE r FROM user_db.refund_record r JOIN user_db.charge_order c ON c.id=r.biz_id WHERE c.device_id='$tag' AND r.biz_type='charge';
DELETE r FROM user_db.charge_start_receipt r JOIN user_db.charge_order c ON c.id=r.charge_order_id WHERE c.device_id='$tag';
DELETE r FROM user_db.charge_end_receipt r JOIN user_db.charge_order c ON c.id=r.charge_order_id WHERE c.device_id='$tag';
DELETE e FROM user_db.charge_event_log e JOIN user_db.charge_order c ON c.id=e.charge_order_id WHERE c.device_id='$tag';
DELETE p FROM user_db.payment_order p JOIN user_db.charge_order c ON c.id=p.biz_id WHERE c.device_id='$tag' AND p.biz_type='charge';
DELETE FROM user_db.active_port_charge WHERE device_id='$tag';
DELETE FROM user_db.charge_order WHERE device_id='$tag';
DELETE FROM user_db.user WHERE openid='$tag';
DELETE FROM gateway_db.charge_command WHERE device_id='$tag';
DELETE e FROM gateway_db.event_outbox e JOIN gateway_db.charge_stop_command c ON e.event_id=c.command_id WHERE c.device_id='$tag';
DELETE FROM gateway_db.charge_stop_command WHERE device_id='$tag';
DELETE FROM gateway_db.device_port WHERE device_id='$tag';
DELETE FROM gateway_db.device WHERE device_id='$tag';
DELETE FROM gateway_db.vendor WHERE vendor_code='$tag';
"@|Out-Null
}

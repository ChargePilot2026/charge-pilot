param([string]$Billing='http://127.0.0.1:8084')
$ErrorActionPreference='Stop'
$taskCompose=Join-Path (Split-Path -Parent $PSScriptRoot) 'compose.dev.yaml'
$tag='fee_'+[guid]::NewGuid().ToString('N').Substring(0,16)
function Sql([string]$query) {
    $result=$query | docker compose -f $taskCompose exec -T mysql sh -c 'export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"; exec mysql -uroot --batch --skip-column-names --default-character-set=utf8mb4 user_db'
    if($LASTEXITCODE -ne 0){throw 'Fixture SQL failed'}
    return $result
}
function Assert([bool]$ok,[string]$message){if(-not $ok){throw $message}}
try {
    $serviceToken=docker compose -f $taskCompose exec -T billing printenv SERVICE_TOKEN
    $headers=@{'X-Service-Token'=$serviceToken.Trim()}
    $uid=[long](Sql "INSERT INTO user (openid) VALUES ('$tag'); SELECT LAST_INSERT_ID();"|Select-Object -Last 1)
    $cid=[long](Sql @"
INSERT INTO charge_order (order_no,user_id,device_id,port_no,port_code,status,started_at,ended_at,charged_kwh,charged_seconds,created_month)
VALUES ('$tag',$uid,'$tag',1,'${tag}:1','completed','2026-09-25 01:00:00','2026-09-25 02:00:00',0.125,3600,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'));
SELECT LAST_INSERT_ID();
"@ | Select-Object -Last 1)
    $quote=@{electric_cents=100;service_cents=40;total_cents=140;pricing=@{station_id=1;station_name='Test';rule_id=99;name='Test';version=3;mode='kwh';time_of_use=@(@{period='all';start='00:00';end='24:00';electric_price_cents=100;service_price_cents=40});service_fee_cents_per_kwh=30;service_fee_cents_per_min=2;min_charge_cents=0};estimated_kwh='1';estimated_minutes=60;quote_expires_at='2026-09-25T01:00:00Z';estimation_basis='fixture'}|ConvertTo-Json -Depth 8 -Compress
    $meter=@{charged_wh=125;charged_seconds=3600;ended_at='2026-09-25T02:00:00Z'}|ConvertTo-Json -Compress
    $quoteId=[guid]::NewGuid().ToString();$stopId=[guid]::NewGuid().ToString()
    Sql "INSERT INTO charge_order_pricing (charge_order_id,quote_id,user_id,port_code,quote_snapshot) VALUES ($cid,'$quoteId',$uid,'${tag}:1','$quote'); INSERT INTO charge_end_receipt (charge_order_id,stop_command_id,meter_json) VALUES ($cid,'$stopId','$meter');"|Out-Null
    # Deliberately bogus caller-supplied metrics/rule must never affect the fee.
    $body=@{order_no=$tag;charge_order_id=$cid;pricing_rule_id=999;charged_kwh=100;charged_seconds=1;peak_kwh=100;off_kwh=0}|ConvertTo-Json
    $first=Invoke-RestMethod "$Billing/api/v1/internal/calculate" -Method Post -Headers $headers -ContentType application/json -Body $body
    Assert ($first.data.electric_cents -eq 13 -and $first.data.service_cents -eq 5 -and $first.data.total_cents -eq 18) 'Fee did not use actual meter and saved rule'
    $again=Invoke-RestMethod "$Billing/api/v1/internal/calculate" -Method Post -Headers $headers -ContentType application/json -Body $body
    Assert ($again.data.calculation_id -eq $first.data.calculation_id) 'Replay created a new fee'
    Assert ((Sql "SELECT COUNT(*) FROM billing_db.fee_calculation WHERE charge_order_id=$cid;") -eq '1') 'Duplicate fee row'
    Assert ((Sql "SELECT CONCAT(user_id,':',pricing_rule_id,':',pricing_rule_version,':',charged_kwh) FROM billing_db.fee_calculation WHERE charge_order_id=$cid;") -eq "${uid}:99:3:0.1250") 'Wrong fee ownership or rule version'
    # Without a linked payment, application must fail and remain durably retryable.
    $deadline=[DateTime]::UtcNow.AddSeconds(20)
    do {
        $attempts=[int](Sql "SELECT attempts FROM billing_db.fee_delivery WHERE charge_order_id=$cid;")
        if($attempts -gt 0){break};Start-Sleep -Milliseconds 250
    } while([DateTime]::UtcNow -lt $deadline)
    Assert ($attempts -gt 0) 'Fee delivery worker did not attempt delivery'
    Assert ((Sql "SELECT delivered FROM billing_db.fee_delivery WHERE charge_order_id=$cid;") -eq '0') 'Failed callback was marked delivered'
    Assert ((Sql "SELECT COUNT(*) FROM charge_fee_receipt WHERE charge_order_id=$cid;") -eq '0') 'Invalid callback partially committed'
    $paymentId=[long](Sql "INSERT INTO payment_order (order_no,biz_type,biz_id,user_id,pay_method,total_cents,paid_cents,status,created_month) VALUES ('${tag}_pay','charge',$cid,$uid,'wechat',100,100,'paid',DATE_FORMAT(UTC_DATE(),'%Y-%m-01')); SELECT LAST_INSERT_ID();"|Select-Object -Last 1)
    Sql "UPDATE charge_order SET payment_order_id=$paymentId WHERE id=$cid; UPDATE billing_db.fee_delivery SET scheduled_at=UTC_TIMESTAMP(3) WHERE charge_order_id=$cid;"|Out-Null
    $deadline=[DateTime]::UtcNow.AddSeconds(20)
    do {
        $delivered=Sql "SELECT delivered FROM billing_db.fee_delivery WHERE charge_order_id=$cid;"
        if($delivered -eq '1'){break};Start-Sleep -Milliseconds 250
    } while([DateTime]::UtcNow -lt $deadline)
    Assert ($delivered -eq '1') 'Fee delivery did not recover'
    Assert ((Sql "SELECT CONCAT(electric_cents,':',service_cents,':',total_cents) FROM charge_order WHERE id=$cid;") -eq '13:5:18') 'Final fee was not applied'
    Assert ((Sql "SELECT CONCAT(COUNT(*),':',SUM(refund_cents)) FROM refund_record WHERE payment_order_id=$paymentId;") -eq '1:82') 'Refund difference is incorrect'
    $payload=Sql "SELECT payload_json FROM billing_db.fee_delivery WHERE charge_order_id=$cid;"
    1..3|ForEach-Object { $replay=Invoke-RestMethod "http://127.0.0.1:8081/api/v1/internal/charge-orders/$cid/fee-result" -Method Post -Headers $headers -ContentType application/json -Body $payload; Assert ($replay.data.ok -eq $true) 'Fee replay was not accepted' }
    Assert ((Sql "SELECT CONCAT(COUNT(*),':',SUM(refund_cents)) FROM refund_record WHERE payment_order_id=$paymentId;") -eq '1:82') 'Replay duplicated refund'
    Assert ((Sql "SELECT COUNT(*) FROM event_outbox WHERE JSON_EXTRACT(envelope_json,'$.payload.charge_order_id')=$cid AND stream='refund_required_stream';") -eq '1') 'Replay duplicated refund event'
    $altered=$payload|ConvertFrom-Json
    $altered.electric_cents++;$altered.total_cents++
    $rejected=Invoke-WebRequest "http://127.0.0.1:8081/api/v1/internal/charge-orders/$cid/fee-result" -Method Post -Headers $headers -ContentType application/json -Body ($altered|ConvertTo-Json -Depth 12) -SkipHttpErrorCheck
    Assert ($rejected.StatusCode -ge 400 -or ($rejected.Content|ConvertFrom-Json).code -ne 0) 'Changed fee replay was accepted'
    Assert ((Sql "SELECT total_cents FROM charge_order WHERE id=$cid;") -eq '18') 'Changed replay overwrote settled fee'
    $refundNo=Sql "SELECT refund_no FROM refund_record WHERE payment_order_id=$paymentId;"
    $deadline=[DateTime]::UtcNow.AddSeconds(20)
    do {
        $task=Sql "SELECT CONCAT(stage,':',attempts>0) FROM admin_db.refund_task WHERE refund_no='$refundNo';"
        if($task -eq 'queued:1'){break};Start-Sleep -Milliseconds 250
    } while([DateTime]::UtcNow -lt $deadline)
    Assert ($task -eq 'queued:1') 'Missing local WeChat credentials did not retain a queued refund task'
    Assert ((Sql "SELECT status FROM refund_record WHERE payment_order_id=$paymentId;") -eq 'pending') 'Unconfigured executor claimed or completed refund'
    Sql "UPDATE payment_order SET wechat_transaction_id='42000$paymentId' WHERE id=$paymentId;"|Out-Null
    1..2|ForEach-Object {
        $execution=Invoke-RestMethod 'http://127.0.0.1:8081/api/v1/internal/refund-records/execution' -Method Post -Headers $headers -ContentType application/json -Body (@{refund_no=$refundNo}|ConvertTo-Json)
        Assert ($execution.data.refund_cents -eq 82 -and $execution.data.total_cents -eq 100 -and $execution.data.status -eq 'processing') 'Automatic refund preparation changed amount or failed replay'
    }
    Assert ((Sql "SELECT COUNT(*) FROM refund_execution e JOIN refund_record r ON r.id=e.refund_record_id WHERE r.payment_order_id=$paymentId;") -eq '1') 'Duplicate execution claim'
    Sql "UPDATE charge_end_receipt SET meter_json=JSON_SET(meter_json,'$.charged_wh',126) WHERE charge_order_id=$cid;"|Out-Null
    $changed=Invoke-WebRequest "$Billing/api/v1/internal/calculate" -Method Post -Headers $headers -ContentType application/json -Body $body -SkipHttpErrorCheck
    $result=$changed.Content|ConvertFrom-Json
    Assert ($changed.StatusCode -ge 400 -or $result.code -ne 0) 'Changed receipt overwrote original fee'
    Assert ((Sql "SELECT total_cents FROM billing_db.fee_calculation WHERE charge_order_id=$cid;") -eq '18') 'Original fee was mutated'
    Write-Host 'PASS: snapshot pricing, fee delivery retry, final fee application, refund reservation, replay idempotency and changed-source rejection'
} finally {
    if($cid){Sql "DELETE t FROM admin_db.refund_task t JOIN refund_record r ON r.refund_no=t.refund_no WHERE r.biz_type='charge' AND r.biz_id=$cid; DELETE e FROM refund_execution e JOIN refund_record r ON r.id=e.refund_record_id WHERE r.biz_type='charge' AND r.biz_id=$cid; DELETE FROM billing_db.fee_delivery WHERE charge_order_id=$cid; DELETE FROM billing_db.fee_receipt WHERE charge_order_id=$cid; DELETE FROM billing_db.fee_calculation WHERE charge_order_id=$cid; DELETE FROM charge_fee_receipt WHERE charge_order_id=$cid; DELETE FROM event_outbox WHERE JSON_EXTRACT(envelope_json,'$.payload.charge_order_id')=$cid; DELETE FROM charge_event_log WHERE charge_order_id=$cid; DELETE FROM refund_record WHERE biz_type='charge' AND biz_id=$cid; DELETE FROM payment_order WHERE biz_type='charge' AND biz_id=$cid; DELETE FROM charge_end_receipt WHERE charge_order_id=$cid; DELETE FROM charge_order_pricing WHERE charge_order_id=$cid; DELETE FROM charge_order WHERE id=$cid;"|Out-Null}
    if($uid){Sql "DELETE FROM user WHERE id=$uid;"|Out-Null}
}

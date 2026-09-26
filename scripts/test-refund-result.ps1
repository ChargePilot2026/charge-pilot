param([string]$UserService='http://127.0.0.1:8081')
$ErrorActionPreference='Stop'
$taskCompose=Join-Path (Split-Path -Parent $PSScriptRoot) 'compose.dev.yaml'
$tag='rr_'+[guid]::NewGuid().ToString('N').Substring(0,16)
function Sql([string]$query){
    $output=$query|docker compose -f $taskCompose exec -T mysql sh -c 'export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"; exec mysql -uroot --batch --skip-column-names user_db'
    if($LASTEXITCODE -ne 0){throw 'Fixture SQL failed'};return $output
}
function Assert([bool]$ok,[string]$message){if(-not $ok){throw $message}}
function Post([string]$path,$body){Invoke-RestMethod "$UserService/api/v1/internal/$path" -Method Post -Headers $headers -ContentType application/json -Body ($body|ConvertTo-Json -Compress)}
try {
    $token=docker compose -f $taskCompose exec -T user printenv SERVICE_TOKEN
    $headers=@{'X-Service-Token'=$token.Trim()}
    $uid=[long](Sql "INSERT INTO user (openid) VALUES ('$tag'); SELECT LAST_INSERT_ID();"|Select-Object -Last 1)
    $paymentId=[long](Sql "INSERT INTO payment_order (order_no,biz_type,biz_id,user_id,pay_method,total_cents,paid_cents,status,created_month) VALUES ('$tag','charge',999999,$uid,'wechat',100,100,'paid',DATE_FORMAT(UTC_DATE(),'%Y-%m-01')); SELECT LAST_INSERT_ID();"|Select-Object -Last 1)
    Sql "INSERT INTO refund_record (refund_no,payment_order_id,user_id,biz_type,biz_id,refund_cents,status,created_month) VALUES ('${tag}_1',$paymentId,$uid,'charge',999999,82,'pending',DATE_FORMAT(UTC_DATE(),'%Y-%m-01')),('${tag}_2',$paymentId,$uid,'charge',999999,18,'pending',DATE_FORMAT(UTC_DATE(),'%Y-%m-01'));"|Out-Null
    foreach($index in 1..2){
        $no="${tag}_$index";$provider="500$($uid)$index"
        $claim=@{event_id=[guid]::NewGuid().ToString();refund_no=$no;admin_user_id=123}
        $first=Post 'refund-records/claim' $claim
        Assert ($first.data.payment_order_id -eq $paymentId -and $first.data.status -eq 'processing') 'Claim failed'
        Assert ((Post 'refund-records/claim' $claim).data.status -eq 'processing') 'Claim retry failed'
        $claim.admin_user_id=124
        $other=Invoke-WebRequest "$UserService/api/v1/internal/refund-records/claim" -Method Post -Headers $headers -ContentType application/json -Body ($claim|ConvertTo-Json) -SkipHttpErrorCheck
        Assert ($other.StatusCode -ge 400 -or ($other.Content|ConvertFrom-Json).code -ne 0) 'Other claimant stole refund'
        $result=@{refund_no=$no;success=$true;wechat_refund_id=$provider}
        if($index -eq 2){
            $duplicate=@{refund_no=$no;success=$true;wechat_refund_id="500$($uid)1"}
            $blocked=Invoke-WebRequest "$UserService/api/v1/internal/refund-records/$no/result" -Method Post -Headers $headers -ContentType application/json -Body ($duplicate|ConvertTo-Json) -SkipHttpErrorCheck
            Assert ($blocked.StatusCode -ge 400 -or ($blocked.Content|ConvertFrom-Json).code -ne 0) 'Provider refund ID was reused'
            Assert ((Sql "SELECT refunded_cents FROM payment_order WHERE id=$paymentId;") -eq '82') 'Rejected provider replay changed accounting'
        }
        1..3|ForEach-Object {Assert ((Post "refund-records/$no/result" $result).data.ok -eq $true) 'Result replay failed'}
        $expected=if($index -eq 1){'82:partial_refunded'}else{'100:refunded'}
        Assert ((Sql "SELECT CONCAT(refunded_cents,':',status) FROM payment_order WHERE id=$paymentId;") -eq $expected) 'Refund counted twice or wrong payment status'
        $result.success=$false;$result.failure_reason='late failure'
        $late=Invoke-WebRequest "$UserService/api/v1/internal/refund-records/$no/result" -Method Post -Headers $headers -ContentType application/json -Body ($result|ConvertTo-Json) -SkipHttpErrorCheck
        Assert ($late.StatusCode -ge 400 -or ($late.Content|ConvertFrom-Json).code -ne 0) 'Late failure reversed success'
        Assert ((Sql "SELECT COUNT(*) FROM event_outbox WHERE JSON_UNQUOTE(JSON_EXTRACT(envelope_json,'$.payload.refund_no'))='$no';") -eq '1') 'Duplicate terminal event'
    }
    Write-Host 'PASS: claim replay/ownership, refund result idempotency, partial/full payment accounting and late-failure rejection'
} finally {
    if($paymentId){Sql "DELETE s FROM refund_success_receipt s JOIN refund_record r ON r.id=s.refund_record_id WHERE r.payment_order_id=$paymentId; DELETE FROM event_outbox WHERE JSON_UNQUOTE(JSON_EXTRACT(envelope_json,'$.payload.refund_no')) IN ('${tag}_1','${tag}_2'); DELETE FROM refund_record WHERE payment_order_id=$paymentId; DELETE FROM payment_order WHERE id=$paymentId;"|Out-Null}
    if($uid){Sql "DELETE FROM user WHERE id=$uid;"|Out-Null}
}

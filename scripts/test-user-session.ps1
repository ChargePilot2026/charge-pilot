$ErrorActionPreference='Stop'
$taskCompose=Join-Path (Split-Path -Parent $PSScriptRoot) 'compose.dev.yaml'
$tag='test_session_'+[guid]::NewGuid().ToString('N')
$refresh='RT_'+[guid]::NewGuid().ToString('N')+[guid]::NewGuid().ToString('N')
$sid=[guid]::NewGuid().ToString()
$sessionKey="auth:user:session:$sid"
$keys=[Collections.Generic.List[string]]::new()
function Sql([string]$query) {
    $value=$query | docker compose -f $taskCompose exec -T mysql sh -c 'export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"; exec mysql -uroot --batch --skip-column-names user_db'
    if($LASTEXITCODE -ne 0){throw 'Session fixture SQL failed'}
    return $value
}
function Request([string]$path,[string]$token) {
    Invoke-RestMethod -Method Post -Uri ('http://127.0.0.1:8081/api/v1/public/auth/'+$path) -Headers @{Authorization="Bearer $token"}
}
function Expect([string]$path,[string]$token,[int]$expected) {
    $status=200
    try { Request $path $token | Out-Null } catch { if(!$_.Exception.Response){throw}; $status=[int]$_.Exception.Response.StatusCode }
    if($status -ne $expected){throw "Expected $path HTTP $expected, got $status"}
}
try {
    $id=Sql "INSERT INTO ``user`` (openid) VALUES ('$tag'); SELECT LAST_INSERT_ID();"
    $identity=@{user_id=[long]($id|Select-Object -Last 1);openid=$tag;sid=$sid}|ConvertTo-Json -Compress
    $key="auth:user:refresh:$refresh"; $keys.Add($key); $keys.Add($sessionKey)
    $identity | docker compose -f $taskCompose exec -T redis-cache redis-cli -x SET $key | Out-Null
    docker compose -f $taskCompose exec -T redis-cache redis-cli EXPIRE $key 60 | Out-Null
    docker compose -f $taskCompose exec -T redis-cache redis-cli SET $sessionKey $refresh EX 60 | Out-Null
    $session=(Request 'refresh' $refresh).data
    if(!$session.token -or !$session.refresh_token -or $session.refresh_token -eq $refresh){throw 'Refresh did not rotate tokens'}
    $keys.Add("auth:user:refresh:$($session.refresh_token)")
    Expect 'refresh' $refresh 401
    Expect 'refresh' $session.token 401
    $history='http://127.0.0.1:8081/api/v1/user/charge/history'
    Invoke-RestMethod -Uri $history -Headers @{Authorization="Bearer $($session.token)"} | Out-Null
    $profileUrl='http://127.0.0.1:8081/api/v1/user/profile'
    $authHeaders=@{Authorization="Bearer $($session.token)"}
    $empty=(Invoke-RestMethod -Uri $profileUrl -Headers $authHeaders).data
    if($empty.wallet.available_cents -ne 0 -or $empty.phone_bound -or $empty.coupon_unused_count -ne 0){throw 'New user profile defaults incorrect'}
    Sql @"
INSERT INTO wallet_account (user_id,balance_cents,frozen_cents) VALUES ($id,4123,100);
INSERT INTO coupon (code,name,discount_type,discount_value_cents) VALUES ('$tag','profile test','amount',100);
SET @coupon=LAST_INSERT_ID();
INSERT INTO coupon_grant (coupon_id,user_id,grant_source,status,expired_at,deleted_at) VALUES
(@coupon,$id,'manual','unused',UTC_TIMESTAMP()+INTERVAL 1 DAY,NULL),
(@coupon,$id,'manual','unused',UTC_TIMESTAMP()-INTERVAL 1 DAY,NULL),
(@coupon,$id,'manual','used',UTC_TIMESTAMP()+INTERVAL 1 DAY,NULL),
(@coupon,$id,'manual','unused',UTC_TIMESTAMP()+INTERVAL 1 DAY,UTC_TIMESTAMP());
INSERT INTO membership_card (user_id,card_type,start_at,end_at,price_cents) VALUES ($id,'month',UTC_TIMESTAMP()-INTERVAL 1 DAY,UTC_TIMESTAMP()+INTERVAL 1 DAY,100);
UPDATE user SET phone_hash='unverified-client-hash' WHERE id=$id;
"@ | Out-Null
    $profile=(Invoke-RestMethod -Uri $profileUrl -Headers $authHeaders).data
    if($profile.wallet.available_cents -ne 4123 -or $profile.wallet.frozen_cents -ne 100 -or $profile.coupon_unused_count -ne 1 -or $profile.phone_bound -or $profile.membership_card.card_type -ne 'month'){throw 'Profile aggregates incorrect'}
    if($profile.PSObject.Properties['openid'] -or $profile.PSObject.Properties['phone_hash']){throw 'Profile exposed private identity fields'}
    Sql "UPDATE coupon SET status='disabled' WHERE code='$tag'; INSERT INTO wallet_account (user_id,balance_cents) VALUES ($id,0);" | Out-Null
    $duplicateStatus=200
    try {Invoke-RestMethod -Uri $profileUrl -Headers $authHeaders | Out-Null} catch {$duplicateStatus=[int]$_.Exception.Response.StatusCode}
    if($duplicateStatus -ne 409){throw 'Duplicate wallet was silently selected'}
    Sql "DELETE FROM wallet_account WHERE user_id=$id AND balance_cents=0;" | Out-Null
    if((Invoke-RestMethod -Uri $profileUrl -Headers $authHeaders).data.coupon_unused_count -ne 0){throw 'Disabled coupon counted as usable'}
    Sql @"
SET @wallet=(SELECT id FROM wallet_account WHERE user_id=$id AND deleted_at IS NULL);
INSERT INTO wallet_txn (txn_no,user_id,wallet_account_id,direction,amount_cents,balance_after_cents,biz_type,created_month,created_at) VALUES
('${tag}_in',$id,@wallet,'in',5000,5000,'recharge',DATE_FORMAT(UTC_DATE(),'%Y-%m-01'),UTC_TIMESTAMP()-INTERVAL 2 MINUTE),
('${tag}_out',$id,@wallet,'out',877,4123,'pay',DATE_FORMAT(UTC_DATE(),'%Y-%m-01'),UTC_TIMESTAMP()-INTERVAL 1 MINUTE),
('${tag}_other',0,@wallet,'out',100,0,'pay',DATE_FORMAT(UTC_DATE(),'%Y-%m-01'),UTC_TIMESTAMP());
"@ | Out-Null
    $balance=(Invoke-RestMethod -Uri 'http://127.0.0.1:8081/api/v1/user/wallet/balance' -Headers $authHeaders).data
    if($balance.available_cents -ne 4123 -or $balance.frozen_cents -ne 100){throw 'Wallet balance incorrect'}
    $txns='http://127.0.0.1:8081/api/v1/user/wallet/txns'
    $first=(Invoke-RestMethod -Uri ($txns+'?page=1&page_size=1') -Headers $authHeaders).data
    $second=(Invoke-RestMethod -Uri ($txns+'?page=2&page_size=1') -Headers $authHeaders).data
    if($first.total -ne 2 -or $first.items[0].txn_no -ne "${tag}_out" -or $first.items[0].amount_cents -ne -877 -or $second.items[0].txn_no -ne "${tag}_in"){throw 'Wallet pagination, ownership or signed amount failed'}
    $filtered=(Invoke-RestMethod -Uri ($txns+'?type=consume') -Headers $authHeaders).data
    if($filtered.total -ne 1 -or $filtered.items[0].txn_type -ne 'consume'){throw 'Wallet type filter failed'}
    foreach($query in @('?page=0','?page_size=0','?page_size=101','?type=invalid','?user_id=0')){
        $status=200
        try{Invoke-RestMethod -Uri ($txns+$query) -Headers $authHeaders | Out-Null}catch{$status=[int]$_.Exception.Response.StatusCode}
        if($status -ne 400){throw "Wallet validation failed for $query"}
    }
    Request 'logout' $refresh | Out-Null
    $accessStatus=200
    try {Invoke-RestMethod -Uri $history -Headers @{Authorization="Bearer $($session.token)"} | Out-Null}
    catch {$accessStatus=[int]$_.Exception.Response.StatusCode}
    if($accessStatus -ne 401){throw 'Logout did not immediately revoke access JWT'}
    Expect 'refresh' $session.refresh_token 401
    $identity | docker compose -f $taskCompose exec -T redis-cache redis-cli -x SET $key | Out-Null
    docker compose -f $taskCompose exec -T redis-cache redis-cli EXPIRE $key 60 | Out-Null
    docker compose -f $taskCompose exec -T redis-cache redis-cli SET $sessionKey $refresh EX 60 | Out-Null
    $access=(Request 'refresh' $refresh).data
    $keys.Add("auth:user:refresh:$($access.refresh_token)")
    Sql "UPDATE ``user`` SET status='frozen' WHERE openid='$tag';" | Out-Null
    $frozenStatus=200
    try {Invoke-RestMethod -Uri $history -Headers @{Authorization="Bearer $($access.token)"} | Out-Null}
    catch {$frozenStatus=[int]$_.Exception.Response.StatusCode}
    if($frozenStatus -ne 403){throw 'Frozen account retained access'}
    Expect 'refresh' $access.refresh_token 403
    Expect 'refresh' $access.refresh_token 401
    Write-Output 'PASS: refresh rotation, replay rejection, JWT rejection, logout revocation and frozen-account rejection.'
} finally {
    foreach($key in $keys){docker compose -f $taskCompose exec -T redis-cache redis-cli DEL $key | Out-Null}
    if($id){Sql "DELETE FROM wallet_txn WHERE txn_no IN ('${tag}_in','${tag}_out','${tag}_other'); DELETE FROM wallet_account WHERE user_id=$id; DELETE FROM coupon_grant WHERE user_id=$id; DELETE FROM membership_card WHERE user_id=$id; DELETE FROM coupon WHERE code='$tag';" | Out-Null}
    Sql "DELETE FROM ``user`` WHERE openid='$tag';" | Out-Null
}

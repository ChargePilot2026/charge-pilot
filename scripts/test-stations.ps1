$ErrorActionPreference='Stop'
$taskCompose=Join-Path (Split-Path -Parent $PSScriptRoot) 'compose.dev.yaml'
$tag='test_station_'+[guid]::NewGuid().ToString('N')
$stationId=$null
function Sql([string]$query){
    $result=$query | docker compose -f $taskCompose exec -T mysql sh -c 'export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"; exec mysql -uroot --batch --skip-column-names admin_db'
    if($LASTEXITCODE -ne 0){throw 'Station fixture SQL failed'}
    return $result
}
function Expect-Forbidden([string]$method,[string]$url,$headers,$body) {
    $status=200
    try {Invoke-RestMethod -Method $method -Uri $url -Headers $headers -ContentType application/json -Body $body | Out-Null}
    catch {if(!$_.Exception.Response){throw};$status=[int]$_.Exception.Response.StatusCode}
    if($status -ne 403){throw "Expected $method $url HTTP 403, got $status"}
}
try {
    $login=Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:8082/api/v1/admin/auth/login' -ContentType application/json -Body (@{username='admin';password='DevAdmin2026!'}|ConvertTo-Json)
    $headers=@{Authorization="Bearer $($login.data.token)"}
    $base='http://127.0.0.1:8082/api/v1/admin/stations'
    $body=@{code=$tag;name='测试站点';longitude=116.41234567;latitude=39.91234567;contact_phone='010-12345678';open_hours='09:00-22:00';status='construction'}
    $client=[Net.Http.HttpClient]::new()
    try {
        $client.DefaultRequestHeaders.Authorization=[Net.Http.Headers.AuthenticationHeaderValue]::new('Bearer',$login.data.token)
        $json=$body|ConvertTo-Json
        $a=$client.PostAsync($base,[Net.Http.StringContent]::new($json,[Text.Encoding]::UTF8,'application/json'))
        $b=$client.PostAsync($base,[Net.Http.StringContent]::new($json,[Text.Encoding]::UTF8,'application/json'))
        [Threading.Tasks.Task]::WaitAll(@($a,$b))
        $responses=@($a.Result,$b.Result)
        $success=$responses|Where-Object {$_.IsSuccessStatusCode}
        if($success){$stationId=($success[0].Content.ReadAsStringAsync().Result|ConvertFrom-Json).data.id}
        if($success.Count -ne 1 -or ($responses|Where-Object {[int]$_.StatusCode -eq 409}).Count -ne 1){throw 'Concurrent station creation was not serialized'}
    } finally {$client.Dispose()}
    if(!$stationId){throw 'Creation returned no id'}
    $detail=(Invoke-RestMethod -Uri "$base/$stationId" -Headers $headers).data
    if($detail.longitude -ne 116.41234567 -or $detail.latitude -ne 39.91234567){throw 'Coordinates lost precision'}
    $duplicate=200
    try{Invoke-RestMethod -Method Post -Uri $base -Headers $headers -ContentType application/json -Body ($body|ConvertTo-Json)|Out-Null}catch{$duplicate=[int]$_.Exception.Response.StatusCode}
    if($duplicate -ne 409){throw 'Duplicate station code accepted'}
    $body.code+='bad';$body.latitude=91
    $invalid=200
    try{Invoke-RestMethod -Method Post -Uri $base -Headers $headers -ContentType application/json -Body ($body|ConvertTo-Json)|Out-Null}catch{$invalid=[int]$_.Exception.Response.StatusCode}
    if($invalid -ne 400){throw 'Invalid coordinates accepted'}
    $update=@{name='编辑后站点';status='disabled';address='';contact_phone='';open_hours=''}|ConvertTo-Json
    1..2|ForEach-Object {Invoke-RestMethod -Method Put -Uri "$base/$stationId" -Headers $headers -ContentType application/json -Body $update|Out-Null}
    $item=(Invoke-RestMethod -Uri $base -Headers $headers).data.items|Where-Object id -eq $stationId
    if($item.name -ne '编辑后站点' -or $item.status -ne 'disabled' -or $item.contact_phone -or $item.open_hours){throw 'Station update/clear did not persist'}
    $audit=Sql "SELECT COUNT(*) FROM audit_log WHERE module='station' AND target_id='$stationId';"
    if($audit -ne '3'){throw 'Station mutations missing audit'}
    Sql @"
INSERT INTO role (code,name) VALUES ('$tag','Station test reader');
SET @role=LAST_INSERT_ID();
INSERT INTO role_permission (role_id,permission_id) SELECT @role,id FROM permission WHERE code='station.read';
INSERT INTO admin_user_role (username,password_hash,role_id,status) SELECT '${tag}_reader',password_hash,@role,'active' FROM admin_user_role WHERE username='admin' AND deleted_at IS NULL LIMIT 1;
"@ | Out-Null
    $readerLogin=Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:8082/api/v1/admin/auth/login' -ContentType application/json -Body (@{username="${tag}_reader";password='DevAdmin2026!'}|ConvertTo-Json)
    $readerHeaders=@{Authorization="Bearer $($readerLogin.data.token)"}
    $readerList=(Invoke-RestMethod -Uri $base -Headers $readerHeaders).data
    if($readerList.permissions.Count -ne 1 -or $readerList.permissions[0] -ne 'station.read'){throw 'Read-only capability list incorrect'}
    Invoke-RestMethod -Uri "$base/$stationId" -Headers $readerHeaders | Out-Null
    Expect-Forbidden 'Post' $base $readerHeaders ($body|ConvertTo-Json)
    Expect-Forbidden 'Put' "$base/$stationId" $readerHeaders $update
    Expect-Forbidden 'Delete' "$base/$stationId" $readerHeaders $null
    Sql "INSERT INTO role_permission (role_id,permission_id) SELECT r.id,p.id FROM role r JOIN permission p ON p.code='station.update' WHERE r.code='$tag';"|Out-Null
    Invoke-RestMethod -Method Put -Uri "$base/$stationId" -Headers $readerHeaders -ContentType application/json -Body $update|Out-Null
    Sql "DELETE rp FROM role_permission rp JOIN role r ON r.id=rp.role_id JOIN permission p ON p.id=rp.permission_id WHERE r.code='$tag' AND p.code='station.update';"|Out-Null
    Expect-Forbidden 'Put' "$base/$stationId" $readerHeaders $update
    Sql "UPDATE admin_user_role SET status='disabled' WHERE username='${tag}_reader';"|Out-Null
    Expect-Forbidden 'Get' $base $readerHeaders $null
    Sql "UPDATE admin_user_role SET status='active' WHERE username='${tag}_reader'; UPDATE role SET deleted_at=UTC_TIMESTAMP() WHERE code='$tag';"|Out-Null
    Expect-Forbidden 'Get' "$base/$stationId" $readerHeaders $null
    $batchValues=(1..205 | ForEach-Object {"('${tag}_batch_$_','Page test $_','${tag}_address',116.4,39.9,'active')"}) -join ','
    Sql "INSERT INTO station (code,name,address,longitude,latitude,status) VALUES $batchValues; INSERT INTO station(code,name,longitude,latitude,deleted_at) VALUES ('${tag}_deleted','deleted',116.4,39.9,UTC_TIMESTAMP());"|Out-Null
    $first=(Invoke-RestMethod -Uri ($base+'?keyword='+$tag+'&page=1&page_size=100') -Headers $headers).data
    $last=(Invoke-RestMethod -Uri ($base+'?keyword='+$tag+'&page=3&page_size=100') -Headers $headers).data
    if($first.total -ne 206 -or $first.items.Count -ne 100 -or $last.items.Count -ne 6 -or !($last.items|Where-Object id -eq $stationId)){throw 'Station pagination still truncates after 200 or includes deleted rows'}
    $byAddress=(Invoke-RestMethod -Uri ($base+'?keyword='+$tag+'_address&status=active') -Headers $headers).data
    if($byAddress.total -ne 205){throw 'Station address search failed'}
    $disabled=(Invoke-RestMethod -Uri ($base+'?keyword='+$tag+'&status=disabled') -Headers $headers).data
    if($disabled.total -ne 1 -or $disabled.items[0].id -ne $stationId){throw 'Station status filter failed'}
    $literal=(Invoke-RestMethod -Uri ($base+'?keyword='+$tag+'%25') -Headers $headers).data
    if($literal.total -ne 0){throw 'Search interpreted literal percent as wildcard'}
    foreach($query in @('?page=0','?page_size=0','?page_size=101','?status=invalid')){
        $status=200
        try{Invoke-RestMethod -Uri ($base+$query) -Headers $headers|Out-Null}catch{$status=[int]$_.Exception.Response.StatusCode}
        if($status -ne 400){throw "Station query validation failed $query"}
    }
    Write-Output 'PASS: create, coordinate precision, duplicate rejection, validation, edit, disable, clear fields and audit.'
}finally{
    Sql "DELETE FROM admin_user_role WHERE username='${tag}_reader'; DELETE rp FROM role_permission rp JOIN role r ON r.id=rp.role_id WHERE r.code='$tag'; DELETE FROM role WHERE code='$tag';"|Out-Null
    if($stationId){Sql "DELETE FROM audit_log WHERE module='station' AND target_id='$stationId'; DELETE FROM station WHERE id=$stationId AND code='$tag';"|Out-Null}
    Sql "DELETE FROM station WHERE LEFT(code,CHAR_LENGTH('$tag'))='$tag';"|Out-Null
    Sql "DELETE FROM station_code_identity WHERE code='$tag';"|Out-Null
}

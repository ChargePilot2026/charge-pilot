$ErrorActionPreference='Stop'
$taskCompose=Join-Path (Split-Path -Parent $PSScriptRoot) 'compose.dev.yaml'
$tag='devread_'+[guid]::NewGuid().ToString('N').Substring(0,12)
function Sql([string]$q){
 $result=$q | docker compose -f $taskCompose exec -T mysql sh -c 'export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"; exec mysql -uroot --batch --skip-column-names admin_db'
 if($LASTEXITCODE -ne 0){throw 'Device read fixture SQL failed'}
 return $result
}
function ExpectStatus([string]$url,$headers,[int]$expected){
 $status=200
 try{Invoke-RestMethod -Uri $url -Headers $headers|Out-Null}catch{if(!$_.Exception.Response){throw};$status=[int]$_.Exception.Response.StatusCode}
 if($status -ne $expected){throw "Expected HTTP $expected, got $status at $url"}
}
$base='http://127.0.0.1:8082/api/v1/admin/devices'
try{
 $login=Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:8082/api/v1/admin/auth/login' -ContentType application/json -Body (@{username='admin';password='DevAdmin2026!'}|ConvertTo-Json)
 $headers=@{Authorization="Bearer $($login.data.token)"}
 $station=Sql "INSERT INTO station(code,name,longitude,latitude) VALUES ('$tag','${tag}_station',116,39); SELECT LAST_INSERT_ID();"
 $values=(1..505|ForEach-Object{"('${tag}_$_',$station,1,'${tag}_model','enabled')"}) -join ','
 Sql "INSERT INTO device_meta(device_id,station_id,vendor_id,model,status) VALUES $values; UPDATE device_meta SET status='disabled',vendor_id=2,install_at='2026-09-26 01:02:03' WHERE device_id='${tag}_1'; INSERT INTO device_meta(device_id,deleted_at) VALUES ('${tag}_deleted',UTC_TIMESTAMP());"|Out-Null
 $a=(Invoke-RestMethod -Uri ($base+'?keyword='+$tag+'&page_size=100') -Headers $headers).data
 $b=(Invoke-RestMethod -Uri ($base+'?keyword='+$tag+'&page=6&page_size=100') -Headers $headers).data
 if($a.total -ne 505 -or $a.items.Count -ne 100 -or $b.items.Count -ne 5 -or $b.items[-1].device_id -ne "${tag}_1"){throw 'Pagination truncated or ordering invalid'}
 if($a.items[0].station_name -ne "${tag}_station"){throw 'Station name missing'}
 foreach($filter in @('status=disabled','vendor_id=2')){
  $d=(Invoke-RestMethod -Uri ($base+'?keyword='+$tag+'&'+$filter) -Headers $headers).data
  if($d.total -ne 1 -or $d.items[0].device_id -ne "${tag}_1"){throw "Filter failed $filter"}
 }
 foreach($keyword in @("${tag}_station","${tag}_model")){
  $d=(Invoke-RestMethod -Uri ($base+'?keyword='+$keyword+'&station_id='+$station) -Headers $headers).data
  if($d.total -ne 505){throw 'Station/model search failed'}
 }
 $d=(Invoke-RestMethod -Uri ($base+'?keyword='+$tag+'%25') -Headers $headers).data
 if($d.total -ne 0){throw 'Literal percent interpreted as wildcard'}
 $detail=(Invoke-RestMethod -Uri "$base/${tag}_1" -Headers $headers).data
 if(!$detail.install_at -or $detail.vendor_id -ne 2){throw 'Detail decoding failed'}
 ExpectStatus "$base/${tag}_deleted" $headers 404
 foreach($q in @('?page=0','?page_size=101','?station_id=0','?vendor_id=0','?status=online','?unexpected=yes')){ExpectStatus ($base+$q) $headers 400}
 $orders=(Invoke-RestMethod -Uri "$base/${tag}_1/orders" -Headers $headers).data
 if($orders.total -ne 0 -or $null -eq $orders.items){throw 'Device orders envelope invalid'}
 Sql "INSERT INTO user_db.charge_order(order_no,user_id,device_id,port_no,status,created_month) VALUES ('${tag}_order1',0,'${tag}_1',1,'pending_payment',DATE_FORMAT(UTC_DATE(),'%Y-%m-01')),('${tag}_order2',0,'${tag}_2',1,'pending_payment',DATE_FORMAT(UTC_DATE(),'%Y-%m-01'));"|Out-Null
 $orders=(Invoke-RestMethod -Uri "$base/${tag}_1/orders?device_id=${tag}_2&device_ids=${tag}_2" -Headers $headers).data
 if($orders.total -ne 1 -or $orders.items[0].device_id -ne "${tag}_1"){throw 'Device order path scope was overridden'}
 Sql "INSERT INTO role(code,name) VALUES ('$tag','Device reader test'); INSERT INTO admin_user_role(username,password_hash,role_id,status) SELECT '$tag',a.password_hash,r.id,'active' FROM admin_user_role a JOIN role r ON r.code='$tag' WHERE a.username='admin' AND a.deleted_at IS NULL LIMIT 1;"|Out-Null
 $reader=Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:8082/api/v1/admin/auth/login' -ContentType application/json -Body (@{username=$tag;password='DevAdmin2026!'}|ConvertTo-Json)
 $rh=@{Authorization="Bearer $($reader.data.token)"}
 ExpectStatus $base $rh 403
 ExpectStatus "$base/${tag}_1" $rh 403
 Sql "INSERT INTO role_permission(role_id,permission_id) SELECT r.id,p.id FROM role r JOIN permission p ON p.code='device.read' WHERE r.code='$tag';"|Out-Null
 ExpectStatus $base $rh 200
 ExpectStatus "$base/${tag}_1/orders" $rh 403
 Sql "DELETE rp FROM role_permission rp JOIN role r ON r.id=rp.role_id WHERE r.code='$tag';"|Out-Null
 ExpectStatus $base $rh 403
 Sql "UPDATE station SET deleted_at=UTC_TIMESTAMP() WHERE code='$tag';"|Out-Null
 $detail=(Invoke-RestMethod -Uri "$base/${tag}_1" -Headers $headers).data
 if($detail.station_name){throw 'Deleted station leaked in device detail'}
 Write-Output 'PASS: 505-device pagination, literal search, station/model/status/vendor filters, detail, deleted rows, validation and live read permissions.'
}finally{
 Sql "DELETE FROM user_db.charge_order WHERE order_no IN ('${tag}_order1','${tag}_order2');"|Out-Null
 Sql "DELETE FROM device_meta WHERE LEFT(device_id,CHAR_LENGTH('$tag'))='$tag'; DELETE FROM station WHERE code='$tag'; DELETE FROM admin_user_role WHERE username='$tag'; DELETE rp FROM role_permission rp JOIN role r ON r.id=rp.role_id WHERE r.code='$tag'; DELETE FROM role WHERE code='$tag';"|Out-Null
}

$ErrorActionPreference='Stop'
$taskCompose=Join-Path (Split-Path -Parent $PSScriptRoot) 'compose.dev.yaml'
$tag='quote_'+[guid]::NewGuid().ToString('N').Substring(0,12)
function Sql([string]$q){$r=$q|docker compose -f $taskCompose exec -T mysql sh -c 'export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"; exec mysql -uroot --batch --skip-column-names';if($LASTEXITCODE -ne 0){throw 'Pricing fixture SQL failed'};return $r}
$serviceToken=(docker compose -f $taskCompose exec -T billing printenv SERVICE_TOKEN).Trim()
$headers=@{'X-Service-Token'=$serviceToken}
$base='http://127.0.0.1:8084/api/v1/internal/quote'
function Quote { (Invoke-RestMethod -Method Post -Uri $base -Headers $headers -ContentType application/json -Body ($script:body|ConvertTo-Json)).data }
function Expect([int]$expected){$status=200;try{Quote|Out-Null}catch{if(!$_.Exception.Response){throw};$status=[int]$_.Exception.Response.StatusCode};if($status -ne $expected){throw "Expected quote HTTP $expected got $status"}}
function RejectStart($body,[int]$expected){
 $status=200
 try{Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:8081/api/v1/user/scan/start' -Headers $userHeaders -ContentType application/json -Body ($body|ConvertTo-Json)|Out-Null}catch{if(!$_.Exception.Response){throw};$status=[int]$_.Exception.Response.StatusCode}
 if($status -ne $expected){throw "Expected checkout rejection $expected got $status"}
}
try{
 Sql @"
INSERT INTO gateway_db.vendor(vendor_code,vendor_name,adapter_class) VALUES ('$tag','pricing-test','test'); SET @vendor=LAST_INSERT_ID();
INSERT INTO gateway_db.device(device_id,vendor_id) VALUES ('$tag',@vendor);
INSERT INTO gateway_db.device_port(device_id,port_no,port_code) VALUES ('$tag',1,'${tag}:1');
INSERT INTO admin_db.station(code,name,longitude,latitude) VALUES ('$tag','pricing-test',116,39);SET @station=LAST_INSERT_ID();
INSERT INTO admin_db.device_meta(device_id,station_id) VALUES ('$tag',@station);
"@|Out-Null
 $script:body=@{port_id="${tag}:1";user_id=123;estimated_kwh='0.500';estimated_minutes=120}
 Expect 404
 Sql "INSERT INTO admin_db.pricing_rule(name,station_id,mode,time_of_use_json,service_fee_cents_per_kwh,min_charge_cents) SELECT '$tag',id,'kwh',JSON_ARRAY(JSON_OBJECT('period','all','start','00:00','end','24:00','electric_price_cents',100)),40,0 FROM admin_db.station WHERE code='$tag';"|Out-Null
 $quote=Quote
 if($quote.total_cents -ne 70 -or $quote.electric_cents -ne 50 -or $quote.service_cents -ne 20 -or $quote.pricing.name -ne $tag -or !$quote.quote_expires_at){throw 'Quote ignored actual station tariff'}
 $userId=Sql "INSERT INTO user_db.user(openid) VALUES ('$tag'); SELECT LAST_INSERT_ID();"
 $refresh='RT_'+[guid]::NewGuid().ToString('N')+[guid]::NewGuid().ToString('N');$sid=[guid]::NewGuid().ToString()
 $identity=@{user_id=[long]$userId;openid=$tag;sid=$sid}|ConvertTo-Json -Compress
 $identity | docker compose -f $taskCompose exec -T redis-cache redis-cli -x SET "auth:user:refresh:$refresh"|Out-Null
 docker compose -f $taskCompose exec -T redis-cache redis-cli SET "auth:user:session:$sid" $refresh EX 300|Out-Null
 $session=(Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:8081/api/v1/public/auth/refresh' -Headers @{Authorization="Bearer $refresh"}).data
 $userHeaders=@{Authorization="Bearer $($session.token)"}
 $publicBody=@{port_id="${tag}:1";estimated_kwh='0.500';estimated_minutes=120}|ConvertTo-Json
 $public=(Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:8081/api/v1/user/scan/quote' -Headers $userHeaders -ContentType application/json -Body $publicBody).data
 if($public.total_cents -ne 70 -or $public.pricing.rule_id -ne $quote.pricing.rule_id){throw 'Public quote did not unwrap billing response'}
 if(!$public.quote_id){throw 'Public quote missing confirmation id'}
 $start=@{quote_id=$public.quote_id;port_id="${tag}:1";estimated_kwh='0.500';estimated_minutes=120}
 $quoteKey="charge:quote:$($public.quote_id)"
 $originalQuote=docker compose -f $taskCompose exec -T redis-cache redis-cli GET $quoteKey
 $start.estimated_minutes=121;RejectStart $start 409;$start.estimated_minutes=120
 $saved=$originalQuote|ConvertFrom-Json;$saved.user_id=0
 ($saved|ConvertTo-Json -Depth 20 -Compress)|docker compose -f $taskCompose exec -T redis-cache redis-cli -x SET $quoteKey|Out-Null
 RejectStart $start 404
 $saved=$originalQuote|ConvertFrom-Json;$saved.expires_at=[DateTimeOffset]::UtcNow.AddSeconds(-1).ToString('o')
 ($saved|ConvertTo-Json -Depth 20 -Compress)|docker compose -f $taskCompose exec -T redis-cache redis-cli -x SET $quoteKey|Out-Null
 RejectStart $start 409
 $originalQuote|docker compose -f $taskCompose exec -T redis-cache redis-cli -x SET $quoteKey|Out-Null
 # Never create a real provider order in this fixture test. Only exercise the
 # pre-write configuration rejection when no merchant private key is configured.
 $configuredKey=docker compose -f $taskCompose exec -T user sh -c 'printf "%s" "$WECHAT_PRIVATE_KEY_PATH"'
 if(!$configuredKey){RejectStart $start 500}
 Sql "UPDATE admin_db.pricing_rule SET min_charge_cents=100 WHERE name='$tag';"|Out-Null
 $quote=Quote;if($quote.total_cents -ne 100 -or $quote.service_cents -ne 50){throw 'Minimum charge components do not sum'}
 RejectStart $start 409
 $hold=docker compose -f $taskCompose exec -T redis-cache redis-cli EXISTS "charge:hold:port_${tag}:1"
 if([int]$hold -ne 0){throw 'Rejected confirmation reserved the port'}
 $script:body.estimated_kwh='1e2';Expect 400;$script:body.estimated_kwh='0.500'
 $script:body.estimated_minutes=0;Expect 400;$script:body.estimated_minutes=120
 Sql "UPDATE admin_db.pricing_rule SET status='disabled' WHERE name='$tag';"|Out-Null;Expect 404
 Sql "UPDATE admin_db.pricing_rule SET status='active',effective_from=UTC_TIMESTAMP()+INTERVAL 1 DAY WHERE name='$tag';"|Out-Null;Expect 404
 Sql "UPDATE admin_db.pricing_rule SET effective_from=NULL,effective_to=UTC_TIMESTAMP()-INTERVAL 1 SECOND WHERE name='$tag';"|Out-Null;Expect 404
 Sql "UPDATE admin_db.pricing_rule SET effective_to=NULL WHERE name='$tag'; INSERT INTO admin_db.pricing_rule(name,station_id) SELECT '${tag}_duplicate',id FROM admin_db.station WHERE code='$tag';"|Out-Null;Expect 409
 Sql "DELETE FROM admin_db.pricing_rule WHERE name='${tag}_duplicate'; UPDATE admin_db.pricing_rule SET station_id=NULL WHERE name='$tag'; INSERT INTO admin_db.pricing_template(code,name,default_pricing_rule_id) SELECT '$tag','template-test',id FROM admin_db.pricing_rule WHERE name='$tag'; UPDATE admin_db.station SET pricing_template_id=(SELECT id FROM admin_db.pricing_template WHERE code='$tag') WHERE code='$tag';"|Out-Null
 $quote=Quote;if($quote.total_cents -ne 100){throw 'Template fallback did not resolve'}
 Sql "UPDATE admin_db.pricing_template SET deleted_at=UTC_TIMESTAMP() WHERE code='$tag';"|Out-Null;Expect 404
 Sql "UPDATE admin_db.pricing_template SET deleted_at=NULL WHERE code='$tag'; UPDATE admin_db.station SET status='disabled' WHERE code='$tag';"|Out-Null;Expect 404
 Sql "UPDATE admin_db.station SET status='active' WHERE code='$tag'; UPDATE gateway_db.device_port SET status='charging' WHERE port_code='${tag}:1';"|Out-Null
 $occupied=Invoke-RestMethod -Method Post -Uri $base -Headers $headers -ContentType application/json -Body ($script:body|ConvertTo-Json)
 if($occupied.code -ne 2001){throw 'Occupied port was quoted successfully'}
 $counts=Sql "SELECT COUNT(*) FROM user_db.charge_order WHERE device_id='$tag';"
 if([int]$counts -ne 0){throw 'Quote created a charge order'}
 Write-Output 'PASS: station pricing, integer amounts, minimum charge, validation, effective dates, conflict, template fallback, disabled resources, occupied port and read-only quoting.'
}finally{
 if($quoteKey){docker compose -f $taskCompose exec -T redis-cache redis-cli DEL $quoteKey|Out-Null}
 foreach($refreshValue in @($refresh,$session.refresh_token)){if($refreshValue){docker compose -f $taskCompose exec -T redis-cache redis-cli DEL "auth:user:refresh:$refreshValue"|Out-Null}}
 if($sid){docker compose -f $taskCompose exec -T redis-cache redis-cli DEL "auth:user:session:$sid"|Out-Null}
 Sql "DELETE FROM user_db.user WHERE openid='$tag';"|Out-Null
 Sql "DELETE FROM gateway_db.device_port WHERE device_id='$tag'; DELETE FROM gateway_db.device WHERE device_id='$tag'; DELETE FROM gateway_db.vendor WHERE vendor_code='$tag'; DELETE FROM admin_db.device_meta WHERE device_id='$tag'; DELETE FROM admin_db.station WHERE code='$tag'; DELETE FROM admin_db.pricing_template WHERE code='$tag'; DELETE FROM admin_db.pricing_rule WHERE name IN ('$tag','${tag}_duplicate');"|Out-Null
}

#!/bin/bash
# 探测 central 的 user 模块（小程序后端）对用户暴露的每一条路由。本脚本：
#   1. 不带任何 token 请求 /api/v1/public/auth/*。
#   2. 用与 user 服务相同的密钥签一个本地开发 JWT（user_id=1 /
#      openid=test_openid），并往 Redis-cache 里 SET 一个桩会话，
#      让会话中间件放行请求。
#   3. 走完全部 17 条 user_routes（profile、charge_*、wallet_*、coupon_*、
#      invoice_*、station_*、phone_*、announcement_*、customer-service、
#      device-fault-reports、scan_*），并打印 HTTP 状态码与响应信封里的 code。
#
# 用法：
#   bash scripts/probes/user_public.sh
#
# 退出码：恒为 0（单个端点失败不算致命，目的是把它们都暴露到表里）。
#
# 输出列：METHOD PATH HTTP_CODE [code=N msg=...]
#   "non-json" 表示请求体在到达处理函数之前就被拒了（例如本该是查询串的
#   位置发了 JSON body）。要修的是探针，不是服务端。
#
# 副作用：本脚本会往 Redis-cache 写 /auth：user：session：test_sid，并在
# user_db.user 里建一行用户数据。退出时会自行清理（尽力而为；被 kill -9
# 杀掉会留下残留）。
set -u
cd "$(dirname "$0")/../.."

USER_HOST="${USER_HOST:-http://localhost:8081}"
REDIS_HOST="${REDIS_HOST:-redis-cache}"
MYSQL_HOST="${MYSQL_HOST:-mysql}"
JWT_SECRET="${JWT_SECRET:-local-dev-jwt-secret-do-not-use-in-production}"
TEST_OPENID="test_openid_probe_$$"  # unique per run so we don't collide
TEST_SID="test_sid_probe_$$"
TEST_USER_ID=1

cleanup() {
    docker compose -f compose.dev.yaml exec -T "$REDIS_HOST" \
        redis-cli DEL "auth:user:session:$TEST_SID" >/dev/null 2>&1 || true
    docker compose -f compose.dev.yaml exec -T "$MYSQL_HOST" \
        mysql -uroot -pchargepilot_root_dev -e \
        "USE user_db; DELETE FROM user_login_identity WHERE openid='$TEST_OPENID'; DELETE FROM user WHERE openid='$TEST_OPENID';" \
        >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "=== seed: insert test user + Redis session ==="
docker compose -f compose.dev.yaml exec -T "$MYSQL_HOST" \
    mysql -uroot -pchargepilot_root_dev -e \
    "USE user_db; INSERT INTO user (openid, status) VALUES ('$TEST_OPENID', 'active'); INSERT INTO user_login_identity (openid) VALUES ('$TEST_OPENID');" \
    >/dev/null 2>&1
# 读回刚插入的 id（不存在 AUTO_INCREMENT 锁竞争的问题）。
TEST_USER_ID=$(docker compose -f compose.dev.yaml exec -T "$MYSQL_HOST" \
    mysql -uroot -pchargepilot_root_dev -N -B -e \
    "USE user_db; SELECT id FROM user WHERE openid='$TEST_OPENID' LIMIT 1;" 2>/dev/null | tr -d '[:space:]')
if [ -z "$TEST_USER_ID" ]; then
    echo "failed to read back inserted user id" >&2
    exit 1
fi
docker compose -f compose.dev.yaml exec -T "$REDIS_HOST" \
    redis-cli SET "auth:user:session:$TEST_SID" "stub_refresh_token" EX 3600 \
    >/dev/null 2>&1
echo "openid=$TEST_OPENID  sid=$TEST_SID  user_id=$TEST_USER_ID"
echo

# 用 user 服务相同的密钥签一个 HS256 的 JWT。
USER_TOKEN=$(python3 -c "
import hmac, hashlib, base64, json, time, sys
secret = sys.argv[1].encode()
header = {'alg': 'HS256', 'typ': 'JWT'}
payload = {
  'sub': sys.argv[2], 'user_id': int(sys.argv[3]), 'sid': sys.argv[4],
  'iat': int(time.time()), 'exp': int(time.time())+7200,
  'iss': 'chargepilot',
}
b64 = lambda d: base64.urlsafe_b64encode(json.dumps(d, separators=(',',':')).encode()).rstrip(b'=').decode()
signing = (b64(header)+'.'+b64(payload)).encode()
sig = base64.urlsafe_b64encode(hmac.new(secret, signing, hashlib.sha256).digest()).rstrip(b'=').decode()
print(b64(header)+'.'+b64(payload)+'.'+sig)
" "$JWT_SECRET" "$TEST_OPENID" "$TEST_USER_ID" "$TEST_SID")
echo

probe_public() {
    local method="$1" path="$2" body="${3:-}"
    local out
    out=$(curl -sS -m 6 -X "$method" "$USER_HOST$path" \
        -H 'Content-Type: application/json' -d "$body" \
        -w '|||%{http_code}|||' 2>&1)
    local code="${out##*|||}" body2="${out%|||*|||}"
    local err=""
    if [ "${code:0:1}" != "2" ]; then
        err=$(echo "$body2" | python3 -c "import sys,json
try:
  o=json.loads(sys.stdin.read())
  print('code='+str(o.get('code'))+' msg='+str(o.get('message','')))
except: print('non-json')" 2>/dev/null || echo "non-json")
    fi
    printf '%-7s %-50s HTTP %s %s\n' "$method" "$path" "$code" "$err"
}

probe_user() {
    local method="$1" path="$2" body="${3:-}"
    local out
    # 启发式判断：body 长得像查询串（在任何 '{' 之前就含 '='，或以 k=v 开头），
    # 就用 curl --data-urlencode 当 URL 查询串发，并跳过 JSON content-type。
    if [[ "$body" == *"="* && "$body" != *"{"* ]]; then
        out=$(curl -sS -m 6 -X "$method" "$USER_HOST$path" \
            -H "Authorization: Bearer $USER_TOKEN" \
            --data "$body" \
            -w '|||%{http_code}|||' 2>&1)
    else
        out=$(curl -sS -m 6 -X "$method" "$USER_HOST$path" \
            -H "Authorization: Bearer $USER_TOKEN" \
            -H 'Content-Type: application/json' -d "$body" \
            -w '|||%{http_code}|||' 2>&1)
    fi
    local code="${out##*|||}" body2="${out%|||*|||}"
    local err=""
    if [ "${code:0:1}" != "2" ]; then
        err=$(echo "$body2" | python3 -c "import sys,json
try:
  o=json.loads(sys.stdin.read())
  print('code='+str(o.get('code'))+' msg='+str(o.get('message','')))
except: print('non-json')" 2>/dev/null || echo "non-json")
    fi
    printf '%-7s %-50s HTTP %s %s\n' "$method" "$path" "$code" "$err"
}

echo "=== /api/v1/public/auth/* (no auth) ==="
probe_public POST /api/v1/public/auth/login        '{"code":"x","appid":"wx_local_dev"}'
probe_public POST /api/v1/public/auth/refresh      '{"token":"x"}'
probe_public POST /api/v1/public/auth/logout       ''

echo
echo "=== /api/v1/user/* (user JWT) ==="
probe_user GET    /api/v1/user/profile
probe_user GET    /api/v1/user/charge/history
probe_user GET    /api/v1/user/charge/ongoing
probe_user GET    '/api/v1/user/charge/ongoing/snapshot?order_id=1'
probe_user GET    '/api/v1/user/charge/ongoing/curve?order_id=1'
probe_user GET    /api/v1/user/charge/1
probe_user GET    /api/v1/user/charge/1/curve
probe_user POST   /api/v1/user/charge/1/feedback   '{"rating":5,"category":"x","content":"x"}'
probe_user POST   /api/v1/user/charge/stop         '{"order_no":"x"}'
probe_user GET    /api/v1/user/wallet/balance
probe_user GET    /api/v1/user/wallet/recharges
probe_user GET    /api/v1/user/wallet/txns
probe_user POST   /api/v1/user/wallet/recharge     '{"request_id":"00000000-0000-0000-0000-000000000001","amount_cents":1}'
probe_user POST   /api/v1/user/wallet/refund       '{"request_id":"00000000-0000-0000-0000-000000000001","amount_cents":1,"reason":"test"}'
probe_user GET    '/api/v1/user/wallet/refunds?page=1&page_size=10'
probe_user GET    /api/v1/user/coupon/my
probe_user POST   /api/v1/user/coupon/preview      '{"coupon_grant_id":1,"estimated_total_cents":1000}'
probe_user GET    /api/v1/user/invoice/my
probe_user POST   /api/v1/user/invoice/apply        '{"biz_type":"charge","biz_id":1,"total_cents":100,"invoice_type":"normal","title":"test"}'
probe_user GET    '/api/v1/user/station/nearby?lat=10&lng=20'
probe_user GET    /api/v1/user/station/1
probe_user GET    /api/v1/user/device/fault-reports
probe_user GET    /api/v1/user/device/fault-reports/1/history
probe_user POST   /api/v1/user/device/report-fault  '{"device_id":"x","fault_type":"broken","description":"x"}'
probe_user POST   /api/v1/user/phone/bind          '{"code":"x"}'
probe_user POST   /api/v1/user/phone/unbind        '{}'
probe_user GET    /api/v1/user/announcement/list
probe_user POST   /api/v1/user/customer-service/entry '{"scene":"general"}'
probe_user POST   /api/v1/user/scan/resolve        '{"code":"x"}'
probe_user POST   /api/v1/user/scan/port           '{"port_id":"x"}'
probe_user POST   /api/v1/user/scan/quote          '{"port_id":"x","estimated_kwh":"1.0","estimated_minutes":60}'
probe_user POST   /api/v1/user/scan/start          '{"port_id":"x","quote_id":"q","estimated_kwh":"1.0","estimated_minutes":60,"coupon_grant_id":null}'
probe_user POST   /api/v1/user/scan/cancel         '{"order_no":"x"}'

echo
echo "--- DONE ---"

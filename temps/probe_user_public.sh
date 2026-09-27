#!/bin/bash
# Probe every user-facing route exposed by services/user (the miniprogram
# backend). The script:
#   1. Hits /api/v1/public/auth/* without any token.
#   2. Mints a local-dev JWT (same secret as the user service) for
#      user_id=1 / openid=test_openid and SETs a stub session into
#      Redis-cache so the session middleware accepts the request.
#   3. Walks all 17 user_routes (profile, charge_*, wallet_*, coupon_*,
#      invoice_*, station_*, phone_*, announcement_*, customer-service,
#      device-fault-reports, scan_*) and prints HTTP status + envelope
#      code.
#
# Usage:
#   bash temps/probe_user_public.sh
#
# Exit code: 0 always (per-endpoint failures are not fatal; the goal is
# to surface them in the table).
#
# Output columns: METHOD PATH HTTP_CODE [code=N msg=...]
#   "non-json" indicates the request body was rejected by axum before
#   reaching the handler (e.g. JSON body sent where a query string is
#   expected). Fix the probe, not the server.
#
# Side effects: this script writes /auth:user:session:test_sid into
# Redis-cache and creates a user row in user_db.user. It cleans up
# after itself on exit (best effort; kill -9 leaves residue).
set -u

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
# Read back the id we just inserted (no AUTO_INCREMENT lock to worry about).
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

# Mint a JWT (HS256) with the same secret the user service uses.
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
    # Heuristic: if body looks like query string (contains '=' before any '{' or starts with k=v),
    # pass it as URL query via curl --data-urlencode and skip JSON content-type.
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
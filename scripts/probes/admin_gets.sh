#!/bin/bash
# Probe every admin-web "list" / "get" / "detail" GET endpoint and report status.
#
# Usage:
#   TOKEN=$(curl -sS -m 5 -X POST http://localhost:8082/api/v1/admin/auth/login \
#     -H 'Content-Type: application/json' \
#     -d '{"username":"admin","password":"DevAdmin2026!"}' \
#     | python3 -c 'import sys,json; print(json.load(sys.stdin)["data"]["token"])')
#   echo "$TOKEN" > .codewave/temps/token.txt
#   bash scripts/probes/admin_gets.sh
#
# Exit codes:
#   0  probe finished (regardless of individual endpoint failures)
#   1  no token file found
#
# Output columns: METHOD PATH HTTP_CODE [code=N msg=...]
#   code=N is the ApiEnvelope.code; msg is the human-readable message.
#   "non-json" indicates the server returned HTML / non-JSON (likely SPA
#   fallback from static_serve::serve_spa — usually a wrong path).
set -u
cd "$(dirname "$0")/../.."

TOKEN_FILE="${TOKEN_FILE:-.codewave/temps/token.txt}"
TOKEN=$(cat "$TOKEN_FILE" 2>/dev/null)
if [ -z "$TOKEN" ]; then
    echo "no token at $TOKEN_FILE (set TOKEN_FILE or write token there first)" >&2
    exit 1
fi
HOST="${PROBE_HOST:-http://localhost:8082}"
hdr=(-H "Authorization: Bearer $TOKEN")

probe() {
    local method="$1"; shift
    local path="$1"; shift
    local label="${1:-$path}"
    local out
    out=$(curl -sS -m 6 -X "$method" "$HOST$path" "${hdr[@]}" -w '|||%{http_code}|||' 2>&1)
    local code="${out##*|||}"
    local body="${out%|||*|||}"
    local first_err=""
    if [ "${code:0:1}" != "2" ]; then
        first_err=$(echo "$body" | python3 -c "import sys,json
try:
  o=json.loads(sys.stdin.read())
  print('code='+str(o.get('code'))+' msg='+str(o.get('message','')))
except Exception:
  print('non-json')" 2>/dev/null || echo "non-json")
    fi
    printf '%-7s %-50s  HTTP %s   %s\n' "$method" "$path" "$code" "$first_err"
}

probe GET    /api/v1/admin/dashboard
probe GET    /api/v1/admin/users
probe GET    /api/v1/admin/users/1
probe GET    /api/v1/admin/roles
probe GET    /api/v1/admin/roles/1
probe GET    /api/v1/admin/permissions
probe GET    /api/v1/admin/stations
probe GET    /api/v1/admin/stations/1
probe GET    /api/v1/admin/devices
probe GET    /api/v1/admin/devices/test-device
probe GET    /api/v1/admin/devices/test-device/orders
probe GET    /api/v1/admin/orders
probe GET    /api/v1/admin/device-imports
probe GET    /api/v1/admin/orders/1
probe GET    /api/v1/admin/orders/1/timeline
probe GET    /api/v1/admin/billing/settlements
probe GET    /api/v1/admin/billing/withdraw
probe GET    /api/v1/admin/billing/refunds
probe GET    /api/v1/admin/billing/invoices
probe GET    /api/v1/admin/billing/reconcile-logs
probe GET    /api/v1/admin/alerts
probe GET    /api/v1/admin/alert-rules
probe GET    /api/v1/admin/alert-subscriptions
probe GET    /api/v1/admin/risk-config
probe GET    /api/v1/admin/coupons
probe GET    /api/v1/admin/coupons/1/stats
probe GET    /api/v1/admin/membership
probe GET    /api/v1/admin/settings/charge-rules
probe GET    /api/v1/admin/settings/pricing-templates
probe GET    /api/v1/admin/settings/split-templates
probe GET    /api/v1/admin/settings/split-templates/1/parties
probe GET    /api/v1/admin/settings/ota
probe GET    /api/v1/admin/announcements
probe GET    /api/v1/admin/customer-service
probe GET    /api/v1/admin/feedback
probe GET    /api/v1/admin/device-fault-reports
probe GET    /api/v1/admin/device-fault-reports/1/history
probe GET    /api/v1/admin/whitelabel
probe GET    /api/v1/admin/webhooks
probe GET    /api/v1/admin/webhooks/1/deliveries
probe GET    /api/v1/admin/ota/packages
probe GET    /api/v1/admin/ota/schedules
probe GET    /api/v1/admin/export/tasks
probe GET    /api/v1/admin/billing/wallet-risks

echo
echo "--- DONE ---"

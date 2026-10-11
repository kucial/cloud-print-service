#!/usr/bin/env bash
# Smoke-test a running instance. Usage: examples/smoke.sh [base_url] [erp_key]
set -euo pipefail
BASE="${1:-http://localhost:8080}"
KEY="${2:-dev-key-erp-0123456789abcdef}"
OPS="${3:-dev-key-ops-0123456789abcdef}"
AUTH=(-H "Authorization: Bearer $KEY")
OPAUTH=(-H "Authorization: Bearer $OPS")
J='Content-Type: application/json'

echo "== fleet status (one round trip) =="
curl -sS "${AUTH[@]}" "$BASE/api/v1/printers/status" | jq '.items[] | {printer_id, usable, state: .status.state}'

echo
echo "== dispatch a label (idempotent) =="
BODY='{"printer_id":"dev-mock-01","external_id":"demo-1","zpl":"^XA^CI28^FO40,40^A0N,40,40^FDCloudPrintService^FS^XZ","biz_ref":"DEMO-1"}'
curl -sS -X POST "${AUTH[@]}" -H "$J" -d "$BODY" "$BASE/api/v1/jobs" | jq
echo "-- retry (must not print a second label):"
curl -sS -X POST "${AUTH[@]}" -H "$J" -d "$BODY" "$BASE/api/v1/jobs" | jq

echo
echo "== job outcome =="
sleep 0.5
curl -sS "${AUTH[@]}" "$BASE/api/v1/jobs?external_id=demo-1" | jq '.items[0] | {job_id, state, attempts, payload_bytes}'

echo
echo "== live events (3s) =="
curl -sN --max-time 3 "${AUTH[@]}" "$BASE/api/v1/events?topics=printer.status" 2>/dev/null | head -4 || true

echo
echo "== admin: test label =="
curl -sS -X POST "${OPAUTH[@]}" "$BASE/api/v1/printers/dev-mock-01/test" | jq

echo
echo "== ops =="
curl -sS "$BASE/readyz" | jq
curl -sS "$BASE/metrics" | grep -E '^cps_(jobs_total|queue_depth|printer_usable)' | head -10

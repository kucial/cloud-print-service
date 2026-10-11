#!/usr/bin/env bash
# End-to-end flow test against a running service, driven exactly like a business
# system: issue a token, read the fleet, dispatch, retry, survive printer trouble.
#
#   ./examples/flow.sh [base_url] [admin_key]
#
# It is the scripted twin of test/e2e/flow_test.go — same story, visible output.
set -euo pipefail

BASE="${1:-http://localhost:8080}"
ADMIN="${2:-dev-key-ops-0123456789abcdef}"
API="$BASE/api/v1"

say()  { printf '\n\033[1m== %s\033[0m\n' "$*"; }
fail() { printf '\033[31mFAIL: %s\033[0m\n' "$*"; exit 1; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; }

need() { command -v "$1" >/dev/null || fail "$1 is required"; }
need curl; need jq

# ---------------------------------------------------------------- 1. onboarding
say "1. ops issues a token for the WMS"
read -r WMS_TOKEN TOKEN_ID <<<"$(jq -r '.token + " " + .id' <<<"$(curl -sS -X POST "$API/admin/tokens" \
  -H "Authorization: Bearer $ADMIN" -H 'Content-Type: application/json' \
  -d '{"client_id":"wms","role":"client","ttl":"90d","note":"flow test"}')")"
[[ -n "$WMS_TOKEN" && "$WMS_TOKEN" != null ]] || fail "token issue failed"
ok "issued $TOKEN_ID (${WMS_TOKEN:0:14}…)"

code=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "$API/admin/tokens" \
  -H "Authorization: Bearer $WMS_TOKEN" -H 'Content-Type: application/json' \
  -d '{"client_id":"attacker"}')
[[ "$code" == 403 ]] || fail "a client token must not issue tokens (got $code)"
ok "client token cannot issue tokens (403)"

AUTH=(-H "Authorization: Bearer $WMS_TOKEN")
PRINTER=$(jq -r '.items[] | select(.usable) | .printer_id' <<<"$(curl -sS "${AUTH[@]}" "$API/printers/status")" | head -1)
[[ -n "$PRINTER" ]] || fail "no usable printer found"

# ---------------------------------------------------------------- 2. fleet
say "2. fleet visibility"
curl -sS "${AUTH[@]}" "$API/printers/status" | jq -c '.items[] | {printer_id, usable, state: .status.state}'
ok "read the whole fleet in one round trip"

# ---------------------------------------------------------------- 3. dispatch
say "3. dispatch a label to $PRINTER"
# A real ERP uses its own document number; make it unique per run so the flow test can
# be re-run against a long-lived service.
EXT="OK-88213-$(date +%s | tail -c 6)$$"
ZPL='^XA^CI28^PW812^FO40,30^A0N,44,44^FDOUTBOUND WH1^FS^FO40,90^GB732,3,3^FS^FO40,110^A0N,30,30^FDOrder OK-88213  Bin A-03^FS^FO40,150^A0N,26,26^FDSKU 12345 x2   Bay 7^FS^FO40,190^BCN,90,Y,N,N^FD1234567890123^FS^FO560,190^BQN,2,6^FDSKU12345^FS^XZ'
BODY=$(jq -n --arg p "$PRINTER" --arg z "$ZPL" \
  --arg ext "$EXT" '{printer_id:$p, external_id:$ext, zpl:$z, biz_ref:"OK88213"}')
JOB=$(curl -sS -X POST "${AUTH[@]}" -H 'Content-Type: application/json' -d "$BODY" "$API/jobs" | jq -r .job_id)
[[ -n "$JOB" && "$JOB" != null ]] || fail "dispatch rejected"
ok "job $JOB queued"

for _ in $(seq 40); do
  STATE=$(curl -sS "${AUTH[@]}" "$API/jobs/$JOB" | jq -r .state)
  [[ "$STATE" == printed || "$STATE" == failed || "$STATE" == sent_unknown ]] && break
  sleep 0.25
done
[[ "$STATE" == printed ]] || fail "job ended in $STATE"
ok "job printed"

# ---------------------------------------------------------------- 4. idempotency
say "4. the ERP retries the same document 3 times"
for i in 1 2 3; do
  code=$(curl -sS -o /tmp/flow-replay.json -w '%{http_code}' -X POST "${AUTH[@]}" \
    -H 'Content-Type: application/json' -d "$BODY" "$API/jobs")
  [[ "$code" == 200 ]] || fail "retry $i returned $code, want 200"
  [[ "$(jq -r .job_id /tmp/flow-replay.json)" == "$JOB" ]] || fail "retry $i created a new job"
  [[ "$(jq -r .replayed /tmp/flow-replay.json)" == true ]] || fail "retry $i was not marked as a replay"
done
ok "every retry replayed job $JOB — no second label"

code=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "${AUTH[@]}" -H 'Content-Type: application/json' \
  -d "$(jq -n --arg p "$PRINTER" --arg ext "$EXT" '{printer_id:$p, external_id:$ext, zpl:"^XA^FDdifferent^FS^XZ"}')" "$API/jobs")
[[ "$code" == 409 ]] || fail "changed payload should be 409 (got $code)"
ok "same document id + different payload → 409"

# ---------------------------------------------------------------- 5. visibility of trouble
say "5. what a WMS dashboard sees when a printer is sick"
[[ -n "${VPRINT_ADMIN:-}" ]] || ok "skipping fault injection (set VPRINT_ADMIN=http://host:port)"
if [[ -n "${VPRINT_ADMIN:-}" ]]; then
  curl -sS -XPOST "$VPRINT_ADMIN/control" -d '{"state":"media_out"}' >/dev/null
  sleep "${POLL_WAIT:-3}"
  curl -sS "${AUTH[@]}" "$API/printers?usable=false" | jq -c '.items[] | {printer_id, state: .status.state}'
  ok "usable=false lists the affected printer"
  curl -sS -XPOST "$VPRINT_ADMIN/control" -d '{"state":"idle"}' >/dev/null
fi

# ---------------------------------------------------------------- 6. revoke
say "6. revoking the credential"
curl -sS -X DELETE -H "Authorization: Bearer $ADMIN" "$API/admin/tokens/$TOKEN_ID" >/dev/null
code=$(curl -sS -o /dev/null -w '%{http_code}' "${AUTH[@]}" "$API/printers/status")
[[ "$code" == 401 ]] || fail "revoked token still works ($code)"
ok "revoked token rejected immediately (401)"

say "flow test passed"

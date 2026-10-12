#!/usr/bin/env bash
# Verify the render pipeline against a published reference image.
#
#   ./examples/verify-render.sh [zpl_url] [reference_png_url] [options]
#
# Defaults to zebrash's own UPS reference. The script starts a virtual printer and a
# throwaway service, dispatches the ZPL as a real job, pulls the rendered label back out
# of the gallery, and compares it to the reference pixel by pixel.
#
# The defaults reproduce the profile the shipped reference was made with:
# greyscale (antialiased) + upright. Override per reference:
#   --mono      1-bit monochrome instead of greyscale
#   --physical  keep ^POI rotated 180° (physically faithful) instead of upright
#   --width-mm / --height-mm / --dpmm / --max-diff-pct
#
# Why it exists: "the renderer is fine" is a claim; this turns it into an assertion, and
# it covers the whole path (HTTP API → queue → TCP 9100 → parser → raster) rather than
# just the rasteriser.
set -euo pipefail

ZPL_URL="${1:-https://raw.githubusercontent.com/swiftpush/zebrash/main/testdata/ups.zpl}"
REF_URL="${2:-https://raw.githubusercontent.com/swiftpush/zebrash/main/testdata/ups_grayscale.png}"
shift 2 || true

WIDTH=101.6; HEIGHT=203.2; DPMM=8; GRAY="-render-grayscale"; UPRIGHT="-render-upright"; MAX_DIFF_PCT=0.05
PRINT_PORT=${PRINT_PORT:-19600}; ADMIN_PORT=${ADMIN_PORT:-19601}; CPS_PORT=${CPS_PORT:-18083}
WORK=$(mktemp -d)

while [[ $# -gt 0 ]]; do
  case "$1" in
    --width-mm) WIDTH="$2"; shift 2 ;;
    --height-mm) HEIGHT="$2"; shift 2 ;;
    --dpmm) DPMM="$2"; shift 2 ;;
    --grayscale) GRAY="-render-grayscale"; shift ;;
    --mono) GRAY=""; shift ;;
    --upright) UPRIGHT="-render-upright"; shift ;;
    --physical) UPRIGHT=""; shift ;;
    --max-diff-pct) MAX_DIFF_PCT="$2"; shift 2 ;;
    *) echo "unknown flag $1" >&2; exit 2 ;;
  esac
done

cleanup() {
  [[ -n "${VPRINT_PID:-}" ]] && kill "$VPRINT_PID" 2>/dev/null || true
  [[ -n "${CPS_PID:-}"    ]] && kill "$CPS_PID"    2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

need() { command -v "$1" >/dev/null || { echo "$1 is required" >&2; exit 1; }; }
need curl; need python3

echo "zpl        $ZPL_URL"
echo "reference  $REF_URL"
profile="greyscale"; [[ -z "$GRAY" ]] && profile="mono"
orient="upright";     [[ -z "$UPRIGHT" ]] && orient="physical (^POI rotated 180)"
echo "geometry   ${WIDTH}x${HEIGHT}mm @ ${DPMM}dpmm, ${profile}, ${orient}"
echo

curl -sSL -o "$WORK/label.zpl"  "$ZPL_URL"
curl -sSL -o "$WORK/ref.png"    "$REF_URL"

go build -o "$WORK/vprint" ./vprint/cmd/vprint
go build -o "$WORK/cps"    ./server/cmd/cps

"$WORK/vprint" -id verify-printer -listen ":$PRINT_PORT" -admin ":$ADMIN_PORT" \
  -render -render-width-mm "$WIDTH" -render-height-mm "$HEIGHT" -render-dpmm "$DPMM" \
  $GRAY $UPRIGHT -print-duration 200ms >"$WORK/vprint.log" 2>&1 &
VPRINT_PID=$!

cat > "$WORK/cps.json" <<EOF
{
  "addr": ":$CPS_PORT",
  "status_poll_interval": "2s",
  "confirm_timeout": "3s",
  "api_keys": { "verify-ops-key-0123456789ab": { "client_id": "ops", "role": "admin" } },
  "printers": [
    { "printer_id": "verify-printer", "name": "verify", "site": "lab", "model": "ZT411",
      "driver": "zebra9100", "host": "127.0.0.1", "port": $PRINT_PORT, "enabled": true }
  ]
}
EOF
"$WORK/cps" -config "$WORK/cps.json" >"$WORK/cps.log" 2>&1 &
CPS_PID=$!

for _ in $(seq 40); do
  curl -sf "http://localhost:$CPS_PORT/readyz" >/dev/null 2>&1 && break
  sleep 0.25
done

python3 - "$WORK" "$CPS_PORT" <<'PY'
import json, sys, time, urllib.request
work, port = sys.argv[1], sys.argv[2]
base = "http://localhost:" + port

def req(path, key=None, body=None):
    r = urllib.request.Request(base + path,
        data=json.dumps(body).encode() if body else None,
        headers={"Authorization": "Bearer " + key, "Content-Type": "application/json"})
    return json.load(urllib.request.urlopen(r))

ops = "verify-ops-key-0123456789ab"
tok = req("/api/v1/admin/tokens", ops, {"client_id": "verify", "ttl": "1h"})["token"]
zpl = open(work + "/label.zpl", newline="").read()      # newline="" keeps CRLF intact
job = req("/api/v1/jobs", tok, {"printer_id": "verify-printer",
                                "external_id": "verify-1", "zpl": zpl})
print("dispatched job", job["job_id"])
for _ in range(80):
    j = req("/api/v1/jobs/" + job["job_id"], tok)
    if j["state"] in ("printed", "failed", "sent_unknown"):
        print("job state:", j["state"], "| attempts:", j["attempts"], "| bytes:", j["payload_bytes"])
        break
    time.sleep(0.25)
else:
    raise SystemExit("job never finished")
PY

# The label lands in the gallery with its rendered preview.
sleep 1
curl -sf "http://localhost:$ADMIN_PORT/labels/1/image" -o "$WORK/ours.png" \
  || { echo "no rendered image in the gallery" >&2; exit 1; }

python3 - "$WORK" "$MAX_DIFF_PCT" <<'PY'
import sys
from PIL import Image, ImageChops
work, max_pct = sys.argv[1], float(sys.argv[2])

ref  = Image.open(work + "/ref.png").convert("L")
ours = Image.open(work + "/ours.png").convert("L")
print()
print(f"reference {ref.size}   ours {ours.size}")
if ref.size != ours.size:
    raise SystemExit("FAIL: dimension mismatch")

a, b = list(ref.getdata()), list(ours.getdata())
diff = [abs(x - y) for x, y in zip(a, b)]
n = len(diff)
ident = sum(1 for d in diff if d == 0)
over = sum(1 for d in diff if d > 32)
pct = 100 * over / n
print(f"bit-identical      {ident:,} / {n:,}  ({100*ident/n:.4f}%)")
print(f"mean |difference|  {sum(diff)/n:.5f}     max {max(diff)}")
print(f"differing > 32     {over:,}  ({pct:.5f}%)   threshold {max_pct}%")
print(f"difference bbox    {ImageChops.difference(ref, ours).getbbox()}")
print()
if pct > max_pct:
    raise SystemExit(f"FAIL: {pct:.4f}% of pixels differ, threshold is {max_pct}%")
print("PASS: render pipeline matches the published reference")
PY

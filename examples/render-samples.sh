#!/usr/bin/env bash
# Render a set of representative ZPL samples to a directory, for eyeballing what the
# virtual printer will show. Handy before wiring a new carrier label format.
#
#   ./examples/render-samples.sh [out_dir] [--backend labelize --labelize-url URL]
#
# Writes out_dir/<name>.png (+ .svg) and prints a manifest.
set -euo pipefail

OUT="${1:-/tmp/cps-render-samples}"
shift || true

need() { command -v "$1" >/dev/null || { echo "$1 is required" >&2; exit 1; }; }
need go
mkdir -p "$OUT"

emit() { # emit <name> <zpl> [extra zplpng flags...]
  local name="$1" zpl="$2"; shift 2
  printf '%s' "$zpl" > "$OUT/$name.zpl"

  # PNG is what the gallery serves, so a PNG failure is fatal; other formats are
  # best-effort because the upstream backends cover different command subsets
  # (e.g. the SVG backend cannot encode every ^GF graphic field).
  if ! go run ./cmd/zplpng "$OUT/$name.zpl" -o "$OUT/$name.png" "$@" 2>>"$OUT/render.log"; then
    echo "  $name: PNG render FAILED (see $OUT/render.log)" >&2
    return 1
  fi
  local note=""
  if ! go run ./cmd/zplpng "$OUT/$name.zpl" -o "$OUT/$name.svg" --format svg "$@" 2>>"$OUT/render.log"; then
    note="   (svg unsupported for this sample)"
  fi
  printf '  %-18s %s%s\n' "$name" "$OUT/$name.png" "$note"
}

echo "rendering samples into $OUT"

emit text-basic \
'^XA^CI28^FO40,40^A0N,48,48^FDSKU-12345^FS^FO40,110^A0N,28,28^FB300,2,0,L,0^FDBin A-03 / Qty 2 / Bay 7^FS^XZ'

emit rotations \
'^XA^FO30,30^A0R,40,40^FDROT R^FS^FO120,30^A0I,40,40^FDROT I^FS^FO210,30^A0B,40,40^FDROT B^FS^FO30,120^A0N,40,40^FDNORMAL^FS^XZ'

emit graphics \
'^XA^FO30,30^GB340,3,3^FS^FO30,50^GC60,60,4^FS^FO110,50^GD80,80,5^FS^FO220,50^GB120,60,4,B,30^FS^FO30,140^FR^GB200,50,3^FS^FO60,150^A0N,30,30^FDREVERSE^FS^XZ'

emit code128 \
'^XA^FO40,30^BY3,3,2^BCN,110,Y,N,N^FD1234567890123^FS^FO40,170^BY2,2,2^BCR,90,Y,N,N^FDROTATED-HRI^FS^XZ'

emit qr-and-text \
'^XA^FO40,30^BQN,2,8^FDhttps://wms.example.com/order/OK-88213^FS^FO320,40^A0N,26,26^FDOrder OK-88213^FS^FO320,90^A0N,22,22^FDShip to: WH1 Dock 4^FS^FO320,130^A0N,22,22^FDWeight: 2.4 kg^FS^XZ'

emit graphic-field \
'^XA^FO40,40^GFA,32,4,8,FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF^FS^FO40,140^A0N,30,30^FDGF GRAPHIC FIELD^FS^FO40,180^A0N,24,24^FD(total,bytes/row,rows,hexdata)^FS^XZ'

emit hex-escape \
'^XA^CI28^FO40,40^A0N,36,36^FH^FD100%_20COTTON_20SIZE_20M^FS^FO40,110^A0N,28,28^FD(^FH with no arg: _ is the hex byte, _20 = space)^FS^XZ'

emit multi-copy \
'^XA^PQ3^FO40,40^A0N,40,40^FDCOPY 1 of ^PQ3^FS^XZ^XA^PQ1^FO40,40^A0N,40,40^FDSECOND FORMAT ON SAME PAYLOAD^FS^XZ'

emit carrier-4x6 \
'^XA^PW812^LL1218^FO40,30^GB732,3,3^FS^FO40,50^A0N,44,44^FDOUTBOUND WH1^FS^FO40,110^A0N,30,30^FDOrder OK-88213   Bin A-03^FS^FO40,150^A0N,26,26^FDSKU 12345 x2   Bay 7^FS^FO40,200^BCN,90,Y,N,N^FD1234567890123^FS^FO560,200^BQN,2,6^FDSKU12345^FS^FO560,380^GB180,90,3^FS^FO575,410^A0N,30,30^FDCHECKED^FS^FO600,500^GB100,60,3^FS^FO600,500^A0N,26,26^FD01/03^FS^XZ'

# Deliberately broken input: the renderer must fail loudly, not emit a blank label.
printf 'this is not zpl' > "$OUT/broken.zpl"
if go run ./cmd/zplpng "$OUT/broken.zpl" -o "$OUT/broken.png" "$@" 2>>"$OUT/render.log"; then
  echo "  WARNING: invalid ZPL rendered without error" >&2
else
  printf '  %-18s %s\n' "broken" "rejected, see $OUT/render.log"
fi

echo
echo "manifest:"
ls -la "$OUT" | sed 's/^/  /'

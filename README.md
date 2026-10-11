# Cloud Print Service (CPS)

A small service that sits between ERP/WMS and warehouse label printers.
**Business systems own the label content (ZPL); CPS owns fleet visibility and safe dispatch.**

> Design, state machine, API contract and failure policy: **[docs/DESIGN.md](docs/DESIGN.md)**

```
 ERP / WMS ──token──▶ ┌───────────────────────────────┐ ──ZPL :9100──▶ label printers
                      │  fleet status · dispatch queue │ ──^HS  :9100──▶ (status socket)
 ops / CI ──admin───▶ └───────────────┬───────────────┘
                                      │
                    virtual printers ─┘ (vprint: real sockets + rendered label images)
```

| | |
|---|---|
| **Fleet visibility** | `GET /api/v1/printers/status` — every printer's state, consumables, queue depth and `usable` flag in one round trip |
| **Dispatch** | `POST /api/v1/jobs` — hand a printer ready-made ZPL, tracked by your business document id |
| **No duplicate labels** | Idempotency on `(client, external_id)`; once bytes reach a printer CPS never retries blindly — it reports `sent_unknown` for a human |
| **Token issuance** | `cpsctl token issue` / `POST /api/v1/admin/tokens` — secrets shown once, stored hashed, rotatable and revocable |
| **Operator panel** | Embedded SPA at `/ui/`: fleet, jobs, tokens, ZPL preview — same origin as the API |
| **Virtual printer** | `vprint` speaks real ZPL on :9100, answers `^HS`/`~HI`/`~JA`, renders every label to PNG, and takes fault injection over HTTP |
| **Testable** | `go test -race ./...` — the flow test drives a virtual printer through the real HTTP API with issued tokens |

Out of scope on purpose: ZPL rendering in the service, USB print agents, document composition.

## Operator panel

```bash
go run ./cmd/cps -config config.json     # then open http://localhost:8080/ → redirects to /ui/
```

Embedded in the binary and served from the **same origin as the API** — the mihomo/clash
pattern, deliberately:

- **No second deployment, no CORS, no node toolchain.** `web/dist` is compiled in with
  `go:embed`; the panel is plain HTML/CSS/JS with zero dependencies, because a warehouse
  laptop may have no internet.
- **Same auth as the API.** Paste an admin token (bootstrap key or an issued one); it is
  kept in `localStorage` and sent as `Authorization: Bearer …`.
- **Live.** Subscribes to `/api/v1/events` with `fetch()` + a hand-rolled SSE reader,
  because `EventSource` cannot set an Authorization header (it would always 401).
  Polling every 5s is the fallback and remains the source of truth.

Views: fleet overview (usable/needs-attention/recent jobs) · per-printer detail with the raw
`^HS` reply and last job · job list with filters and cancel · token issue/list/rotate/revoke ·
**ZPL preview** · ops (health/ready/metrics).

The preview tab renders ZPL through `POST /api/v1/admin/preview` using the same engine as
the virtual printer — check a template before it reaches a real printer. Admin-only, since
it burns CPU on arbitrary input.

Disable or relocate it:

```jsonc
"ui": { "enabled": true, "path": "/ui/", "title": "Cloud Print Service" }
```

## Reference integration (`cmd/demo-web`)

A WMS packing console that is meant to be **read**, not deployed. It shows the six rules a
correct ERP/WMS integration follows — stable `external_id`, retry only on `503 queue_full`
and transport errors, never retry `409`, never auto-retry `sent_unknown`, read state from
the API (never from the `202`), and use a *client* token for dispatch with the admin token
reserved for previews.

```bash
cpsctl token issue --client-id wms --ttl 90d
go run ./cmd/demo-web -token cps_live_…            # http://localhost:8090
```

Its **Resend** button re-submits a whole wave unchanged; every line must come back
`replayed: true` with zero new labels on the printer. Verified live:

```
release OK-8801           → 1 label
resend OK-8801 (×2)       → still 1 label
```

## Quick start (no hardware)

```bash
# 1. two virtual printers with rendered previews (gallery on :19200)
go run ./cmd/vprint -count 2 -id wh1-pack -render -render-dir /tmp/labels

# 2. the service
cp config.example.json config.json
go run ./cmd/cps -config config.json

# 3. give the WMS a credential
go run ./cmd/cpsctl token issue --client-id wms --ttl 90d
export WMS_KEY=cps_live_…

# 4. see the fleet, print a label
curl -s localhost:8080/api/v1/printers/status -H "Authorization: Bearer $WMS_KEY" | jq
curl -s -XPOST localhost:8080/api/v1/jobs \
  -H "Authorization: Bearer $WMS_KEY" -H 'Content-Type: application/json' \
  -d '{"printer_id":"sim-pack-01","external_id":"OK-88213-4","biz_ref":"OK88213",
       "zpl":"^XA^CI28^FO40,40^A0N,40,40^FDHello^FS^FO40,100^BCN,90,Y,N,N^FD1234567890^FS^XZ"}' | jq
```

Then open **http://localhost:19200** — the label gallery shows exactly what the printer
received, rendered, next to the raw ZPL.

## Render samples

```bash
./examples/render-samples.sh /tmp/cps-render-samples   # text, rotations, graphics,
                                                        # Code128, QR, ^GF, hex escape,
                                                        # multi-format, 4x6 carrier
```

Writes `<name>.png` + `<name>.svg` + the source `.zpl` for each sample, and fails loudly
on input that is not renderable.

## Verify the whole thing

```bash
go test -race ./...                              # unit + flow tests
VPRINT_ADMIN=http://localhost:19200 ./examples/flow.sh   # scripted flow against a live service
./examples/smoke.sh                              # quick API walkthrough
```

`examples/flow.sh` is the acceptance story: issue a token → read the fleet → dispatch →
retry 3× without a second label → printer runs out of paper → dashboard flags it → revoke
the credential. `test/e2e/flow_test.go` is the same story in Go, driving a virtual printer
over a real socket.

## Virtual printer (`cmd/vprint`)

```bash
go run ./cmd/vprint -count 3 -id wh1-pack -base-port 19100 -base-admin-port 19200 \
  -render -render-dir /tmp/labels -print-duration 300ms
```

- **Print port** — raw ZPL, answers `^HS` (host status), `~HI` (model/serial), `~JA` (cancel).
  Counts each `^XA…^XZ` as one label, so "did we double print?" has a real answer.
- **Gallery / control port** (`http://localhost:19200`)
  - `GET /` — every label rendered to PNG next to its ZPL
  - `GET /labels/{n}/image`, `GET /labels/{n}/zpl`
  - `POST /control` — `{"state":"media_out"}`, `{"offline":true}`,
    `{"close_on_print":true}` (dies mid-write), `{"status_delay_ms":2000}`,
    `{"state":"always_printing"}` (never confirms) → jobs end `sent_unknown`
- **Rendering** — in-process [zebrash](https://github.com/swiftpush/zebrash) (Go, MIT) by
  default, no container. For the widest command coverage and Labelary-calibrated output, run
  [labelize](https://github.com/GOODBOY008/labelize) and pass
  `-render-backend labelize -labelize-url http://localhost:8090`
  (`docker compose --profile sim up labelize`).
- Standalone rendering without any service: `go run ./cmd/zplpng label.zpl -o label.png`
  (`--format svg|pdf` also works).
- `^POI` labels come out rotated 180° — physically what the printer does. Pass
  `-render-upright` (or `-upright` to zplpng) for the readable orientation that published
  reference renders use.
- Prove the whole path is faithful:
  `./examples/verify-render.sh` — starts a printer + service, dispatches zebrash's
  `testdata/ups.zpl`, pulls the render back out and diffs it against the published
  `ups_grayscale.png`. Currently **1,321,938 / 1,321,938 pixels identical**.
  The same comparison runs in CI as `TestRenderMatchesUPSReference`.

## Token issuance

Bootstrap keys in `config.json` are break-glass only. Business systems get real tokens:

```bash
cpsctl token issue  --client-id wms --role client --ttl 90d --note "pilot site wh1"
cpsctl token list
cpsctl token rotate tok_7_4YET6CFB     # old secret dies, new one printed once
cpsctl token revoke tok_7_4YET6CFB
```

The secret is shown **once** and only its SHA-256 is stored. Listing shows prefix, client,
expiry, status and usage count — never the secret.

| Endpoint | Purpose |
|---|---|
| `POST /api/v1/admin/tokens` | issue (`client_id`, `role`, `ttl`, `note`) → secret in the response |
| `GET /api/v1/admin/tokens` | list; `?client_id=` filter; `active` count |
| `POST /api/v1/admin/tokens/{id}/rotate` | revoke + reissue, same identity |
| `DELETE /api/v1/admin/tokens/{id}` | revoke |
| `DELETE /api/v1/admin/tokens/{id}/purge` | forget it entirely |

All admin routes require an admin credential — either a bootstrap key or a token issued
with `role=admin`. A revoked token is rejected identically to an unknown one, so probing
cannot tell them apart.

## Layout

```
cmd/cps/          service entrypoint: config → drivers → poller → job workers → HTTP
cmd/cpsctl/       admin CLI (issue/list/rotate/revoke tokens, printers, test label)
cmd/vprint/       virtual printers + label gallery + fault injection
cmd/demo-web/     reference WMS integration (release waves, idempotency tests)
cmd/zplpng/       ZPL → PNG/SVG/PDF from a file or stdin
web/dist/         operator panel (embedded via go:embed, no build step)
internal/printer/ Driver interface, zebra9100 + mock, status cache & poller
internal/job/     validation, idempotency, per-printer FIFO, retry, cancel
internal/api/     HTTP handlers, auth, rate limit, SSE, token admin
internal/token/   token issuing, hashing, verification, rotation
internal/render/  ZPL → image: zebrash (default) or labelize
internal/fakeprinter/  virtual printer + gallery, built on internal/render
internal/bus/     in-process pub/sub for events
internal/metrics/ dependency-free Prometheus text registry
db/schema.sql     production schema (Postgres)
test/e2e/         flow test: virtual printers + real HTTP API + issued tokens
docs/DESIGN.md    the design document
```

## Design decisions worth knowing

1. **Two sockets per printer.** Status replies interleave into the 9100 print stream, so
   status gets its own connection — it can never corrupt a label or starve behind a payload.
2. **The status API never touches a printer.** A background poller fills a cached snapshot,
   so a hung TCP write can never block an ERP request. Expect up to one poll interval of
   staleness; `POST /api/v1/printers/probe` forces a refresh.
3. **At-most-once after the first byte.** CPS cannot distinguish "printed but the ACK was
   lost" from "not printed". Retrying an ambiguous outcome duplicates labels — a real
   inventory defect — so it reports `sent_unknown` and lets a human decide.
4. **Per-printer FIFO, one worker per printer.** Ordering inside a device, independent
   failure domains across devices, bounded memory (`503 queue_full`, never a backlog).
5. **`usable` needs evidence.** Never-observed, stale, offline, disabled or out-of-media all
   read as *not usable*; `unknown` is never optimistically reported as usable.
6. **Minimal dependencies.** The service path is stdlib-only. The single external dependency
   is [zebrash](https://github.com/swiftpush/zebrash) (MIT, pure Go), used *only* by the
   virtual printer to render label previews.

## Roadmap (proposed)

See **[docs/PLAN-agent-discovery.md](docs/PLAN-agent-discovery.md)** for the full plan:
a USB/edge agent, auto printer discovery, and the code reorganisation that makes both fit.
Shape of it:

| Binary | Role |
|---|---|
| `cps` | server: fleet registry, dispatch, status, API, panel, discovery coordinator |
| `vprint` | virtual network printer (also the test double for the agent) |
| `cps-agent` | edge agent: USB + local discovery, TCP forwarder, enroll/heartbeat |
| `demo-web` ✅ | business-module **integration reference** — a WMS-like console (see [cmd/demo-web/README.md](cmd/demo-web/README.md)) |
| `cpsctl` | admin CLI |

The design decision that makes the agent cheap: **a USB printer behind the agent is
presented to the server as a loopback TCP endpoint**, so `driver: zebra9100` is unchanged
and every dispatch guarantee (per-printer FIFO, no retry after the first byte,
`sent_unknown` for ambiguous outcomes) applies to USB printers for free.

Verified end to end with no hardware — a virtual printer behind the agent:

```bash
cpsctl agent enroll --id pc7-pack01 --site wh1      # one-time token
cps-agent -enroll <token> -site wh1                # discovers, serves, registers, heartbeats
cpsctl printers                                    # the agent's printers are in the fleet
```

* dispatch through the agent → 1 attempt, `printed`, one label on the device
* printer offline → `sent_unknown`, never a false success
* recovery → `printed`
* agent revoked → its printers leave the fleet; config printers untouched
* agent dies → its printers stay but go **unusable** as their status goes stale

Agent credentials are a separate token *kind*: they may register and report status, never
dispatch a job or read business data.

## Not done yet

- Postgres store (schema in `db/schema.sql`; the binary ships the memory store, so issued
  tokens live in memory and are lost on restart)
- Token persistence + per-key rate limits (auth already goes through `token.Verify`)
- TSC / Honeywell / SNMP drivers — add via `printer.Register("tsc9100", …)`
- Callback to ERP on job state change (events are already on the bus; expose a webhook)

Open questions and milestones: `docs/DESIGN.md` §13–14.

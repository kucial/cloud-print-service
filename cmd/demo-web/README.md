# demo-web — reference integration

A **WMS packing console** that talks to Cloud Print Service the way a real business
system should. It is open source on purpose: point your developers at this directory
instead of at the API docs.

It is also a working demo: it fabricates outbound orders, releases them as waves, and
shows every job's real state.

```bash
# 1. give it a credential (client-scoped — never admin)
cpsctl token issue --client-id wms --ttl 90d          # → cps_live_…

# 2. run it
go run ./cmd/demo-web -token cps_live_…               # → http://localhost:8090

# optional: enable the label preview (needs an admin token)
go run ./cmd/demo-web -token cps_live_… -admin-token cps_live_admin…
```

Flags: `-url` (service), `-site` / `-printer` (target selection), `-simulate N` (N jobs
per minute of background traffic, to watch queueing and backpressure), `-addr`.

---

## The six rules this module demonstrates

| # | Rule | Where |
|---|---|---|
| 1 | **Stable `external_id` per business document line.** This is what makes retries safe: the service de-duplicates on `(client, external_id)`. | `app.go: Release()` → `"%s-line-%d"` |
| 2 | **Retry only unambiguous failures.** `503 queue_full` means nothing was printed → retry with backoff + jitter. Every 4xx (including `409 idempotency_conflict`) → stop, a human must look. | `main.go: dispatch()` |
| 3 | **Transport errors are retried** — safe *only* because of rule 1. The service replays the original job rather than printing a second label. | `main.go: dispatch()` |
| 4 | **A job in `sent_unknown` is never retried automatically.** It means bytes may be on the printer. | UI + `reconcile()` never re-submits a terminal job |
| 5 | **State comes from the API**, never from the `202 Accepted`. Live via SSE, polling as fallback. | `app.go: watchJobs()` / `reconcile()` |
| 6 | **Least privilege.** Dispatch uses a client token. The admin token is optional and only ever calls `/admin/preview`. | `main.go`, `app.go: preview()` |

Two more that are easy to get wrong:

- **Payloads are never logged.** Only job ids, byte counts and states. Label content is
  customer data (SKUs, addresses); `zpl` is in memory and in transit, never in a log line.
- **A wave is a wave, not a stampede.** Releases are bounded (4 in flight) so a 200-line
  pick list cannot turn into a self-inflicted denial of service.

---

## Minimal integration, if you only copy five lines

```go
req := map[string]any{
    "printer_id":  "wh1-pack-03",
    "external_id": "OK-88213-4",     // stable: order line id
    "biz_ref":     "OK88213",
    "zpl":         zpl,             // final ZPL, produced by you
}
// POST /api/v1/jobs  → 202 {job_id, state, replayed}
// 200 + replayed:true means "already queued/printed — nothing new happened"
// 503 queue_full     → back off, the printer is busy
// 409                → same external_id with a different payload: investigate
```

Then read the state:

```bash
curl -s -H "Authorization: Bearer $TOKEN" "$CPS/api/v1/jobs/$JOB_ID" | jq .state
```

---

## What the console shows

- Orders with per-line job state: `queued → printed`, or `sent_unknown` / `failed` when
  the printer misbehaved — shown honestly rather than smoothed over.
- **Resend** re-submits an entire wave unchanged. Every line must come back
  `replayed: true` and **zero extra labels** appear on the printer. That is the property
  worth testing before you go live.
- Event log of every dispatch decision, including retries.
- Optional label preview rendered server-side.

## Trying it without hardware

```bash
go run ./cmd/vprint -count 2 -id wh1-pack -base-port 19100 -base-admin-port 19200 -render
go run ./cmd/cps      -config config.example.json
```

Point the service config at the virtual printers, issue a token, and release a wave.
To see backpressure, set a small `queue_size_per_printer` and run `-simulate 120`.

## Extending it into a real module

Replace `generateOrders()` with your order book; keep everything else. Specifically, keep:

- the **dispatch retry rules** in `main.go: dispatch()`,
- the **`external_id` scheme** — it is the join key between your data and the label,
- the **SSE + polling reconciliation**, so a dropped connection cannot lose a state,
- the **separation of client and admin tokens**.

Replace the demo UI in `web/` with your own; the Go side is a plain `net/http` service with
no framework, so it ports easily.

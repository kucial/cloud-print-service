# Cloud Print Service (CPS) — Design

> Tiny, boring service that sits between ERP/WMS and a fleet of label printers.
> Business systems own the label content (ZPL); CPS owns **fleet visibility** and **safe dispatch**.

---

## 1. Goals

| # | Goal | Why it matters |
|---|------|----------------|
| G1 | Let ERP/WMS query **who are my printers and are they usable right now** | Warehouse UI needs "printer 3 is paper-out" before it lets an operator confirm an outbound order |
| G2 | Let ERP/WMS **dispatch a print job** to a printer by id, with traceability back to the business document | Single entry point, no per-site scripts |
| G3 | Make dispatch **safe to retry** from the caller side | Network retries are the norm; double labels are the fear |
| G4 | Make the printer fleet **pluggable per vendor** | Zebra today, TSC/Argox/Honeywell tomorrow |
| G5 | Keep the ops surface small | One binary, one Postgres, ~10 config knobs |

## 2. Non-goals (MVP)

Explicitly **out of scope** — stated so nobody builds them by accident:

- ❌ Template / document rendering (ZPL is generated **upstream** by ERP/WMS and posted raw).
- ❌ Print agent on the warehouse PC for USB printers (all MVP printers are **Ethernet/TCP**).
- ❌ ZPL parsing, previewing, splitting, resizing.
- ❌ ERP/WMS master data, order workflows, inventory.
- ❌ HA clustering / multi-region. A single instance is the unit of failure.

---

## 3. Terminology

| Term | Meaning |
|------|---------|
| **Printer** | A registered physical device, addressed by stable `printer_id` (e.g. `wh1-pack-03`) |
| **Fleet** | All printers known to CPS |
| **Job** | One dispatch = one ZPL payload + one printer + one business reference |
| **Dispatch** | Getting bytes onto the printer's socket |
| **Confirm** | Positive evidence the label actually printed (best-effort) |
| **Zone / site** | Physical grouping (`wh1`, `wh2`) used for filtering and routing |

---

## 4. Architecture

```
 ┌──────────┐  ┌──────────┐
 │ ERP / WMS│  │  Ops UI  │            HTTPS + API key
 └────┬─────┘  └────┬─────┘
      │  POST /jobs │  GET /printers, SSE /events
      ▼             ▼
 ┌──────────────────────────────────────────────┐
 │                 Cloud Print Service          │
 │  ┌────────────────────────────────────────┐  │
 │  │ HTTP API  (net/http, stdlib only)       │  │
 │  │  auth · rate-limit · idempotency        │  │
 │  ├────────────────────────────────────────┤  │
 │  │ Printer Manager   ── one poll goroutine │  │
 │  │   per printer      per printer (status)│  │
 │  │   + status cache   (fresh, in-memory)  │  │
 │  ├────────────────────────────────────────┤  │
 │  │ Job Service                           │  │
 │  │   per-printer FIFO queue (bounded)     │  │
 │  │   worker per printer, retry, cancel    │  │
 │  ├────────────────────────────────────────┤  │
 │  │ Driver registry                       │  │
 │  │   zebra9100 │ tsc9100 │ snmp │ mock    │  │
 │  ├────────────────────────────────────────┤  │
 │  │ Store (interface) → Memory impl (MVP)  │  │
 │  │              → Postgres impl (prod)    │  │
 │  └────────────────────────────────────────┘  │
 └───────┬──────────────────────────┬───────────┘
         │ TCP 9100 (raw ZPL)       │ SQL
         ▼                          ▼
   ┌───────────┐              ┌───────────┐
   │ Printers  │              │ Postgres  │
   │ :9100     │              │ jobs,     │
   │           │              │ attempts, │
   │ (dedicated│              │ printers  │
   │  status   │              │ api_keys  │
   │  socket)  │              └───────────┘
   └───────────┘
```

**Single binary, single process.** Deliberate: one process owns both sockets and queues, so
ordering guarantees and failure handling stay easy to reason about. Scale path, if ever needed,
is `shard by printer_id` → N CPS instances + router. That is a deployment change, not a redesign.

### Request flow: dispatch

```
POST /api/v1/jobs
  → auth key → client scope check
  → idempotency lookup (client_id, external_id)   ── hit ─→ return existing job (200, replayed)
  → validate (printer exists & enabled & payload ≤ 1MB)
  → persist job(state=queued)
  → enqueue on printer FIFO ── full ─→ 503 queue_full (caller may retry later)
  → 202 Accepted { job_id, state: queued }

worker(printer_id)
  → job.state=sending
  → driver.Send(zpl)  ── connect/write error ─→ backoff retry (nothing printed yet)
  → job.state=printed   (confirmed: printer returned to ready, no error)
     or job.state=sent_unknown (bytes written, no confirmation → human check)
     or job.state=failed  (error before any byte flushed)
```

---

## 5. Printer connectivity & driver model

All MVP printers are **raw-socket label printers** on port `9100` (raw ZPL/TSPL, no driver installed
on the OS). Each printer gets **two logical connections**:

| Connection | Direction | Protocol | Why separate |
|---|---|---|---|
| **print** | CPS → printer | ZPL bytes, fire-and-forget | Long writes, big payloads |
| **status** | CPS ↔ printer | short query/response (`^HS`) | **Responses interleave into the print stream**; keeping status on its own socket prevents polluting label data and avoids starving status under a print backlog |

Driver interface (the only vendor-specific surface):

```go
type Driver interface {
    // Send writes the raw payload to the printer. Returns bytes actually flushed.
    Send(ctx context.Context, data []byte) (int, error)
    // Status returns a decoded status snapshot. ErrStatusUnsupported is allowed.
    Status(ctx context.Context) (Status, error)
    // Cancel aborts in-progress printing (e.g. Zebra ~JA). Best-effort.
    Cancel(ctx context.Context) error
    // Identify returns model/serial/firmware for onboarding (~HI). Best-effort.
    Identify(ctx context.Context) (map[string]string, error)
    Close() error
}
```

MVP ships `zebra9100` + `mock`. A `tsc9100` is ~60 lines (TSPL echo/parser differences) and can be
added without touching the API or job layers.

### Status polling

- One goroutine per printer, every `status_poll_interval` (default **15s**), staggered by
  `printer_id` hash to avoid a thundering herd.
- Timeout per probe: 3s. On failure → `state=offline`, increment `status_fail_total`.
- Snapshot cached in memory; the API **never** talks to a printer inline (a hung TCP write must not
  block an ERP request). Cached snapshot carries `observed_at` + `stale: bool` (>3× interval).

### Zebra `^HS` decoding

`^HS` returns up to four numeric codes (device state, media, ribbon, printhead) terminated by
`^HS`. The decoder is **lossless**: raw bytes are always returned in `raw`, and the mapping lives in
one function so it can be tuned per model without touching the service.

| field | 0 | 1 | 2 | 3 |
|---|---|---|---|---|
| device | idle | printing | busy | error |
| media | ok | low | out | — |
| ribbon | ok | low | out | — |
| printhead | ok | error | — | — |

> **Assumption to validate on hardware** (first milestone task): exact field semantics and whether
> `^HS` requires "host status" to be enabled in the printer's Link-OS config. If a model does not
> support it, fall back to TCP reachability only (`state=idle|offline`) — the API contract is
> unchanged because `state` degrades to `unknown`/`offline` rather than lying.

---

## 6. Job lifecycle & state semantics

```
                    ┌──────────────► cancelled      (queued, operator/system abort)
                    │
  queued ──► sending ──► printed       (dispatched + physically confirmed via ^HS)
     │           │   ├─► sent_unknown  (bytes sent, confirmation missing → needs human check)
     │           │   └─► failed        (error BEFORE any byte reached printer socket)
     │           └─── retryable error ──► back to sending (attempt n+1, bounded)
     └──► failed (queue TTL / superseded / validation)
```

#### Meaning of states: is `printed` "confirmed" or "bytes on wire"?

- **`printed` means physically confirmed by the printer.**
  It is **not** merely "all bytes reached the socket". CPS maintains a dedicated secondary status socket to each printer. After the full byte stream is flushed on the print socket, CPS probes the printer (`^HS`) and verifies that the printer transitioned into physical printing and returned to normal/idle without an error state. Only upon this confirmation is the job marked `printed`.
- **`sent_unknown`: what is it, when does it happen, and is it ever resolved?**
  - **What it is**: Bytes reached the printer socket, but CPS could not obtain hardware confirmation that the label printed (e.g. status probe timed out, printer severed connection immediately after write, or CPS was restarted while the job was in-flight).
  - **Why automated retries halt**: In warehouse operations, blindly re-sending an unconfirmed job causes duplicate labels on physical cartons or pallets. To prevent split shipments and inventory corruption, CPS refuses to auto-retry ambiguous outcomes.
  - **How it is resolved**: A warehouse operator performs visual verification at the printer tray. Once verified, the operator resolves the job via `POST /api/v1/jobs/{job_id}/resolve` (or directly in the Web UI):
    - `resolution: "printed"`: Operator confirms the physical label is on the tray.
    - `resolution: "failed"`: Operator confirms the label never emerged, safely releasing the ERP/WMS or operator to dispatch a reprint.
- **`failed`**: The job encountered an error **before** any byte was written to the printer socket (e.g. unreachable host, DNS failure, or exceeded queue TTL). It is provably safe to retry.
- **`cancelled`**: The job was cancelled by an admin while still waiting in the FIFO queue before transmission.

**The one rule that matters:** *after a byte is flushed to the printer, we never auto-retry.*

Rationale: CPS cannot distinguish "the label printed but the ACK was lost" from "the label did not
print". Retrying on an ambiguous outcome duplicates labels in a warehouse — a real inventory defect.
So:

- Failure **before** flush (connect refused, DNS, connection reset before write, payload rejected)
  → safe to retry automatically with exponential backoff.
- Failure **after** flush (write timeout mid-payload, no confirmation, printer error reported after)
  → job ends `sent_unknown` (or `failed` with `partial_write=true`) and is surfaced for a human.

`copies > 1` is rendered **upstream** — CPS sends the payload as-is and does not interpret ZPL.

### Job fields

`id` (uuid), `client_id`, `external_id` (business id), `printer_id`, `state`, `state_detail`,
`status_message`, `payload_ref`, `payload_bytes`, `payload_sha256`, `copies`, `page_copies`,
`attempts`, `last_error`, `created_at`, `sent_at`, `finished_at`, `biz_ref`, `meta`.

---

## 7. HTTP API

Base path `/api/v1`. JSON in/out (UTF-8). One exception: `POST /jobs/raw` takes
`application/octet-stream` to avoid base64 for large payloads.

### 7.1 Printer visibility

| Method | Path | Role | Notes |
|---|---|---|---|
| GET | `/printers` | client | `?site=&model=&enabled=&usable=` |
| GET | `/printers/{printer_id}` | client | Spec + last known status |
| GET | `/printers/{printer_id}/status` | client | Cached snapshot, no printer I/O |
| GET | `/printers/status` | client | **Bulk** status — `?ids=a,b,c`; omitted = whole fleet. This is the WMS dashboard endpoint (single round trip) |
| GET | `/printers/{printer_id}/jobs` | client | Recent jobs on that printer |
| POST | `/printers/{printer_id}/test` | admin | Dispatch built-in test label |
| POST | `/printers/{printer_id}/identify` | admin | Read model/serial/firmware |

`GET /printers` returns **usable-ness**, which is what a business system actually asks:

```jsonc
{
  "items": [{
    "printer_id": "wh1-pack-03",
    "name": "Packing station 3",
    "site": "wh1", "model": "ZT411", "driver": "zebra9100",
    "enabled": true,
    "usable": false,                 // enabled && online && not in a consumable/error state
    "queue_depth": 2,
    "status": {
      "state": "paper_out",          // idle|printing|busy|media_low|media_out|ribbon_out|head_error|error|offline|disabled|unknown
      "raw": "^HS0,2,0,0^HS",        // lossless passthrough
      "detail": "media=out",
      "observed_at": "2026-01-08T10:21:04Z",
      "stale": false,
      "last_job": {"job_id": "…", "state": "printed", "at": "2026-01-08T10:20:58Z"}
    }
  }]
}
```

### 7.2 Dispatch

| Method | Path | Role | Description |
|---|---|---|---|
| POST | `/jobs` | client | Dispatch a print job (idempotent on `external_id`) |
| POST | `/jobs/raw` | client | Raw binary payload dispatch (`application/octet-stream`) |
| POST | `/jobs/batch` | client | Multi-job batch dispatch (per-item independent acceptance) |
| GET | `/jobs/{job_id}` | client | Read job state, status message, and attempts |
| GET | `/jobs` | client | Query jobs (`?printer_id=&state=&from=&to=&external_id=&limit=&cursor=`) |
| POST | `/jobs/{job_id}/cancel` | client | Cancel a queued job before transmission |
| POST | `/jobs/{job_id}/resolve` | client/admin | Manually resolve a `sent_unknown` job (`printed` or `failed`) |

#### Maximum payload size & HTTP 422 Unprocessable Entity

- **Payload Size Limit**: Configured by `max_payload_bytes` (default `1,048,576` bytes / `1 MB`). Payloads exceeding this limit return HTTP `422` with `error.reason: "payload_too_large"`.
- **Structured Error Response**:
```jsonc
{
  "error": {
    "code": "invalid_request",          // broad error classification
    "reason": "payload_too_large",       // stable granular reason for UI localization
    "message": "invalid request: payload exceeds maximum allowed size (1048576 bytes limit)",
    "details": {
      "reason": "payload_too_large",
      "max_payload_bytes": 1048576
    }
  }
}
```

| HTTP Status | Error `code` | Error `reason` | When it happens / How client handles |
|---|---|---|---|
| **400** | `invalid_request` | `invalid_json`, `unknown_field` | Malformed JSON or unknown JSON fields in request |
| **401** | `unauthorized` | — | Missing or invalid Bearer token |
| **403** | `forbidden` | — | Client token attempted an admin operation (e.g. token issuance) |
| **404** | `printer_not_found` | — | `printer_id` does not exist in the fleet registry |
| **404** | `job_not_found` | — | `job_id` does not exist or belongs to another client tenant |
| **409** | `idempotency_conflict` | — | Same `external_id` re-submitted with a different payload hash. Stop and investigate! |
| **409** | `printer_disabled` | — | Target printer is administratively disabled (`enabled: false`) |
| **409** | `printer_not_usable` | — | Target printer is offline or in error state (when `reject_when_not_usable` is true) |
| **409** | `not_cancellable` | — | Job already reached `printed`, `sent_unknown`, or `failed` |
| **422** | `invalid_request` | `payload_too_large` | ZPL payload exceeds `max_payload_bytes` (default 1 MB) |
| **422** | `invalid_request` | `payload_empty` | Payload was provided but is 0 bytes |
| **422** | `invalid_request` | `missing_payload` | Neither `zpl` nor `payload_base64` was supplied |
| **422** | `invalid_request` | `duplicate_payload` | Both `zpl` and `payload_base64` were supplied in the same request |
| **422** | `invalid_request` | `invalid_base64` | `payload_base64` string is malformed |
| **422** | `invalid_request` | `missing_printer_id` | `printer_id` is missing or empty |
| **422** | `invalid_request` | `missing_external_id` | `external_id` (or `Idempotency-Key`) is missing |
| **422** | `invalid_request` | `invalid_copies` | `copies` is not between 1 and 50 |
| **422** | `invalid_request` | `invalid_page_copies` | `page_copies` array length does not match multi-page ZPL format count |
| **422** | `invalid_request` | `invalid_priority` | `priority` is not `normal` or `high` |
| **503** | `queue_full` | — | Printer FIFO queue is full (default 50). **Retry with backoff + jitter!** |

**POST /jobs**

```jsonc
// headers
// Authorization: Bearer <api key>
// Idempotency-Key: ok-order-88213-line-4        (or rely on external_id, see below)
{
  "printer_id": "wh1-pack-03",
  "external_id": "OK88213-4",          // caller-unique; the real idempotency anchor
  "zpl": "^XA^FO50,50^FDHello^FS^XZ",   // OR "payload_base64" for binary
  "copies": 1,
  "biz_ref": "OK88213",                // human traceable doc no.
  "priority": "normal",                // normal|high
  "meta": {"source": "erp", "user": "k.zhang"}
}
```

```jsonc
// 202 Accepted
{
  "job_id": "0f9c…",
  "state": "queued",
  "printer_id": "wh1-pack-03",
  "replayed": false,          // true ⇒ this is the same job you already submitted
  "links": {"self": "/api/v1/jobs/0f9c…"}
}
```

**Idempotency contract**

- Anchor = `(client_id, external_id)`, unique index in DB.
- Repeat submit → same `job_id`, `200 OK`, `"replayed": true`, **no second label**.
- Same `Idempotency-Key` with a **different payload hash** → `409 idempotency_conflict`.
- No `external_id` allowed unless `Idempotency-Key` is present (or auto-derived) — otherwise a
  blind retry would create a duplicate label; require callers to make retries explicit.

**Batch** (`POST /jobs/batch`) takes up to 200 items, is processed **per item independently**
(all-or-nothing would be wrong here), returns `200` with per-item result objects. HTTP status stays
`202` when at least one item was accepted.

### 7.3 Events

`GET /events?printer_ids=wh1-pack-03&topics=status,job` → **SSE**, keep-alive every 15s.

```
event: printer.status
data: {"printer_id":"wh1-pack-03","state":"media_out","observed_at":"…","stale":false}

event: job.state
data: {"job_id":"0f9c…","printer_id":"wh1-pack-03","state":"printed","at":"…"}
```

Purpose: WMS dashboard live updates and ERP not needing to poll. Optional — polling
`/printers/status` is fully sufficient, SSE is an optimisation.

### 7.4 Errors, auth, limits

Error envelope (stable `code` strings, always machine-readable):

```json
{"error":{"code":"printer_not_usable","message":"printer wh1-pack-03 is paper_out",
          "details":{"state":"media_out","observed_at":"…"}}}
```

`401 unauthorized` · `403 forbidden` · `404 not_found` · `409 idempotency_conflict`,
`printer_disabled`, `printer_not_usable` · `422 invalid_request` · `429 rate_limited` · `503 queue_full`.

| Concern | Policy |
|---|---|
| Auth | `Authorization: Bearer <token>`. Two sources, in order: **bootstrap keys** from config (break-glass, survives an empty token store) and **issued tokens** (what business systems use). Admin-only routes rejected for `client`. |
| Tenant isolation | Every job and query scoped by `client_id`; cross-client `job_id` → `404`, never `403` (don't leak existence) |
| Rate limit | Token bucket per client, default 30 req/s, burst 100 — enough to never bother a human, enough to stop a runaway ERP loop |
| Payload | ≤ 1 MB, must be valid UTF-8 or base64; `copies` 1–50 |
| Timeouts | HTTP server 10s read / 30s write; printer connect 3s / write 5s |

### 7.5 Token issuance (for business systems)

Bootstrap keys live in config and cannot be rotated without a restart, so they are treated
as break-glass only. Each integration gets its own short-lived, revocable credential.

```
ops/CI ──POST /api/v1/admin/tokens──▶ issue ──▶ "cps_live_<32 base32 chars>"   (shown ONCE)
                                       │
                                       ├──▶ sha256(secret) stored with client_id, role, ttl, note
                                       └──▶ secret handed to the integration team out of band
```

| Endpoint | Behaviour |
|---|---|
| `POST /api/v1/admin/tokens` | `{client_id, role, ttl, note}` → `201` with the plaintext secret, once |
| `GET /api/v1/admin/tokens` | never returns secrets; `?client_id=` filter, `active` count, usage per token |
| `POST /api/v1/admin/tokens/{id}/rotate` | revokes the old token and mints a replacement with the same identity/TTL — the zero-downtime rotation primitive |
| `DELETE /api/v1/admin/tokens/{id}` | revoke (kept for audit) |
| `DELETE /api/v1/admin/tokens/{id}/purge` | remove entirely |

Decisions:

- **Hash at rest** (SHA-256). A database dump yields no usable credential. Lookup is by
  digest, so verification is a map hit, not a scan of every secret.
- **Prefix for humans**: listings show `cps_live_4YET6CFB…` — enough to identify a token in
  a support conversation, not enough to use it.
- **Revoked and expired answer identically** to unknown tokens (`401`, same body). The
  distinction exists only in `cps_auth_failures_total{reason=revoked|expired|unknown}`,
  because it is useful to operators and useless to an attacker.
- **Expiry is enforced on every request** (`expires_at`), default `token_default_ttl` (90d).
- **Admin tokens can mint admin tokens.** Bootstrap keys are the trust root; that is
  deliberate for small deployments, and the reason token storage must be durable in prod.
- `cpsctl token issue|list|rotate|revoke` wraps these over HTTP, so the CLI exercises the
  same authorisation path as everything else.

**Known MVP limitation:** the token store is in-memory, so restarting the service drops
issued tokens and every integration must be re-issued. The Postgres store (schema below)
is the fix; `authenticate()` already calls `token.Verify`, so nothing above that layer moves.

### 7.6 Operator panel

Pattern taken from mihomo/clash: the UI is a static bundle embedded in the binary and
served from the same origin as the API.

```
GET /                → 302 /ui/          (operators land on the panel)
GET /ui/             → index.html        (no-store: never serve a stale panel after upgrade)
GET /ui/app.js|css   → embedded assets
GET /ui/<anything>   → index.html        (client-side routes deep-link)
POST /api/v1/admin/preview → rendered ZPL (the body is the image; metadata in X-Render-* headers)
```

Decisions:

- **Same auth as the API**, not a second auth system. The panel asks for an admin token and
  stores it in `localStorage`; every call sends `Authorization: Bearer …`.
- **SSE via `fetch()` + a manual frame parser.** `EventSource` cannot set headers, so it
  would 401 against this API. Polling every 5s remains the fallback, and REST stays the
  source of truth.
- **The panel is an operator tool, not a product surface**: fleet state, jobs, tokens,
  preview. Business systems keep using the API — nothing here is required to integrate.
- `ui.enabled=false` turns it off entirely; `render.backend` selects the preview engine.
  A build without a render engine serves everything except preview (`501 not_configured`).

---

## 8. Data model (Postgres)

```sql
create table printers (
  printer_id     text primary key,          -- 'wh1-pack-03', human-stable
  name           text not null,
  site           text not null default '',
  model          text not null default '',
  driver         text not null,             -- 'zebra9100' | 'tsc9100' | 'mock'
  host           text not null,
  port           int  not null default 9100,
  labels_per_sec int,                       -- optional hint, not used for scheduling in MVP
  enabled        boolean not null default true,
  created_at     timestamptz not null default now(),
  updated_at     timestamptz not null default now()
);
create index on printers (site, enabled);

create table jobs (
  id             uuid primary key,
  client_id      text not null,
  external_id    text not null,
  printer_id     text not null,
  state          text not null,             -- queued|sending|printed|sent_unknown|failed|cancelled
  payload        text not null,             -- MVP: inline. Later: uri to object storage
  payload_bytes  int  not null,
  payload_sha256 text not null,
  copies         int  not null default 1,
  priority       text not null default 'normal',
  biz_ref        text not null default '',
  meta           jsonb not null default '{}',
  attempts       int  not null default 0,
  last_error     text,
  partial_write  boolean not null default false,
  idempotency_key text,
  queued_at      timestamptz not null default now(),
  sent_at        timestamptz,
  finished_at    timestamptz,
  constraint jobs_client_external_uniq unique (client_id, external_id)
);
create index on jobs (printer_id, queued_at desc);
create index on jobs (client_id, queued_at desc);
create index on jobs (state) where state in ('queued','sending');   -- backlog / stuck scan
create index on jobs (client_id, external_id);

create table job_attempts (
  id          bigserial primary key,
  job_id      uuid not null references jobs(id),
  attempt_no  int  not null,
  started_at  timestamptz not null,
  finished_at timestamptz,
  result      text,                        -- ok|write_error|connect_error|partial_write|unconfirmed
  bytes_sent  int not null default 0,
  error       text
);
create index on job_attempts (job_id);

create table api_tokens (                    -- issued credentials (§7.5)
  id           text primary key,            -- 'tok_7_4YET6CFB'
  key_hash     text not null unique,        -- sha256(secret); the plaintext is never stored
  client_id    text not null,
  role         text not null default 'client',
  prefix       text not null,               -- first 8 chars, for human identification only
  note         text not null default '',    -- e.g. ticket id
  created_at   timestamptz not null default now(),
  expires_at   timestamptz,
  revoked_at   timestamptz,
  last_used_at timestamptz,
  uses         int not null default 0
);
create index if not exists api_tokens_client_idx on api_tokens (client_id, created_at desc);
create index if not exists api_tokens_active_idx  on api_tokens (client_id) where revoked_at is null;

create table printer_events (               -- status transition history (audit/troubleshooting)
  id         bigserial primary key,
  printer_id text not null,
  state      text not null,
  raw        text,
  at         timestamptz not null default now()
);
create index on printer_events (printer_id, at desc);
```

`printer_events` is what turns "the printer was flapping at 02:00" from a mystery into a query.

---

## 9. Concurrency, backpressure, failure policy

| Failure | Detection | Behaviour |
|---|---|---|
| Printer powered off / cable pulled | `connect` refused / timeout on status probe | `state=offline`, `usable=false`; dispatch is still accepted by default and ends `sent_unknown`/`failed` — set `reject_when_not_usable: true` to answer `409 printer_not_usable` instead |
| Printer TCP accept but no response | probe timeout | `state=unknown`, `stale=true` |
| Consumables | decoded status | `state=media_out`; dispatch **allowed** but flagged (ERP decides); `usable=false` for dashboards |
| Backlog grows | bounded per-printer queue (default 50) | `503 queue_full` — the caller decides to wait/fail over/alert a human. Never unbounded RAM. |
| Transient network error pre-flush | `Send` returns error with `bytes==0` | auto-retry: 3 attempts, 500ms → 2s → 8s, jittered |
| Write timeout mid-payload | `Send` returns partial bytes | **no retry**; `failed` + `partial_write=true` (operator may have a partial label to remove) |
| Confirmation missing | status poll after send | `sent_unknown`, surfaced in `/jobs` + metrics |
| CPS restart mid-queue | in-flight jobs reaped on boot | `sending` → `sent_unknown` if we can't prove bytes weren't written; `queued` jobs are re-queued from DB (prod store), or left `queued` in memory (MVP store) |
| Stuck job detector | sweeper every 60s | `queued > 5min` or `sending > 2min` → flag + event + metric |

**Per-printer FIFO** with one worker goroutine per printer = no interleaving inside one printer,
no need for cross-job locking, and a printer's queue can fail independently of the fleet.

---

## 10. Configuration

```jsonc
// config.json — env override: CPS_CONFIG
{
  "addr": ":8080",
  "store": { "driver": "memory" },              // memory | postgres
  "postgres_dsn": "postgres://cps:***@localhost:5432/cps?sslmode=disable",
  "status_poll_interval": "15s",
  "status_stale_after": "45s",
  "printer_dial_timeout": "3s",
  "printer_write_timeout": "5s",
  "queue_size_per_printer": 50,
  "max_attempts": 3,
  "confirm_timeout": "10s",
  "reject_when_not_usable": false,
  "max_payload_bytes": 1048576,
  "rate_limit_rps": 30,
  "printers": [
    { "printer_id": "wh1-pack-03", "name": "Packing station 3", "site": "wh1",
      "model": "ZT411", "driver": "zebra9100", "host": "10.20.1.53", "port": 9100 }
  ]
}
```

MVP: fleet is **declared in config** (git = fleet change history, reviewable, zero admin UI).
Roadmap: `POST/PUT /printers` + UI, writing to `printers` table, config used only as bootstrap seed.

---

## 11. Observability

**Metrics** (`GET /metrics`, Prometheus text format, no SDK dependency):

```
cps_jobs_total{state,printer_id}      cps_jobs_submitted_total{printer_id,client_id}
cps_dispatch_seconds{printer_id}      cps_confirm_seconds{printer_id}
cps_dispatch_failures_total{reason,printer_id}
cps_printer_usable{printer_id}        cps_printer_status_age_seconds{printer_id}
cps_queue_depth{printer_id}           cps_queue_full_total{printer_id}
cps_sent_unknown_total{printer_id}    cps_http_requests_total{route,status}
cps_http_request_seconds{route}
```

`cps_dispatch_seconds` = queue wait + write; `cps_confirm_seconds` = evidence gathering.
A large gap between the two means a slow printer, not a slow API.

**Logs** — structured JSON, one line per job lifecycle transition:

```json
{"ts":"…","level":"info","event":"job.state","job_id":"…","printer_id":"wh1-pack-03",
 "client_id":"erp","from":"sending","to":"printed","attempts":1,"bytes":86,"ms":41}
```

**SSE events** — `printer.status`, `job.state` (see §7.3).

**Health** — `/healthz` (process alive), `/readyz` (store reachable + config valid).

---

## 12. Security notes

- API keys: `sha256` at rest, constant-time compare, `expires_at` supported, per-key rate limit.
- Printer payloads contain business data (SKUs, addresses) → never logged; log only
  `bytes` + `sha256`. `GET /jobs` never returns the payload by default (`?include_payload=1`, admin only).
- No inbound printer ports exposed; only outbound 9100 from CPS. Printer stays on the warehouse LAN.
- All DB access parameterised; `meta` is `jsonb` and never interpolated into SQL.

---

## 13. Milestones

| # | Milestone | Done when |
|---|---|---|
| M1 | **Hardware spike** | `^HS` decoded on our actual model(s); confirm/poll behaviour verified; `~JA` cancel works |
| M2 | Fleet read | printers declared in config, `/printers*` + bulk status + SSE status, mock driver |
| M3 | Dispatch | `/jobs` with idempotency, per-printer queue, retry policy, `printed/sent_unknown/failed` |
| M4 | Harden | Postgres store, auth keys, rate limit, metrics, stuck-job sweeper, requeue on restart |
| M5 | Pilot | 1 site, 2–5 printers, ERP integration live, dashboard of `usable=false` |

M0–M2 are done in this repository, including the virtual printer with rendered label
previews and runtime token issuance (§15).

**Explicitly deferred**: rendering, USB agent, multi-site routing, UI, ZPL reprint tooling,
Link-OS MQTT confirmation, printer firmware management.

---

## 14. Open questions for stakeholders

1. Which printer models at each site (Zebra only, or TSC/Honeywell too)? → decides driver scope.
2. Is `^HS` available/allowed on those models, or is TCP reachability the only signal?
3. May dispatch continue on `media_out` (printer error surfaces later), or must CPS reject
   with `409 printer_not_usable`? *(Recommendation: allow, but expose `usable` so the caller chooses.)*
4. Who resolves `sent_unknown` — warehouse operator UI, or an ERP callback? *(Recommendation:
   expose in API now; add callback `POST {erp_callback}` on state change as a follow-up.)*
5. Do jobs need an SLA on queue wait (e.g. reject after 30s) or is `202` enough?

---

## 15. Test strategy and the virtual printer

The failure modes that matter here are all *systemic* (a printer is offline, a socket dies
mid-write, a document is retried). They cannot be found by unit-testing a parser, so the
tooling is built around a **virtual printer that misbehaves on command**.

### 15.1 `vprint` — printer simulator

| Capability | Why it exists |
|---|---|
| Raw ZPL on :9100, answers `^HS`, `~HI`, `~JA` | exercises the **real** driver path: two sockets, host-query handshake, framing |
| Counts each `^XA…^XZ` as one label | answers "did we double print?" with evidence instead of inference |
| Renders each label to PNG | see what the ERP actually asked for, without hardware |
| `POST /control {"offline":true}` | connection refused, stale caches, `503`-worthy fleet state |
| `POST /control {"close_on_print":true}` | printer dies **mid-write** → the partial-write / no-retry rule |
| `POST /control {"state":"always_printing"}` | never confirms → jobs must end `sent_unknown`, never `printed` |
| `POST /control {"status_delay_ms":2000}` | slow status → poll timeouts, stale flags |
| `POST /control {"state":"media_out"}` | consumable alarms on the dashboard |

Rendering is pluggable (`pkg/render`):

| Backend | What it is | When |
|---|---|---|
| **zebrash** (default) | in-process Go library, MIT — real carrier labels (UPS/FedEx/DHL/Amazon/USPS), PNG/PDF/SVG | always; no container, no network |
| **labelize** | self-hosted Rust microservice, 38+ commands, 12 symbologies, calibrated against Labelary | when you need exact parity; `docker compose --profile sim up labelize` |

Rendering is **never** on the print path: a slow or broken renderer annotates the label
with a warning and the print still succeeds. Printing is the product; the image is a
debugging aid. A panic inside a third-party renderer is recovered and turned into an error
— observed in the wild: zebrash v1.38.1 divide-by-zero on some `^GF` graphic fields.

`^POI` (inverted orientation) is rendered rotated 180° by default, because that is what
physically emerges; `-render-upright` shows the readable orientation used by published
reference images.

**Correctness is asserted, not assumed.** `examples/verify-render.sh` dispatches zebrash's
own `testdata/ups.zpl` through the full pipeline (HTTP → queue → TCP → parser → raster) and
diffs the result against the committed `ups_grayscale.png`:

```
reference (813, 1626)   ours (813, 1626)
bit-identical      1,321,938 / 1,321,938  (100.0000%)
mean |difference|  0.00000     max 0
differing > 32     0  (0.00000%)
PASS: render pipeline matches the published reference
```

The same check runs offline in `TestRenderMatchesUPSReference` (the payload is committed as
a fixture; the reference is cached and the test skips without network).

`vprint/cmd/zplpng` renders a file or stdin with the same backends — useful when you want to check
one label without starting a fleet.

### 15.2 Test layers

```
server/internal/driver/zebra_test.go            ^HS decoding table (unit)
server/internal/driver/zebra_integration_test.go real driver vs virtual printer over TCP
server/internal/job/service_test.go              retry policy, idempotency, FIFO, tenant isolation
internal/api/server_test.go               HTTP contract: auth, error codes, SSE, limits
internal/render/render_test.go            both backends, determinism, failure surfacing
internal/token/token_test.go              hashing, expiry, rotation, purge
test/e2e/flow_test.go                     THE FLOW, end to end, no mocks
examples/flow.sh                          the same flow against a live service, visible
```

`test/e2e` is the acceptance test and encodes the product's promises as assertions:

1. an unauthenticated caller cannot issue tokens; a client token cannot either
2. an issued token works immediately on the endpoints a business system uses
3. the fleet reads `idle` + `usable` with the raw `^HS` preserved
4. dispatch → `printed`, **exactly one label on the printer**
5. the label carries a rendered preview (so "what printed?" is answerable)
6. three retries replay the same job and print nothing extra
7. the same document id with a different payload is `409`
8. paper-out shows up on `?usable=false`
9. offline → the job never reports `printed` (it is `sent_unknown`/`failed`)
10. after recovery the printer works again
11. rotation kills the old credential instantly; revocation too
12. tenant isolation across two issued tokens
13. one wedged printer does not stall another

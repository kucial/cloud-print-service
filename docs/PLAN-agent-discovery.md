# Plan — USB agent, auto discovery, code reorganisation

Three related pieces of work: an edge **agent** for USB-only printers, **auto discovery**
of printers on the network, and a **reorganisation** of the codebase so both fit without
turning it into a swamp. Plus one more binary: a **demo web module** that doubles as the
reference integration for ERP/WMS developers.

Status: proposal. Nothing implemented yet.

### Locked decisions

| # | Decision |
|---|---|
| D1 | **USB-only printers are a first-class target**, not a fallback. The agent must drive raw USB. |
| D2 | **Server and packing PCs share a LAN**, and packing PCs can reach the server. Transport **A** (agent listens, server connects) is the design. The reverse tunnel is dropped, not deferred. |
| D3 | **Windows first** for the agent. Linux/macOS come after, and only if a site asks. |
| D4 | **`demo-web` is a Go binary**, open source, and is the reference integration for ERP/WMS developers. |

Consequences of D1+D3 that shape the rest of this document: USB code is on the critical
path, Windows is the only platform we must get right first, and every extra DLL or driver
install step on a packing PC is a support ticket waiting to happen — so the agent tries
hard to avoid needing one (§6.3).

---

## 1. Why the agent is mostly not a driver

The tempting design is "write a USB driver and teach the server about USB". That is wrong
twice over: it puts platform-specific code (udev rules, WinUSB, interface claiming) into
the server, and it multiplies the driver matrix.

**The key move: an agent makes a USB printer look exactly like a network printer.**

```
USB printer ──USB──▶ cps-agent (on the packing PC) ──TCP 127.0.0.1:19100──▶ cps (server)
                                                                          driver: zebra9100
                                                                          host: 127.0.0.1
                                                                          port: <agent port>
```

Consequences:

- `internal/driver` gains **nothing**. The server keeps one `zebra9100` driver, one
  queue model, one retry policy, one status decoder. The USB-ness is invisible above the
  agent's TCP port.
- All existing guarantees (per-printer FIFO, at-most-once after first byte, idempotency,
  `sent_unknown`) apply to USB printers **unchanged**.
- The agent is a small process: forwarder + discovery + a control channel. It can be
  written, audited and updated independently of the server.
- If the agent dies, the printer shows `offline` — exactly like a network cable pulled.
  No new failure mode.

What the agent *does* add, beyond forwarding:

1. **Discovery** of locally attached printers (and optionally its LAN segment).
2. **Registration** — the server cannot see a USB printer until someone tells it about it.
3. **Status push** — an agent-attached printer can report richer status (USB-level vendor
   codes) than the generic socket path.

**Non-goal for the agent**: rendering, queueing, retry, idempotency, template handling.
It moves bytes and reports facts. All policy stays in the server, where it is testable
without hardware.

---

## 2. Binary inventory

| Binary | Purpose | Depends on | Ships in |
|---|---|---|---|
| `cps` | server: fleet registry, dispatch queue, status poller, API, embedded panel, discovery coordinator | stdlib + render engine | warehouse server / VPS |
| `vprint` | virtual **network** printer: ZPL on :9100, `^HS`/`^HI`/`~JA`, renders every label to PNG, fault injection, gallery | render engine | dev/CI only |
| `cps-agent` | edge agent: USB + local discovery, TCP forwarder, enroll/heartbeat/status push | stdlib + (optional) libusb, OS printer APIs | packing PCs, one per site/PC |
| `demo-web` ✅ | business-module **integration reference**: a WMS-like console that issues real jobs with correct idempotency/retry/SSE handling | stdlib | repo only |
| `cpsctl` | admin CLI (exists): tokens, printers, agents, discovery | stdlib | operator machine |
| `zplpng` | dev tool (exists): render a file to PNG/SVG/PDF | render engine | repo only |

Four product binaries; `cpsctl` and `zplpng` are companions.

`vprint` also earns a second job: it is the **test double for the agent** — run it behind
`cps-agent` and the whole USB path (forwarder, registration, status) is exercisable on a
laptop with no hardware and no privileges. That is the main reason to build the agent's
transports behind interfaces.

---

## 3. Architecture

```
 ┌────────────┐        ┌──────────────────────────────────────────┐
 │ ERP / WMS  │        │                cps  (server)            │
 │ (API)      │        │  ┌────────────────────────────────────┐  │
 └────────────┘        │  │ fleet.Registry                      │  │
 ┌────────────┐        │  │  ├─ config source   (declared)     │  │
 │ demo-web   │  api   │  │  ├─ discovery src  (candidates)    │  │
 │ (reference)│◀──────▶│  │  └─ agent source   (registered)    │  │
 └────────────┘        │  ├────────────────────────────────────┤  │
 ┌────────────┐        │  │ dispatch: per-printer FIFO, retry  │  │
 │ cps panel  │ panel  │  │ driver:   zebra9100 | tsc | mock   │  │
 └────────────┘        │  │ tokens, metrics, render preview    │  │
                       │  └──────────────┬─────────────────────┘  │
 ┌────────────────┐    │                 │ discover (LAN)        │
 │ cps-agent (PC) │    │                 ▼                        │
 │  usb → TCP fwd │────┼──▶  warehouse LAN printers (discovery)   │
 │  enroll/heart  │    │                                          │
 └────────────────┘    └──────────────────────────────────────────┘
        ▲ control channel (agent dials out, HTTPS)
```

**Flow of a USB print job**

```
WMS ──POST /jobs{printer_id:"pc7-zd421-01"}──▶ cps
   registry resolves agent pc7 → loopback endpoint 127.0.0.1:19123
   per-printer worker ──ZPL──▶ agent forwarder ──USB──▶ printer
   agent pushes status ──▶ cps status cache ──▶ panel / WMS GET /printers
```

---

## 4. Fleet registry — the server-side change that makes this work

Today the fleet *is* config (`internal/printer.Manager` is built from a slice). Adding
agents and discovery means the fleet becomes **data with sources**:

```go
// internal/fleet
type Source interface {
    Name() string
    // Sync brings the source's view into the registry. It never mutates other sources.
    Sync(ctx context.Context, r *Registry) error
}

type Registry struct {   // printer_id -> Printer, plus source attribution
    Lookup(id string) (model.Printer, bool)
    Upsert(p model.Printer, src string) error
    Remove(id, src string) error          // only removes if no other source claims it
    List(f Filter) []model.Printer
    Candidates() []model.Candidate       // discovered, not yet approved
}
```

Sources:

| Source | Provides | Refresh |
|---|---|---|
| `config` | declared printers (today's behaviour) | at boot |
| `agent` | printers an enrolled agent reports | on register/heartbeat |
| `discovery` | **candidates** — found but not printers | every N minutes |

A printer is addressable when at least one source claims it. If config and an agent both
claim the same physical device, config wins and the agent's claim is ignored (with a log
line) — explicit beats implicit.

> This is the reason the reorganisation happens *before* the agent work: the registry is a
> new architectural seam, and putting it in a `printer` package that also holds the TCP
> driver would make it worse.

---

## 5. Discovery

### 5.1 Probes (pluggable, data-driven)

| Probe | Finds | Cost | Notes |
|---|---|---|---|
| `mdns` | printers advertising DNS-SD on the LAN | low | Link-OS printers are expected to advertise a `_pdl-datastream._tcp`-style service; **verify the exact type on our hardware in M0** (see risks) |
| `tcp` | printers that answer on :9100 but do not advertise | low | opt-in CIDR sweep, off by default |
| `usb` | attached USB printers | local only | agent-only; needs no privileges for enumeration, needs them for opening |
| `queue` | OS print queues (`lpstat`, Windows `Win32_PrintJob`) | local only | agent-only; useful to learn "this site uses driver-shares" |

### 5.2 Discovery yields candidates, not printers

A warehouse LAN contains whatever else is plugged in. Silently registering everything as
printers — and therefore printing to it — is not a safe default.

```
discovered device ──▶ Candidate{printer_id?, vendor, model, serial, host, port,
                              sources:[mdns|tcp|usb], first_seen, last_seen, probe_result}
        │
        ├──▶ operator approves (panel button / cpsctl printer adopt <id>)
        └──▶ auto-adopt ONLY if a policy matches (opt-in, off by default):
              e.g. site tag in the device name + allowed models + site is `trusted`
```

Policy sketch (config, opt-in):

```jsonc
"discovery": {
  "enabled": true,
  "mdns_interval": "60s",
  "candidates_ttl": "10m",          // unseen devices stop being listed
  "auto_adopt": {
    "enabled": false,
    "site": "wh1",
    "require_model_prefix": ["ZT4", "ZD4"],
    "require_source": "mdns"
  }
}
```

### 5.3 Identity

`printer_id` must stay human-stable (`wh1-pack-03`), so a device's identity is:

```
printer_id = <site>/<location>-<model>-<n>       operator-chosen on adopt
device_key = serial | mac | (vid,pid,port-path)  used to dedupe and to notice "the same
                                                device came back after a reboot"
```

---

## 6. Agent

### 6.1 Responsibilities

1. **Enroll** — one-time exchange of a bootstrap token for a long-lived agent token.
   `cpsctl agent enroll --site wh1` prints a token; the agent uses it once, then stores its
   own credential. A stolen bootstrap token is worthless after first use.
2. **Enumerate** — list USB devices that look like printers (vendor/product IDs, class,
   and optionally a probe of the device's status endpoint).
3. **Forward** — accept ZPL on a loopback (or LAN) TCP port per printer and write it to
   USB. One goroutine per printer; no buffering beyond the socket; no retry.
4. **Status** — poll the printer's local status channel and push it to the server; push
   changes immediately, heartbeat every 15s regardless.
5. **Announce** — host, OS, agent version, uptime, printer list, capabilities.
6. **Degrade loudly** — if a USB device disappears, say so (`unplugged`), never silently
   drop the printer.

### 6.2 Transports (start simple, keep the door open)

**Decision (D2): transport A only.** The agent listens per printer on the LAN and the
server connects. No reverse tunnel, no multiplexer, no extra state machine — the topology
guarantees it is unnecessary. One consequence to respect: the agent's listening port must
be reachable from the server, so the firewall rule is part of installation, not an
afterthought.

The byte-mover stays behind an interface anyway, so the USB and TCP backends are swappable
and testable:

```go
// internal/agent
type Channel interface {   // one logical printer
    Write(ctx context.Context, p []byte) (int, error)   // n>0 with err ⇒ do not retry
    Status(ctx context.Context) (model.Status, error)
    Close() error
}
```

### 6.3 Windows first: three ways to reach a printer, tried in order

Raw USB must be supported (D1), but it is also the *most expensive* way in on Windows —
it is the only tier that needs a driver change or an extra DLL on a packing PC. So the
agent escalates, cheapest first, and reports which tier it used:

| Tier | Mechanism | Needs on the PC | Verdict |
|---|---|---|---|
| **1. Driver share** | Zebra's driver exposes the printer on a **local TCP port** (`^HI` answers on e.g. `127.0.0.1:9100`). Found by probing listening loopback ports for a `^HI` reply. | driver only | **default** — no privileges, no extra software |
| **2. WinUSB** | Bind the printer's USB interface to WinUSB, drive it via `DeviceIoControl` on top of `golang.org/x/sys/windows`. | one-time device-driver swap (INF or Zadig) | **raw USB path** — pure Go, no DLL, no cgo |
| **3. libusb** | `github.com/google/gousb` | libusb.dll (LGPL-2.1) | fallback, build-tagged, only if tier 2 stalls |

Tier 1 deserves emphasis: on a correctly installed Windows packing PC it will very likely
win, and it means the agent needs **no** USB privileges at all. Tier 1 also tells us
something important during M0 — which printers already have a driver share, and therefore
which ones genuinely need tiers 2/3.

**Enumeration (no privileges required):** SetupAPI device enumeration filtered by vendor ID
(Zebra = `0x0A5F`) and USB device class, via `golang.org/x/sys/windows`. Enumeration is
always safe; *opening* a bulk endpoint is what may fail, and that failure is what drives
escalation.

**Agent packaging on Windows:**

| Concern | Approach |
|---|---|
| Runs as a service | `golang.org/x/sys/windows/svc` with `-install` / `-uninstall`; auto-start so it is up before the packing station app |
| Single instance | named mutex / lock file in `%ProgramData%\cps-agent` |
| Config & credentials | `%ProgramData%\cps-agent\config.json`, ACL-restricted; agent token written once, never logged |
| Firewall | `netsh advfirewall` rule for the agent's port, created by `-install`, limited to the site subnet where known |
| Status as a service | register with the SCM so `sc query cps-agent` works for support |
| Log | Windows Event Log (`windows/svc/eventlog`) plus a rolling file; operators on site will not read stdout |
| Upgrade | single `.exe` + `-install`; version reported in every heartbeat so the fleet view shows drift |

**Linux/macOS:** out of scope for v1 (D3). The `driver.Driver` and `agent.Channel`
interfaces are where they land later (udev rules on Linux; driver share on macOS).

### 6.4 Dependency impact

| Need | Choice | Effect |
|---|---|---|
| mDNS/DNS-SD | `github.com/brutella/dnssd` (MIT, active 2026) | new dep, pure Go, server + agent |
| Windows API | `golang.org/x/sys/windows` (already in the tree via deps) | **cgo-free**: SetupAPI, WinUSB, SCM, Event Log |
| raw USB, tier 2 | WinUSB via `x/sys/windows` `DeviceIoControl` | ~400–600 lines of ustruct work, but no DLL, no LGPL, no cgo |
| raw USB, tier 3 | `github.com/google/gousb` (Apache-2.0, **last push 2024**) | pulls **libusb = LGPL-2.1**; build-tagged, opt-in, ships no libusb binary |

Guard rails: CI builds `cps`, `vprint`, `demo-web`, `cpsctl` with `CGO_ENABLED=0` and fails
if any of them starts depending on the USB tier. The libusb tier is opt-in
(`-tags usb_libusb`) precisely so the default Windows build has zero native dependencies.

> Correction worth recording: I initially assumed a pure-Go WinUSB binding (`google/go-usb`)
> existed. It does not — hence the explicit tier 2 work rather than a dependency.

---

## 7. API additions

```
POST   /api/v1/admin/agents/enroll            {site} → one-time bootstrap token
GET    /api/v1/agents                          fleet of agents, health, printer counts
POST   /api/v1/agents/register                 (agent token) upsert agent + printers
POST   /api/v1/agents/heartbeat                (agent token) status snapshot
POST   /api/v1/agents/{agent_id}/printers/{pid}/status   (agent token) status push
POST   /api/v1/admin/discovery/scan            force a discovery sweep
GET    /api/v1/admin/discovery/candidates      found devices not yet adopted
POST   /api/v1/admin/discovery/candidates/{key}/adopt   → create printer (id, site, name)
DELETE /api/v1/admin/discovery/candidates/{key}         dismiss
```

Plus `cpsctl agent enroll|list|revoke`, `cpsctl discover scan|list|adopt`.

Panel additions: **Agents** view (health, version, printer counts, last seen) and
**Discovered** view (candidates with Adopt / Dismiss). Both read-only until an operator acts.

Auth model for agents: agent tokens are a separate token class (`kind=agent`) with only the
three `/agents/*` write routes — an agent cannot mint tokens, read jobs, or dispatch.

---

## 8. Data model additions

```sql
create table agents (
  id          text primary key,
  site        text not null default '',
  token_hash  text not null unique,
  hostname    text not null default '',
  os          text not null default '',
  version     text not null default '',
  last_seen   timestamptz,
  created_at  timestamptz not null default now()
);

create table printers (
  -- existing columns …
  source      text not null default 'config',   -- config|agent|discovery
  source_ref  text not null default '',        -- agent id or device key
  serial      text not null default '',
  device_key  text not null default ''         -- dedupe across restarts
);
create unique index printers_device_key_idx on printers (device_key) where device_key <> '';

create table printer_candidates (
  device_key  text primary key,
  source      text not null,                   -- mdns|tcp|usb|queue
  vendor      text, model text, serial text,
  host        text, port int,
  site        text not null default '',
  detail      jsonb not null default '{}',
  first_seen  timestamptz not null default now(),
  last_seen   timestamptz not null default now(),
  dismissed   boolean not null default false
);

create table api_tokens (
  -- existing columns …
  kind        text not null default 'business'  -- business|agent
  agent_id    text
);
```

---

## 9. Code reorganisation

### 9.1 Problems today

- `internal/printer` conflates three things: the **driver** (transport), the **manager**
  (cache, polling, usability) and the **fleet** (what exists). Discovery/agents need to
  split exactly along those lines.
- `internal/api` is doing auth, token issuance, HTTP, SSE, the panel and preview.
- `internal/fakeprinter` is test tooling that lives beside production code and imports
  the render engine.
- `internal/job` owns policy that will need to be reusable by non-HTTP callers (demo-web's
  embedded mode, a future CLI).

### 9.2 Target layout

```
cmd/
  cps/            server            cps-agent/   edge agent
  cpsctl/         admin CLI         vprint/      virtual network printer
  demo-web/       integration ref    zplpng/      dev tool

internal/
  domain/         types, states, errors, validation          ← imports nothing
  fleet/          Registry, sources, usability, status cache
  discovery/      probes: mdns, tcp, candidates policy
  driver/         transport only: zebra9100, tsc9100, mock
  dispatch/       queue, worker, retry, idempotency, confirm
  store/          Store interface, memory, postgres
  token/          business + agent credentials
  httpapi/        handlers, authn/authz, sse, admin routes, preview
  panel/          embedded SPA wiring (web/dist via go:embed)
  render/         zpl → image (zebrash | labelize)
  virtual/        virtual printer + gallery                    ← dev/CI only
  agent/          usb, forwarder, enroll, heartbeat, transports ← cmd/cps-agent only
  observ/         metrics, logging
  platform/       config, paths, service files

web/dist/         panel assets (embedded)
test/e2e/         flow tests (virtual printers + real HTTP)
docs/             design, plan, runbook
examples/         scripts
```

### 9.3 Rules that keep it from drifting again

1. `domain` imports nothing internal. Enforceable with a test.
2. Dependency direction: `httpapi → dispatch/fleet/token`, `dispatch → driver+store+fleet`,
   `fleet → domain`. Never the reverse.
3. `agent` and `virtual` are **leaf toolkits**: nothing in the server imports them.
4. `driver` knows nothing about jobs, HTTP or the registry.
5. One `Store` interface; memory and Postgres live in `store/`.
6. No package over ~800 lines. (`internal/api` today is the counterexample.)

### 9.4 Staged, no behaviour change

| Step | Move | Gate |
|---|---|---|
| R1 | `internal/model` → `internal/domain`, add `domain_test` import guard | build + tests green |
| R2 | `internal/printer/driver.go`+`zebra.go`+`mock.go` → `internal/driver` | tests green |
| R3 | `internal/printer/manager.go` → `internal/fleet` (minus the fleet bits) | tests green |
| R4 | `internal/api` → `internal/httpapi` + `internal/panel` | tests green |
| R5 | `internal/fakeprinter` → `internal/virtual` | tests green |
| R6 | `internal/bus`,`internal/metrics` → `internal/observ` | tests green |
| R7 | introduce `fleet.Registry` + `config` source; config behaviour unchanged | **all existing tests pass untouched** |

Each step is one commit, no logic edits. If a step turns out to need logic changes, the
boundary is wrong — stop and redraw it.

---

## 10. Phases and acceptance criteria

| Phase | Deliverable | Done when |
|---|---|---|
| **M0 · Observe** | sniffing script + `vprint` mDNS advertisement | we know what our real printers advertise (service type, TXT records, port) and have the finding written down |
| **M1 · demo-web** ✅ | `demo-web` binary + annotated reference README | **done** — `cmd/demo-web`, retry rules pinned by tests, verified live: 3 full wave resends → 0 extra labels |
| **M2 · Reorg R1–R6** | package moves | every existing test passes with no behavioural change |
| **M3 · Registry** | `fleet.Registry` + sources + config source (R7) | config-only behaviour is byte-identical; registry has unit tests for precedence |
| **M4 · Discovery** | mdns probe + candidates + adopt/dismiss + panel + CLI | a real printer appears as a candidate, is adopted, then prints |
| **M5 · Agent v1** | enroll, enumerate, forward, register, heartbeat, panel/CLI | `cps-agent` in front of a `vprint` on the same laptop: server dispatches, status flows, unplug is detected |
| **M6 · Real USB (Windows)** | tier 1 probe + tier 2 WinUSB backend + service install + firewall | an actual USB printer with **no driver share** prints through the agent on Windows; unplug is detected; the agent survives a service restart |
| **M7 · Harden** | agent fleet management, token rotation, metrics, requeue on agent loss, runbook | agent restart mid-job leaves a truthful job state |

M5 is the important one: it validates the whole design with zero hardware.

---

## 11. Testing strategy

| Layer | Approach |
|---|---|
| `domain`, `dispatch`, `fleet` | table tests (already the house style) |
| discovery probes | each probe gets a **fake responder** (our own mDNS advertiser in `internal/virtual`) — no real hardware in CI |
| agent | `vprint` behind the agent = full stack on a laptop; USB behind an interface with a fake device |
| registry | precedence, dedupe across restarts, candidate TTL, adopt/dismiss |
| end-to-end | `test/e2e` grows: agent enrolled → printer registered → job → status → unplug → recover |
| manual | `examples/` scripts + a real-hardware runbook per OS |

No new test requires hardware. Hardware runs are a separate, documented checklist.

---

## 12. Risks

| Risk | Impact | Mitigation |
|---|---|---|
| Zebra mDNS service type differs from expectation | mdns probe finds nothing | M0 verifies first; probe table is config; tcp sweep as fallback; `~HI` identify confirms every candidate before adopt |
| Raw USB permissions (udev/WinUSB) | agent needs root / manual setup | prefer driver shares; ship udev rules; document the Windows WinUSB step |
| `gousb` unmaintained | future USB breakage | tier 3 is opt-in and isolated; tier 2 (x/sys) is the supported path |
| WinUSB binding needs `ustruct` work | schedule risk on tier 2 | tier 1 likely covers most sites; tier 2 is scoped to devices that genuinely need it |
| libusb is LGPL-2.1 | shipping it is a compliance decision | default build ships no libusb; tier 3 is opt-in and never bundles the DLL |
| Agent dials out vs server dials in | agents behind NAT never connect | A now, B designed-for; agent health is visible so it fails loudly |
| Duplicate labels from a flaky USB hop | inventory damage | the agent never retries and never buffers; the server's "no retry after first byte" rule applies unchanged |
| Fleet sprawl from auto-adopt | printing to the wrong device | auto-adopt off by default, policy-scoped, and reversible |
| Reorg churn | long review, merge conflicts | staged, one package per commit, no behaviour change, no big-bang branch |

---

## 13. Resolved, and still open

**Resolved by this round:** USB-only printers are supported (D1); same-LAN topology with
transport A (D2); Windows-first agent (D3); `demo-web` as an open-source Go binary (D4).

**Still open:**

1. **Who approves adoption?** Warehouse operator, IT, or auto-policy by site? Default in the
   plan is operator-initiated, auto-adopt off.
2. **Do the packing PCs run the Zebra driver today?** This decides whether M6 is one week
   (tier 1 covers it) or a month (tier 2 WinUSB work). M0 answers it on one machine.
3. **Printer enable/disable from the panel?** Config-only today, because git is the fleet
   history. With discovery it becomes tempting — but it needs an audit trail first.
4. **Does `demo-web` need its own persistence** (a real order file), or is generated
   in-memory demo data enough for a reference?
5. **Agent auto-update?** Handy on Windows, but a printing agent that can rewrite itself is
   a supply-chain decision, not an implementation detail.

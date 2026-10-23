# Project status

Snapshot of where Cloud Print Service stands: what exists, what is verified, what is
deliberately not built, and what to do next.

- **Design:** [DESIGN.md](DESIGN.md) — architecture, state machine, API contract, failure policy
- **Roadmap & decisions:** [PLAN-agent-discovery.md](PLAN-agent-discovery.md) — agent, discovery, reorg
- **Status file last updated:** after commit `f032494`

---

## 1. One-paragraph summary

A single Go service that sits between ERP/WMS and warehouse printers — label printers
(ZPL), receipt printers (ESC/POS) and office printers (IPP, PCL/PostScript). Business
systems own the content (ZPL, ESC/POS, or a PDF/image CPS converts); the service owns
**fleet visibility** and **dispatch safety**. Around it: a virtual printer that misbehaves on command, an edge agent that
makes USB printers look like network printers, an admin CLI, a reference WMS integration,
and an embedded operator panel. 12 commits, ~12.4k lines of Go, no hardware required for
any test.

---

## 2. Milestones

| # | Milestone | Status | Evidence |
|---|---|---|---|
| M0 | Observe: what do our printers advertise? | **partial** | `vprint -mdns` advertises `_pdl-datastream._tcp`; real-printer capture still owed (needs one machine on a warehouse LAN) |
| M1 | `demo-web` reference integration | **done** | retry rules pinned by tests; live: 3 wave resends → 0 extra labels |
| M2 | Reorganisation R1–R6 | **done** | one commit per package, all pre-existing tests unchanged |
| M2b | `internal/store` extraction + layer guard | **done** | `internal/domain/layers_test.go` fails if a layer reaches upward |
| M3 | `fleet.Registry` + sources | **done** | precedence, provenance, candidate lifecycle |
| M4 | mDNS discovery + adoption | **done (unit + probe tested; live sweep needs a warehouse LAN)** | candidates API, panel + CLI, `vprint -mdns`; see §6 for what blocked live proof on this host |
| M5 | Agent v1 (driver-share tier) | **done** | end-to-end against `vprint`, no hardware |
| M6 | Real USB (printer-class driver) | **built** | pure Go via `usbprint.sys` (Windows) / `usblp` (Linux); opt-in libusb on macOS (`-tags usb_libusb`). Verified end to end on a real GK888t over libusb; Windows/Linux paths unit-tested only |
| M7 | Harden: runbook, agent-loss recovery, metrics polish | **done** | [RUNBOOK.md](RUNBOOK.md); `FailOnAgentLost` + panel Agents/Discovery views; metrics for scans/adoptions; agent-loss e2e test |
| M8 | Receipt (ESC/POS) and office (IPP, PCL/PostScript) printers | **done against virtual printers** | drivers `escpos9100`, `ipp`, `pdl9100`; agent classifies USB printers by device id; PDF/PNG/JPEG converted per printer (poppler in Docker); `vprint -language escpos|ipp|pdl`. No real receipt or office printer tried yet (§5) |
| M9 | Epson inkjets (ESC/P-R) | **done on a real Epson L355 over Wi-Fi** | driver `escpr9100` (raw 9100, never queried: status is reachability); `pkg/escpr` encodes colour raster at 360 dpi on A4, black ink only for grey jobs; PDF/PNG/JPEG converted to it; discovery identifies Epsons from their TXT record and sends `~HI` only to printers that may be Zebras; the agent serves ESC/P-R USB printers and probes no other maker's printer. Verified: encoder test page and a colour PNG through the job API (adopted from discovery). Not tried: PDF on this printer (needs poppler), the USB path on real hardware |

---

## 3. What is verified, and how

Everything below was exercised end to end during development, not just unit-tested.

| Property | How it was verified |
|---|---|
| Dispatch → printed, exactly one label | `vprint` counts `^XA…^XZ`; live curl runs |
| ERP retry ×3 prints nothing extra | 3 resends of a whole wave → printer label count unchanged |
| Offline printer never reports a false success | `vprint` `{"offline":true}` → job `sent_unknown` |
| Ambiguous outcome is never auto-retried | attempt log: `write_error 0 bytes` retried, `partial_write` not |
| Status stays honest under stale sockets | status socket redials once; cached snapshot never reads `usable` without evidence |
| Render matches a published reference | `examples/verify-render.sh`: zebrash `ups.zpl` → **1,321,938 / 1,321,938 pixels identical** |
| Agent end to end | enroll → discover → forward → register → heartbeat → print, all against `vprint` |
| Agent credential cannot dispatch | `TestAgentRegistersAndServesAPrinter` + `TestCredentialKindFlowsIntoRequest` |
| Tenant isolation | cross-client job read/cancel → `404`, never `403` |
| Layering | `TestLayerDependencyDirection` fails on an upward import |
| Receipt printer end to end | raw ESC/POS, PNG→raster, copies, paper-out → not usable, test receipt: `TestReceiptPrinterEndToEnd` against `vprint -language escpos`; through the agent's local-share tier on this Mac |
| Office printer over IPP | copies as a job attribute, confirmed by the printer's job state; aborted → failed; held past `track_timeout` → `sent_unknown`; refused format not retried: `TestOfficePrinter*`; discovered over real mDNS and adopted |
| Office printer on 9100 (PJL) | status and languages from PJL; discovery asks over PJL, never `~HI` (0 stray pages over 4 scans, real mDNS); PCL test page, PostScript via the agent |
| PDF conversion | in the Docker image (poppler 25.04): the A4 test page as a receipt raster, a 4×6 `^GFA` label (rendered back with zebrash), PostScript for a PCL/PS printer |

Run everything:

```bash
go test -race ./...                                  # 10 packages, all green
./examples/verify-render.sh                         # render vs published reference
VPRINT_ADMIN=http://localhost:19200 ./examples/flow.sh   # scripted flow against a live service
```

CI (`.github/workflows/ci.yml`): gofmt, vet, `CGO_ENABLED=0` build of all five binaries,
`go test -race`, the e2e flow, the render check, and the Docker build.

---

## 4. Binaries

| Binary | Status | Purpose |
|---|---|---|
| `cps` | done | server: fleet registry, dispatch queue, status poller, REST API, embedded panel, ZPL preview, discovery, agent surface, PDF/image conversion |
| `cpsctl` | done | admin CLI: tokens, printers, **agents**, **discover** |
| `vprint` | done | virtual network printer: real ZPL, `^HS`/`~HI`/`~JA`, renders every label, gallery, fault injection, `-mdns` advertise; `-language escpos|ipp|pdl` for receipt and office printers |
| `cps-agent` | done (TCP share + USB) | edge agent: discover, forward, register, heartbeat, status push; label, receipt, office and Epson inkjet printers; `-tags gui` builds `cps-agent-gui.exe` (tray icon + local setup/status/settings page) |
| `demo-web` | done | reference WMS integration (open source, meant to be read) |
| `zplpng` | done | dev tool: file/stdin → PNG/SVG/PDF |

---

## 5. Known gaps (deliberate)

| Gap | Why | Next step |
|---|---|---|
| **USB on Windows not yet run on hardware** | the agent opens `usbprint.sys` interfaces directly (no driver swap, coexists with ZDesigner); compiled and unit-tested, never run on a Windows PC | run the release zip on one packing PC with a USB Zebra |
| **Real printers speak `~HS`, not `^HS`** | server, agent and vprint used a made-up `^HS` reply until the GK888t capture; now real three-line `~HS` is sent and decoded (`pkg/printer`), legacy `^HS` still parsed. `~HS` has no media/ribbon-low flags | `~HQES` on Link-OS printers for low warnings |
| Postgres store | single servers use SQLite (default: jobs, tokens, agent credentials and panel edits survive restarts); Postgres only matters for several servers | implement `store.Postgres` if a site ever needs more than one server |
| mDNS live sweep | see §6 | one machine on the warehouse LAN |
| Agent auto-update / fleet rollout | Windows service (`-enroll <token> -install`: SCM, recovery restarts, firewall rule, ACL'd config, `agent.log`) is built and exercised by the `agent-windows` CI job; binaries come from `release-agent.yml` (`agent-v*` tags), unsigned | code-sign the `.exe`; self-update or MSI/Intune rollout |
| Agent finds printers only at start | no hot-plug rescan; with none found the process exits and the service manager (or, in the desktop app, its supervisor) restarts it after 30s | periodic rescan inside `Run` |
| Desktop agent not yet run on Windows | `cps-agent-gui.exe` is cross-built and its panel and supervisor are unit-tested; end-to-end run only on macOS (setup, settings change, print through `vprint`). Tray, Start at login and the firewall prompt are untested on Windows | run the release zip on the packing PC |
| **No real receipt or office printer tried** | ESC/POS, IPP and PJL are implemented to the specs and tested against `vprint`; the USB device-id table uses representative ids, not captures | print to one Epson/Xprinter and one office printer; capture their device ids, `DLE EOT` and IPP attributes as test data |
| An ESC/P-R job reports printed after the settle window | the inkjet answers nothing on 9100, so a paper jam or empty ink tank is invisible to CPS; the job ends `printed` with the message "Sent; the printer cannot confirm printing", as for every printer that reports no state | read status from Epson's SNMP or its web page, if it proves to matter |
| ESC/P-R prints A4 only, at normal quality | one paper size and quality are built in | add Letter/A5 and draft/high as printer settings when asked |
| An IPP job holds the printer's queue while followed | the per-printer worker waits for the job's outcome (up to `track_timeout`, 10 min) before the next job | send while following, if office printers prove slow |
| Document preview has no panel button | `POST /api/v1/admin/printers/{id}/preview` returns the converted first page as a PNG; the panel's Preview tab is still ZPL-only | add a document upload to the Preview tab |
| PDF to an IPP printer without PDF | only single-page PDFs go as JPEG; URF/PWG raster is not produced | add a PWG raster encoder if such printers turn up |
| Job callback to ERP | events are on the bus | expose a webhook |

---

## 6. In-flight: mDNS discovery live verification

**State:** implemented, unit-tested (7 tests, including the "empty types scan nothing"
regression), wired into the server with `/admin/discovery*` and `cpsctl discover *`.
**Not yet demonstrated:** the server discovering an advertised printer.

What was tried, on this machine:

1. `vprint -mdns -mdns-name "Lab Packer 1"` starts and logs `advertising over mDNS`.
2. The `dnssd` CLI **did** browse it and resolve it — twice, with entries and TXT records.
3. Our probe and a bare `dnssd.LookupType` call in a separate process both return **0
   entries**, while the CLI does.
4. Restarting the advertiser changed nothing.

So the library, the service type and the advertisement are all demonstrably correct; what
is unreliable is **multicast discovery between two processes on this host** (the same
library, same machine, CLI works — in-process probe does not). That is an environment
question, not a design one, and it is exactly what M0 was for: settle it on real hardware
before trusting it in production.

Interim position: discovery is **off by default** in `config.example.json`, and even when
on it can only produce *candidates* — an operator must adopt one — so a flaky probe can
never make a wrong device dispatchable. The unit tests pin the candidate lifecycle
(seen → dismissed → expired → adopted → one addressable printer) without needing a network.

To close it out:

```bash
go run ./vprint/cmd/vprint -mdns -mdns-name "Lab Packer 1"        # terminal 1
curl -XPOST -H "Authorization: Bearer $ADMIN" \
     localhost:8080/api/v1/admin/discovery/scan | jq        # terminal 2
```

Then on a machine on the warehouse LAN, run the same two commands against a real printer
and record what it advertises (that is the M0 deliverable: service type, TXT keys, port).

---

| Gap closed by this round | Where |
|---|---|
| Panel shows Agents + Discovery | `web/dist/app.js`, routes added |
| Agent-loss fails queued jobs loudly | `internal/job/agent_loss.go`, `TestAgentLossFailsQueuedJobs` |
| Runbook for operators | [RUNBOOK.md](RUNBOOK.md) |
| Print sockets are per-job (reboot-safe) | `internal/driver/zebra.go` |
| Status-timeout desync | `internal/driver/zebra.go`, regression-tested |
| Panic in a job is contained | `internal/job/service.go: executeSafely` |
| Confirmation no longer trusts an immediate idle | `internal/job/service.go: confirm` |

## 7. Architecture decisions worth not undoing

1. **At-most-once after the first byte.** Bytes on a printer + no confirmation = `sent_unknown`
   for a human. Never retried. Retrying an ambiguous outcome duplicates labels.
2. **Idempotency on `(client_id, external_id)`.** Retry returns the original job;
   same id + different payload is `409`.
3. **Two sockets per printer.** Status replies interleave into the print stream.
4. **The status API never touches a printer.** A poller fills a cache; a hung TCP write
   cannot block an ERP request.
5. **Per-printer FIFO, one worker per printer** — and *exactly one poller*, enforced.
6. **`usable` needs evidence.** Never observed, stale, offline, disabled or out of media
   all mean not usable. `unknown` is never optimistic.
7. **A USB printer behind the agent is a loopback TCP endpoint.** No USB code in the server.
8. **Discovery finds; a human decides.** Findings are candidates until adopted.
9. **Token kind decides reach, not role.** An agent credential can register and report
   status, never dispatch or read business data.
10. **The layering is a test, not a convention** (`internal/domain/layers_test.go`).

---

## 8. Commit map

```
a8d3e03  initial: service, vprint, tokens, panel, demo-web, render, tests
11a1359  fix: poll for printer state instead of asserting it immediately
3d66b66  refactor: internal/printer → internal/driver (transport)
76b2a03  refactor: internal/printer → internal/fleet (manager/status)
53d4fca  refactor: internal/api → internal/httpapi
9ab9dde  refactor: internal/fakeprinter → internal/virtual
737858c  refactor: bus + metrics → internal/observ
4024b2c  feat: cps-agent + fleet.Registry (R7) + token kinds
6949215  fix(fleet): read the printer spec under its lock
2e2864b  docs: record the agent/registry deliverables
bed9b2d  refactor: internal/store + layer-direction tests
7cdd83f  feat(discovery): mDNS probe, candidates, adoption
f032494  fix(discovery): default the mDNS probe's service types
```

---

## 9. Suggested next three tasks

1. **M0 on real hardware** (30 min): one packing PC + one printer → capture what it
   advertises, confirm tier 1 or decide tier 2 is needed.
2. **Windows USB tier 2** (WinUSB, `-tags winusb`): the only path to true USB-only support
   (D1). Cross-compile check: `GOOS=windows GOARCH=amd64 go build ./agent/cmd/cps-agent`.
3. **Adopted discovery printers are not persisted**: after a restart they are candidates
   again. Persist adoptions like operator edits (the store already has the table pattern).

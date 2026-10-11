# Project status

Snapshot of where Cloud Print Service stands: what exists, what is verified, what is
deliberately not built, and what to do next.

- **Design:** [DESIGN.md](DESIGN.md) — architecture, state machine, API contract, failure policy
- **Roadmap & decisions:** [PLAN-agent-discovery.md](PLAN-agent-discovery.md) — agent, discovery, reorg
- **Status file last updated:** after commit `f032494`

---

## 1. One-paragraph summary

A single Go service that sits between ERP/WMS and warehouse label printers. Business
systems own the label content (ZPL); the service owns **fleet visibility** and **dispatch
safety**. Around it: a virtual printer that misbehaves on command, an edge agent that
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
| M4 | mDNS discovery + adoption | **code + unit tests done; live sweep blocked** | see §6 |
| M5 | Agent v1 (driver-share tier) | **done** | end-to-end against `vprint`, no hardware |
| M6 | Real USB on Windows (tier 2/3) | **not built** | build-tagged stubs, see §5 |
| M7 | Harden: runbook, agent-loss recovery, metrics polish | **partial** | agent revocation tested; runbook still owed |

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
| `cps` | done | server: fleet registry, dispatch queue, status poller, REST API, embedded panel, ZPL preview, discovery, agent surface |
| `cpsctl` | done | admin CLI: tokens, printers, **agents**, **discover** |
| `vprint` | done | virtual network printer: real ZPL, `^HS`/`~HI`/`~JA`, renders every label, gallery, fault injection, `-mdns` advertise |
| `cps-agent` | done (tier 1) | edge agent: discover, forward, register, heartbeat, status push |
| `demo-web` | done | reference WMS integration (open source, meant to be read) |
| `zplpng` | done | dev tool: file/stdin → PNG/SVG/PDF |

---

## 5. Known gaps (deliberate)

| Gap | Why | Next step |
|---|---|---|
| **Raw USB (tier 2/3)** | Windows-first means WinUSB via `x/sys/windows` or libusb; both are build-tagged stubs so the default build stays pure Go with no native deps | implement WinUSB behind `-tags winusb`; verify on one real packing PC |
| **Tier 1 is what ships today** | most Windows packing PCs get a driver TCP share, which needs no privileges at all | measure on real hardware before investing in tier 2 |
| Postgres store | schema exists (`db/schema.sql`), binary uses the memory store | implement `store.Postgres`; issued tokens currently die on restart |
| mDNS live sweep | see §6 | one machine on the warehouse LAN |
| Agent auto-update / fleet rollout | Windows service install (`-install`) is stubbed for non-Windows | implement `svc` wiring, firewall rule, SCM reporting |
| Panel has no Agents/Discovery views | APIs exist and are tested | add two views + `cpsctl` parity in the panel |
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
go run ./cmd/vprint -mdns -mdns-name "Lab Packer 1"        # terminal 1
curl -XPOST -H "Authorization: Bearer $ADMIN" \
     localhost:8080/api/v1/admin/discovery/scan | jq        # terminal 2
```

Then on a machine on the warehouse LAN, run the same two commands against a real printer
and record what it advertises (that is the M0 deliverable: service type, TXT keys, port).

---

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
   (D1). Cross-compile check: `GOOS=windows GOARCH=amd64 go build ./cmd/cps-agent`.
3. **Postgres store** (unblocks durable tokens + job history): `store.Postgres` against the
   existing schema, then re-issue-free restarts.

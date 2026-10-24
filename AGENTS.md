# AGENTS.md

Guidance for coding agents (Claude Code, Codex, Cursor, …) working in this repository.
For using or deploying CPS rather than changing it, read `site/llms-full.txt`.

## What this is

Cloud Print Service: a Go print gateway between business systems and warehouse printers.
The server (`cps`) takes jobs over HTTP and delivers them to label (ZPL), receipt (ESC/POS) and
office printers (IPP, PCL/PostScript, Epson ESC/P-R), directly over the LAN or through
`cps-agent` on a machine with USB printers. User docs: `docs/INSTALL.md`; design and failure
policy: `docs/DESIGN.md`; what is verified on real hardware: `docs/STATUS.md`.

## Layout

| path | what |
|---|---|
| `server/cmd/cps`, `server/cmd/cpsctl` | server and admin CLI |
| `server/internal/domain` | core types; `layers_test.go` enforces the import graph |
| `server/internal/{driver,fleet,job,convert,discovery,store,token,config,observ}` | printer drivers, fleet/status polling, job queue and state machine, PDF/image conversion, mDNS discovery, SQLite/memory store, tokens, config, metrics |
| `server/internal/httpapi` | HTTP API; `openapi.yaml` and `openapi.json` are the contract, both hand-maintained |
| `server/web` | operator panel (React + TS + Vite); `dist/` is embedded with `go:embed` and committed |
| `agent/cmd/cps-agent` | edge agent: USB backends per OS, Windows service and GUI (`-tags gui`) |
| `pkg/` | shared libraries: `printer` (kinds, formats, IEEE 1284 ids), `escpos`, `escpr`, `ipp`, `render` (ZPL → PNG), `agentapi` |
| `vprint/` | virtual printer used by tests and demos |
| `business-demo/` | sample business system; its own module in `go.work` |
| `packaging/` | ASUS router packages (koolshare plugin, `/jffs` tarball) |
| `site/` | GitHub Pages project page, English (`index.html`) and zh-Hans (`zh/index.html`), plus `llms.txt` |

## Checks (same as CI, `.github/workflows/ci.yml`)

```bash
gofmt -l .                                   # must print nothing
go vet ./...
CGO_ENABLED=0 go build ./...                 # the server must build without cgo
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go vet ./agent/... && \
  GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go vet -tags gui ./agent/...
go test -race -count=1 ./...
(cd business-demo && go test -race -count=1 ./...)
go test -count=1 -run 'TestBusinessSystemFlow|TestTwoPrintersAreIndependent|TestTenantIsolation' ./server/test/e2e/
```

PDF conversion tests skip without poppler (`pdftoppm`); install it to run them.
On macOS the agent's USB backend needs `-tags usb_libusb` and `brew install libusb`.

## Rules

- **Never retry after bytes reach a printer.** A job whose bytes were flushed ends `printed`
  (confirmed), `sent_unknown` (unconfirmed, a human resolves it) or `failed`; it is never
  re-sent automatically. Keep this when touching `server/internal/job` or drivers: a
  duplicate shipping label is a real inventory defect.
- Do not call a job confirmed without evidence from the printer. When a driver has no status
  channel, say so in the job's detail and message.
- Respect the layer guard. If a package needs a new import, update the map in
  `server/internal/domain/layers_test.go` deliberately; do not work around it.
- Keep the server and `pkg/` cgo-free. libusb belongs only to the agent, behind `usb_libusb`.
- API changes: update `openapi.yaml` and `openapi.json` together. Both are served as is and
  nothing checks that they agree, so keep them in step by hand. Update the user docs, and
  `site/llms-full.txt` if the integration contract changes.
- Panel changes: edit `server/web/src`, then `npm run build` (or `pnpm build`) in
  `server/web` and commit `server/web/dist` with the source. On a rebase conflict in
  `dist/`, rebuild instead of merging the bundle.
- Discovery and the agent must not send probes (e.g. Zebra `~HI`) to printers that are not
  Zebras: on an inkjet or laser that prints junk. `printer.MayBeZebra` gates this.
- Tests use `vprint` or in-process fakes, never real printers. Printing to a real device
  costs paper and ink: ask the user first.
- The project page has two languages. A change to `site/index.html` needs the same change in
  `site/zh/index.html`, which shares `site/style.css`.
- Commits: conventional style, `feat(agent): …`, `fix(job): …`, `docs: …`, `test(convert): …`;
  run `gofmt` before committing.

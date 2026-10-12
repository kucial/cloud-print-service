# Installation

Cloud Print Service (CPS) is one server plus, optionally, an agent on each PC that has USB
printers. **The server runs standalone**: a single binary and a JSON config file, with the
operator panel built in. No database, no Docker and no Docker Compose are required.

| Component | What it is | Where it runs |
|---|---|---|
| `cps` | the server: API, operator panel (`/ui/`), print queues, printer status | one machine on the same LAN as the printers |
| `cpsctl` | admin CLI (tokens, agents, printers, diagnostics) | anywhere that can reach the server |
| `cps-agent` | makes USB printers on a PC reachable by the server | each packing PC with USB printers |

`dev/docker-compose.yml` is a development mock-up of a site (virtual printers, a demo
business system, two networks). It is not a deployment manifest.

---

## 1. Before you start

### Network

| From | To | Port | Why |
|---|---|---|---|
| business systems, browsers | server | 8080 (configurable) | API and panel |
| server | network printers | 9100 | print data and status |
| server | agent PCs | 19100 and up, one per USB printer | print data and status for USB printers |
| agent PCs | server | 8080 | registration and heartbeats |
| server | LAN multicast (mDNS) | 5353/udp | optional printer discovery |

The server connects **to** the agent PCs, so their firewall must allow the agent's ports
(the Windows service installer adds that rule).

### What survives a restart — read this

The server currently keeps its working state **in memory**. A Postgres store is planned
(`server/db/schema.sql`), but not built; `store_driver: "postgres"` is rejected for now.

| Survives a restart (in `config.json`) | Lost on restart |
|---|---|
| network printers declared in `printers` | tokens issued with `cpsctl token issue` |
| bootstrap keys in `api_keys` | agent credentials — **every agent must enroll again** |
| all other settings | job history and queued jobs |
| | edits made in the panel (names, paper size, direction, …) |

Until the persistent store lands:

- Put every **network printer** and its settings (paper size, direction) in `config.json`.
- Give **long-lived integrations** a strong key in `api_keys` rather than an issued token.
- Plan restarts: after one, re-enroll each agent (section 5).

Settings saved **on the printer itself** (label top / left shift, calibration) are not
affected — they live in the printer.

---

## 2. Install the server

### Build

The server is built from source (Go 1.26 or newer). From the repository root:

```bash
# for the machine you are on
go build -o cps    ./server/cmd/cps
go build -o cpsctl ./server/cmd/cpsctl

# or for a Linux server from any OS — no C toolchain needed
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o cps    ./server/cmd/cps
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o cpsctl ./server/cmd/cpsctl
```

The panel is compiled into `cps`; there is nothing else to copy.

### Configure

Start from `server/config.example.json`, or from this minimal config:

```json
{
  "addr": ":8080",
  "api_keys": {
    "<generate-an-admin-key>": { "client_id": "ops", "role": "admin" },
    "<generate-a-key-for-erp>": { "client_id": "erp", "role": "client" }
  },
  "printers": [
    {
      "printer_id": "wh1-pack-01",
      "name": "Packing station 1",
      "site": "wh1",
      "driver": "zebra9100",
      "host": "192.168.10.21",
      "port": 9100,
      "enabled": true,
      "paper_size": "100x150mm"
    }
  ],
  "discovery": { "enabled": true, "site": "wh1" },
  "ui": { "enabled": true, "path": "/ui/", "title": "Cloud Print Service" }
}
```

- **Keys:** generate each one with `openssl rand -hex 24`. Never reuse the `dev-key-…`
  values from the examples. Admin keys can do everything; give business systems `client`.
- **Printers:** list network printers here. USB printers are added by agents (section 5),
  not in this file.
- Every other setting has a working default; see `server/internal/config/config.go`.

The server looks for its config in this order: `-config <path>`, then `$CPS_CONFIG`, then
`./config.json`. An invalid config stops the server with a message naming the problem.

### Run it as a service (Linux, systemd)

```bash
sudo useradd --system --no-create-home cps
sudo install -m 755 cps cpsctl /usr/local/bin/
sudo install -d -m 750 -o cps /etc/cps
sudo install -m 640 -o cps config.json /etc/cps/config.json
```

`/etc/systemd/system/cps.service`:

```ini
[Unit]
Description=Cloud Print Service
After=network-online.target
Wants=network-online.target

[Service]
User=cps
ExecStart=/usr/local/bin/cps -config /etc/cps/config.json
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now cps
journalctl -u cps -f        # logs are JSON lines on stdout
```

On Windows or macOS, run `cps -config config.json` directly, or under your usual service
manager.

### Or run it in Docker

One container is enough; Compose is not needed.

```bash
docker build -f server/Dockerfile -t cps .
docker run -d --name cps --restart unless-stopped \
  -p 8080:8080 \
  -v "$PWD/config.json:/etc/cps/config.json:ro" \
  cps
```

The container runs as a non-root user (uid 10001), so the mounted config must be
readable by it (e.g. `chmod 644 config.json`, or `chown 10001` it).

For **printer discovery** (mDNS), the container must see the printer LAN's multicast
traffic: on Linux use `--network host` instead of `-p 8080:8080`. Without discovery, the
bridge network is fine.

### HTTPS

`cps` serves plain HTTP. For HTTPS, put a reverse proxy in front and keep `cps` on the
LAN. The panel uses a live event stream (`/api/v1/events`), so the proxy must not buffer
responses. With Caddy:

```
print.example.com {
    reverse_proxy 127.0.0.1:8080 {
        flush_interval -1
    }
}
```

With nginx, add `proxy_buffering off;` and `proxy_read_timeout 1h;` to the location.

### Check it

```bash
curl http://<server>:8080/healthz                 # 200 OK
export CPSCTL_URL=http://<server>:8080 CPSCTL_ADMIN_KEY=<your-admin-key>
cpsctl status                                     # service ready, N printers
cpsctl printers                                   # every printer, usable or not and why
```

Open `http://<server>:8080/ui/` and log in with the admin key.

---

## 3. Add network printers

Either declare them in `config.json` (section 2), or let discovery find them: printers
that advertise over mDNS appear under **Discovery** in the panel, and `cpsctl discover
adopt <device-key> --printer-id wh1-pack-02 --site wh1` adds one. Adopted printers are
lost on restart (section 1); copy them into the config to keep them.

Zebra printers must be in **ZPL mode** to report status. Check one with:

```bash
cpsctl diag wh1-pack-01      # what the printer says about itself, plus any mismatches
cpsctl test wh1-pack-01      # prints an alignment label sized to its media
```

---

## 4. Credentials for business systems

For testing, issue a token:

```bash
cpsctl token issue --client-id wms --ttl 90d     # shown once
```

For production integrations, use a `client` key in `api_keys` until tokens survive
restarts (section 1). The API is described at `http://<server>:8080/docs/`
(OpenAPI at `/openapi.yaml`).

---

## 5. Install the agent (USB printers)

Get `cps-agent` from the CI build: a tag like `agent-v0.2.0` publishes a release with
Windows and Linux archives and `SHA256SUMS`; a manual run of the `release-agent` workflow
leaves the same files as a workflow artifact.

On the server side, each agent needs a one-time enrollment token:

```bash
cpsctl agent enroll --id pc7 --site wh1 --ttl 24h    # usable once
```

The token works once: the agent exchanges it for its own long-lived credential on first
contact. Without `--ttl` it stays valid for a year until used, so keep it short for a
one-off install.

### Windows

The agent talks to USB printers through Windows' own USB printer driver: keep the vendor
driver (e.g. ZDesigner) installed; nothing has to be replaced.

1. Copy `cps-agent.exe` to the PC, e.g. `C:\Program Files\cps-agent\`.
2. Open a **Command Prompt as Administrator** in that folder and enroll:

   ```bat
   set CPS_AGENT_SERVER=http://<server>:8080
   set CPS_AGENT_HOST=<this PC's LAN address>
   cps-agent.exe -enroll <token> -id pc7 -site wh1 -install
   ```

   `-install` registers a service that starts with Windows, restarts itself if it stops,
   and adds the firewall rule. `CPS_AGENT_HOST` is the address the server will dial; set it
   explicitly if the PC has VPN or virtual adapters.
3. Check: the printer appears in the panel as `pc7-<serial>` and turns **usable** within
   about 30 seconds. `cpsctl agent list` shows the agent and its last heartbeat.

| | |
|---|---|
| config and credential | `%ProgramData%\cps-agent\agent.json` (readable by SYSTEM, Administrators and the enrolling user only) |
| log | `%ProgramData%\cps-agent\agent.log` |
| remove | `cps-agent.exe -uninstall` (Administrator) |
| re-enroll after a server restart | `cps-agent.exe -uninstall`, delete `agent.json`, then step 2 with a new token |

To try the agent before installing the service, leave out `-install`: it runs in the
window and logs there. Windows may show a SmartScreen warning because the executable is
not code-signed yet ("More info → Run anyway").

### Linux

The agent uses the kernel's USB printer devices (`/dev/usb/lp0`, …).

```bash
sudo useradd --system --groups lp --no-create-home cps-agent
sudo install -m 755 cps-agent /usr/local/bin/
sudo install -d -m 700 -o cps-agent /etc/cps-agent
sudo -u cps-agent env CPS_AGENT_SERVER=http://<server>:8080 CPS_AGENT_HOST=<this host's LAN IP> \
  cps-agent -enroll <token> -id pc7 -site wh1 -config /etc/cps-agent/agent.json
# Ctrl+C once it logs "registered with server", then run it as a service:
```

`/etc/systemd/system/cps-agent.service`:

```ini
[Unit]
Description=Cloud Print Service agent
After=network-online.target
Wants=network-online.target

[Service]
User=cps-agent
ExecStart=/usr/local/bin/cps-agent -config /etc/cps-agent/agent.json
Restart=always
RestartSec=30

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now cps-agent
```

### macOS

Only for development: macOS has no kernel USB printer device, so build the agent with
libusb (`brew install libusb`, then `go build -tags usb_libusb ./agent/cmd/cps-agent`).

---

## 6. Upgrading

- **Server:** replace the binary (or image) and restart. Remember what a restart loses
  (section 1): agents must re-enroll.
- **Agent (Windows):** `cps-agent.exe -uninstall`, replace the `.exe`, `cps-agent.exe -install`.
  The saved config and credential are reused, so no new token is needed (unless the
  server restarted meanwhile).

---

## 7. Troubleshooting

| Symptom | Likely cause | What to do |
|---|---|---|
| Printer stays *not usable*, state `unknown` | the printer does not answer status queries | set it to ZPL mode; run `cpsctl diag <printer-id>` |
| Agent logs `no local printers found` | no USB printer seen, or it is not in ZPL mode | check the USB cable and driver; the log line names any printer that did not identify itself |
| Agent registered, printer *not usable* | heartbeats stopped, or the server cannot reach the agent's port | `cpsctl agent list` for the last heartbeat; allow ports 19100+ in the PC firewall |
| Agent gets 401 after the server restarted | agent credentials are in memory (section 1) | re-enroll the agent with a new token |
| Labels print shifted or clipped | the printer's offset or calibration | print `cpsctl test <printer-id>`; adjust *label top* / *left shift* in the panel's Edit tab, or calibrate |
| Chinese prints as blanks or boxes | the font is not on the printer | the printer's fonts are listed in the panel's Info tab and in the API as `available_fonts` |

Day-to-day operations are in [RUNBOOK.md](RUNBOOK.md).

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

### Where the server keeps its data

Jobs, issued tokens, agent credentials and edits made in the panel are kept in **one
SQLite file** (`cps.db`), so a restart loses nothing: integrations stay logged in,
agents keep their credentials, queued jobs are sent after the restart, and a job that was
mid-send is marked `sent_unknown` instead of being printed twice.

| Setting | Default | Notes |
|---|---|---|
| `store_driver` | `sqlite` | `memory` keeps nothing across restarts (tests and demos only) |
| `data_path` | `$CPS_DATA_PATH`, else `cps.db` in the working directory | the directory is created if needed; the file is readable by the server's user only |

Live printer status, the list of agents and each printer's font list are not stored:
they come back by themselves within a minute of a restart. Settings saved **on the
printer itself** (label top / left shift, calibration) live in the printer.

Back it up while the server runs with SQLite's online backup (a plain copy of a busy
database may be inconsistent):

```bash
sqlite3 /var/lib/cps/cps.db ".backup '/backups/cps-$(date +%F).db'"
```

To restore, stop the server, put the backup in place as `cps.db` (removing any
`cps.db-wal` / `cps.db-shm` next to it), and start the server.

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

To convert PDFs for receipt, label and PCL printers, install poppler and fonts on the
server too: they are what turns a PDF into dots. PDFs for office printers that take PDF,
and images for every printer, work without them.

```bash
sudo apt install poppler-utils fonts-liberation fonts-wqy-zenhei   # Debian/Ubuntu
brew install poppler                                               # macOS
```

The server logs `PDF conversion available` at start when it finds `pdftoppm`; set
`pdf_tools_dir` in the config if it is not on the PATH. The Docker image includes them.

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
sudo install -d -m 750 -o cps /etc/cps /var/lib/cps
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
Environment=CPS_DATA_PATH=/var/lib/cps/cps.db
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
  -v cps-data:/var/lib/cps \
  cps
```

The image keeps its database in `/var/lib/cps`; the `cps-data` volume is what keeps it
when the container is replaced. Without a volume, upgrading the container loses jobs,
tokens and agent credentials.

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
not stored yet: after a restart they are listed as discovery candidates again (adopting
one again restores its panel edits). To keep a printer permanently, put it in the config.

Zebra printers must be in **ZPL mode** to report status. Check one with:

```bash
cpsctl diag wh1-pack-01      # what the printer says about itself, plus any mismatches
cpsctl test wh1-pack-01      # prints an alignment label sized to its media
```

### Receipt and office printers

The `driver` says how CPS talks to a printer; its kind and the formats it takes follow
from it, and are learned from the printer where it can say.

| printer | `driver` | port | status from | takes as is |
|---|---|---|---|---|
| Zebra and ZPL label printers | `zebra9100` | 9100 | `~HS` | ZPL |
| ESC/POS receipt printers (Epson TM, Xprinter, …) | `escpos9100` | 9100 | DLE EOT | ESC/POS |
| office printers with IPP (most network laser/inkjet) | `ipp` | 631 | IPP printer state and the job's own state | what the printer reports (PDF, JPEG, URF, …) |
| office printers on JetDirect | `pdl9100` | 9100 | PJL | what PJL reports (PCL, PostScript) |

```json
{ "printer_id": "wh1-desk-01", "name": "Front desk", "driver": "escpos9100",
  "host": "192.168.10.40", "media_width_mm": 80, "enabled": true },
{ "printer_id": "hq-office-01", "name": "Office 3F", "driver": "ipp",
  "host": "192.168.10.60", "path": "ipp/print", "enabled": true }
```

- Prefer `ipp` for an office printer that has it: copies go as a job attribute, and a
  job is confirmed by the printer's own record of it, not by watching its status.
- Discovery finds IPP printers (`_ipp._tcp`) and JetDirect printers that advertise their
  languages; receipt printers rarely advertise, so declare them.
- `formats` can be declared to keep a printer to fewer formats than it reports.
- Receipt printers: set `media_width_mm` to 58 or 80. It sets the test page's columns and
  how wide converted images print.
- Calibration, offsets, fonts and `diag` are label-printer tools; for other printers the
  panel hides them and the API answers 409.

---

## 4. Credentials for business systems

Issue each business system its own token, and give it to that system once:

```bash
cpsctl token issue --client-id wms --ttl 365d     # the secret is shown once
cpsctl token list                                 # who has access, last used
cpsctl token rotate <token-id>                    # replace a leaked or expiring one
cpsctl token revoke <token-id>
```

Tokens are stored (as hashes) in the server's database, so they survive restarts. Keep
the `api_keys` in `config.json` for administrators and break-glass access.

The API is described at `http://<server>:8080/docs/` (OpenAPI at `/openapi.yaml`).

---

## 5. Install the agent (USB printers)

Get `cps-agent` from the CI build: a tag like `agent-v0.2.0` publishes a release with
Windows and Linux archives and `SHA256SUMS`; a manual run of the `release-agent` workflow
leaves the same files as a workflow artifact.

The agent serves label, receipt and office printers. It tells them apart by the
printer's USB device id (maker, model, command set): a Zebra is confirmed with `~HI`, a
receipt printer (ESC/POS) and an office printer (PCL/PostScript) are asked in their own
language. Each appears on the server with its driver, kind and formats. Host-based
inkjets that need a vendor driver to render pages cannot be served.

On the server side, each agent needs a one-time enrollment token:

```bash
cpsctl agent enroll --id pc7 --site wh1 --ttl 24h    # usable once
```

The token works once: the agent exchanges it for its own long-lived credential on first
contact. Without `--ttl` it stays valid for a year until used, so keep it short for a
one-off install.

On Windows there are two ways to run it. Pick one per PC: both would compete for the
same printer.

| | desktop app (`cps-agent-gui.exe`) | service (`cps-agent.exe -install`) |
|---|---|---|
| set up with | a page: server address and token | an Administrator command prompt |
| runs | while someone is signed in (optional start at login) | from boot, signed in or not |
| shows status | tray icon and a status page | `agent.log`, `cpsctl agent list` |
| suits | a packing desk with one regular user | shared or unattended PCs |

Either way the agent talks to USB printers through Windows' own USB printer driver: keep
the vendor driver (e.g. ZDesigner) installed; nothing has to be replaced.

### Windows: desktop app

1. Copy `cps-agent-gui.exe` from the Windows archive to the PC and double-click it. A
   page opens in the browser.
2. Enter the server address (`http://<server>:8080`), the token, and the site. The agent
   ID defaults to the PC name; use the `--id` you gave `cpsctl agent enroll`.
   **Connect** checks the token against the server right away and shows the reason if
   it is refused.
3. When Windows Firewall asks whether `cps-agent-gui.exe` may accept connections, allow
   it on **Private** networks. The server prints by connecting to the agent, so this
   needs an administrator once. Without it, the printer shows up but jobs fail with
   "connection refused" or time out.
4. The tray icon turns green once the agent is connected. Left-click it for the status
   page (printers, last heartbeat, recent log); right-click for **Start at login** and
   **Quit**.

The **Settings** tab changes the site, ports, heartbeat interval and the address the
server dials. Changing the server address or the agent ID asks for a new token, because
the credential belongs to both. The agent restarts when you save.

Grey means not set up, amber means no printer found (plug it in; the agent looks again
every 30 seconds), and red means the server cannot be reached. Config and log are in the
same place as for the service (table below). If the service is installed, the desktop
app stays idle and says so; remove the service first to switch.

### Windows: service

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
| re-enroll (new server, or its database was lost) | `cps-agent.exe -uninstall`, delete `agent.json`, then step 2 with a new token |

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

### ASUS router with koolshare firmware

On an ASUS router running koolshare firmware (official-mod or Merlin-mod) on the
hnd/axhnd/axhnd.675x platform, such as the TUF-AX3000 or RT-AX86U, the agent installs
from the software center and is configured on its own page. It serves the USB printer
plugged into the router.

1. Get `cpsagent-<version>.tar.gz` from the release, or build it yourself:
   `packaging/koolshare/build.sh` writes `dist/cpsagent.tar.gz`. Don't rename the file,
   except to change the part after the first `-`: the software center takes the plugin
   name from it.
2. Software Center → Offline install: upload the file and install.
3. Open **Cloud Print Agent** in the software center. Fill in the server URL and the
   enrollment token, plus the agent ID (default: the router's host name) and the site,
   switch it on and press **Apply**. The server takes the agent ID from this page,
   not from `cpsctl agent enroll --id`.
4. The page shows the enrollment, the USB printers found and the log. The printer appears
   in the panel within about 30 seconds.

Turn off the router's own printer server (USB Application → Network Printer Server):
it keeps the USB printer open, and the page warns while it is running.

The agent advertises the router's LAN address unless the page sets another one. When no
printer is found it retries every 30 seconds, so a printer plugged in later is picked up.

| | |
|---|---|
| config and credential | `/koolshare/configs/cpsagent/agent.json` (on the jffs partition, survives reboots) |
| log | in RAM, `/tmp/upload/cpsagent_log.txt` (shown on the page, kept under 256 KB) |
| started by | the software center on boot (`/koolshare/init.d/S99cpsagent.sh`) |
| re-enroll | enter a new token and Apply; if it fails, the previous enrollment is kept |
| remove | uninstall it in the software center; this also deletes the credential |

### ASUS router or AiMesh node without koolshare

An AiMesh node has no software center (and no admin page of its own), and a router may
run stock or Merlin firmware without koolshare. There, use `cps-agent-asuswrt.tar.gz`
(from the release, or `packaging/asuswrt/build.sh`) over SSH:

```sh
# copy the file to the device's /tmp first (scp)
tar -xzf /tmp/cps-agent-asuswrt.tar.gz -C /jffs
/jffs/cps-agent/cps-agent.sh enroll http://<server>:8080 <token> <site> <agent-id>
/jffs/cps-agent/cps-agent.sh status
```

`cps-agent.sh` also takes `start`, `stop`, `restart`, `log` and `uninstall`. On
Asuswrt-Merlin, `cps-agent.sh autostart on` starts the agent on boot from
`/jffs/scripts/services-start` (and turns on JFFS custom scripts). Stock firmware runs
nothing from `/jffs` on boot: start it by hand after a reboot.

A node gets its address from the main router: reserve a fixed IP for it there, since the
server dials that address. To advertise another address or port, put `HOST=...` or
`BASE_PORT=...` in `/jffs/cps-agent/settings`. The log is `/tmp/cps-agent.log`.

### macOS

Only for development: macOS has no kernel USB printer device, so build the agent with
libusb (`brew install libusb`, then `go build -tags usb_libusb ./agent/cmd/cps-agent`).

---

## 6. Upgrading

- **Server:** replace the binary (or image) and restart; the database carries over (in
  Docker, keep the same volume). Back it up first (section 1).
- **Agent (Windows):** `cps-agent.exe -uninstall`, replace the `.exe`, `cps-agent.exe -install`.
  The saved config and credential are reused, so no new token is needed.

---

## 7. Troubleshooting

| Symptom | Likely cause | What to do |
|---|---|---|
| Printer stays *not usable*, state `unknown` | the printer does not answer status queries | set it to ZPL mode; run `cpsctl diag <printer-id>` |
| Agent logs `no local printers found` | no USB printer seen, a Zebra not in ZPL mode, or a printer whose device id does not say what it is | check the USB cable and driver; the log line names any printer that did not identify itself, with its device id |
| Agent registered, printer *not usable* | heartbeats stopped, or the server cannot reach the agent's port | `cpsctl agent list` for the last heartbeat; allow ports 19100+ in the PC firewall |
| Agent gets 401 | its credential was revoked, or the server's database was replaced or lost | re-enroll the agent with a new token |
| Server will not start: `cannot open the data store` | `data_path` is not writable by the server's user | fix the directory's owner, or point `data_path` / `CPS_DATA_PATH` elsewhere |
| Labels print shifted or clipped | the printer's offset or calibration | print `cpsctl test <printer-id>`; adjust *label top* / *left shift* in the panel's Edit tab, or calibrate |
| Chinese prints as blanks or boxes | the font is not on the printer | the printer's fonts are listed in the panel's Info tab and in the API as `available_fonts` |
| Job refused: `format_not_supported` | the printer cannot take that format and CPS cannot convert it (ZPL to a receipt printer, PCL to IPP) | send PDF or PNG, or the printer's own language; the printer's `formats` lists what it takes |
| Job refused: `pdf_conversion_unavailable` | the PDF needs converting and the server has no poppler | install `poppler-utils` (section 2) or use the Docker image |
| Converted PDF prints without text | the PDF does not embed its fonts and the server has none to stand in | install `fonts-liberation` (and `fonts-wqy-zenhei` for Chinese) |
| Receipt or office printer shows state `unknown` | it prints but does not report status (no PJL, or a write-only USB interface) | jobs still print and are confirmed after a short settle time |

Day-to-day operations are in [RUNBOOK.md](RUNBOOK.md).

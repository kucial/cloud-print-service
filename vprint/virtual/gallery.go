package virtual

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
)

// Gallery serves the debug & inspection web view for a virtual printer:
// every label received is rendered as an image, with real-time status and raw ZPL next to it.
type Gallery struct {
	p   *Printer
	id  string
	mux *http.ServeMux
}

// NewGallery wires the control + preview routes.
func NewGallery(p *Printer, id string) *Gallery {
	g := &Gallery{p: p, id: id, mux: http.NewServeMux()}
	g.mux.HandleFunc("GET /{$}", g.index)
	g.mux.HandleFunc("GET /control/snapshot", g.snapshot)
	g.mux.HandleFunc("GET /control/labels", g.labels)
	g.mux.HandleFunc("GET /control/labels/reset", g.reset)
	g.mux.HandleFunc("POST /control", g.control)
	g.mux.HandleFunc("GET /labels/{index}/image", g.image)
	g.mux.HandleFunc("GET /labels/{index}/zpl", g.zpl)
	return g
}

func (g *Gallery) ServeHTTP(w http.ResponseWriter, r *http.Request) { g.mux.ServeHTTP(w, r) }

func (g *Gallery) snapshot(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, g.p.Snapshot())
}

func (g *Gallery) labels(w http.ResponseWriter, r *http.Request) {
	ls := g.p.Labels()
	writeJSON(w, http.StatusOK, map[string]any{"count": len(ls), "items": ls})
}

func (g *Gallery) reset(w http.ResponseWriter, r *http.Request) {
	g.p.Reset()
	writeJSON(w, http.StatusOK, map[string]any{"status": "reset"})
}

func (g *Gallery) control(w http.ResponseWriter, r *http.Request) {
	var c Control
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	g.p.Control(c)
	writeJSON(w, http.StatusOK, g.p.Snapshot())
}

// image serves the rendered label; format is selected with ?format=png|svg|pdf.
func (g *Gallery) image(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		http.Error(w, "bad index", http.StatusBadRequest)
		return
	}
	data, ct, ok := g.p.Image(idx)
	if !ok {
		http.Error(w, "no rendered image for this label (is rendering enabled?)", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (g *Gallery) zpl(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		http.Error(w, "bad index", http.StatusBadRequest)
		return
	}
	for _, l := range g.p.Labels() {
		if l.Index == idx {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", g.id+"_"+strconv.Itoa(idx)+".zpl"))
			_, _ = w.Write([]byte(l.ZPL))
			return
		}
	}
	http.Error(w, "unknown label", http.StatusNotFound)
}

func (g *Gallery) index(w http.ResponseWriter, r *http.Request) {
	snap := g.p.Snapshot()
	var b strings.Builder
	b.WriteString(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>` + html.EscapeString(g.id) + ` · Virtual Printer Web View</title>
  <style>
    :root {
      --bg: #0c0d10;
      --card-bg: rgba(22, 24, 30, 0.85);
      --card-border: rgba(255, 255, 255, 0.08);
      --card-hover: rgba(255, 255, 255, 0.12);
      --text: #f3f4f6;
      --text-muted: #9ca3af;
      --accent: #38bdf8;
      --accent-glow: rgba(56, 189, 248, 0.15);
      --ok: #10b981;
      --ok-bg: rgba(16, 185, 129, 0.15);
      --bad: #f43f5e;
      --bad-bg: rgba(244, 63, 94, 0.15);
      --warn: #f59e0b;
      --warn-bg: rgba(245, 158, 11, 0.15);
      --code-bg: #090a0d;
    }
    * { box-sizing: border-box; }
    body {
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, system-ui, sans-serif;
      margin: 0;
      background: var(--bg);
      color: var(--text);
      min-height: 100vh;
      display: flex;
      flex-direction: column;
    }
    header {
      position: sticky;
      top: 0;
      z-index: 50;
      background: rgba(12, 13, 16, 0.88);
      backdrop-filter: blur(16px);
      -webkit-backdrop-filter: blur(16px);
      border-bottom: 1px solid var(--card-border);
      padding: 12px 24px;
      display: flex;
      align-items: center;
      justify-content: space-between;
      flex-wrap: wrap;
      gap: 12px;
    }
    .header-left {
      display: flex;
      align-items: center;
      gap: 12px;
      flex-wrap: wrap;
    }
    .printer-title {
      font-size: 16px;
      font-weight: 700;
      letter-spacing: -0.02em;
      display: flex;
      align-items: center;
      gap: 8px;
    }
    .badge {
      font-size: 12px;
      font-weight: 500;
      padding: 3px 10px;
      border-radius: 9999px;
      display: inline-flex;
      align-items: center;
      gap: 5px;
      border: 1px solid transparent;
    }
    .badge-ok { background: var(--ok-bg); color: var(--ok); border-color: rgba(16, 185, 129, 0.3); }
    .badge-bad { background: var(--bad-bg); color: var(--bad); border-color: rgba(244, 63, 94, 0.3); }
    .badge-warn { background: var(--warn-bg); color: var(--warn); border-color: rgba(245, 158, 11, 0.3); }
    .badge-neutral { background: rgba(255, 255, 255, 0.05); color: var(--text-muted); border-color: var(--card-border); }
    .badge-accent { background: var(--accent-glow); color: var(--accent); border-color: rgba(56, 189, 248, 0.3); }

    .pulse {
      width: 7px;
      height: 7px;
      border-radius: 50%;
      background: currentColor;
      box-shadow: 0 0 8px currentColor;
    }

    .header-actions {
      display: flex;
      align-items: center;
      gap: 8px;
    }
    .btn {
      font-size: 12px;
      font-weight: 500;
      padding: 6px 12px;
      border-radius: 8px;
      cursor: pointer;
      border: 1px solid var(--card-border);
      background: rgba(255, 255, 255, 0.04);
      color: var(--text);
      transition: all 0.15s ease;
      display: inline-flex;
      align-items: center;
      gap: 6px;
      text-decoration: none;
    }
    .btn:hover {
      background: rgba(255, 255, 255, 0.08);
      border-color: var(--card-hover);
    }
    .btn-active {
      background: var(--accent-glow);
      color: var(--accent);
      border-color: rgba(56, 189, 248, 0.3);
    }
    select.btn {
      appearance: none;
      -webkit-appearance: none;
      padding-right: 24px;
      background-image: url("data:image/svg+xml;charset=UTF-8,%3csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='%239ca3af' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'%3e%3cpolyline points='6 9 12 15 18 9'%3e%3c/polyline%3e%3c/svg%3e");
      background-repeat: no-repeat;
      background-position: right 8px center;
      background-size: 12px;
    }

    main {
      padding: 24px;
      flex: 1;
      max-width: 1600px;
      width: 100%;
      margin: 0 auto;
    }
    .grid {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(360px, 1fr));
      gap: 20px;
    }
    .card {
      background: var(--card-bg);
      border: 1px solid var(--card-border);
      border-radius: 14px;
      overflow: hidden;
      display: flex;
      flex-direction: column;
      transition: border-color 0.2s ease, transform 0.2s ease;
      box-shadow: 0 4px 20px rgba(0, 0, 0, 0.3);
    }
    .card:hover {
      border-color: var(--card-hover);
      transform: translateY(-2px);
    }
    .card-top {
      padding: 12px 16px;
      border-bottom: 1px solid var(--card-border);
      display: flex;
      align-items: center;
      justify-content: space-between;
      font-size: 12px;
      color: var(--text-muted);
    }
    .card-num {
      font-weight: 700;
      color: var(--accent);
      font-mono;
    }
    .preview-box {
      padding: 20px;
      background:
        linear-gradient(45deg, #18191e 25%, transparent 25%),
        linear-gradient(-45deg, #18191e 25%, transparent 25%),
        linear-gradient(45deg, transparent 75%, #18191e 75%),
        linear-gradient(-45deg, transparent 75%, #18191e 75%);
      background-size: 16px 16px;
      background-position: 0 0, 0 8px, 8px -8px, -8px 0px;
      background-color: #121317;
      display: flex;
      align-items: center;
      justify-content: center;
      min-height: 220px;
    }
    .preview-img {
      max-width: 100%;
      height: auto;
      max-height: 380px;
      border-radius: 4px;
      box-shadow: 0 6px 24px rgba(0, 0, 0, 0.6);
      background: #fff;
      display: block;
      cursor: zoom-in;
      transition: transform 0.2s ease;
    }
    .preview-img:hover {
      transform: scale(1.02);
    }
    .card-meta {
      padding: 12px 16px;
      font-size: 11px;
      color: var(--text-muted);
      border-top: 1px solid var(--card-border);
      display: flex;
      align-items: center;
      justify-content: space-between;
      background: rgba(0, 0, 0, 0.2);
    }
    .zpl-drawer {
      padding: 12px 16px;
      border-top: 1px solid var(--card-border);
      background: var(--code-bg);
    }
    .zpl-header {
      display: flex;
      align-items: center;
      justify-content: space-between;
      margin-bottom: 8px;
    }
    .zpl-header span {
      font-size: 11px;
      text-transform: uppercase;
      letter-spacing: 0.05em;
      color: var(--text-muted);
      font-weight: 600;
    }
    pre {
      margin: 0;
      font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
      font-size: 11px;
      color: #94a3b8;
      max-height: 120px;
      overflow-y: auto;
      white-space: pre-wrap;
      word-break: break-all;
      background: rgba(0,0,0,0.3);
      padding: 8px;
      border-radius: 6px;
      border: 1px solid rgba(255, 255, 255, 0.04);
    }
    .empty-state {
      text-align: center;
      padding: 80px 20px;
      max-width: 520px;
      margin: 40px auto;
      background: var(--card-bg);
      border: 1px dashed var(--card-border);
      border-radius: 16px;
    }
    .empty-icon {
      font-size: 40px;
      margin-bottom: 16px;
      opacity: 0.6;
    }
    .empty-title {
      font-size: 18px;
      font-weight: 600;
      margin-bottom: 8px;
    }
    .empty-text {
      color: var(--text-muted);
      font-size: 13px;
      line-height: 1.6;
    }
    .toast {
      position: fixed;
      bottom: 24px;
      right: 24px;
      background: #1e293b;
      color: #fff;
      padding: 10px 16px;
      border-radius: 8px;
      font-size: 13px;
      box-shadow: 0 10px 30px rgba(0,0,0,0.5);
      border: 1px solid rgba(255,255,255,0.1);
      display: none;
      z-index: 100;
    }
  </style>
</head>
<body>
  <header>
    <div class="header-left">
      <div class="printer-title">
        <span>🖨️</span>
        <span>` + html.EscapeString(g.id) + `</span>
      </div>
      <span class="badge ` + onOff(snap.Offline) + `" id="status-badge">
        <span class="pulse"></span>
        <span>` + offlineLabel(snap.Offline) + `</span>
      </span>
      <span class="badge badge-neutral" id="state-badge">State: ` + html.EscapeString(snap.State) + `</span>
      <span class="badge badge-neutral" id="media-badge">Media: ` + html.EscapeString(snap.Media) + `</span>
      <span class="badge badge-accent" id="count-badge">` + strconv.Itoa(snap.Labels) + ` labels handled</span>
      <span class="badge badge-neutral">` + html.EscapeString(snap.Renderer) + `</span>` +
		func() string {
			var extra strings.Builder
			if snap.DPI > 0 || snap.DPMM > 0 {
				extra.WriteString(`<span class="badge badge-neutral">` + strconv.Itoa(snap.DPI) + ` DPI (` + strconv.Itoa(snap.DPMM) + ` dpmm)</span>`)
			}
			if snap.PaperSize != "" {
				extra.WriteString(`<span class="badge badge-neutral">Roll: ` + html.EscapeString(snap.PaperSize) + `</span>`)
			}
			if snap.Orientation != "" {
				extra.WriteString(`<span class="badge badge-neutral">` + html.EscapeString(snap.Orientation) + ` · ` + html.EscapeString(snap.PrintDirection) + `</span>`)
			}
			if snap.CJKFont != "" {
				extra.WriteString(`<span class="badge badge-accent">中 ` + html.EscapeString(snap.CJKFont) + `</span>`)
			}
			return extra.String()
		}() + `
    </div>

    <div class="header-actions">
      <button class="btn btn-active" id="auto-btn" onclick="toggleAuto()">
        <span class="pulse" style="background:#38bdf8"></span>
        <span>Auto-Refresh (2s)</span>
      </button>

      <button class="btn" onclick="fetchSnapshot()">
        <span>↻ Refresh</span>
      </button>

      <select class="btn" onchange="simulateFault(this.value)">
        <option value="">⚡ Fault Injection...</option>
        <option value="idle">Restore Normal (Idle)</option>
        <option value="media_out">Simulate Media Out</option>
        <option value="media_low">Simulate Media Low</option>
        <option value="ribbon_out">Simulate Ribbon Out</option>
        <option value="head_error">Simulate Head Error</option>
        <option value="offline">Simulate Network Offline</option>
      </select>

      <button class="btn" onclick="resetLabels()" title="Clear handled labels">
        <span>🗑️ Clear</span>
      </button>
    </div>
  </header>

  <main id="main-content">
`)

	labels := snap.LabelList
	if len(labels) == 0 {
		b.WriteString(`
    <div class="empty-state" id="empty-state">
      <div class="empty-icon">🖨️</div>
      <div class="empty-title">Waiting for Print Jobs</div>
      <div class="empty-text">
        This virtual printer is listening on <strong>` + html.EscapeString(snap.Addr) + `</strong> and ready to accept ZPL.<br>
        Dispatch a job from the Cloud Print Service or Demo WMS Console to watch it render in real-time.
      </div>
    </div>
    <div class="grid" id="labels-grid" style="display:none"></div>
`)
	} else {
		b.WriteString(`<div class="grid" id="labels-grid">`)
		for i := len(labels) - 1; i >= 0; i-- {
			l := labels[i]
			b.WriteString(renderLabelCard(l))
		}
		b.WriteString(`</div>`)
	}

	b.WriteString(`
  </main>

  <div class="toast" id="toast"></div>

  <script>
    let autoRefresh = true;
    let autoInterval = null;
    let lastCount = ` + strconv.Itoa(snap.Labels) + `;

    function showToast(msg) {
      const t = document.getElementById('toast');
      t.textContent = msg;
      t.style.display = 'block';
      setTimeout(() => { t.style.display = 'none'; }, 2500);
    }

    function toggleAuto() {
      autoRefresh = !autoRefresh;
      const btn = document.getElementById('auto-btn');
      if (autoRefresh) {
        btn.classList.add('btn-active');
        btn.querySelector('span:last-child').textContent = 'Auto-Refresh (2s)';
        startAuto();
      } else {
        btn.classList.remove('btn-active');
        btn.querySelector('span:last-child').textContent = 'Auto-Refresh (Paused)';
        clearInterval(autoInterval);
      }
    }

    function startAuto() {
      clearInterval(autoInterval);
      autoInterval = setInterval(fetchSnapshot, 2000);
    }

    async function fetchSnapshot() {
      try {
        const res = await fetch('/control/snapshot');
        if (!res.ok) return;
        const data = await res.json();
        updateUI(data);
      } catch (e) {}
    }

    function updateUI(data) {
      // Update badges
      const statusBadge = document.getElementById('status-badge');
      if (data.offline) {
        statusBadge.className = 'badge badge-bad';
        statusBadge.innerHTML = '<span class="pulse"></span><span>OFFLINE</span>';
      } else {
        statusBadge.className = 'badge badge-ok';
        statusBadge.innerHTML = '<span class="pulse"></span><span>ONLINE</span>';
      }
      document.getElementById('state-badge').textContent = 'State: ' + data.state;
      document.getElementById('media-badge').textContent = 'Media: ' + data.media;
      document.getElementById('count-badge').textContent = data.labels + ' labels handled';

      if (data.labels !== lastCount) {
        lastCount = data.labels;
        location.reload();
      }
    }

    async function simulateFault(state) {
      if (!state) return;
      try {
        let payload = {};
        if (state === 'offline') {
          payload = { offline: true };
        } else {
          payload = { state: state, offline: false };
        }
        const res = await fetch('/control', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(payload)
        });
        if (res.ok) {
          showToast('Fault set: ' + state);
          fetchSnapshot();
        }
      } catch (e) {
        showToast('Failed to apply fault');
      }
    }

    async function resetLabels() {
      if (!confirm('Clear all received labels on this virtual printer?')) return;
      try {
        const res = await fetch('/control/labels/reset');
        if (res.ok) {
          location.reload();
        }
      } catch (e) {
        showToast('Reset failed');
      }
    }

    function copyZPL(id) {
      const code = document.getElementById('zpl-' + id).textContent;
      navigator.clipboard.writeText(code).then(() => {
        showToast('ZPL copied to clipboard');
      });
    }

    startAuto();
  </script>
</body>
</html>
`)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

func renderLabelCard(l Label) string {
	var b strings.Builder
	idxStr := strconv.Itoa(l.Index)
	b.WriteString(`<div class="card" id="card-` + idxStr + `">`)
	b.WriteString(`<div class="card-top">`)
	b.WriteString(`<span class="card-num">#` + idxStr + `</span>`)
	b.WriteString(`<span>` + l.ReceivedAt.Format("15:04:05") + ` · ` + strconv.Itoa(l.Bytes) + ` B</span>`)
	b.WriteString(`</div>`)

	b.WriteString(`<div class="preview-box">`)
	if l.Image != nil && l.Image.Available {
		b.WriteString(`<a href="/labels/` + idxStr + `/image" target="_blank" title="Click to view full image">`)
		b.WriteString(`<img class="preview-img" src="/labels/` + idxStr + `/image" alt="Label #` + idxStr + `">`)
		b.WriteString(`</a>`)
	} else {
		errText := "Rendering unavailable"
		if l.Image != nil && l.Image.Error != "" {
			errText = l.Image.Error
		}
		b.WriteString(`<div style="color:var(--text-muted);font-size:12px">` + html.EscapeString(errText) + `</div>`)
	}
	b.WriteString(`</div>`)

	b.WriteString(`<div class="card-meta">`)
	if l.Image != nil && l.Image.Available {
		b.WriteString(`<span>` + html.EscapeString(l.Image.Backend) + ` · ` + strconv.Itoa(l.Image.WidthDots) + `×` + strconv.Itoa(l.Image.HeightDots) + ` dots</span>`)
		b.WriteString(`<span>` + strconv.FormatInt(l.Image.DurationMS, 10) + ` ms</span>`)
	} else {
		b.WriteString(`<span>Raw stream received</span>`)
	}
	b.WriteString(`</div>`)

	b.WriteString(`<div class="zpl-drawer">`)
	b.WriteString(`<div class="zpl-header">`)
	b.WriteString(`<span>ZPL Payload</span>`)
	b.WriteString(`<div style="display:flex;gap:8px">`)
	b.WriteString(`<button class="btn" style="padding:2px 8px;font-size:10px" onclick="copyZPL('` + idxStr + `')">Copy</button>`)
	b.WriteString(`<a class="btn" style="padding:2px 8px;font-size:10px" href="/labels/` + idxStr + `/zpl">Download</a>`)
	b.WriteString(`</div>`)
	b.WriteString(`</div>`)
	b.WriteString(`<pre id="zpl-` + idxStr + `">` + html.EscapeString(l.ZPL) + `</pre>`)
	b.WriteString(`</div>`)

	b.WriteString(`</div>`)
	return b.String()
}

func onOff(offline bool) string {
	if offline {
		return "badge-bad"
	}
	return "badge-ok"
}

func offlineLabel(offline bool) string {
	if offline {
		return "OFFLINE"
	}
	return "ONLINE"
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

package virtual

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
)

// Gallery serves the debug UI: every label received, rendered as an image, with the
// raw ZPL next to it. This is the fastest way to answer "what did the ERP actually
// send, and what came out of the printer?".
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
	b.WriteString(`<!doctype html><meta charset="utf-8"><title>vprint ` + html.EscapeString(g.id) + `</title>`)
	b.WriteString(`<style>
 body{font:14px/1.5 ui-monospace,SFMono-Regular,Menlo,monospace;margin:0;background:#111;color:#eee}
 header{padding:12px 20px;background:#1c1c1c;display:flex;gap:20px;align-items:center;flex-wrap:wrap}
 h1{font-size:15px;margin:0;font-weight:600}
 .pill{background:#333;padding:2px 8px;border-radius:10px;font-size:12px}
 .ok{background:#14532d;color:#86efac}.bad{background:#7f1d1d;color:#fecaca}
 main{display:flex;flex-wrap:wrap;gap:16px;padding:16px}
 figure{margin:0;background:#1c1c1c;padding:10px;border-radius:6px;width:320px}
 figure img,figure object{width:100%;background:#fff;border-radius:3px;display:block}
 figcaption{font-size:12px;color:#bbb;margin-top:6px;word-break:break-all}
 pre{white-space:pre-wrap;word-break:break-all;font-size:11px;color:#9ca3af;max-height:9em;overflow:auto;margin:6px 0 0}
 a{color:#7dd3fc} .none{opacity:.5}
</style><header><h1>vprint · ` + html.EscapeString(g.id) + `</h1>`)
	fmt.Fprintf(&b, `<span class="pill">%s</span>`, html.EscapeString(snap.State))
	fmt.Fprintf(&b, `<span class="pill">media: %s</span>`, html.EscapeString(snap.Media))
	fmt.Fprintf(&b, `<span class="pill %s">%s</span>`, onOff(snap.Offline), offlineLabel(snap.Offline))
	fmt.Fprintf(&b, `<span class="pill">labels: %d</span>`, snap.Labels)
	fmt.Fprintf(&b, `<span class="pill">renderer: %s</span>`, html.EscapeString(snap.Renderer))
	if snap.RenderDir != "" {
		fmt.Fprintf(&b, `<span class="pill">dir: %s</span>`, html.EscapeString(snap.RenderDir))
	}
	b.WriteString(`<span class="pill">faults: POST /control {"state":"media_out"} {"offline":true} {"close_on_print":true}</span>`)
	b.WriteString(`</header><main>`)

	labels := snap.LabelList
	if len(labels) == 0 {
		b.WriteString(`<p>No labels received yet. Dispatch a job at the service, or:<br>
			<code>curl -XPOST localhost:PORT/control -d '{"state":"media_out"}'</code></p>`)
	}
	for i := len(labels) - 1; i >= 0; i-- { // newest first
		l := labels[i]
		fmt.Fprintf(&b, `<figure><a href="/labels/%d/image" target="_blank">`, l.Index)
		if l.Image != nil && l.Image.Available {
			b.WriteString(`<img src="/labels/` + strconv.Itoa(l.Index) + `/image" alt="label ` + strconv.Itoa(l.Index) + `">`)
		} else {
			errText := "not rendered"
			if l.Image != nil && l.Image.Error != "" {
				errText = l.Image.Error
			}
			b.WriteString(`<div class="none">` + html.EscapeString(errText) + `</div>`)
		}
		b.WriteString(`</a><figcaption>#` + strconv.Itoa(l.Index) + ` · ` +
			l.ReceivedAt.Format("15:04:05") + ` · ` + strconv.Itoa(l.Bytes) + ` B`)
		if l.Image != nil && l.Image.Available {
			fmt.Fprintf(&b, ` · %s %d×%d · %dms`, html.EscapeString(l.Image.Backend),
				l.Image.WidthDots, l.Image.HeightDots, l.Image.DurationMS)
			for _, warn := range l.Image.Warnings {
				b.WriteString(` <span class="pill">` + html.EscapeString(warn) + `</span>`)
			}
		}
		b.WriteString(`<br><a href="/labels/` + strconv.Itoa(l.Index) + `/zpl">download .zpl</a>`)
		b.WriteString(`<pre>` + html.EscapeString(l.ZPL) + `</pre></figcaption></figure>`)
	}
	b.WriteString(`</main>`)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

func onOff(offline bool) string {
	if offline {
		return "bad"
	}
	return "ok"
}

func offlineLabel(offline bool) string {
	if offline {
		return "OFFLINE"
	}
	return "online"
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

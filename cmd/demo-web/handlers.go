package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/kucial/cloud-print-service/internal/driver"
)

// Handler exposes the demo console's own API plus the embedded UI.
//
// The console API is deliberately thin — it exists so the browser UI (and a curious
// reader) can drive the reference integration without a browser devtools session.
func (a *app) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/orders", a.handleOrders)
	mux.HandleFunc("POST /api/orders/{id}/release", a.handleRelease)
	mux.HandleFunc("POST /api/orders/{id}/resend", a.handleResend)
	mux.HandleFunc("POST /api/orders/{id}/lines/{no}/printer", a.handleSetLinePrinter)
	mux.HandleFunc("GET /api/orders/{id}/lines/{no}/preview", a.handleLinePreview)
	mux.HandleFunc("GET /api/printers", a.handlePrinters)
	mux.HandleFunc("GET /api/jobs/{job_id}", a.handleJob)
	mux.HandleFunc("POST /api/preview", a.handlePreview)
	mux.HandleFunc("GET /api/traces", a.handleTraces)
	mux.HandleFunc("DELETE /api/traces", a.handleClearTraces)
	mux.HandleFunc("POST /api/traces/send-sample", a.handleSendSample)
	mux.HandleFunc("GET /api/config", a.handleConfig)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	mux.Handle("/", a.uiHandler())
	return mux
}

func (a *app) handleOrders(w http.ResponseWriter, r *http.Request) {
	a.reconcile(r.Context())
	a.reconcileTargetPrinters()

	a.mu.RLock()
	out := make([]map[string]any, 0, len(a.ords))
	for _, o := range a.ords {
		out = append(out, map[string]any{
			"id": o.ID, "customer": o.Customer, "sku": o.SKU, "qty": o.Qty,
			"bin": o.Bin, "created": o.Created, "lines": o.Lines,
		})
	}
	a.mu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "count": len(out)})
}

func (a *app) handleTraces(w http.ResponseWriter, r *http.Request) {
	traces := a.traces.List()
	writeJSON(w, http.StatusOK, map[string]any{
		"items": traces,
		"count": len(traces),
	})
}

func (a *app) handleClearTraces(w http.ResponseWriter, r *http.Request) {
	a.traces.Clear()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *app) handleSendSample(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action string `json:"action"` // "fleet_status" | "submit_4x6" | "submit_4x2" | "preview"
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	switch body.Action {
	case "fleet_status":
		var out any
		err := a.cli.do(r.Context(), "GET", "/api/v1/printers/status", a.cfg.token, nil, &out)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": out})

	case "submit_4x6":
		pID := "sim-pack-01"
		if len(a.PrinterIDs()) > 0 {
			pID = a.PrinterIDs()[0]
		}
		res, err := a.cli.dispatch(r.Context(), map[string]any{
			"printer_id":  pID,
			"external_id": fmt.Sprintf("sample-4x6-%d", time.Now().Unix()),
			"biz_ref":     "SAMPLE-4x6",
			"zpl":         "^XA^CI28^FO50,50^A0N,40,40^FDSAMPLE 4x6 LABEL^FS^FO50,110^BCN,80,Y,N,N^FD12345678^FS^XZ",
			"copies":      1,
		})
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": res})

	case "preview":
		png, err := a.preview(r.Context(), "^XA^CI28^FO40,40^A0N,40,40^FDAPI INSPECTOR PREVIEW^FS^FO40,100^BCN,80,Y,N,N^FD88889999^FS^XZ", 101.6, 50.8)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bytes": len(png)})

	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unknown action"})
	}
}

func (a *app) handlePrinters(w http.ResponseWriter, r *http.Request) {
	_ = a.refreshPrinters(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"items": a.Printers(),
		"count": len(a.Printers()),
	})
}

func (a *app) handleSetLinePrinter(w http.ResponseWriter, r *http.Request) {
	orderID := r.PathValue("id")
	lineNo, err := strconv.Atoi(r.PathValue("no"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid line number"})
		return
	}
	var body struct {
		PrinterID string `json:"printer_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if err := a.SetLinePrinter(orderID, lineNo, body.PrinterID); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "printer_id": body.PrinterID})
}

func (a *app) handleLinePreview(w http.ResponseWriter, r *http.Request) {
	orderID := r.PathValue("id")
	lineNo, err := strconv.Atoi(r.PathValue("no"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid line number"})
		return
	}
	a.mu.RLock()
	var targetLine *Line
	for _, o := range a.ords {
		if o.ID == orderID {
			for i := range o.Lines {
				if o.Lines[i].No == lineNo {
					targetLine = &o.Lines[i]
					break
				}
			}
		}
	}
	a.mu.RUnlock()
	if targetLine == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "line not found"})
		return
	}
	zplToRender := targetLine.ZPL
	pageStr := r.URL.Query().Get("page")
	if pageStr != "" {
		pages := driver.SplitZPLPages(targetLine.ZPL)
		if pNum, err := strconv.Atoi(pageStr); err == nil && pNum >= 1 && pNum <= len(pages) {
			zplToRender = pages[pNum-1]
		}
	}
	png, err := a.preview(r.Context(), zplToRender, targetLine.WidthMM, targetLine.HeightMM)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (a *app) handleRelease(w http.ResponseWriter, r *http.Request) {
	a.handleWave(w, r, false)
}

func (a *app) handleResend(w http.ResponseWriter, r *http.Request) {
	a.handleWave(w, r, true)
}

func (a *app) handleWave(w http.ResponseWriter, r *http.Request, resend bool) {
	orderID := r.PathValue("id")
	results, err := a.releaseWave(r.Context(), orderID)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	replayed := 0
	for _, res := range results {
		if res.Replayed {
			replayed++
		}
	}
	verb := "released"
	if resend {
		verb = "replayed"
	}
	a.log.Info("wave "+verb, "order", orderID, "lines", len(results), "replayed", replayed)
	writeJSON(w, http.StatusOK, map[string]any{
		"order": orderID, "action": verb, "lines": len(results), "replayed": replayed,
	})
}

// releaseWave wraps a release so it refreshes the printer list on demand.
func (a *app) releaseWave(ctx context.Context, orderID string) ([]dispatchResult, error) {
	_ = a.refreshPrinters(ctx)
	if len(a.Printers()) == 0 {
		return nil, errNoPrinters
	}
	return a.Release(ctx, orderID)
}

func (a *app) handleJob(w http.ResponseWriter, r *http.Request) {
	j, err := a.pollJob(r.Context(), r.PathValue("job_id"))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	if j == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no job id"})
		return
	}
	writeJSON(w, http.StatusOK, j)
}

func (a *app) handlePreview(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ZPL      string  `json:"zpl"`
		WidthMM  float64 `json:"width_mm"`
		HeightMM float64 `json:"height_mm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	png, err := a.preview(r.Context(), body.ZPL, body.WidthMM, body.HeightMM)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (a *app) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"cps":        a.cfg.base,
		"site":       a.cfg.site,
		"printer":    a.cfg.printerID,
		"printers":   a.Printers(),
		"preview":    a.cfg.adminToken != "",
		"simulate":   a.cfg.simulate,
		"token_kind": "client (dispatch) / admin (preview only)",
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

var errNoPrinters = errNoPrintersT{}

type errNoPrintersT struct{}

func (errNoPrintersT) Error() string {
	return "no usable printer is visible to this token — check GET /api/v1/printers/status"
}

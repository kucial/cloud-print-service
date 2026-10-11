package main

import (
	"context"
	"encoding/json"
	"net/http"
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
	mux.HandleFunc("GET /api/jobs/{job_id}", a.handleJob)
	mux.HandleFunc("POST /api/preview", a.handlePreview)
	mux.HandleFunc("GET /api/config", a.handleConfig)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	mux.Handle("/", a.uiHandler())
	return mux
}

func (a *app) handleOrders(w http.ResponseWriter, r *http.Request) {
	a.reconcile(r.Context())
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
	a.mu.RLock()
	ids := a.printerIDs
	a.mu.RUnlock()
	if len(ids) == 0 {
		if err := a.refreshPrinters(ctx); err != nil {
			return nil, err
		}
		ids = a.PrinterIDs()
		if len(ids) == 0 {
			return nil, errNoPrinters
		}
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
		ZPL string `json:"zpl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	png, err := a.preview(r.Context(), body.ZPL)
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
		"printers":   a.PrinterIDs(),
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

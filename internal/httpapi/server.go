// Package api exposes the HTTP surface consumed by ERP/WMS and operator tooling.
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kongkx/cloud-print-service/internal/discovery"
	"github.com/kongkx/cloud-print-service/internal/domain"
	"github.com/kongkx/cloud-print-service/internal/fleet"
	"github.com/kongkx/cloud-print-service/internal/job"
	"github.com/kongkx/cloud-print-service/internal/observ"
	"github.com/kongkx/cloud-print-service/internal/render"
	"github.com/kongkx/cloud-print-service/internal/store"
	"github.com/kongkx/cloud-print-service/internal/token"
)

type Server struct {
	mgr      *fleet.Manager
	jobs     *job.Service
	bus      *observ.Bus
	log      *slog.Logger
	met      *observ.Registry
	auth     *Auth
	tokens   *token.Service
	agents   *fleet.AgentRegistry
	web      WebConfig
	renderer render.Renderer
	disco    *discovery.Service
	agentTTL time.Duration

	maxBatch   int
	maxPayload int64
	startedAt  time.Time
}

// New builds the API server. auth may be nil in tests to disable authentication.
func New(mgr *fleet.Manager, js *job.Service, b *observ.Bus, auth *Auth, tok *token.Service,
	log *slog.Logger, met *observ.Registry, maxBatch int, maxPayload int64) *Server {
	return &Server{mgr: mgr, jobs: js, bus: b, auth: auth, tokens: tok, log: log, met: met,
		maxBatch: maxBatch, maxPayload: maxPayload, startedAt: time.Now()}
}

// Handler returns the fully wired http.Handler.
func (s *Server) Handler() http.Handler {
	apiMux := http.NewServeMux()

	// --- fleet visibility -------------------------------------------------
	apiMux.HandleFunc("GET /api/v1/printers", s.listPrinters)
	apiMux.HandleFunc("GET /api/v1/printers/status", s.bulkStatus)
	apiMux.HandleFunc("GET /api/v1/printers/{printer_id}", s.getPrinter)
	apiMux.HandleFunc("PATCH /api/v1/printers/{printer_id}", RequireAdmin(s.updatePrinter))
	apiMux.HandleFunc("GET /api/v1/printers/{printer_id}/status", s.getPrinterStatus)
	apiMux.HandleFunc("GET /api/v1/printers/{printer_id}/jobs", s.listPrinterJobs)
	apiMux.HandleFunc("POST /api/v1/printers/{printer_id}/test", RequireAdmin(s.testPrint))
	apiMux.HandleFunc("POST /api/v1/printers/{printer_id}/identify", RequireAdmin(s.identify))
	apiMux.HandleFunc("POST /api/v1/printers/probe", RequireAdmin(s.probeAll))

	// --- dispatch ---------------------------------------------------------
	apiMux.HandleFunc("POST /api/v1/jobs", s.submitJob)
	apiMux.HandleFunc("POST /api/v1/jobs/raw", s.submitRawJob)
	apiMux.HandleFunc("POST /api/v1/jobs/batch", s.submitBatch)
	apiMux.HandleFunc("GET /api/v1/jobs", s.listJobs)
	apiMux.HandleFunc("GET /api/v1/jobs/{job_id}", s.getJob)
	apiMux.HandleFunc("POST /api/v1/jobs/{job_id}/cancel", s.cancelJob)
	apiMux.HandleFunc("GET /api/v1/jobs/{job_id}/attempts", s.getJobAttempts)

	// --- events -----------------------------------------------------------
	apiMux.HandleFunc("GET /api/v1/events", s.events)

	// --- token administration (admin only) --------------------------------
	apiMux.HandleFunc("POST /api/v1/admin/tokens", RequireAdmin(s.issueToken))
	apiMux.HandleFunc("GET /api/v1/admin/tokens", RequireAdmin(s.listTokens))
	apiMux.HandleFunc("DELETE /api/v1/admin/tokens/{token_id}", RequireAdmin(s.revokeToken))
	apiMux.HandleFunc("POST /api/v1/admin/tokens/{token_id}/rotate", RequireAdmin(s.rotateToken))
	apiMux.HandleFunc("DELETE /api/v1/admin/tokens/{token_id}/purge", RequireAdmin(s.deleteToken))
	apiMux.HandleFunc("POST /api/v1/admin/preview", RequireAdmin(s.previewZPL))

	// --- agent (edge) surface: scope-limited, never dispatches ----------------------
	apiMux.HandleFunc("POST /api/v1/agents/register", RequireAgent(s.agentRegister))
	apiMux.HandleFunc("POST /api/v1/agents/heartbeat", RequireAgent(s.agentHeartbeat))
	apiMux.HandleFunc("POST /api/v1/agents/{agent_id}/printers/{printer_id}/status", RequireAgent(s.agentPrinterStatus))
	apiMux.HandleFunc("GET /api/v1/agents", RequireAdmin(s.listAgents))
	apiMux.HandleFunc("POST /api/v1/admin/agents/enroll", RequireEnroll(s.enrollAgent))
	apiMux.HandleFunc("DELETE /api/v1/agents/{agent_id}", RequireAdmin(s.deleteAgent))

	// --- discovery: candidates until a human adopts them ---------------------------
	apiMux.HandleFunc("GET /api/v1/admin/discovery", RequireAdmin(s.discoveryStatus))
	apiMux.HandleFunc("POST /api/v1/admin/discovery/scan", RequireAdmin(s.scanDiscovery))
	apiMux.HandleFunc("GET /api/v1/admin/discovery/candidates", RequireAdmin(s.listCandidates))
	apiMux.HandleFunc("POST /api/v1/admin/discovery/candidates/{device_key}/adopt", RequireAdmin(s.adoptCandidate))
	apiMux.HandleFunc("POST /api/v1/admin/discovery/candidates/{device_key}/dismiss", RequireAdmin(s.dismissCandidate))

	// --- ops: unauthenticated, carries no business data -------------------
	opsMux := http.NewServeMux()
	opsMux.HandleFunc("GET /healthz", s.healthz)
	opsMux.HandleFunc("GET /readyz", s.readyz)
	opsMux.HandleFunc("GET /metrics", s.metrics)

	root := http.NewServeMux()
	if s.auth != nil {
		// Every /api/v1 route requires a bearer key; ops routes stay open.
		root.Handle("/api/v1/", s.auth.Middleware(apiMux))
	} else {
		root.Handle("/api/v1/", apiMux)
	}
	root.Handle("/", opsMux)

	// Operator panel: embedded assets, same origin as the API, no separate deploy.
	if s.web.Enabled {
		wh, err := newWebHandler(s.web)
		if err != nil {
			return logRequests(s.log, errorHandler(err))
		}
		wh.mount(root)
	}
	return logRequests(s.log, root)
}

// ---------------------------------------------------------------- printers

// SetAgentTTL sets the lifetime of exchanged agent credentials (default 1 year).
func (s *Server) SetAgentTTL(d time.Duration) { s.agentTTL = d }

// SetAgents wires the edge-agent surface (registration, heartbeat, status).
func (s *Server) SetAgents(a *fleet.AgentRegistry) { s.agents = a }

// SetWebConfig enables the embedded operator panel (see WebConfig).
func (s *Server) SetWebConfig(cfg WebConfig) { s.web = cfg }

func errorHandler(err error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusInternalServerError, "panel_unavailable", err.Error())
	})
}

func (s *Server) listPrinters(w http.ResponseWriter, r *http.Request) {
	f := fleet.Filter{
		Site:  r.URL.Query().Get("site"),
		Model: r.URL.Query().Get("model"),
	}
	if v := r.URL.Query().Get("enabled"); v != "" {
		b := v == "true" || v == "1"
		f.Enabled = &b
	}
	if v := r.URL.Query().Get("usable"); v != "" {
		b := v == "true" || v == "1"
		f.Usable = &b
	}
	items := s.mgr.List(f)
	s.refreshGauges(items)
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"count": len(items),
	})
}

// bulkStatus is the warehouse-dashboard endpoint: one round trip for the whole fleet.
func (s *Server) bulkStatus(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ids := splitCSV(q.Get("ids"))
	var items []domain.Printer
	if len(ids) > 0 {
		items = s.mgr.ViewsByIDs(ids)
	} else {
		items = s.mgr.List(fleet.Filter{
			Site:  q.Get("site"),
			Model: q.Get("model"),
		})
	}
	s.refreshGauges(items)

	usable, blocked := 0, 0
	for _, p := range items {
		if p.Usable {
			usable++
		} else {
			blocked++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":       items,
		"count":       len(items),
		"usable":      usable,
		"not_usable":  blocked,
		"observed_at": time.Now().UTC(),
	})
}

func (s *Server) getPrinter(w http.ResponseWriter, r *http.Request) {
	p, ok := s.mgr.Get(r.PathValue("printer_id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "printer_not_found", "unknown printer "+r.PathValue("printer_id"))
		return
	}
	writeJSON(w, http.StatusOK, p.View())
}

func (s *Server) updatePrinter(w http.ResponseWriter, r *http.Request) {
	printerID := r.PathValue("printer_id")
	var body struct {
		Name        *string `json:"name"`
		Site        *string `json:"site"`
		Remark      *string `json:"remark"`
		CustomLabel *string `json:"custom_label"`
		Enabled     *bool   `json:"enabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	p, err := s.mgr.Registry().UpdateMetadata(printerID, body.Name, body.Site, body.Remark, body.CustomLabel, body.Enabled)
	if err != nil {
		writeErr(w, http.StatusNotFound, "printer_not_found", err.Error())
		return
	}
	_ = s.mgr.Sync(r.Context())
	live, ok := s.mgr.Get(printerID)
	if ok && live != nil {
		writeJSON(w, http.StatusOK, live.View())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) getPrinterStatus(w http.ResponseWriter, r *http.Request) {
	p, ok := s.mgr.Get(r.PathValue("printer_id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "printer_not_found", "unknown printer "+r.PathValue("printer_id"))
		return
	}
	v := p.View()
	writeJSON(w, http.StatusOK, map[string]any{
		"printer_id":  v.PrinterID,
		"status":      v.Status,
		"usable":      v.Usable,
		"queue_depth": v.QueueDepth,
	})
}

func (s *Server) listPrinterJobs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("printer_id")
	if _, ok := s.mgr.Get(id); !ok {
		writeErr(w, http.StatusNotFound, "printer_not_found", "unknown printer "+id)
		return
	}
	jobs, err := s.jobs.List(r.Context(), store.Filter{
		ClientID: ClientFrom(r.Context()).ID, PrinterID: id,
		Limit: intParam(r, "limit", 20),
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": jobs, "count": len(jobs)})
}

func (s *Server) testPrint(w http.ResponseWriter, r *http.Request) {
	p, ok := s.mgr.Get(r.PathValue("printer_id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "printer_not_found", "unknown printer "+r.PathValue("printer_id"))
		return
	}
	external := r.URL.Query().Get("external_id")
	if external == "" {
		external = fmt.Sprintf("test-%s-%d", p.ID(), time.Now().Unix())
	}
	zpl := fmt.Sprintf("^XA^CI28^FO40,40^A0N,50,50^FDCloudPrintService OK^FS"+
		"^FO40,110^A0N,30,30^FDprinter: %s^FS^FO40,150^A0N,30,30^FD%s^FS^XZ",
		p.Spec().Name, time.Now().Format("2006-01-02 15:04:05"))
	j, replayed, err := s.jobs.Submit(r.Context(), job.SubmitRequest{
		ClientID: ClientFrom(r.Context()).ID, PrinterID: p.ID(),
		ExternalID: external, ZPL: zpl, BizRef: "test", Priority: "high",
	})
	if err != nil {
		writeJobErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, jobAccepted(j, replayed))
}

func (s *Server) identify(w http.ResponseWriter, r *http.Request) {
	p, ok := s.mgr.Get(r.PathValue("printer_id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "printer_not_found", "unknown printer "+r.PathValue("printer_id"))
		return
	}
	ctx, cancel := contextWithTimeout(r, 5*time.Second)
	defer cancel()
	info, err := p.Identify(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"printer_id": p.ID(), "info": info, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"printer_id": p.ID(), "info": info})
}

func (s *Server) probeAll(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()
	s.mgr.ProbeAll(ctx)
	writeJSON(w, http.StatusOK, map[string]any{"probed": s.mgr.Len(), "at": time.Now().UTC()})
}

// ------------------------------------------------------------------- jobs

type submitBody struct {
	PrinterID     string            `json:"printer_id"`
	ExternalID    string            `json:"external_id"`
	ZPL           string            `json:"zpl"`
	PayloadBase64 string            `json:"payload_base64"`
	Copies        int               `json:"copies"`
	Priority      string            `json:"priority"`
	BizRef        string            `json:"biz_ref"`
	Meta          map[string]string `json:"meta"`
}

func (s *Server) submitJob(w http.ResponseWriter, r *http.Request) {
	var b submitBody
	if err := decodeJSON(r, &b); err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	s.dispatch(w, r, job.SubmitRequest{
		ClientID:       ClientFrom(r.Context()).ID,
		PrinterID:      b.PrinterID,
		ExternalID:     b.ExternalID,
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
		ZPL:            b.ZPL,
		PayloadBase64:  b.PayloadBase64,
		Copies:         b.Copies,
		Priority:       b.Priority,
		BizRef:         b.BizRef,
		Meta:           b.Meta,
	})
}

// submitRawJob takes application/octet-stream ZPL to avoid base64 for large payloads.
func (s *Server) submitRawJob(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/octet-stream") && !strings.HasPrefix(ct, "text/plain") {
		writeErr(w, http.StatusUnsupportedMediaType, "invalid_request", "expected application/octet-stream")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, s.maxPayload+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "cannot read body: "+err.Error())
		return
	}
	if int64(len(body)) > s.maxPayload {
		writeErr(w, http.StatusUnprocessableEntity, "payload_too_large",
			fmt.Sprintf("payload exceeds %d bytes", s.maxPayload))
		return
	}
	q := r.URL.Query()
	s.dispatch(w, r, job.SubmitRequest{
		ClientID:       ClientFrom(r.Context()).ID,
		PrinterID:      q.Get("printer_id"),
		ExternalID:     q.Get("external_id"),
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
		ZPL:            string(body),
		Copies:         intParam(r, "copies", 1),
		Priority:       q.Get("priority"),
		BizRef:         q.Get("biz_ref"),
	})
}

func (s *Server) submitBatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []submitBody `json:"items"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	if len(body.Items) == 0 {
		writeErr(w, http.StatusUnprocessableEntity, "invalid_request", "items must not be empty")
		return
	}
	if len(body.Items) > s.maxBatch {
		writeErr(w, http.StatusUnprocessableEntity, "invalid_request",
			fmt.Sprintf("batch is larger than %d items", s.maxBatch))
		return
	}
	// Per-item independent outcome: one bad label must not sink the batch.
	results := make([]any, 0, len(body.Items))
	accepted := 0
	for i, it := range body.Items {
		j, replayed, err := s.jobs.Submit(r.Context(), job.SubmitRequest{
			ClientID:       ClientFrom(r.Context()).ID,
			PrinterID:      it.PrinterID,
			ExternalID:     it.ExternalID,
			IdempotencyKey: r.Header.Get("Idempotency-Key"),
			ZPL:            it.ZPL,
			PayloadBase64:  it.PayloadBase64,
			Copies:         it.Copies,
			Priority:       it.Priority,
			BizRef:         it.BizRef,
			Meta:           it.Meta,
		})
		switch {
		case err != nil:
			results = append(results, map[string]any{"index": i, "accepted": false, "error": errString(err)})
		default:
			accepted++
			results = append(results, map[string]any{
				"index": i, "accepted": true, "replayed": replayed,
				"job_id": j.ID, "state": j.State, "printer_id": j.PrinterID,
			})
		}
	}
	status := http.StatusOK
	if accepted > 0 {
		status = http.StatusAccepted
	}
	writeJSON(w, status, map[string]any{
		"accepted": accepted, "rejected": len(body.Items) - accepted, "items": results,
	})
}

func (s *Server) dispatch(w http.ResponseWriter, r *http.Request, req job.SubmitRequest) {
	j, replayed, err := s.jobs.Submit(r.Context(), req)
	if err != nil {
		writeJobErr(w, err)
		return
	}
	status := http.StatusAccepted
	if replayed {
		status = http.StatusOK // nothing new happened; tell the caller clearly
	}
	writeJSON(w, status, jobAccepted(j, replayed))
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	j, err := s.jobs.Get(r.Context(), ClientFrom(r.Context()).ID, r.PathValue("job_id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "job_not_found", "unknown job "+r.PathValue("job_id"))
		return
	}
	writeJSON(w, http.StatusOK, j)
}

func (s *Server) getJobAttempts(w http.ResponseWriter, r *http.Request) {
	at, err := s.jobs.Attempts(r.Context(), ClientFrom(r.Context()).ID, r.PathValue("job_id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "job_not_found", "unknown job "+r.PathValue("job_id"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": at, "count": len(at)})
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	j, err := s.jobs.Cancel(r.Context(), ClientFrom(r.Context()).ID, r.PathValue("job_id"))
	if err != nil {
		if errors.Is(err, job.ErrNotCancellable) {
			writeErr(w, http.StatusConflict, "not_cancellable", err.Error())
			return
		}
		writeErr(w, http.StatusNotFound, "job_not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, j)
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.Filter{
		ClientID:   ClientFrom(r.Context()).ID,
		PrinterID:  q.Get("printer_id"),
		State:      q.Get("state"),
		ExternalID: q.Get("external_id"),
		Limit:      intParam(r, "limit", 50),
	}
	f.From, _ = timeParam(q.Get("from"))
	f.To, _ = timeParam(q.Get("to"))
	items, err := s.jobs.List(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

// ----------------------------------------------------------------- events

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "internal_error", "streaming unsupported")
		return
	}
	q := r.URL.Query()
	topics := map[string]bool{}
	for _, t := range splitCSV(q.Get("topics")) {
		topics[t] = true
	}
	if len(topics) == 0 {
		topics["fleet.status"] = true
		topics["job.state"] = true
	}
	printerFilter := map[string]bool{}
	for _, id := range splitCSV(q.Get("printer_ids")) {
		printerFilter[id] = true
	}

	ch, cancel := s.bus.Subscribe(64)
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "event: ready\ndata: {\"at\":%q}\n\n", time.Now().UTC().Format(time.RFC3339))
	flusher.Flush()

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if !topics[ev.Topic] {
				continue
			}
			if len(printerFilter) > 0 {
				m, _ := ev.Data.(map[string]any)
				if pid, _ := m["printer_id"].(string); !printerFilter[pid] {
					continue
				}
			}
			payload, err := json.Marshal(ev.Data)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Topic, payload)
			flusher.Flush()
		}
	}
}

// ------------------------------------------------------------------- ops

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "uptime_s": int(time.Since(s.startedAt).Seconds())})
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if err := s.jobs.Store().Ping(r.Context()); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "not_ready", "store unreachable: "+err.Error())
		return
	}
	if s.mgr.Len() == 0 {
		writeErr(w, http.StatusServiceUnavailable, "not_ready", "no printers configured")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "printers": s.mgr.Len()})
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	for _, v := range s.mgr.List(fleet.Filter{}) {
		age := 0.0
		if !v.Status.ObservedAt.IsZero() {
			age = time.Since(v.Status.ObservedAt).Seconds()
		}
		s.met.Set("cps_printer_usable", boolGauge(v.Usable), "printer_id", v.PrinterID)
		s.met.Set("cps_queue_depth", float64(v.QueueDepth), "printer_id", v.PrinterID)
		s.met.Set("cps_printer_status_age_seconds", age, "printer_id", v.PrinterID)
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = io.WriteString(w, s.met.Render())
}

func (s *Server) refreshGauges(items []domain.Printer) {
	for _, p := range items {
		s.met.Set("cps_queue_depth", float64(p.QueueDepth), "printer_id", p.PrinterID)
		s.met.Set("cps_printer_usable", boolGauge(p.Usable), "printer_id", p.PrinterID)
	}
}

// ----------------------------------------------------------------- helpers

func jobAccepted(j *domain.Job, replayed bool) map[string]any {
	return map[string]any{
		"job_id":      j.ID,
		"state":       j.State,
		"printer_id":  j.PrinterID,
		"external_id": j.ExternalID,
		"replayed":    replayed,
		"links":       map[string]string{"self": "/api/v1/jobs/" + j.ID},
	}
}

func writeJobErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, job.ErrPrinterNotFound):
		writeErr(w, http.StatusNotFound, "printer_not_found", err.Error(), nil)
	case errors.Is(err, job.ErrPrinterDisabled):
		writeErr(w, http.StatusConflict, "printer_disabled", err.Error(), nil)
	case errors.Is(err, job.ErrPrinterNotUsable):
		writeErr(w, http.StatusConflict, "printer_not_usable", err.Error(), nil)
	case errors.Is(err, job.ErrQueueFull):
		// Caller may retry later or fail over: this is backpressure, not a bad request.
		w.Header().Set("Retry-After", "2")
		writeErr(w, http.StatusServiceUnavailable, "queue_full", err.Error(), nil)
	case errors.Is(err, store.ErrConflict):
		writeErr(w, http.StatusConflict, "idempotency_conflict", err.Error(), nil)
	case errors.Is(err, job.ErrInvalidRequest):
		writeErr(w, http.StatusUnprocessableEntity, "invalid_request", err.Error(), nil)
	default:
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error(), nil)
	}
}

type errBody struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details,omitempty"`
	} `json:"error"`
}

func writeErr(w http.ResponseWriter, status int, code, msg string, details ...map[string]any) {
	var b errBody
	b.Error.Code = code
	b.Error.Message = msg
	if len(details) > 0 {
		b.Error.Details = details[0]
	}
	writeJSON(w, status, b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func intParam(r *http.Request, name string, def int) int {
	if v := r.URL.Query().Get(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func timeParam(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, fmt.Errorf("bad timestamp %q, want RFC3339", s)
	}
	return &t, nil
}

func boolGauge(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func errString(err error) string { return err.Error() }

func itoa(i int) string { return strconv.Itoa(i) }

// Package api exposes the HTTP surface consumed by ERP/WMS and operator tooling.
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kucial/cloud-print-service/pkg/printer"
	"github.com/kucial/cloud-print-service/pkg/render"
	"github.com/kucial/cloud-print-service/server/internal/discovery"
	"github.com/kucial/cloud-print-service/server/internal/domain"
	"github.com/kucial/cloud-print-service/server/internal/fleet"
	"github.com/kucial/cloud-print-service/server/internal/job"
	"github.com/kucial/cloud-print-service/server/internal/observ"
	"github.com/kucial/cloud-print-service/server/internal/store"
	"github.com/kucial/cloud-print-service/server/internal/token"
)

type Server struct {
	// streamsDone ends every open event stream (see StopStreams).
	streamsDone chan struct{}
	stopOnce    sync.Once

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
		maxBatch: maxBatch, maxPayload: maxPayload, startedAt: time.Now(),
		streamsDone: make(chan struct{})}
}

// StopStreams ends every open event stream. http.Server.Shutdown waits for handlers to
// return but never cancels them, and an event stream never returns on its own: with a
// panel open, shutdown would sit out its whole grace period (and docker stop would kill
// the process first). Register it with RegisterOnShutdown; clients reconnect anyway.
func (s *Server) StopStreams() {
	s.stopOnce.Do(func() { close(s.streamsDone) })
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
	apiMux.HandleFunc("GET /api/v1/printers/{printer_id}/diagnostics", RequireAdmin(s.diagnostics))
	apiMux.HandleFunc("POST /api/v1/printers/{printer_id}/maintenance", RequireAdmin(s.maintenance))
	apiMux.HandleFunc("POST /api/v1/printers/probe", RequireAdmin(s.probeAll))

	// --- dispatch ---------------------------------------------------------
	apiMux.HandleFunc("POST /api/v1/jobs", s.submitJob)
	apiMux.HandleFunc("POST /api/v1/jobs/raw", s.submitRawJob)
	apiMux.HandleFunc("POST /api/v1/jobs/batch", s.submitBatch)
	apiMux.HandleFunc("GET /api/v1/jobs", s.listJobs)
	apiMux.HandleFunc("GET /api/v1/jobs/status", s.bulkJobStatus)
	apiMux.HandleFunc("POST /api/v1/jobs/status", s.bulkJobStatus)
	apiMux.HandleFunc("GET /api/v1/jobs/{job_id}", s.getJob)
	apiMux.HandleFunc("POST /api/v1/jobs/{job_id}/cancel", s.cancelJob)
	apiMux.HandleFunc("POST /api/v1/jobs/{job_id}/resolve", s.resolveJob)
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
	apiMux.HandleFunc("POST /api/v1/admin/printers/{printer_id}/preview", RequireAdmin(s.previewDocument))

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

	// OpenAPI documentation: interactive web preview + raw JSON/YAML specs
	s.mountOpenAPI(root)

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
		Site:      r.URL.Query().Get("site"),
		Model:     r.URL.Query().Get("model"),
		PaperSize: r.URL.Query().Get("paper_size"),
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
			Site:      q.Get("site"),
			Model:     q.Get("model"),
			PaperSize: q.Get("paper_size"),
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
	var patch fleet.MetadataPatch
	if err := decodeJSON(r, &patch); err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	p, err := s.mgr.Registry().UpdateMetadata(printerID, patch)
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
	req := job.SubmitRequest{ClientID: ClientFrom(r.Context()).ID, PrinterID: p.ID(),
		ExternalID: external, BizRef: "test", Priority: "high"}
	var page map[string]any
	switch p.Spec().Kind {
	case printer.KindReceipt:
		data, cols := testReceiptESCPOS(p.Spec().Name, p.ID(), p.Spec().MediaWidthMM, time.Now())
		req.Payload, req.Format = data, string(printer.FormatESCPOS)
		page = map[string]any{"kind": "receipt", "columns": cols}
	case printer.KindLabel:
		geo := testLabelSize(r.Context(), p)
		cjk := testLabelFont(r.Context(), p)
		req.ZPL = testLabelZPL(p.Spec().Name, p.ID(), geo, cjk, time.Now())
		page = map[string]any{"kind": "label", "width_mm": geo.WidthMM, "height_mm": geo.HeightMM,
			"dpi": printer.DPIFromDPMM(geo.DPMM), "size_source": geo.Source,
			"cjk_font": cjk.Font, "cjk_note": cjk.Note}
	case printer.KindDocument:
		// An inkjet that takes only ESC/P-R gets the PDF page, rendered for it by the
		// converter (which needs poppler on the server).
		inkjet := slices.Contains(p.Spec().Formats, printer.FormatESCPR)
		if !slices.Contains(p.Spec().Formats, printer.FormatPDF) && !inkjet {
			if !slices.Contains(p.Spec().Formats, printer.FormatPCL) {
				writeErr(w, http.StatusConflict, "not_supported_by_printer",
					fmt.Sprintf("the test page is PDF or PCL and this printer takes %v", p.Spec().Formats))
				return
			}
			req.Payload, req.Format = testPagePCL(p.Spec().Name, p.ID(), time.Now()), string(printer.FormatPCL)
			page = map[string]any{"kind": "document", "paper": "A4", "language": "pcl"}
			break
		}
		var formats []string
		for _, f := range p.Spec().Formats {
			formats = append(formats, string(f))
		}
		data, err := testPagePDF(p.Spec().Name, p.ID(), formats, time.Now())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		req.Payload, req.Format = data, string(printer.FormatPDF)
		page = map[string]any{"kind": "document", "paper": "A4"}
	default:
		writeErr(w, http.StatusConflict, "not_supported_by_printer",
			fmt.Sprintf("no test page for %s printers yet", p.Spec().Kind))
		return
	}
	j, replayed, err := s.jobs.Submit(r.Context(), req)
	if err != nil {
		s.writeJobErr(w, err)
		return
	}
	out := jobAccepted(j, replayed)
	out["label"] = page // kept as "label" for clients written against label printers
	writeJSON(w, http.StatusAccepted, out)
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

	// Update printer metadata with ~HI hardware-verified resolution and CJK font
	patch := fleet.MetadataPatch{}
	if dpiStr, ok := info["dpi"]; ok {
		if dpiVal, err := strconv.Atoi(dpiStr); err == nil && dpiVal > 0 {
			dpiHI := true
			resSrc := "hi"
			patch.DPI = &dpiVal
			patch.DPIResolvedFromHI = &dpiHI
			patch.ResolutionSource = &resSrc
			if dpmmStr, ok := info["dpmm"]; ok {
				if dpmmVal, err := strconv.ParseFloat(dpmmStr, 64); err == nil && dpmmVal > 0 {
					patch.DPMM = &dpmmVal
				}
			}
		}
	}
	if font, ok := info["cjk_font"]; ok && font != "" {
		fontHI := true
		fontSrc := "hi"
		cjkSupp := true
		patch.CJKFont = &font
		patch.CJKSupported = &cjkSupp
		patch.FontResolvedFromHI = &fontHI
		patch.FontSource = &fontSrc
	}
	if patch.DPI != nil || patch.CJKFont != nil {
		_, _ = s.mgr.Registry().UpdateMetadata(p.ID(), patch)
		_ = s.mgr.Sync(r.Context())
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
	Format        string            `json:"format"` // zpl (default), escpos, pdf, png, …
	Copies        int               `json:"copies"`
	PageCopies    []int             `json:"page_copies"`
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
		Format:         b.Format,
		Copies:         b.Copies,
		PageCopies:     b.PageCopies,
		Priority:       b.Priority,
		BizRef:         b.BizRef,
		Meta:           b.Meta,
	})
}

// submitRawJob takes the document as the request body, to avoid base64 for large
// payloads. The format comes from ?format=, else from the Content-Type (application/pdf,
// image/png, image/jpeg); octet-stream and text/plain without ?format= mean ZPL.
func (s *Server) submitRawJob(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	switch ct = strings.TrimSpace(strings.ToLower(ct)); ct {
	case "", "application/octet-stream", "text/plain":
	default:
		f, ok := printer.ParseFormat(ct)
		if !ok {
			writeErr(w, http.StatusUnsupportedMediaType, "invalid_request",
				"expected application/octet-stream (with ?format=), text/plain, application/pdf, image/png or image/jpeg")
			return
		}
		if format == "" {
			format = string(f)
		}
	}
	var rdr io.Reader = r.Body
	if s.maxPayload > 0 {
		rdr = io.LimitReader(r.Body, s.maxPayload+1)
	}
	body, err := io.ReadAll(rdr)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "cannot read body: "+err.Error())
		return
	}
	if s.maxPayload > 0 && int64(len(body)) > s.maxPayload {
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
		Format:         format,
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
			PageCopies:     it.PageCopies,
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
		s.writeJobErr(w, err)
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

func (s *Server) resolveJob(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Resolution string `json:"resolution"` // "printed" | "failed"
		Note       string `json:"note"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	clientID := ClientFrom(r.Context()).ID
	if ClientFrom(r.Context()).Role == "admin" {
		clientID = "" // admin can resolve any client's job
	}
	j, err := s.jobs.Resolve(r.Context(), clientID, r.PathValue("job_id"), domain.JobState(body.Resolution), body.Note)
	if err != nil {
		if errors.Is(err, job.ErrJobNotFound) || errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "job_not_found", err.Error())
			return
		}
		writeErr(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
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
	if idsStr := q.Get("ids"); idsStr != "" {
		f.IDs = splitCSV(idsStr)
		if f.Limit < len(f.IDs) {
			f.Limit = len(f.IDs)
		}
	}
	f.From, _ = timeParam(q.Get("from"))
	f.To, _ = timeParam(q.Get("to"))
	items, err := s.jobs.List(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	res := map[string]any{"items": items, "count": len(items)}
	if len(f.IDs) > 0 {
		summary := map[string]int{
			"queued": 0, "sending": 0, "printed": 0, "sent_unknown": 0, "failed": 0, "cancelled": 0,
		}
		for _, j := range items {
			summary[string(j.State)]++
		}
		res["summary"] = summary
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) bulkJobStatus(w http.ResponseWriter, r *http.Request) {
	var ids []string
	if r.Method == http.MethodPost {
		var body struct {
			IDs []string `json:"ids"`
		}
		if err := decodeJSON(r, &body); err == nil && len(body.IDs) > 0 {
			ids = body.IDs
		}
	}
	if len(ids) == 0 {
		ids = splitCSV(r.URL.Query().Get("ids"))
	}
	limit := len(ids)
	if limit < 100 {
		limit = 100
	}
	f := store.Filter{
		ClientID: ClientFrom(r.Context()).ID,
		IDs:      ids,
		Limit:    limit,
	}
	items, err := s.jobs.List(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	summary := map[string]int{
		"queued": 0, "sending": 0, "printed": 0, "sent_unknown": 0, "failed": 0, "cancelled": 0,
	}
	for _, j := range items {
		summary[string(j.State)]++
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":   items,
		"count":   len(items),
		"summary": summary,
	})
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
		case <-s.streamsDone:
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

func (s *Server) writeJobErr(w http.ResponseWriter, err error) {
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
		reason := jobInvalidReason(err)
		details := map[string]any{"reason": reason}
		if errors.Is(err, job.ErrPayloadTooLarge) {
			if s.maxPayload > 0 {
				details["max_payload_bytes"] = s.maxPayload
			}
		}
		writeErrReason(w, http.StatusUnprocessableEntity, "invalid_request", reason, err.Error(), details)
	default:
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error(), nil)
	}
}

func jobInvalidReason(err error) string {
	switch {
	case errors.Is(err, job.ErrPayloadTooLarge):
		return "payload_too_large"
	case errors.Is(err, job.ErrPayloadEmpty):
		return "payload_empty"
	case errors.Is(err, job.ErrMissingPayload):
		return "missing_payload"
	case errors.Is(err, job.ErrDuplicatePayload):
		return "duplicate_payload"
	case errors.Is(err, job.ErrInvalidBase64):
		return "invalid_base64"
	case errors.Is(err, job.ErrMissingPrinterID):
		return "missing_printer_id"
	case errors.Is(err, job.ErrMissingExternalID):
		return "missing_external_id"
	case errors.Is(err, job.ErrInvalidCopies):
		return "invalid_copies"
	case errors.Is(err, job.ErrInvalidPageCopies):
		return "invalid_page_copies"
	case errors.Is(err, job.ErrInvalidPriority):
		return "invalid_priority"
	case errors.Is(err, job.ErrInvalidFormat):
		return "invalid_format"
	case errors.Is(err, job.ErrFormatUnsupported):
		return "format_not_supported"
	case errors.Is(err, job.ErrCopiesConflict):
		return "copies_conflict"
	case errors.Is(err, job.ErrCopiesUnreadable):
		return "copies_not_applicable"
	case errors.Is(err, job.ErrUnreadable):
		return "document_unreadable"
	case errors.Is(err, job.ErrPDFToolsMissing):
		return "pdf_conversion_unavailable"
	default:
		return "invalid_request"
	}
}

type errBody struct {
	Error struct {
		Code    string         `json:"code"`
		Reason  string         `json:"reason,omitempty"`
		Message string         `json:"message"`
		Details map[string]any `json:"details,omitempty"`
	} `json:"error"`
}

func writeErr(w http.ResponseWriter, status int, code, msg string, details ...map[string]any) {
	writeErrReason(w, status, code, "", msg, details...)
}

func writeErrReason(w http.ResponseWriter, status int, code, reason, msg string, details ...map[string]any) {
	var b errBody
	b.Error.Code = code
	b.Error.Reason = reason
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
	// Allow large payloads (e.g. CJK TrueType font files in base64 / inline) up to 128 MB.
	dec := json.NewDecoder(io.LimitReader(r.Body, 128<<20))
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

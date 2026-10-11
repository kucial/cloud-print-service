package httpapi

import (
	"net/http"
	"time"

	"github.com/kongkx/cloud-print-service/internal/discovery"
	"github.com/kongkx/cloud-print-service/internal/domain"
	"github.com/kongkx/cloud-print-service/internal/fleet"
)

// Discovery endpoints. Everything here is admin-only: a warehouse LAN contains whatever
// else is plugged in, and the whole point of the candidate model is that a human decides
// what becomes a printer.

func (s *Server) SetDiscovery(d *discovery.Service) { s.disco = d }

// scanDiscovery runs one sweep now instead of waiting for the interval.
func (s *Server) scanDiscovery(w http.ResponseWriter, r *http.Request) {
	if s.disco == nil {
		writeErr(w, http.StatusNotImplemented, "not_configured",
			"discovery is disabled; set discovery.enabled=true")
		return
	}
	start := time.Now()
	found, err := s.disco.Scan(r.Context())
	status := s.disco.Status()
	payload := map[string]any{
		"found":      found,
		"candidates": s.mgr.Registry().Candidates(false),
		"status":     status,
		"took":       time.Since(start).String(),
	}
	if err != nil {
		// A partial sweep is still useful: report what was found alongside the error.
		payload["warning"] = err.Error()
		s.met.Inc("cps_discovery_scan_errors_total")
	}
	s.met.Inc("cps_discovery_scan_total")
	s.met.Set("cps_discovery_candidates", float64(len(s.mgr.Registry().Candidates(false))))
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) discoveryStatus(w http.ResponseWriter, r *http.Request) {
	if s.disco == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	status := s.disco.Status()
	status.Candidates = len(s.mgr.Registry().Candidates(false))
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) listCandidates(w http.ResponseWriter, r *http.Request) {
	includeDismissed := r.URL.Query().Get("dismissed") == "true"
	items := s.mgr.Registry().Candidates(includeDismissed)
	s.met.Set("cps_discovery_candidates", float64(len(items)))
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

// adoptCandidate turns a device into a dispatchable printer. The printer_id is required
// because ids are a human, site-aware decision ("wh1-pack-04"), not a guess.
func (s *Server) adoptCandidate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PrinterID string `json:"printer_id"`
		Site      string `json:"site"`
		Name      string `json:"name"`
		Model     string `json:"model"`
		Host      string `json:"host"`
		Port        int    `json:"port"`
		Remark      string `json:"remark"`
		CustomLabel string `json:"custom_label"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	if body.PrinterID == "" {
		writeErr(w, http.StatusUnprocessableEntity, "invalid_request",
			"printer_id is required: adoption is an explicit, human decision")
		return
	}
	p := domain.Printer{
		PrinterID: body.PrinterID, Site: body.Site, Name: body.Name, Model: body.Model,
		Driver: "zebra9100", Port: 9100, Enabled: true,
		Host: body.Host, Remark: body.Remark, CustomLabel: body.CustomLabel,
	}
	if err := s.mgr.Registry().AdoptCandidate(r.PathValue("device_key"), fleet.SourceDiscovery, "", p); err != nil {
		writeErr(w, http.StatusNotFound, "candidate_not_found", err.Error())
		return
	}
	if err := s.mgr.Sync(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, "sync_failed", err.Error())
		return
	}
	s.met.Inc("cps_discovery_adopted_total")
	s.log.Info("candidate adopted", "device_key", r.PathValue("device_key"), "printer_id", body.PrinterID)
	writeJSON(w, http.StatusCreated, map[string]any{"printer_id": body.PrinterID, "status": "adopted"})
}

func (s *Server) dismissCandidate(w http.ResponseWriter, r *http.Request) {
	if !s.mgr.Registry().DismissCandidate(r.PathValue("device_key")) {
		writeErr(w, http.StatusNotFound, "candidate_not_found", "unknown device "+r.PathValue("device_key"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "dismissed"})
}

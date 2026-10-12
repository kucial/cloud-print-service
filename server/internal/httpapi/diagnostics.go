package httpapi

import (
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/kucial/cloud-print-service/pkg/printer"
	"github.com/kucial/cloud-print-service/server/internal/domain"
)

// Printer diagnostics: ask the device what it actually is and how it is set up, and say
// where that disagrees with what the fleet declares. Read-only — nothing is printed.
//
// Born from bringing up a real GK888t: its status, media width, label length and print
// offset were all only discoverable by querying the printer directly.

type diagnosticsReport struct {
	PrinterID string               `json:"printer_id"`
	CheckedAt time.Time            `json:"checked_at"`
	Identity  *printer.HostID      `json:"identity,omitempty"`
	Status    *domain.Status       `json:"status,omitempty"`
	Config    *printer.HostConfig  `json:"config,omitempty"`
	Declared  declaredMedia        `json:"declared"`
	Warnings  []diagnosticsWarning `json:"warnings"`
	Errors    map[string]string    `json:"errors,omitempty"` // per query: hi, hs, hh
	RawID     string               `json:"raw_identity,omitempty"`
	RawConfig string               `json:"raw_config,omitempty"`
}

type declaredMedia struct {
	PaperSize     string  `json:"paper_size,omitempty"`
	MediaWidthMM  float64 `json:"media_width_mm,omitempty"`
	MediaHeightMM float64 `json:"media_height_mm,omitempty"`
	DPI           int     `json:"dpi,omitempty"`
	// Feed direction is physical: no Zebra query reports it, so it is only ever declared.
	PrintDirection string `json:"print_direction,omitempty"`
}

type diagnosticsWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (s *Server) diagnostics(w http.ResponseWriter, r *http.Request) {
	p, ok := s.mgr.Get(r.PathValue("printer_id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "printer_not_found", "unknown printer "+r.PathValue("printer_id"))
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	spec := p.Spec()
	rep := diagnosticsReport{
		PrinterID: p.ID(), CheckedAt: time.Now().UTC(), Errors: map[string]string{},
		Declared: declaredMedia{PaperSize: spec.PaperSize, MediaWidthMM: spec.MediaWidthMM,
			MediaHeightMM: spec.MediaHeightMM, DPI: spec.DPI, PrintDirection: spec.PrintDirection},
		Warnings: []diagnosticsWarning{},
	}

	if info, err := p.Identify(ctx); err != nil {
		rep.Errors["hi"] = err.Error()
	} else {
		rep.RawID = info["raw"]
		if id, ok := printer.ParseHostID(info["raw"]); ok {
			rep.Identity = &id
		}
	}
	if st, err := p.Status(ctx); err != nil {
		rep.Errors["hs"] = err.Error()
	} else {
		rep.Status = &st
	}
	if raw, err := p.HostConfig(ctx); err != nil {
		rep.Errors["hh"] = err.Error()
	} else {
		rep.RawConfig = raw
		if c, ok := printer.ParseHostConfig(raw); ok {
			rep.Config = &c
		} else {
			rep.Errors["hh"] = "unrecognised configuration reply"
		}
	}
	rep.Warnings = append(rep.Warnings, compareMedia(rep.Declared, rep.Config, rep.Status)...)
	if len(rep.Errors) == 0 {
		rep.Errors = nil
	}
	writeJSON(w, http.StatusOK, rep)
}

// compareMedia flags where the printer's own setup contradicts the declared spec.
func compareMedia(d declaredMedia, c *printer.HostConfig, st *domain.Status) []diagnosticsWarning {
	var out []diagnosticsWarning
	add := func(code, format string, a ...any) {
		out = append(out, diagnosticsWarning{Code: code, Message: fmt.Sprintf(format, a...)})
	}
	if st != nil && st.State.BlocksUse() {
		add("status_blocking", "printer reports %s: %s", st.State, st.Detail)
	}
	if c == nil {
		return out
	}
	if d.MediaWidthMM == 0 && d.MediaHeightMM == 0 {
		add("media_undeclared", "no paper size declared; printer is set to %.1f mm wide, %.1f mm label pitch",
			c.PrintWidthMM, c.LabelLengthMM)
	}
	// Print width is the printable width, so a narrower roll than declared is the
	// failure that cuts labels off; allow a few mm for the printer's own margins.
	if d.MediaWidthMM > 0 && c.PrintWidthMM > 0 && math.Abs(c.PrintWidthMM-d.MediaWidthMM) > 4 {
		add("width_mismatch", "printer print width is %.1f mm, declared media is %.1f mm wide",
			c.PrintWidthMM, d.MediaWidthMM)
	}
	// Label length is the measured pitch: label plus gap, usually 2–4 mm more.
	if d.MediaHeightMM > 0 && c.LabelLengthMM > 0 {
		if diff := c.LabelLengthMM - d.MediaHeightMM; diff < -1 || diff > 5 {
			add("length_mismatch", "printer measures a %.1f mm label pitch, declared label is %.1f mm long "+
				"(recalibrate if the roll was changed)", c.LabelLengthMM, d.MediaHeightMM)
		}
	}
	if d.DPI > 0 && c.DPI > 0 && d.DPI != c.DPI {
		add("dpi_mismatch", "printer resolution is %d dpi, declared %d dpi: labels will print at the wrong scale",
			c.DPI, d.DPI)
	}
	if c.LabelTopDots != 0 || c.LeftPosDots != 0 {
		add("offset_set", "print offset is adjusted on the printer (label top %+d, left %+d dots)",
			c.LabelTopDots, c.LeftPosDots)
	}
	if d.PrintDirection == "" {
		add("direction_undeclared", "feed direction is not declared and the printer cannot report it: "+
			"look at a printed label — if its top comes out of the printer last, set bottom_first")
	}
	if c.Language != "" && !strings.HasPrefix(strings.ToUpper(c.Language), "ZPL") {
		add("not_zpl", "printer language is %q; ZPL labels may not print", c.Language)
	}
	return out
}

// Maintenance actions change the printer, so they are admin-only, refused while work is
// queued or printing (calibration feeds labels), and logged.
type maintenanceRequest struct {
	Action string `json:"action"` // calibrate | label_top
	Dots   int    `json:"dots"`   // label_top: -120..120, positive moves print down
}

func (s *Server) maintenance(w http.ResponseWriter, r *http.Request) {
	p, ok := s.mgr.Get(r.PathValue("printer_id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "printer_not_found", "unknown printer "+r.PathValue("printer_id"))
		return
	}
	var req maintenanceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	var cmd, note string
	switch req.Action {
	case "calibrate":
		cmd, note = "~JC", "the printer feeds a few blank labels while it measures the gap"
	case "label_top":
		if req.Dots < -120 || req.Dots > 120 {
			writeErr(w, http.StatusUnprocessableEntity, "invalid_request", "dots must be between -120 and 120")
			return
		}
		cmd, note = fmt.Sprintf("^XA^LT%d^JUS^XZ", req.Dots), "saved on the printer; applies to every label"
	default:
		writeErr(w, http.StatusUnprocessableEntity, "invalid_request", "action must be calibrate or label_top")
		return
	}
	v := p.View()
	if v.QueueDepth > 0 || v.Status.State.Busy() {
		writeErr(w, http.StatusConflict, "printer_busy",
			fmt.Sprintf("printer has %d queued job(s) and is %s; retry when idle", v.QueueDepth, v.Status.State))
		return
	}
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()
	if err := p.Control(ctx, cmd); err != nil {
		writeErr(w, http.StatusBadGateway, "printer_unreachable", err.Error())
		return
	}
	s.log.Info("printer maintenance", "event", "printer.maintenance", "printer_id", p.ID(),
		"action", req.Action, "command", cmd, "client_id", ClientFrom(r.Context()).ID)
	writeJSON(w, http.StatusOK, map[string]any{"printer_id": p.ID(), "action": req.Action, "sent": cmd, "note": note})
}

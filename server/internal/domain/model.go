// Package model holds the domain types shared by every layer.
package domain

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/kucial/cloud-print-service/pkg/printer"
)

// PrinterState is the coarse, vendor-neutral state of a printer. It lives in pkg/printer
// so the edge agent can speak it without importing server internals.
type PrinterState = printer.State

const (
	StateUnknown   = printer.StateUnknown
	StateIdle      = printer.StateIdle
	StatePrinting  = printer.StatePrinting
	StateBusy      = printer.StateBusy
	StateMediaLow  = printer.StateMediaLow
	StateMediaOut  = printer.StateMediaOut
	StateRibbonLow = printer.StateRibbonLow
	StateRibbonOut = printer.StateRibbonOut
	StateHeadError = printer.StateHeadError
	StateError     = printer.StateError
	StateOffline   = printer.StateOffline
	StateDisabled  = printer.StateDisabled
)

// JobRef is a pointer from a printer to the last job that touched it.
type JobRef struct {
	JobID string    `json:"job_id"`
	State JobState  `json:"state"`
	At    time.Time `json:"at"`
}

// Status is a snapshot of printer status. Never includes business payload data.
type Status struct {
	State      PrinterState `json:"state"`
	Raw        string       `json:"raw,omitempty"`
	Detail     string       `json:"detail,omitempty"`
	ObservedAt time.Time    `json:"observed_at"`
	Stale      bool         `json:"stale"`
	LastJob    *JobRef      `json:"last_job,omitempty"`
}

// MockCfg drives the in-memory fake printer used by tests and demos.
type MockCfg struct {
	State     string `json:"state"`      // state to report, default idle
	DelayMS   int    `json:"delay_ms"`   // artificial send latency
	FailEvery int    `json:"fail_every"` // fail every Nth send before any byte is flushed
	Offline   bool   `json:"offline"`    // refuse connections
}

// Printer is both the declared spec and the live view of a printer.
type Printer struct {
	PrinterID string `json:"printer_id"`
	Name      string `json:"name"`
	Site      string `json:"site"`
	Model     string `json:"model"`
	Driver    string `json:"driver"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	// Path is the IPP resource path (e.g. "ipp/print"); other drivers ignore it.
	Path        string `json:"path,omitempty"`
	Enabled     bool   `json:"enabled"`
	Remark      string `json:"remark,omitempty"`
	CustomLabel string `json:"custom_label,omitempty"`

	// Kind is label, receipt or document; it defaults from the driver. Formats lists
	// what the printer takes as is (learned from the printer, or declared); anything
	// else a client sends is converted to one of them, or refused.
	Kind    printer.Kind     `json:"kind"`
	Formats []printer.Format `json:"formats"`

	// Installed media/roll specifications
	PaperSize     string  `json:"paper_size,omitempty"`      // standard size tag, e.g. "4x6", "100x150mm", "4x2", "2x1"
	MediaWidthMM  float64 `json:"media_width_mm,omitempty"`  // roll width in mm (e.g. 100, 101.6)
	MediaHeightMM float64 `json:"media_height_mm,omitempty"` // label length/pitch in mm (e.g. 150, 152.4)

	// Print resolution and geometry
	DPI               int     `json:"dpi,omitempty"`               // dots per inch, e.g. 203, 300, 600
	DPMM              float64 `json:"dpmm,omitempty"`              // dots per mm, e.g. 8, 12, 24
	ResolutionSource  string  `json:"resolution_source,omitempty"` // "hi" (hardware verified) | "config" (declared) | "default"
	DPIResolvedFromHI bool    `json:"dpi_resolved_from_hi"`        // true if DPI was verified from ~HI hardware query
	Orientation       string  `json:"orientation,omitempty"`       // "normal" (0°), "inverted" (180°), "portrait", "landscape"
	MediaOrientation  string  `json:"media_orientation,omitempty"` // alias for media feed alignment
	PrintDirection    string  `json:"print_direction,omitempty"`   // "top_first", "bottom_first", "normal", "reverse"

	// CJK (Chinese / Japanese / Korean) font specifications
	CJKSupported       bool     `json:"cjk_supported,omitempty"` // whether printer has CJK font installed
	CJKFont            string   `json:"cjk_font,omitempty"`      // primary CJK font identifier, e.g. "SIMSUN.FNT", "ANSD.TTF"
	Fonts              []string `json:"fonts,omitempty"`         // installed/available fonts list, e.g. ["0", "SIMSUN.FNT"]
	FontSource         string   `json:"font_source,omitempty"`   // "hi" (hardware verified) | "config" (declared) | "default"
	FontResolvedFromHI bool     `json:"font_resolved_from_hi"`   // true if font was verified from ~HI hardware query

	// AvailableFonts is what the printer reports it stores (^HW): nil until the server
	// has asked, empty when it has none. Business systems choose fonts from this.
	AvailableFonts []printer.StoredFile `json:"available_fonts"`

	// Live fields, filled by the printer manager.
	// Capabilities names what the printer can do beyond printing: diagnostics,
	// maintenance and fonts (label printers), test_print. The panel shows tools by it.
	Capabilities []string `json:"capabilities,omitempty"`
	Usable       bool     `json:"usable"`
	QueueDepth   int      `json:"queue_depth"`
	Status       Status   `json:"status"`

	Mock *MockCfg `json:"mock,omitempty"`
}

// ParsePaperSize parses standard label and roll paper size strings, with full support
// for millimeter ('mm') and inch ('in') units (e.g. "100x150mm", "100x150", "4x6", "4x6in", "80*50mm").
// It returns the physical width and height in millimeters, plus a normalized canonical tag.
func ParsePaperSize(s string) (widthMM, heightMM float64, normTag string, ok bool) {
	raw := strings.TrimSpace(strings.ToLower(s))
	if raw == "" {
		return 0, 0, "", false
	}

	// Known named presets
	switch raw {
	case "4x6", "4x6in", "4x6\"":
		return 101.6, 152.4, "4x6", true
	case "4x2", "4x2in", "4x2\"":
		return 101.6, 50.8, "4x2", true
	case "2x1", "2x1in", "2x1\"":
		return 50.8, 25.4, "2x1", true
	case "3x2", "3x2in", "3x2\"":
		return 76.2, 50.8, "3x2", true
	case "a6":
		return 105.0, 148.0, "A6", true
	case "a4":
		return 210.0, 297.0, "A4", true
	}

	// Check if units are explicitly declared
	isMM := strings.HasSuffix(raw, "mm")
	isIn := strings.HasSuffix(raw, "in") || strings.HasSuffix(raw, "\"")

	cleaned := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(raw, "mm"), "in"), "\"")
	cleaned = strings.TrimSpace(cleaned)

	var parts []string
	if strings.Contains(cleaned, "x") {
		parts = strings.Split(cleaned, "x")
	} else if strings.Contains(cleaned, "*") {
		parts = strings.Split(cleaned, "*")
	}

	if len(parts) == 2 {
		wStr := strings.TrimSpace(parts[0])
		hStr := strings.TrimSpace(parts[1])
		w, err1 := strconv.ParseFloat(wStr, 64)
		h, err2 := strconv.ParseFloat(hStr, 64)
		if err1 == nil && err2 == nil && w > 0 && h > 0 {
			if isIn {
				widthMM = math.Round(w*25.4*10) / 10
				heightMM = math.Round(h*25.4*10) / 10
				normTag = fmt.Sprintf("%gx%gin", w, h)
				return widthMM, heightMM, normTag, true
			}
			if isMM || (w >= 10 && h >= 10) {
				// Either explicit mm, or numbers like 100x150, 80x50 which are clearly millimeters
				widthMM = w
				heightMM = h
				normTag = fmt.Sprintf("%gx%gmm", w, h)
				return widthMM, heightMM, normTag, true
			}
			// Small bare numbers (e.g. 4x6, 3x2, 2.25x1.25) are assumed inches
			widthMM = math.Round(w*25.4*10) / 10
			heightMM = math.Round(h*25.4*10) / 10
			normTag = fmt.Sprintf("%gx%g", w, h)
			return widthMM, heightMM, normTag, true
		}
	}

	return 0, 0, raw, false
}

// MatchesPaperSize tests whether this printer's paper size matches a filter tag or dimension.
// It matches exact tags ("4x6", "100x150mm"), normalized aliases, and physical mm dimensions within 2.5mm tolerance.
func (p *Printer) MatchesPaperSize(filter string) bool {
	f := strings.TrimSpace(filter)
	if f == "" {
		return true
	}
	if strings.EqualFold(p.PaperSize, f) {
		return true
	}

	fw, fh, fNorm, fOk := ParsePaperSize(f)
	if strings.EqualFold(p.PaperSize, fNorm) {
		return true
	}
	pw, ph, pNorm, pOk := ParsePaperSize(p.PaperSize)
	if fOk && pOk && strings.EqualFold(pNorm, fNorm) {
		return true
	}

	curW := p.MediaWidthMM
	curH := p.MediaHeightMM
	if curW == 0 && curH == 0 && pOk {
		curW, curH = pw, ph
	}

	if fOk && curW > 0 && curH > 0 {
		// 2.5 mm tolerance (~0.1 inch), accounts for rounding 4" (101.6mm) vs 100mm
		if math.Abs(curW-fw) <= 2.5 && math.Abs(curH-fh) <= 2.5 {
			return true
		}
	}
	return false
}

// DefaultPort is where a printer of this driver listens when the config does not say.
func DefaultPort(driver string) int {
	if driver == printer.DriverIPP {
		return 631
	}
	return 9100
}

// CheckKindAndFormats rejects a kind or format name the service does not know, so a
// typo in the config stops the server instead of silently refusing every job later.
func (p *Printer) CheckKindAndFormats() error {
	switch p.Kind {
	case printer.KindLabel, printer.KindReceipt, printer.KindDocument:
	default:
		return fmt.Errorf("kind %q must be label, receipt or document", p.Kind)
	}
	for i, f := range p.Formats {
		canon, ok := printer.ParseFormat(string(f))
		if !ok {
			return fmt.Errorf("unknown format %q", f)
		}
		p.Formats[i] = canon
	}
	return nil
}

// Normalize fills derived defaults for kind, formats, resolution, paper dimensions,
// orientation, and CJK fonts.
func (p *Printer) Normalize() {
	if p.Kind == "" {
		p.Kind = printer.KindForDriver(p.Driver)
	}
	if len(p.Formats) == 0 {
		p.Formats = printer.DefaultFormats(p.Driver)
	}
	if p.Kind != printer.KindLabel {
		// Rolls, label orientation and printer fonts are label-printer facts; a receipt
		// or office printer keeps only what it declared (e.g. paper width).
		p.normalizeResolution()
		return
	}
	p.normalizeResolution()

	// 2. Paper Size & Physical Dimensions (mm unit support)
	if p.PaperSize != "" {
		w, h, _, ok := ParsePaperSize(p.PaperSize)
		if ok {
			if p.MediaWidthMM == 0 {
				p.MediaWidthMM = w
			}
			if p.MediaHeightMM == 0 {
				p.MediaHeightMM = h
			}
		}
	} else if p.MediaWidthMM > 0 && p.MediaHeightMM > 0 {
		p.PaperSize = fmt.Sprintf("%gx%gmm", p.MediaWidthMM, p.MediaHeightMM)
	}

	// 3. Orientation & Print Direction
	if p.Orientation != "" && p.MediaOrientation == "" {
		p.MediaOrientation = p.Orientation
	} else if p.MediaOrientation != "" && p.Orientation == "" {
		p.Orientation = p.MediaOrientation
	}
	if p.Orientation == "" {
		if p.MediaWidthMM > 0 && p.MediaHeightMM > 0 && p.MediaWidthMM > p.MediaHeightMM {
			p.Orientation = "landscape"
			p.MediaOrientation = "landscape"
		} else {
			p.Orientation = "normal"
			p.MediaOrientation = "normal"
		}
	}
	// PrintDirection is left empty when undeclared: it is a physical fact of how the
	// printer feeds (no Zebra query reports it), and a guessed default reads as truth.

	// The printer's own font list, once known, is the truth about CJK support.
	if p.AvailableFonts != nil {
		p.CJKSupported = false
		for _, f := range p.AvailableFonts {
			if f.CJK {
				p.CJKSupported = true
			}
		}
	}

	// 4. CJK font support
	if p.CJKFont != "" {
		p.CJKSupported = true
		found := false
		for _, f := range p.Fonts {
			if strings.EqualFold(f, p.CJKFont) {
				found = true
				break
			}
		}
		if !found {
			p.Fonts = append(p.Fonts, p.CJKFont)
		}
	}
	if p.FontSource == "" {
		if p.FontResolvedFromHI {
			p.FontSource = "hi"
		} else if p.CJKFont != "" || len(p.Fonts) > 0 {
			p.FontSource = "config"
		} else {
			p.FontSource = "default"
		}
	}
	if p.FontResolvedFromHI {
		p.FontSource = "hi"
	}
}

// JobState is the lifecycle position of a dispatch job.
type JobState string

const (
	// JobQueued: Job was accepted, validated, and placed in the per-printer FIFO queue.
	JobQueued JobState = "queued"

	// JobSending: Worker opened TCP socket to printer (:9100) and is actively transmitting ZPL bytes.
	JobSending JobState = "sending"

	// JobPrinted: All payload bytes were delivered to the socket AND the printer's status
	// channel (^HS) confirmed physical execution and return to idle.
	JobPrinted JobState = "printed"

	// JobSentUnknown: Bytes were transmitted to the printer socket, but physical execution
	// confirmation timed out or connection dropped. Terminal for automatic retries to prevent
	// duplicate physical labels on packages. Resolved by operator inspection via API/panel.
	JobSentUnknown JobState = "sent_unknown"

	// JobFailed: Failed before transmission or failed with partial socket write. Zero bytes
	// confirmed printed; safe to retry.
	JobFailed JobState = "failed"

	// JobCancelled: Cancelled by operator while waiting in queue before transmission started.
	JobCancelled JobState = "cancelled"
)

// Terminal reports whether the job will not change state again on its own.
func (s JobState) Terminal() bool {
	switch s {
	case JobPrinted, JobSentUnknown, JobFailed, JobCancelled:
		return true
	}
	return false
}

// Job is one dispatch of one payload to one printer.
type Job struct {
	ID            string            `json:"job_id"`
	ClientID      string            `json:"client_id"`
	ExternalID    string            `json:"external_id"`
	PrinterID     string            `json:"printer_id"`
	State         JobState          `json:"state"`
	StateDetail   string            `json:"state_detail,omitempty"`   // detailed explanation of state guarantees and safety rules
	StatusMessage string            `json:"status_message,omitempty"` // short summary suitable for business UI
	Payload       string            `json:"-"`                        // never serialized by default; see docs/DESIGN.md §12
	Bytes         int               `json:"payload_bytes"`
	PayloadSHA    string            `json:"payload_sha256"`
	Format        printer.Format    `json:"format,omitempty"`        // what is sent: zpl, escpos, pdf, …
	SourceFormat  printer.Format    `json:"source_format,omitempty"` // what the client sent, when CPS converted it
	Copies        int               `json:"copies"`
	PageCopies    []int             `json:"page_copies,omitempty"`
	Priority      string            `json:"priority"`
	BizRef        string            `json:"biz_ref"`
	Meta          map[string]string `json:"meta,omitempty"`

	Attempts       int        `json:"attempts"`
	LastError      string     `json:"last_error,omitempty"`
	PartialWrite   bool       `json:"partial_write"`
	IdempotencyKey string     `json:"-"`
	QueuedAt       time.Time  `json:"queued_at"`
	SentAt         *time.Time `json:"sent_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
}

// Attempt is one physical send attempt against a printer.
type Attempt struct {
	ID         int64      `json:"id"`
	JobID      string     `json:"job_id"`
	AttemptNo  int        `json:"attempt_no"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Result     string     `json:"result"` // ok|connect_error|write_error|partial_write|unconfirmed
	BytesSent  int        `json:"bytes_sent"`
	Error      string     `json:"error,omitempty"`
}

// normalizeResolution keeps DPI and DPMM consistent and records where they came from.
func (p *Printer) normalizeResolution() {
	if p.DPI > 0 && p.DPMM == 0 {
		switch p.DPI {
		case 203:
			p.DPMM = 8
		case 300:
			p.DPMM = 12
		case 600:
			p.DPMM = 24
		default:
			p.DPMM = math.Round((float64(p.DPI)/25.4)*10) / 10
		}
	} else if p.DPMM > 0 && p.DPI == 0 {
		switch int(p.DPMM) {
		case 8:
			p.DPI = 203
		case 12:
			p.DPI = 300
		case 24:
			p.DPI = 600
		default:
			p.DPI = int(p.DPMM*25.4 + 0.5)
		}
	}
	if p.ResolutionSource == "" {
		if p.DPIResolvedFromHI {
			p.ResolutionSource = "hi"
		} else if p.DPI > 0 || p.DPMM > 0 {
			p.ResolutionSource = "config"
		} else {
			p.ResolutionSource = "default"
		}
	}
	if p.DPIResolvedFromHI {
		p.ResolutionSource = "hi"
	}

}

// Package model holds the domain types shared by every layer.
package domain

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// PrinterState is the coarse, vendor-neutral state of a printer.
// The raw vendor codes stay available in Status.Raw so decoding can be tuned per model.
type PrinterState string

const (
	StateUnknown   PrinterState = "unknown"
	StateIdle      PrinterState = "idle"
	StatePrinting  PrinterState = "printing"
	StateBusy      PrinterState = "busy"
	StateMediaLow  PrinterState = "media_low"
	StateMediaOut  PrinterState = "media_out"
	StateRibbonLow PrinterState = "ribbon_low"
	StateRibbonOut PrinterState = "ribbon_out"
	StateHeadError PrinterState = "head_error"
	StateError     PrinterState = "error"
	StateOffline   PrinterState = "offline"
	StateDisabled  PrinterState = "disabled"
)

// Activity reports whether the printer state represents an active problem that blocks use.
func (s PrinterState) BlocksUse() bool {
	switch s {
	case StateMediaOut, StateRibbonOut, StateHeadError, StateError, StateOffline, StateDisabled:
		return true
	}
	return false
}

// Activity reports whether the printer is currently busy producing output.
func (s PrinterState) Busy() bool {
	return s == StatePrinting || s == StateBusy
}

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
	PrinterID   string `json:"printer_id"`
	Name        string `json:"name"`
	Site        string `json:"site"`
	Model       string `json:"model"`
	Driver      string `json:"driver"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Enabled     bool   `json:"enabled"`
	Remark      string `json:"remark,omitempty"`
	CustomLabel string `json:"custom_label,omitempty"`

	// Installed media/roll specifications
	PaperSize     string  `json:"paper_size,omitempty"`      // standard size tag, e.g. "4x6", "100x150mm", "4x2", "2x1"
	MediaWidthMM  float64 `json:"media_width_mm,omitempty"`  // roll width in mm (e.g. 100, 101.6)
	MediaHeightMM float64 `json:"media_height_mm,omitempty"` // label length/pitch in mm (e.g. 150, 152.4)

	// Print resolution and geometry
	DPI              int     `json:"dpi,omitempty"`               // dots per inch, e.g. 203, 300, 600
	DPMM             float64 `json:"dpmm,omitempty"`              // dots per mm, e.g. 8, 12, 24
	Orientation      string  `json:"orientation,omitempty"`       // "normal" (0°), "inverted" (180°), "portrait", "landscape"
	MediaOrientation string  `json:"media_orientation,omitempty"` // alias for media feed alignment
	PrintDirection   string  `json:"print_direction,omitempty"`   // "top_first", "bottom_first", "normal", "reverse"

	// CJK (Chinese / Japanese / Korean) font specifications
	CJKSupported bool     `json:"cjk_supported,omitempty"` // whether printer has CJK font installed
	CJKFont      string   `json:"cjk_font,omitempty"`      // primary CJK font identifier, e.g. "SIMSUN.FNT", "ANSD.TTF"
	Fonts        []string `json:"fonts,omitempty"`         // installed/available fonts list, e.g. ["0", "SIMSUN.FNT"]

	// Live fields, filled by the printer manager.
	Usable     bool   `json:"usable"`
	QueueDepth int    `json:"queue_depth"`
	Status     Status `json:"status"`

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

// Normalize fills derived defaults for resolution, paper dimensions, orientation, and CJK fonts.
func (p *Printer) Normalize() {
	// 1. Resolution (DPI <-> DPMM)
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
	if p.PrintDirection == "" {
		p.PrintDirection = "top_first"
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

// Package agentapi is the wire contract between cps-agent and the server's agent routes.
//
// Both sides import these types, so a field added or renamed here changes the agent and
// the server together instead of drifting apart in two hand-written payloads. The
// package depends only on pkg/printer: the agent must never need server internals.
package agentapi

import (
	"encoding/json"
	"net/url"
	"time"

	"github.com/kucial/cloud-print-service/pkg/printer"
)

// Routes, relative to the server's base URL.
const (
	PathRegister  = "/api/v1/agents/register"
	PathHeartbeat = "/api/v1/agents/heartbeat"
)

// PrinterStatusPath is the route for pushing one printer's status out of band.
func PrinterStatusPath(agentID, printerID string) string {
	return "/api/v1/agents/" + url.PathEscape(agentID) + "/printers/" + url.PathEscape(printerID) + "/status"
}

// Printer is what an agent reports about one local printer: its declared spec and
// capabilities. Live fields (usable, queue depth, cached status) are the server's to
// compute and are deliberately absent. JSON names match the server's printer model.
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

	// Kind and the formats the printer takes as is; empty means "derive from Driver"
	// (agents before receipt and office printers only served Zebra label printers).
	Kind    printer.Kind     `json:"kind,omitempty"`
	Formats []printer.Format `json:"formats,omitempty"`

	PaperSize     string  `json:"paper_size,omitempty"`
	MediaWidthMM  float64 `json:"media_width_mm,omitempty"`
	MediaHeightMM float64 `json:"media_height_mm,omitempty"`

	DPI               int     `json:"dpi,omitempty"`
	DPMM              float64 `json:"dpmm,omitempty"`
	ResolutionSource  string  `json:"resolution_source,omitempty"`
	DPIResolvedFromHI bool    `json:"dpi_resolved_from_hi"`
	Orientation       string  `json:"orientation,omitempty"`
	MediaOrientation  string  `json:"media_orientation,omitempty"`
	PrintDirection    string  `json:"print_direction,omitempty"`

	CJKSupported       bool     `json:"cjk_supported,omitempty"`
	CJKFont            string   `json:"cjk_font,omitempty"`
	Fonts              []string `json:"fonts,omitempty"`
	FontSource         string   `json:"font_source,omitempty"`
	FontResolvedFromHI bool     `json:"font_resolved_from_hi"`

	// Agents built before this contract existed sent the server's whole printer model,
	// live fields included. The server decodes strictly, so these are accepted and
	// ignored to keep already-deployed agents registering. Never set them.
	LegacyUsable     json.RawMessage `json:"usable,omitempty"`
	LegacyQueueDepth json.RawMessage `json:"queue_depth,omitempty"`
	LegacyStatus     json.RawMessage `json:"status,omitempty"`
	// Server-side facts (the printer's font list) that a full printer model carries.
	LegacyAvailableFonts json.RawMessage `json:"available_fonts,omitempty"`
}

// Status is one printer status observation as the agent saw it.
type Status struct {
	State      printer.State `json:"state"`
	Raw        string        `json:"raw,omitempty"`
	Detail     string        `json:"detail,omitempty"`
	ObservedAt time.Time     `json:"observed_at"`

	// Sent by agents built before this contract; accepted and ignored. Never set it.
	LegacyStale json.RawMessage `json:"stale,omitempty"`
}

// RegisterRequest is sent when an agent starts or re-attaches.
type RegisterRequest struct {
	AgentID   string    `json:"agent_id"`
	Site      string    `json:"site"`
	Hostname  string    `json:"hostname"`
	OS        string    `json:"os"`
	Version   string    `json:"version"`
	AgentAddr string    `json:"agent_addr"` // where the server can reach the agent
	Printers  []Printer `json:"printers"`
}

// RegisterResponse carries a durable credential only on the first registration, when
// the bootstrap token is exchanged and spent.
type RegisterResponse struct {
	Token   string    `json:"token,omitempty"`
	TokenID string    `json:"token_id,omitempty"`
	Expires time.Time `json:"expires,omitzero"`
}

// HeartbeatRequest refreshes liveness and carries status snapshots keyed by printer_id.
type HeartbeatRequest struct {
	AgentID string            `json:"agent_id"`
	Status  map[string]Status `json:"status"`
}

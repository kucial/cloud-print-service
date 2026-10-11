// Package model holds the domain types shared by every layer.
package domain

import "time"

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
	PrinterID string `json:"printer_id"`
	Name      string `json:"name"`
	Site      string `json:"site"`
	Model     string `json:"model"`
	Driver    string `json:"driver"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Enabled     bool   `json:"enabled"`
	Remark      string `json:"remark,omitempty"`
	CustomLabel string `json:"custom_label,omitempty"`

	// Live fields, filled by the printer manager.
	Usable     bool   `json:"usable"`
	QueueDepth int    `json:"queue_depth"`
	Status     Status `json:"status"`

	Mock *MockCfg `json:"mock,omitempty"`
}

// JobState is the lifecycle position of a dispatch job.
type JobState string

const (
	JobQueued      JobState = "queued"
	JobSending     JobState = "sending"
	JobPrinted     JobState = "printed"
	JobSentUnknown JobState = "sent_unknown"
	JobFailed      JobState = "failed"
	JobCancelled   JobState = "cancelled"
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
	ID         string            `json:"job_id"`
	ClientID   string            `json:"client_id"`
	ExternalID string            `json:"external_id"`
	PrinterID  string            `json:"printer_id"`
	State      JobState          `json:"state"`
	Payload    string            `json:"-"` // never serialized by default; see docs/DESIGN.md §12
	Bytes      int               `json:"payload_bytes"`
	PayloadSHA string            `json:"payload_sha256"`
	Copies     int               `json:"copies"`
	Priority   string            `json:"priority"`
	BizRef     string            `json:"biz_ref"`
	Meta       map[string]string `json:"meta,omitempty"`

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

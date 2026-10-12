package main

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// APITrace represents one captured HTTP exchange between demo-web and the Cloud Print Service.
type APITrace struct {
	ID              string            `json:"id"`
	Timestamp       time.Time         `json:"timestamp"`
	Category        string            `json:"category"` // "dispatch" | "fleet" | "preview" | "jobs"
	Title           string            `json:"title"`
	Method          string            `json:"method"`
	URL             string            `json:"url"`
	Path            string            `json:"path"`
	RequestHeaders  map[string]string `json:"request_headers"`
	RequestBody     string            `json:"request_body,omitempty"`
	Status          int               `json:"status"`
	StatusText      string            `json:"status_text"`
	ResponseHeaders map[string]string `json:"response_headers"`
	ResponseBody    string            `json:"response_body,omitempty"`
	LatencyMS       int64             `json:"latency_ms"`
	CurlCommand     string            `json:"curl_command"`
}

type traceRecorder struct {
	mu     sync.RWMutex
	traces []*APITrace
	max    int
	seq    int64
}

func newTraceRecorder(max int) *traceRecorder {
	if max <= 0 {
		max = 60
	}
	return &traceRecorder{max: max, traces: make([]*APITrace, 0, max)}
}

func (r *traceRecorder) Record(t *APITrace) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	t.ID = fmt.Sprintf("trace_%d", r.seq)
	if t.Timestamp.IsZero() {
		t.Timestamp = time.Now().UTC()
	}
	if t.StatusText == "" && t.Status > 0 {
		t.StatusText = fmt.Sprintf("%d %s", t.Status, http.StatusText(t.Status))
	}
	if t.CurlCommand == "" {
		t.CurlCommand = buildCurl(t)
	}

	// Prepend to show newest first
	r.traces = append([]*APITrace{t}, r.traces...)
	if len(r.traces) > r.max {
		r.traces = r.traces[:r.max]
	}
}

func (r *traceRecorder) List() []*APITrace {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]*APITrace(nil), r.traces...)
}

func (r *traceRecorder) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.traces = r.traces[:0]
}

func buildCurl(t *APITrace) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("curl -i -X %s \\\n  %q", t.Method, t.URL))
	for k, v := range t.RequestHeaders {
		b.WriteString(fmt.Sprintf(" \\\n  -H %q", fmt.Sprintf("%s: %s", k, v)))
	}
	if t.RequestBody != "" {
		escaped := strings.ReplaceAll(t.RequestBody, "'", "'\\''")
		b.WriteString(fmt.Sprintf(" \\\n  -d '%s'", escaped))
	}
	return b.String()
}

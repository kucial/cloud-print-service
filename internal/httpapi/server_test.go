package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kucial/cloud-print-service/internal/domain"
	"github.com/kucial/cloud-print-service/internal/fleet"
	"github.com/kucial/cloud-print-service/internal/job"
	"github.com/kucial/cloud-print-service/internal/observ"
	"github.com/kucial/cloud-print-service/internal/render"
	"github.com/kucial/cloud-print-service/internal/store"
	"github.com/kucial/cloud-print-service/internal/token"
)

const (
	erpKey = "test-erp-key-0000000001"
	opsKey = "test-ops-key-0000000001"
	wmsKey = "test-wms-key-0000000001"
)

func newTestServer(t *testing.T, specs ...domain.Printer) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	b := observ.NewBus()
	mgr, err := fleet.NewManager(specs, log, b, fleet.ManagerOpts{PollInterval: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	store := store.NewMemoryStore()
	met := observ.NewMetrics()
	svc := job.NewService(job.Config{QueueSize: 10, MaxAttempts: 2, ConfirmTimeout: 200 * time.Millisecond,
		BaseBackoff: time.Millisecond, MaxPayloadSize: 4096}, store, mgr, b, log, met)
	t.Cleanup(svc.Stop)
	if err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	tok := token.NewService(token.NewMemoryStore(), time.Hour)
	auth := NewAuth(map[string]KeySpec{
		erpKey: {ClientID: "erp", Role: "client"},
		wmsKey: {ClientID: "wms", Role: "client"},
		opsKey: {ClientID: "ops", Role: "admin"},
	}, tok, 0, 0, met)
	s := New(mgr, svc, b, auth, tok, log, met, 50, 4096)
	s.SetWebConfig(WebConfig{Enabled: true})
	s.SetRenderer(render.NewZebrash())
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// httpServer is the subset of httptest.Server the panel tests use.
type httpServer = httptest.Server

// newTestServerWith builds a server with a custom panel configuration.
func newTestServerWith(t *testing.T, web WebConfig, specs ...domain.Printer) *httpServer {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	b := observ.NewBus()
	mgr, err := fleet.NewManager(specs, log, b, fleet.ManagerOpts{PollInterval: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	store := store.NewMemoryStore()
	svc := job.NewService(job.Config{QueueSize: 10, MaxAttempts: 1, ConfirmTimeout: 100 * time.Millisecond,
		MaxPayloadSize: 4096}, store, mgr, b, log, observ.NewMetrics())
	t.Cleanup(svc.Stop)
	tok := token.NewService(token.NewMemoryStore(), time.Hour)
	auth := NewAuth(map[string]KeySpec{
		erpKey: {ClientID: "erp", Role: "client"},
		opsKey: {ClientID: "ops", Role: "admin"},
	}, tok, 0, 0, observ.NewMetrics())
	s := New(mgr, svc, b, auth, tok, log, observ.NewMetrics(), 50, 4096)
	s.SetWebConfig(web)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// newTestServerNoRenderer mimics a build where no render engine is available.
func newTestServerNoRenderer(t *testing.T) *httpServer {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	b := observ.NewBus()
	mgr, err := fleet.NewManager(nil, log, b, fleet.ManagerOpts{PollInterval: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	svc := job.NewService(job.Config{QueueSize: 1, MaxAttempts: 1}, store.NewMemoryStore(), mgr, b, log, observ.NewMetrics())
	t.Cleanup(svc.Stop)
	tok := token.NewService(token.NewMemoryStore(), time.Hour)
	auth := NewAuth(map[string]KeySpec{opsKey: {ClientID: "ops", Role: "admin"}}, tok, 0, 0, observ.NewMetrics())
	s := New(mgr, svc, b, auth, tok, log, observ.NewMetrics(), 50, 4096)
	s.SetWebConfig(WebConfig{Enabled: true})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func mockSpec(id string, cfg *domain.MockCfg) domain.Printer {
	return domain.Printer{PrinterID: id, Name: id, Site: "wh1", Model: "mock", Driver: "mock",
		Host: "127.0.0.1", Port: 9100, Enabled: true, Mock: cfg}
}

func do(t *testing.T, ts *httptest.Server, method, path, key, body string) (*http.Response, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &out)
	return resp, out
}

func TestRequiresAuth(t *testing.T) {
	ts := newTestServer(t, mockSpec("p1", &domain.MockCfg{State: "idle"}))
	resp, _ := do(t, ts, "GET", "/api/v1/printers", "", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	// Ops endpoints stay open (no business data).
	resp, _ = do(t, ts, "GET", "/healthz", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", resp.StatusCode)
	}
}

func TestAdminOnlyRoutes(t *testing.T) {
	ts := newTestServer(t, mockSpec("p1", &domain.MockCfg{State: "idle"}))
	resp, body := do(t, ts, "POST", "/api/v1/printers/p1/test", erpKey, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d (%v), want 403 for a client key", resp.StatusCode, body)
	}
	resp, _ = do(t, ts, "POST", "/api/v1/printers/p1/test", opsKey, "")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("admin test print: status = %d, want 202", resp.StatusCode)
	}
}

func TestBulkStatusEndpoint(t *testing.T) {
	ts := newTestServer(t,
		mockSpec("ok1", &domain.MockCfg{State: "idle"}),
		mockSpec("low1", &domain.MockCfg{State: "media_low"}),
		mockSpec("out1", &domain.MockCfg{State: "media_out"}),
	)
	// Status is normally filled by the background poller; force one pass.
	resp, _ := do(t, ts, "POST", "/api/v1/printers/probe", opsKey, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("probe status = %d", resp.StatusCode)
	}
	resp, body := do(t, ts, "GET", "/api/v1/printers/status", erpKey, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	items := body["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("want 3 printers in one round trip, got %d", len(items))
	}
	if body["usable"].(float64) != 2 {
		t.Fatalf("usable = %v, want 2 (media_low is usable, media_out is not)", body["usable"])
	}
	// Filtered bulk read.
	_, body = do(t, ts, "GET", "/api/v1/printers/status?ids=out1", erpKey, "")
	if len(body["items"].([]any)) != 1 {
		t.Fatalf("ids filter broken: %v", body["items"])
	}
	// usable=false filter is what a WMS dashboard uses to raise an alarm.
	_, body = do(t, ts, "GET", "/api/v1/printers?usable=false", erpKey, "")
	if len(body["items"].([]any)) != 1 {
		t.Fatalf("usable filter broken: %v", body["items"])
	}
}

func TestSubmitAndReplay(t *testing.T) {
	ts := newTestServer(t, mockSpec("p1", &domain.MockCfg{State: "idle"}))
	payload := `{"printer_id":"p1","external_id":"OK-1","zpl":"^XA^FDhello^FS^XZ","biz_ref":"OK-1"}`

	resp, body := do(t, ts, "POST", "/api/v1/jobs", erpKey, payload)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d (%v), want 202", resp.StatusCode, body)
	}
	jobID := body["job_id"].(string)
	if body["replayed"] != false {
		t.Fatalf("first submit must not be a replay: %v", body)
	}

	// A blind ERP retry must not print a second label.
	resp, body = do(t, ts, "POST", "/api/v1/jobs", erpKey, payload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replay status = %d, want 200", resp.StatusCode)
	}
	if body["job_id"] != jobID || body["replayed"] != true {
		t.Fatalf("replay must return the same job_id with replayed=true: %v", body)
	}

	// Same external_id, different payload -> 409.
	_, body = do(t, ts, "POST", "/api/v1/jobs", erpKey,
		`{"printer_id":"p1","external_id":"OK-1","zpl":"^XA^FDother^FS^XZ"}`)
	if body["error"].(map[string]any)["code"] != "idempotency_conflict" {
		t.Fatalf("expected idempotency_conflict, got %v", body)
	}

	// Job reaches printed and is readable.
	deadline := time.Now().Add(3 * time.Second)
	var state string
	for time.Now().Before(deadline) {
		_, b := do(t, ts, "GET", "/api/v1/jobs/"+jobID, erpKey, "")
		state = b["state"].(string)
		if state == "printed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if state != "printed" {
		t.Fatalf("job state = %s, want printed", state)
	}
}

func TestSubmitErrorCodes(t *testing.T) {
	ts := newTestServer(t,
		mockSpec("p1", &domain.MockCfg{State: "idle"}),
		domain.Printer{PrinterID: "disabled1", Name: "off", Site: "wh1", Driver: "mock", Host: "h", Enabled: false, Mock: &domain.MockCfg{State: "idle"}},
	)
	cases := []struct {
		name, body, code string
		status           int
	}{
		{"unknown printer", `{"printer_id":"ghost","external_id":"a","zpl":"^XA^XZ"}`, "printer_not_found", 404},
		{"disabled printer", `{"printer_id":"disabled1","external_id":"b","zpl":"^XA^XZ"}`, "printer_disabled", 409},
		{"no external id", `{"printer_id":"p1","zpl":"^XA^XZ"}`, "invalid_request", 422},
		{"empty payload", `{"printer_id":"p1","external_id":"c","zpl":""}`, "invalid_request", 422},
		{"bad copies", `{"printer_id":"p1","external_id":"d","zpl":"^XA^XZ","copies":99}`, "invalid_request", 422},
		{"bad base64", `{"printer_id":"p1","external_id":"e","payload_base64":"!!!"}`, "invalid_request", 422},
		{"both payloads", `{"printer_id":"p1","external_id":"f","zpl":"^XA^XZ","payload_base64":"WA=="}`, "invalid_request", 422},
		{"unknown field", `{"printer_id":"p1","external_id":"g","zpl":"^XA^XZ","oops":1}`, "invalid_request", 422},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := do(t, ts, "POST", "/api/v1/jobs", erpKey, tc.body)
			if resp.StatusCode != tc.status {
				t.Fatalf("status = %d (%v), want %d", resp.StatusCode, body, tc.status)
			}
			got := body["error"].(map[string]any)["code"]
			if got != tc.code {
				t.Fatalf("code = %v, want %v", got, tc.code)
			}
		})
	}
}

func TestPayloadTooLarge(t *testing.T) {
	ts := newTestServer(t, mockSpec("p1", &domain.MockCfg{State: "idle"}))
	big := strings.Repeat("x", 5000)
	_, body := do(t, ts, "POST", "/api/v1/jobs", erpKey,
		`{"printer_id":"p1","external_id":"big","zpl":"`+big+`"}`)
	if body["error"].(map[string]any)["code"] != "invalid_request" {
		t.Fatalf("expected invalid_request for oversized payload, got %v", body)
	}
}

func TestRawDispatch(t *testing.T) {
	ts := newTestServer(t, mockSpec("p1", &domain.MockCfg{State: "idle"}))
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/jobs/raw?printer_id=p1&external_id=raw-1", bytes.NewReader([]byte("^XA^FDraw^FS^XZ")))
	req.Header.Set("Authorization", "Bearer "+erpKey)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, raw)
	}
}

func TestBatchDispatchIsPerItem(t *testing.T) {
	ts := newTestServer(t, mockSpec("p1", &domain.MockCfg{State: "idle"}))
	body := `{"items":[
		{"printer_id":"p1","external_id":"b-1","zpl":"^XA^FD1^FS^XZ"},
		{"printer_id":"ghost","external_id":"b-2","zpl":"^XA^FD2^FS^XZ"},
		{"printer_id":"p1","external_id":"b-3","zpl":"^XA^FD3^FS^XZ"}]}`
	resp, out := do(t, ts, "POST", "/api/v1/jobs/batch", erpKey, body)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d (%v)", resp.StatusCode, out)
	}
	if out["accepted"].(float64) != 2 || out["rejected"].(float64) != 1 {
		t.Fatalf("accepted/rejected wrong: %v", out)
	}
	items := out["items"].([]any)
	if items[1].(map[string]any)["accepted"] != false {
		t.Fatalf("item 1 should be rejected: %v", items[1])
	}
}

func TestTenantIsolationOverHTTP(t *testing.T) {
	ts := newTestServer(t, mockSpec("p1", &domain.MockCfg{State: "idle"}))
	_, body := do(t, ts, "POST", "/api/v1/jobs", erpKey,
		`{"printer_id":"p1","external_id":"iso","zpl":"^XA^FDx^FS^XZ"}`)
	id := body["job_id"].(string)

	resp, _ := do(t, ts, "GET", "/api/v1/jobs/"+id, wmsKey, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-tenant GET status = %d, want 404 (never leak existence)", resp.StatusCode)
	}
	resp, _ = do(t, ts, "POST", "/api/v1/jobs/"+id+"/cancel", wmsKey, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-tenant cancel status = %d, want 404", resp.StatusCode)
	}
	// And the job list is scoped too.
	_, list := do(t, ts, "GET", "/api/v1/jobs", wmsKey, "")
	if list["count"].(float64) != 0 {
		t.Fatalf("wms must not see erp jobs: %v", list)
	}
}

func TestSSEEvents(t *testing.T) {
	ts := newTestServer(t, mockSpec("p1", &domain.MockCfg{State: "idle"}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/v1/events?topics=job.state&printer_ids=p1", nil)
	req.Header.Set("Authorization", "Bearer "+erpKey)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		do(t, ts, "POST", "/api/v1/jobs", erpKey, `{"printer_id":"p1","external_id":"sse-1","zpl":"^XA^FDs^FS^XZ"}`)
	}()

	sc := bufio.NewScanner(resp.Body)
	sawState := false
	for sc.Scan() && time.Now().Before(ctxDeadline(ctx)) {
		line := sc.Text()
		if strings.HasPrefix(line, "event: job.state") {
			sawState = true
			break
		}
	}
	if !sawState {
		t.Fatal("did not receive a job.state SSE event")
	}
}

func ctxDeadline(ctx context.Context) time.Time {
	if d, ok := ctx.Deadline(); ok {
		return d
	}
	return time.Now().Add(time.Second)
}

func TestMetricsEndpoint(t *testing.T) {
	ts := newTestServer(t, mockSpec("p1", &domain.MockCfg{State: "idle"}))
	do(t, ts, "POST", "/api/v1/jobs", erpKey, `{"printer_id":"p1","external_id":"m-1","zpl":"^XA^FDx^FS^XZ"}`)
	resp, err := ts.Client().Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"cps_jobs_total", "cps_queue_depth", "cps_printer_usable"} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Fatalf("metric %s missing from:\n%s", want, raw)
		}
	}
}

func TestReadyzReflectsPrinterCount(t *testing.T) {
	ts := newTestServer(t)
	resp, _ := do(t, ts, "GET", "/readyz", "", "")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when no printers are configured", resp.StatusCode)
	}
}

func TestSubmitJobWithPageCopies(t *testing.T) {
	ts := newTestServer(t, mockSpec("p1", &domain.MockCfg{State: "idle"}))

	// Submit a 2-page job with page_copies: [4, 1]
	body := `{
		"printer_id": "p1",
		"external_id": "api-tail-01",
		"zpl": "^XA^FDBox 12^FS^XZ^XA^FDTail 2^FS^XZ",
		"page_copies": [4, 1]
	}`
	resp, reply := do(t, ts, "POST", "/api/v1/jobs", erpKey, body)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %+v", resp.StatusCode, reply)
	}
	jobID, ok := reply["job_id"].(string)
	if !ok || jobID == "" {
		t.Fatalf("missing job_id: %+v", reply)
	}

	// Fetch job details: verify copies == 5 and page_copies == [4, 1]
	getResp, getReply := do(t, ts, "GET", "/api/v1/jobs/"+jobID, erpKey, "")
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("get job failed: %d, %+v", getResp.StatusCode, getReply)
	}
	if copies, _ := getReply["copies"].(float64); copies != 5 {
		t.Fatalf("expected job copies == 5, got %v", copies)
	}
	pc, ok := getReply["page_copies"].([]any)
	if !ok || len(pc) != 2 || pc[0].(float64) != 4 || pc[1].(float64) != 1 {
		t.Fatalf("unexpected page_copies: %+v", getReply["page_copies"])
	}
}


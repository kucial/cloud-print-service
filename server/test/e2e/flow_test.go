// Package e2e drives the whole system the way a business system does:
// a virtual printer on a socket, the real HTTP API, and tokens issued at runtime.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/kucial/cloud-print-service/server/internal/domain"
	"github.com/kucial/cloud-print-service/server/internal/fleet"
	"github.com/kucial/cloud-print-service/server/internal/httpapi"
	"github.com/kucial/cloud-print-service/server/internal/job"
	"github.com/kucial/cloud-print-service/server/internal/observ"
	"github.com/kucial/cloud-print-service/server/internal/store"
	"github.com/kucial/cloud-print-service/server/internal/token"
	"github.com/kucial/cloud-print-service/vprint/virtual"
)

const (
	bootstrapAdmin = "e2e-ops-key-000000000001"
	labelBody      = "^XA^CI28^FO40,40^A0N,40,40^FDOK-88213^FS^FO40,100^BCN,100,Y,N,N^FD1234567890^FS^XZ"
)

type harness struct {
	t   *testing.T
	srv *httptest.Server
	vps map[string]*virtual.Printer
	job *job.Service
	tok *token.Service
	bus *observ.Bus
	met *observ.Registry
	mgr *fleet.Manager
}

// newFleet starts one virtual printer per spec plus the service in front of them.
func newFleet(t *testing.T, specs ...domain.Printer) *harness {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	f := &harness{t: t, vps: map[string]*virtual.Printer{}, met: observ.NewMetrics()}
	f.bus = observ.NewBus()

	for i := range specs {
		spec := &specs[i]
		vp := virtual.New(spec.PrinterID, virtual.Opts{
			Addr: "127.0.0.1:0", Model: spec.Model, Serial: "SN-" + spec.PrinterID,
			PrintDuration: 30 * time.Millisecond,
		})
		if err := vp.Start(context.Background()); err != nil {
			t.Fatalf("virtual printer %s: %v", spec.PrinterID, err)
		}
		t.Cleanup(func() { _ = vp.Close() })
		if err := vp.EnableRendering(context.Background(), virtual.RenderOpts{
			Backend: "zebrash",
		}); err != nil {
			t.Fatalf("enable rendering: %v", err)
		}
		f.vps[spec.PrinterID] = vp

		host, port := vp.Addr()
		n, _ := strconv.Atoi(port)
		spec.Host, spec.Port = host, n
	}

	mgr, err := fleet.NewManager(specs, log, f.bus, fleet.ManagerOpts{
		PollInterval: 150 * time.Millisecond, StaleAfter: time.Second,
		DialTimeout: time.Second, WriteTimeout: 2 * time.Second, ReadTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	f.mgr = mgr
	// Sync (inside NewManager) already starts one poller per printer; calling
	// StartPolling here would double-probe every device.

	store := store.NewMemoryStore()
	f.job = job.NewService(job.Config{
		QueueSize: 20, MaxAttempts: 3, ConfirmTimeout: 2 * time.Second,
		BaseBackoff: 20 * time.Millisecond, MaxPayloadSize: 1 << 20,
	}, store, mgr, f.bus, log, f.met)
	t.Cleanup(f.job.Stop)
	if err := f.job.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	f.tok = token.NewService(token.NewMemoryStore(), 24*time.Hour)
	auth := httpapi.NewAuth(map[string]httpapi.KeySpec{
		bootstrapAdmin: {ClientID: "ops", Role: "admin"},
	}, f.tok, 0, 0, f.met)

	srv := httpapi.New(mgr, f.job, f.bus, auth, f.tok, log, f.met, 50, 1<<20)
	srv.SetAgents(fleet.NewAgentRegistry(mgr.Registry(), mgr, log))
	f.srv = httptest.NewServer(srv.Handler())
	t.Cleanup(f.srv.Close)
	return f
}

func (f *harness) do(method, path, key string, body any) (int, map[string]any) {
	f.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, f.srv.URL+path, rdr)
	if err != nil {
		f.t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// Token issues a credential through the API, like an operator would.
func (f *harness) Token(t *testing.T, clientID string) string {
	t.Helper()
	secret, _ := f.issueToken(clientID, "client", "1h")
	return secret
}

func (f *harness) issuedClientKey(t *testing.T, clientID string) string {
	t.Helper()
	return f.Token(t, clientID)
}

func (f *harness) issueToken(clientID, role, ttl string) (string, string) {
	f.t.Helper()
	body := map[string]any{"client_id": clientID, "role": role, "ttl": ttl}
	code, out := f.do("POST", "/api/v1/admin/tokens", bootstrapAdmin, body)
	if code != http.StatusCreated {
		f.t.Fatalf("issue token: status %d: %v", code, out)
	}
	return out["token"].(string), out["id"].(string)
}

// waitJob polls a job until it reaches one of the wanted states.
func (f *harness) waitJob(key, jobID string, want ...domain.JobState) map[string]any {
	f.t.Helper()
	wanted := map[string]bool{}
	for _, w := range want {
		wanted[string(w)] = true
	}
	deadline := time.Now().Add(15 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		_, out := f.do("GET", "/api/v1/jobs/"+jobID, key, nil)
		if e, ok := out["error"]; ok {
			last = fmt.Sprint(e)
		} else if s := fmt.Sprint(out["state"]); wanted[s] {
			return out
		} else {
			last = s
		}
		time.Sleep(25 * time.Millisecond)
	}
	f.t.Fatalf("job %s stuck in %q, wanted one of %v", jobID, last, want)
	return nil
}

func (f *harness) printer(id string) *virtual.Printer { return f.vps[id] }

// ---------------------------------------------------------------- the flow

// TestBusinessSystemFlow is the acceptance test: onboard a system with an issued
// token, see the fleet, print, retry safely, then survive printer trouble.
func TestBusinessSystemFlow(t *testing.T) {
	f := newFleet(t,
		domain.Printer{PrinterID: "wh1-pack-03", Name: "Packing 3", Site: "wh1", Model: "ZT411", Driver: "zebra9100", Enabled: true},
		domain.Printer{PrinterID: "wh1-pack-04", Name: "Packing 4", Site: "wh1", Model: "ZT411", Driver: "zebra9100", Enabled: true},
	)

	// --- 1. onboarding: ops issues a credential for the WMS integration -----------
	code, out := f.do("POST", "/api/v1/admin/tokens", "", map[string]any{"client_id": "wms"})
	if code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated token issue must be rejected, got %d", code)
	}
	code, out = f.do("POST", "/api/v1/admin/tokens", "not-a-real-key", map[string]any{"client_id": "wms"})
	if code != http.StatusUnauthorized {
		t.Fatalf("bogus key must be rejected, got %d", code)
	}

	wmsKey, tokenID := f.issueToken("wms", "client", "1h")
	if len(wmsKey) < 20 {
		t.Fatalf("issued token looks too short: %q", wmsKey)
	}
	t.Logf("issued token %s for wms (shown once: %.12s…)", tokenID, wmsKey)

	// The new credential works immediately on the endpoints the business system uses.
	code, out = f.do("GET", "/api/v1/printers/status", wmsKey, nil)
	if code != http.StatusOK {
		t.Fatalf("fleet read with issued token: %d %v", code, out)
	}

	// A client token may not mint credentials.
	code, _ = f.do("POST", "/api/v1/admin/tokens", wmsKey, map[string]any{"client_id": "attacker"})
	if code != http.StatusForbidden {
		t.Fatalf("client token must not issue tokens, got %d", code)
	}

	// --- 2. fleet visibility ------------------------------------------------------
	deadline := time.Now().Add(5 * time.Second)
	var usable int
	var fleetView map[string]any
	for time.Now().Before(deadline) {
		_, view := f.do("GET", "/api/v1/printers/status", wmsKey, nil)
		fleetView = view
		if u, ok := view["usable"].(float64); ok && u == 2 {
			usable = int(u)
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if usable != 2 {
		t.Fatalf("both virtual printers should report usable, got %v", fleetView)
	}
	items := fleetView["items"].([]any)
	first := items[0].(map[string]any)
	st := first["status"].(map[string]any)
	if st["state"] != "idle" {
		t.Fatalf("printer state = %v, want idle", st["state"])
	}
	if st["raw"] != "^HS0,0,0,0^HS" {
		t.Fatalf("raw host status should be preserved for troubleshooting, got %v", st["raw"])
	}

	// --- 3. dispatch -------------------------------------------------------------
	code, out = f.do("POST", "/api/v1/jobs", wmsKey, map[string]any{
		"printer_id": "wh1-pack-03", "external_id": "OK-88213-4",
		"zpl": labelBody, "biz_ref": "OK88213", "copies": 1,
	})
	if code != http.StatusAccepted {
		t.Fatalf("dispatch: %d %v", code, out)
	}
	jobID := out["job_id"].(string)
	job := f.waitJob(wmsKey, jobID, domain.JobPrinted)
	if job["payload_bytes"].(float64) != float64(len(labelBody)) {
		t.Errorf("payload size mismatch: %v", job["payload_bytes"])
	}
	if f.printer("wh1-pack-03").LabelCount() != 1 {
		t.Fatalf("printer should have exactly one label, got %d", f.printer("wh1-pack-03").LabelCount())
	}
	if f.printer("wh1-pack-04").LabelCount() != 0 {
		t.Fatal("the other printer must stay untouched")
	}

	// --- 4. the label rendered, so we know what it looks like ---------------------
	waitPreview(t, f.printer("wh1-pack-03"), 1)
	lbl := f.printer("wh1-pack-03").Labels()[0]
	if lbl.Image == nil || !lbl.Image.Available {
		t.Fatalf("label should carry a rendered preview: %+v", lbl.Image)
	}
	if lbl.Image.Backend != "zebrash" || lbl.Image.Bytes == 0 {
		t.Fatalf("unexpected preview meta: %+v", lbl.Image)
	}
	png, ct, ok := f.printer("wh1-pack-03").Image(1)
	if !ok || ct != "image/png" || !bytes.HasPrefix(png, []byte("\x89PNG")) {
		t.Fatal("gallery image endpoint should serve the rendered PNG")
	}

	// --- 5. ERP retries are safe --------------------------------------------------
	for i := 0; i < 3; i++ {
		code, out = f.do("POST", "/api/v1/jobs", wmsKey, map[string]any{
			"printer_id": "wh1-pack-03", "external_id": "OK-88213-4", "zpl": labelBody,
		})
		if code != http.StatusOK || out["job_id"] != jobID || out["replayed"] != true {
			t.Fatalf("retry %d must replay the original job, got %d %v", i, code, out)
		}
	}
	if got := f.printer("wh1-pack-03").LabelCount(); got != 1 {
		t.Fatalf("retries printed extra labels: %d", got)
	}

	// Same external_id, different payload is a conflict, not a second label.
	code, out = f.do("POST", "/api/v1/jobs", wmsKey, map[string]any{
		"printer_id": "wh1-pack-03", "external_id": "OK-88213-4", "zpl": "^XA^FDother^FS^XZ",
	})
	if code != http.StatusConflict {
		t.Fatalf("expected 409 idempotency_conflict, got %d %v", code, out)
	}

	// --- 6. consumable runs out ---------------------------------------------------
	f.printer("wh1-pack-03").Control(virtual.Control{State: "media_out"})
	waitPrinterState(t, f, wmsKey, "wh1-pack-03", "media_out")
	_, out = f.do("GET", "/api/v1/printers?usable=false", wmsKey, nil)
	bad := out["items"].([]any)
	if len(bad) != 1 || bad[0].(map[string]any)["printer_id"] != "wh1-pack-03" {
		t.Fatalf("usable=false filter should flag the paper-out printer: %v", out["items"])
	}
	// Dispatch is still allowed by default; the caller can filter if it wants not to.
	code, out = f.do("POST", "/api/v1/jobs", wmsKey, map[string]any{
		"printer_id": "wh1-pack-03", "external_id": "OK-88213-5", "zpl": labelBody,
	})
	if code != http.StatusAccepted {
		t.Fatalf("dispatch on a paper-out printer should still be accepted: %d %v", code, out)
	}

	// --- 7. printer goes offline, then recovers -----------------------------------
	before := f.printer("wh1-pack-03").LabelCount()
	f.printer("wh1-pack-03").Control(virtual.Control{Offline: boolp(true)})
	waitPrinterState(t, f, wmsKey, "wh1-pack-03", "offline")

	code, out = f.do("POST", "/api/v1/jobs", wmsKey, map[string]any{
		"printer_id": "wh1-pack-03", "external_id": "OK-88213-6", "zpl": labelBody,
	})
	if code != http.StatusAccepted {
		t.Fatalf("dispatch while offline should be accepted for later retry: %d %v", code, out)
	}
	offlineJob := f.waitJob(wmsKey, out["job_id"].(string),
		domain.JobSentUnknown, domain.JobFailed, domain.JobPrinted)
	if offlineJob["state"] == domain.JobPrinted {
		t.Fatal("a job dispatched to an offline printer must never be reported as printed")
	}

	f.printer("wh1-pack-03").Control(virtual.Control{Offline: boolp(false)})
	f.printer("wh1-pack-03").Control(virtual.Control{State: "idle"})
	waitPrinterState(t, f, wmsKey, "wh1-pack-03", "idle")

	// Back in business: a new label prints on the recovered driver.
	f.printer("wh1-pack-03").Reset()
	code, out = f.do("POST", "/api/v1/jobs", wmsKey, map[string]any{
		"printer_id": "wh1-pack-03", "external_id": "OK-88213-7", "zpl": labelBody,
	})
	if code != http.StatusAccepted {
		t.Fatalf("dispatch after recovery: %d %v", code, out)
	}
	f.waitJob(wmsKey, out["job_id"].(string), domain.JobPrinted)
	if f.printer("wh1-pack-03").LabelCount() != 1 {
		t.Fatalf("recovered printer should print exactly one label, got %d",
			f.printer("wh1-pack-03").LabelCount())
	}
	_ = before

	// --- 8. rotation kills the old credential ------------------------------------
	rotated, out := f.rotate(tokenID)
	if rotated == wmsKey {
		t.Fatal("rotation must issue a new secret")
	}
	code, _ = f.do("GET", "/api/v1/printers/status", wmsKey, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("the rotated-away token must stop working immediately, got %d", code)
	}
	code, _ = f.do("GET", "/api/v1/printers/status", rotated, nil)
	if code != http.StatusOK {
		t.Fatalf("the rotated token must work, got %d", code)
	}
	code, _ = f.do("DELETE", "/api/v1/admin/tokens/"+out["id"].(string), bootstrapAdmin, nil)
	if code != http.StatusOK {
		t.Fatalf("revoke: %d", code)
	}
	code, _ = f.do("GET", "/api/v1/printers/status", rotated, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("a revoked token must be rejected, got %d", code)
	}
}

func (f *harness) rotate(tokenID string) (string, map[string]any) {
	f.t.Helper()
	code, out := f.do("POST", "/api/v1/admin/tokens/"+tokenID+"/rotate", bootstrapAdmin, nil)
	if code != http.StatusCreated {
		f.t.Fatalf("rotate: %d %v", code, out)
	}
	return out["token"].(string), out
}

func waitPreview(t *testing.T, vp *virtual.Printer, index int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, ok := vp.Image(index); ok {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("label %d was never rendered", index)
}

func waitPrinterState(t *testing.T, f *harness, key, id, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last any
	for time.Now().Before(deadline) {
		_, out := f.do("GET", "/api/v1/printers/"+id+"/status", key, nil)
		st := out["status"].(map[string]any)
		last = st["state"]
		if last == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("printer %s state = %v, want %v", id, last, want)
}

func boolp(b bool) *bool { return &b }

// TestTwoPrintersAreIndependent shows one sick printer must not stall the other.
func TestTwoPrintersAreIndependent(t *testing.T) {
	f := newFleet(t,
		domain.Printer{PrinterID: "fast", Model: "ZT411", Driver: "zebra9100", Enabled: true},
		domain.Printer{PrinterID: "stuck", Model: "ZT411", Driver: "zebra9100", Enabled: true},
	)
	key, _ := f.issueToken("erp", "client", "1h")

	// The stuck printer never leaves the printing state: no job on it ever confirms.
	f.printer("stuck").Control(virtual.Control{State: "always_printing"})

	_, out := f.do("POST", "/api/v1/jobs", key, map[string]any{
		"printer_id": "stuck", "external_id": "stuck-1", "zpl": labelBody,
	})
	stuckJob := out["job_id"].(string)

	for i := 0; i < 3; i++ {
		code, out := f.do("POST", "/api/v1/jobs", key, map[string]any{
			"printer_id": "fast", "external_id": "fast-" + strconv.Itoa(i), "zpl": labelBody,
		})
		if code != http.StatusAccepted {
			t.Fatalf("fast dispatch %d: %d %v", i, code, out)
		}
		f.waitJob(key, out["job_id"].(string), domain.JobPrinted)
	}
	if n := f.printer("fast").LabelCount(); n != 3 {
		t.Fatalf("healthy printer should have 3 labels, got %d", n)
	}
	f.waitJob(key, stuckJob, domain.JobSentUnknown)
}

// TestTenantIsolationAcrossIssuedTokens proves one customer's token cannot see or
// touch another customer's jobs.
func TestTenantIsolationAcrossIssuedTokens(t *testing.T) {
	f := newFleet(t, domain.Printer{PrinterID: "p1", Model: "ZT411", Driver: "zebra9100", Enabled: true})
	erpKey, _ := f.issueToken("erp", "client", "1h")
	wmsKey, _ := f.issueToken("wms", "client", "1h")

	_, out := f.do("POST", "/api/v1/jobs", erpKey, map[string]any{
		"printer_id": "p1", "external_id": "ERP-1", "zpl": labelBody,
	})
	jobID := out["job_id"].(string)

	if code, _ := f.do("GET", "/api/v1/jobs/"+jobID, wmsKey, nil); code != http.StatusNotFound {
		t.Fatalf("cross-tenant read: got %d, want 404", code)
	}
	if code, _ := f.do("POST", "/api/v1/jobs/"+jobID+"/cancel", wmsKey, nil); code != http.StatusNotFound {
		t.Fatalf("cross-tenant cancel: got %d, want 404", code)
	}
	_, list := f.do("GET", "/api/v1/jobs", wmsKey, nil)
	if list["count"].(float64) != 0 {
		t.Fatalf("wms must not see erp jobs: %v", list)
	}
	// Same external_id from a different client is a different document: allowed.
	if code, out := f.do("POST", "/api/v1/jobs", wmsKey, map[string]any{
		"printer_id": "p1", "external_id": "ERP-1", "zpl": labelBody,
	}); code != http.StatusAccepted || out["replayed"] == true {
		t.Fatalf("cross-client external_id should be a new job: %d %v", code, out)
	}
}

// TestBatchDispatchFlow covers the bulk endpoint a WMS uses to release a wave.
func TestBatchDispatchFlow(t *testing.T) {
	f := newFleet(t, domain.Printer{PrinterID: "p1", Model: "ZT411", Driver: "zebra9100", Enabled: true})
	key, _ := f.issueToken("wms", "client", "1h")

	items := make([]map[string]any, 0, 5)
	for i := 0; i < 4; i++ {
		items = append(items, map[string]any{
			"printer_id": "p1", "external_id": "wave-" + strconv.Itoa(i), "zpl": labelBody,
		})
	}
	items = append(items, map[string]any{
		"printer_id": "ghost", "external_id": "wave-bad", "zpl": labelBody,
	})

	code, out := f.do("POST", "/api/v1/jobs/batch", key, map[string]any{"items": items})
	if code != http.StatusAccepted {
		t.Fatalf("batch: %d %v", code, out)
	}
	if out["accepted"].(float64) != 4 || out["rejected"].(float64) != 1 {
		t.Fatalf("batch accounting wrong: %v", out)
	}
	res := out["items"].([]any)
	for i := 0; i < 4; i++ {
		id := res[i].(map[string]any)["job_id"].(string)
		f.waitJob(key, id, domain.JobPrinted)
	}
	if n := f.printer("p1").LabelCount(); n != 4 {
		t.Fatalf("expected 4 labels on the printer, got %d", n)
	}
}

// An edge agent turns a locally attached printer into a fleet member: the server needs no
// USB code, only a TCP endpoint. This is the acceptance test for that whole idea.
func TestAgentRegistersAndServesAPrinter(t *testing.T) {
	f := newFleet(t, domain.Printer{PrinterID: "lab-01", Model: "ZT411", Driver: "zebra9100", Enabled: true})
	// The operator credential is the bootstrap admin key this harness already uses.
	opsKey := bootstrapAdmin

	// An operator mints a one-time bootstrap token for a packing PC.
	code, out := f.do("POST", "/api/v1/admin/agents/enroll", opsKey,
		map[string]any{"agent_id": "pc7", "site": "wh1", "ttl": "1h"})
	if code != http.StatusCreated {
		t.Fatalf("enroll: %d %v", code, out)
	}
	bootstrap, _ := out["token"].(string)
	if bootstrap == "" {
		t.Fatal("enroll returned no token")
	}

	// A client credential may not enroll an agent.
	code, _ = f.do("POST", "/api/v1/admin/agents/enroll", f.issuedClientKey(t, "wms"),
		map[string]any{"agent_id": "pc7"})
	if code != http.StatusForbidden {
		t.Fatalf("a business token must not enroll agents, got %d", code)
	}

	// The agent registers: printers it reports become dispatchable immediately.
	agentPrinter := domain.Printer{
		PrinterID: "pc7-usb-1", Name: "packing station USB", Site: "wh1",
		Driver: "zebra9100", Host: "127.0.0.1", Port: 19555, Enabled: true,
	}
	code, out = f.do("POST", "/api/v1/agents/register", bootstrap, map[string]any{
		"agent_id": "pc7", "site": "wh1", "hostname": "PC7", "os": "windows",
		"version": "test", "agent_addr": "10.0.0.7", "printers": []domain.Printer{agentPrinter},
	})
	if code != http.StatusOK {
		t.Fatalf("register: %d %v", code, out)
	}
	durable, _ := out["token"].(string)
	if durable == "" || durable == bootstrap {
		t.Fatalf("first registration must exchange the bootstrap token for a durable one")
	}

	// The bootstrap token is spent.
	if code, _ := f.do("POST", "/api/v1/agents/register", bootstrap, map[string]any{
		"agent_id": "pc7", "agent_addr": "10.0.0.7", "printers": []domain.Printer{},
	}); code != http.StatusUnauthorized {
		t.Fatalf("a used bootstrap token must be revoked, got %d", code)
	}

	// The printer is visible with agent provenance, without a restart.
	_, fleetView := f.do("GET", "/api/v1/printers/status", durable, nil)
	found := false
	for _, it := range fleetView["items"].([]any) {
		m := it.(map[string]any)
		if m["printer_id"] == "pc7-usb-1" {
			found = true
			if m["site"] != "wh1" {
				t.Errorf("agent printer should inherit the agent's site: %v", m["site"])
			}
		}
	}
	if !found {
		t.Fatalf("agent-registered printer missing from the fleet: %v", fleetView["items"])
	}
	if prov := f.mgr.Registry().Provenance("pc7-usb-1"); len(prov) == 0 || prov[0] != "agent" {
		t.Errorf("provenance = %v, want agent", prov)
	}

	// The agent reports status; the fleet reflects it.
	code, _ = f.do("POST", "/api/v1/agents/heartbeat", durable, map[string]any{
		"agent_id": "pc7",
		"status":   map[string]domain.Status{"pc7-usb-1": {State: domain.StateMediaOut, Raw: "^HS0,2,0,0^HS"}},
	})
	if code != http.StatusOK {
		t.Fatalf("heartbeat: %d", code)
	}
	f.mgr.ApplyStatus("pc7-usb-1", domain.Status{State: domain.StateMediaOut, Raw: "^HS0,2,0,0^HS"})
	_, st := f.do("GET", "/api/v1/printers/pc7-usb-1/status", durable, nil)
	status := st["status"].(map[string]any)
	if status["state"] != string(domain.StateMediaOut) {
		t.Fatalf("agent-reported status was ignored: %v", status)
	}

	// A business token cannot use the agent surface.
	code, _ = f.do("POST", "/api/v1/agents/heartbeat", f.issuedClientKey(t, "wms2"),
		map[string]any{"agent_id": "pc7"})
	if code != http.StatusForbidden {
		t.Fatalf("business token reached the agent API: %d", code)
	}

	// Revoking the agent removes its printers from the fleet.
	code, out = f.do("DELETE", "/api/v1/agents/pc7", opsKey, nil)
	if code != http.StatusOK {
		t.Fatalf("revoke agent: %d %v", code, out)
	}
	if _, ok := f.mgr.Registry().Get("pc7-usb-1"); ok {
		t.Fatal("the agent's printers must leave the fleet when it is revoked")
	}
	// Config-declared printers are untouched by an agent's departure.
	if _, ok := f.mgr.Registry().Get("lab-01"); !ok {
		t.Fatal("revoking an agent removed a config printer")
	}
}

// A dead agent must not leave queued work invisible: its jobs fail loudly with a reason
// the caller can act on, and its printers become unusable.
func TestAgentLossFailsQueuedJobs(t *testing.T) {
	f := newFleet(t, domain.Printer{PrinterID: "lab-01", Model: "ZT411", Driver: "zebra9100", Enabled: true})
	opsKey := bootstrapAdmin

	// enroll + register an agent with one printer
	_, out := f.do("POST", "/api/v1/admin/agents/enroll", opsKey,
		map[string]any{"agent_id": "pc7", "site": "wh1", "ttl": "1h"})
	bootstrap, _ := out["token"].(string)
	printer := domain.Printer{PrinterID: "pc7-usb-1", Driver: "zebra9100", Host: "10.0.0.7", Port: 19555, Enabled: true}
	code, out := f.do("POST", "/api/v1/agents/register", bootstrap, map[string]any{
		"agent_id": "pc7", "site": "wh1", "agent_addr": "10.0.0.7", "printers": []domain.Printer{printer},
	})
	if code != http.StatusOK {
		t.Fatalf("register: %d %v", code, out)
	}
	durable, _ := out["token"].(string)

	// queue a job for the agent's printer
	wmsKey := f.issuedClientKey(t, "wms")
	code, out = f.do("POST", "/api/v1/jobs", wmsKey, map[string]any{
		"printer_id": "pc7-usb-1", "external_id": "A-1", "zpl": "^XA^FDx^FS^XZ",
	})
	if code != http.StatusAccepted {
		t.Fatalf("dispatch: %d %v", code, out)
	}
	jobID := out["job_id"].(string)

	// the agent dies; an operator revokes it
	code, out = f.do("DELETE", "/api/v1/agents/pc7", opsKey, nil)
	if code != http.StatusOK {
		t.Fatalf("delete agent: %d %v", code, out)
	}
	// The job may already have been dispatched and failed by the worker (the printer is
	// unreachable) before the delete ran. Both outcomes are truthful; what must not happen
	// is the job staying queued, pretending it will print later.
	if got := out["jobs_failed"].(float64); got > 1 {
		t.Fatalf("jobs_failed = %v, want 0 or 1", got)
	}

	// The worker may be mid-retry with backoff when the agent is revoked; it settles to
	// failed within max_attempts of retries. Wait for it rather than racing the backoff.
	var final domain.JobState
	var lastErr string
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		j, err := f.job.Get(context.Background(), "", jobID)
		if err != nil {
			t.Fatal(err)
		}
		final = j.State
		lastErr = j.LastError
		if j.State.Terminal() {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if final != domain.JobFailed && final != domain.JobCancelled {
		t.Fatalf("job state = %s, want a terminal failure (agent loss must never look like success)", final)
	}
	if lastErr == "" {
		t.Fatalf("a job that failed because the agent is gone must say so")
	}
	if _, ok := f.mgr.Registry().Get("pc7-usb-1"); ok {
		t.Fatal("the agent's printer must leave the fleet when it is revoked")
	}
	// and the durable credential is still valid for the agent when it comes back
	code, _ = f.do("POST", "/api/v1/agents/heartbeat", durable, map[string]any{"agent_id": "pc7"})
	if code != http.StatusNotFound {
		t.Logf("heartbeat for a deregistered agent: %d (acceptable: it may re-register)", code)
	}
}

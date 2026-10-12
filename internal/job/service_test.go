package job

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kucial/cloud-print-service/internal/domain"
	"github.com/kucial/cloud-print-service/internal/fleet"
	"github.com/kucial/cloud-print-service/internal/observ"
	"github.com/kucial/cloud-print-service/internal/store"
)

type harness struct {
	svc   *Service
	mgr   *fleet.Manager
	bus   *observ.Bus
	st    *store.MemoryStore
	specs []domain.Printer
}

func newHarness(t *testing.T, specs ...domain.Printer) *harness {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	b := observ.NewBus()
	mgr, err := fleet.NewManager(specs, log, b, fleet.ManagerOpts{PollInterval: time.Minute})
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	t.Cleanup(mgr.Close)
	st := store.NewMemoryStore()
	svc := NewService(Config{
		QueueSize: 5, MaxAttempts: 3, ConfirmTimeout: 300 * time.Millisecond,
		BaseBackoff: time.Millisecond, QueueTTL: time.Minute,
	}, st, mgr, b, log, observ.NewMetrics())
	t.Cleanup(svc.Stop)
	ctx := context.Background()
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	return &harness{svc: svc, mgr: mgr, bus: b, st: st, specs: specs}
}

func mockPrinter(id string, cfg *domain.MockCfg) domain.Printer {
	return domain.Printer{PrinterID: id, Name: id, Site: "t", Model: "mock", Driver: "mock",
		Host: "127.0.0.1", Port: 9100, Enabled: true, Mock: cfg}
}

func waitState(t *testing.T, svc *Service, id string, want domain.JobState) *domain.Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, err := svc.Get(context.Background(), "", id)
		if err == nil && j.State == want {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	j, _ := svc.Get(context.Background(), "", id)
	got := domain.JobState("<missing>")
	if j != nil {
		got = j.State
	}
	t.Fatalf("job %s state = %s, want %s", id, got, want)
	return nil
}

func TestSubmitDispatchesAndConfirms(t *testing.T) {
	h := newHarness(t, mockPrinter("p1", &domain.MockCfg{State: "idle"}))
	j, replayed, err := h.svc.Submit(context.Background(), SubmitRequest{
		ClientID: "erp", PrinterID: "p1", ExternalID: "ok-1", ZPL: "^XA^FDhi^FS^XZ",
	})
	if err != nil || replayed {
		t.Fatalf("submit: err=%v replayed=%v", err, replayed)
	}
	got := waitState(t, h.svc, j.ID, domain.JobPrinted)
	if got.Bytes == 0 || got.PayloadSHA == "" {
		t.Fatalf("payload metadata missing: %+v", got)
	}
	if got.SentAt == nil || got.FinishedAt == nil {
		t.Fatal("timestamps not set")
	}
}

func TestIdempotencyPreventsDuplicateLabels(t *testing.T) {
	h := newHarness(t, mockPrinter("p1", &domain.MockCfg{State: "idle"}))
	ctx := context.Background()
	req := SubmitRequest{ClientID: "erp", PrinterID: "p1", ExternalID: "ok-42", ZPL: "^XA^FDa^FS^XZ"}

	first, replayed, err := h.svc.Submit(ctx, req)
	if err != nil || replayed {
		t.Fatalf("first: err=%v replayed=%v", err, replayed)
	}
	second, replayed, err := h.svc.Submit(ctx, req)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replayed || second.ID != first.ID {
		t.Fatalf("replay must return the same job: %s vs %s (replayed=%v)", first.ID, second.ID, replayed)
	}

	// Same external_id, different payload -> conflict, never a second label.
	req.ZPL = "^XA^FDBIFFERENT^FS^XZ"
	if _, _, err := h.svc.Submit(ctx, req); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expected store.ErrConflict, got %v", err)
	}

	// Different client reusing the same external_id is a different document.
	other := req
	other.ClientID = "wms"
	if _, replayed, err := h.svc.Submit(ctx, other); err != nil || replayed {
		t.Fatalf("cross-client submit: err=%v replayed=%v", err, replayed)
	}
}

func TestMissingExternalIDRejected(t *testing.T) {
	h := newHarness(t, mockPrinter("p1", nil))
	if _, _, err := h.svc.Submit(context.Background(), SubmitRequest{
		ClientID: "erp", PrinterID: "p1", ZPL: "^XA^FDa^FS^XZ",
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest, got %v", err)
	}
}

func TestRetryOnZeroByteFailure(t *testing.T) {
	// FailEvery=2 => first attempt fails with 0 bytes, second succeeds.
	h := newHarness(t, mockPrinter("p1", &domain.MockCfg{State: "idle", FailEvery: 2}))
	j, _, err := h.svc.Submit(context.Background(), SubmitRequest{
		ClientID: "erp", PrinterID: "p1", ExternalID: "retry-1", ZPL: "^XA^FDa^FS^XZ",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	got := waitState(t, h.svc, j.ID, domain.JobPrinted)
	if got.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", got.Attempts)
	}
	ats, _ := h.st.Attempts(context.Background(), j.ID)
	if len(ats) != 2 || ats[0].Result != "write_error" || ats[1].Result != "ok" {
		t.Fatalf("unexpected attempt log: %+v", ats)
	}
}

func TestExhaustedRetriesFail(t *testing.T) {
	h := newHarness(t, mockPrinter("p1", &domain.MockCfg{FailEvery: 1}))
	j, _, err := h.svc.Submit(context.Background(), SubmitRequest{
		ClientID: "erp", PrinterID: "p1", ExternalID: "always-fail", ZPL: "^XA^FDa^FS^XZ",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	got := waitState(t, h.svc, j.ID, domain.JobFailed)
	if got.Attempts != 3 {
		t.Fatalf("attempts = %d, want 3 (max_attempts)", got.Attempts)
	}
	if got.PartialWrite {
		t.Fatal("zero-byte failures must not be marked partial_write")
	}
	if got.LastError == "" {
		t.Fatal("expected last_error to be recorded")
	}
}

func TestUnconfirmedBecomesSentUnknown(t *testing.T) {
	// Printer answers "printing" forever: bytes are sent but never confirmed.
	h := newHarness(t, mockPrinter("p1", &domain.MockCfg{State: "printing"}))
	j, _, err := h.svc.Submit(context.Background(), SubmitRequest{
		ClientID: "erp", PrinterID: "p1", ExternalID: "unconfirmed-1", ZPL: "^XA^FDa^FS^XZ",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	got := waitState(t, h.svc, j.ID, domain.JobSentUnknown)
	if got.Attempts != 1 {
		t.Fatalf("attempts = %d: an unconfirmed job must NOT be retried (no duplicate labels)", got.Attempts)
	}
}

func TestQueueFullIsBackpressure(t *testing.T) {
	// A blocked printer: every status probe reports printing, so each job burns the
	// confirm window and the single worker stays busy.
	slow := mockPrinter("p1", &domain.MockCfg{State: "printing", DelayMS: 20})
	h := newHarness(t, slow)
	h.svc.cfg.QueueSize = 1
	h.svc.cfg.ConfirmTimeout = 200 * time.Millisecond

	var sawFull bool
	for i := 0; i < 8 && !sawFull; i++ {
		_, _, err := h.svc.Submit(context.Background(), SubmitRequest{
			ClientID: "erp", PrinterID: "p1", ExternalID: "q" + string(rune('a'+i)), ZPL: "^XA^FDa^FS^XZ",
		})
		if errors.Is(err, ErrQueueFull) {
			sawFull = true
		}
	}
	if !sawFull {
		t.Fatal("expected ErrQueueFull once the per-printer queue filled up")
	}
}

func TestCancelQueuedJob(t *testing.T) {
	h := newHarness(t, mockPrinter("p1", &domain.MockCfg{State: "printing"}))
	h.svc.cfg.ConfirmTimeout = 3 * time.Second
	// First job occupies the single per-printer worker (its confirm loop is long).
	first, _, _ := h.svc.Submit(context.Background(), SubmitRequest{
		ClientID: "erp", PrinterID: "p1", ExternalID: "blocker", ZPL: "^XA^FDa^FS^XZ",
	})
	second, _, err := h.svc.Submit(context.Background(), SubmitRequest{
		ClientID: "erp", PrinterID: "p1", ExternalID: "to-cancel", ZPL: "^XA^Fbb^FS^XZ",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	got, err := h.svc.Cancel(context.Background(), "erp", second.ID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if got.State != domain.JobCancelled {
		t.Fatalf("state = %s, want cancelled", got.State)
	}
	// Cancelling in-flight work is a request, not an error.
	if _, err := h.svc.Cancel(context.Background(), "erp", first.ID); err != nil {
		t.Fatalf("cancelling an in-flight job: %v", err)
	}
	// A cancelled job must never reach the driver.
	time.Sleep(200 * time.Millisecond)
	if j, _ := h.svc.Get(context.Background(), "", second.ID); j.State != domain.JobCancelled {
		t.Fatalf("cancelled job state = %s, want cancelled", j.State)
	}
}

func TestCancelTerminalJobConflicts(t *testing.T) {
	h := newHarness(t, mockPrinter("p1", &domain.MockCfg{State: "idle"}))
	j, _, _ := h.svc.Submit(context.Background(), SubmitRequest{
		ClientID: "erp", PrinterID: "p1", ExternalID: "done", ZPL: "^XA^FDa^FS^XZ",
	})
	waitState(t, h.svc, j.ID, domain.JobPrinted)
	if _, err := h.svc.Cancel(context.Background(), "erp", j.ID); !errors.Is(err, ErrNotCancellable) {
		t.Fatalf("expected ErrNotCancellable for a printed job, got %v", err)
	}
}

func TestTenantIsolation(t *testing.T) {
	h := newHarness(t, mockPrinter("p1", &domain.MockCfg{State: "idle"}))
	j, _, _ := h.svc.Submit(context.Background(), SubmitRequest{
		ClientID: "erp", PrinterID: "p1", ExternalID: "iso-1", ZPL: "^XA^FDa^FS^XZ",
	})
	if _, err := h.svc.Get(context.Background(), "wms", j.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-tenant read must be NotFound, got %v", err)
	}
	if _, err := h.svc.Cancel(context.Background(), "wms", j.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-tenant cancel must be NotFound, got %v", err)
	}
}

func TestUnknownPrinterRejected(t *testing.T) {
	h := newHarness(t, mockPrinter("p1", nil))
	_, _, err := h.svc.Submit(context.Background(), SubmitRequest{
		ClientID: "erp", PrinterID: "nope", ExternalID: "x1", ZPL: "^XA^FDa^FS^XZ",
	})
	if !errors.Is(err, ErrPrinterNotFound) {
		t.Fatalf("expected ErrPrinterNotFound, got %v", err)
	}
}

func TestFIFOOrderPerPrinter(t *testing.T) {
	h := newHarness(t, mockPrinter("p1", &domain.MockCfg{State: "idle"}))
	var ids []string
	for i := 0; i < 5; i++ {
		j, _, err := h.svc.Submit(context.Background(), SubmitRequest{
			ClientID: "erp", PrinterID: "p1", ExternalID: "fifo-" + string(rune('0'+i)), ZPL: "^XA^FDa^FS^XZ",
		})
		if err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
		ids = append(ids, j.ID)
	}
	for _, id := range ids {
		waitState(t, h.svc, id, domain.JobPrinted)
	}
	jobs, err := h.st.ListJobs(context.Background(), store.Filter{PrinterID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	// ListJobs returns newest-first; reverse to recover dispatch order.
	for i := 0; i < len(jobs)/2; i++ {
		jobs[i], jobs[len(jobs)-1-i] = jobs[len(jobs)-1-i], jobs[i]
	}
	for i, j := range jobs {
		if j.ID != ids[i] {
			t.Fatalf("position %d: got %s want %s (order not preserved)", i, j.ID, ids[i])
		}
	}
}

func TestPerPrinterOrderDoesNotBlockOtherPrinters(t *testing.T) {
	h := newHarness(t,
		mockPrinter("blocked", &domain.MockCfg{State: "printing"}),
		mockPrinter("fast", &domain.MockCfg{State: "idle"}),
	)
	ctx := context.Background()
	slow, _, _ := h.svc.Submit(ctx, SubmitRequest{ClientID: "erp", PrinterID: "blocked", ExternalID: "s1", ZPL: "^XA^FDa^FS^XZ"})
	waitForState(t, h.svc, slow.ID, domain.JobSending, 2*time.Second)
	fast, _, err := h.svc.Submit(ctx, SubmitRequest{ClientID: "erp", PrinterID: "fast", ExternalID: "f1", ZPL: "^XA^FDa^FS^XZ"})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, h.svc, fast.ID, domain.JobPrinted)
	if j, _ := h.svc.Get(ctx, "", slow.ID); j.State != domain.JobSending {
		t.Fatalf("blocked printer job state = %s, want sending (independent workers)", j.State)
	}
}

// waitForState polls until want is reached or the timeout expires (never fails the test).
func waitForState(t *testing.T, svc *Service, id string, want domain.JobState, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if j, err := svc.Get(context.Background(), "", id); err == nil && j.State == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Logf("state %s not reached within %s (continuing)", want, within)
}

func TestRestartMarksInFlightAsSentUnknown(t *testing.T) {
	h := newHarness(t, mockPrinter("p1", &domain.MockCfg{State: "idle"}))
	ctx := context.Background()

	// Simulate a job that was mid-write when the process died.
	inflight := &domain.Job{
		ID: "inflight-1", ClientID: "erp", ExternalID: "inflight-1", PrinterID: "p1",
		State: domain.JobSending, Payload: "^XA^FDa^FS^XZ", Bytes: 11,
		PayloadSHA: "deadbeef", Copies: 1, Priority: "normal", QueuedAt: time.Now().UTC(),
	}
	if err := h.st.CreateJob(ctx, inflight); err != nil {
		t.Fatal(err)
	}
	n, err := h.st.RequeueUnfinished(ctx)
	if err != nil || n != 1 {
		t.Fatalf("requeue: n=%d err=%v", n, err)
	}
	got, _ := h.st.GetJob(ctx, "inflight-1")
	if got.State != domain.JobSentUnknown {
		t.Fatalf("state = %s, want sent_unknown (we cannot prove zero bytes were written)", got.State)
	}
	if got.FinishedAt == nil {
		t.Fatal("finished_at must be set when a job is resolved")
	}
}

func TestConcurrentIdenticalSubmitsCreateOneJob(t *testing.T) {
	h := newHarness(t, mockPrinter("p1", &domain.MockCfg{State: "idle"}))
	ctx := context.Background()
	req := SubmitRequest{ClientID: "erp", PrinterID: "p1", ExternalID: "race-1", ZPL: "^XA^FDa^FS^XZ"}

	var wg sync.WaitGroup
	ids := make([]string, 8)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			j, _, err := h.svc.Submit(ctx, req)
			if err == nil {
				ids[i] = j.ID
			}
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, id := range ids {
		if id != "" {
			seen[id] = true
		}
	}
	if len(seen) != 1 {
		t.Fatalf("expected exactly one job id across retries, got %v", seen)
	}
}

func TestJobStateTerminal(t *testing.T) {
	for _, s := range []domain.JobState{domain.JobPrinted, domain.JobSentUnknown, domain.JobFailed, domain.JobCancelled} {
		if !s.Terminal() {
			t.Errorf("%s should be terminal", s)
		}
	}
	for _, s := range []domain.JobState{domain.JobQueued, domain.JobSending} {
		if s.Terminal() {
			t.Errorf("%s should not be terminal", s)
		}
	}
}

func TestRejectWhenNotUsable(t *testing.T) {
	specs := []domain.Printer{mockPrinter("out", &domain.MockCfg{State: "media_out"})}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	b := observ.NewBus()
	mgr, err := fleet.NewManager(specs, log, b, fleet.ManagerOpts{PollInterval: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	mgr.ProbeAll(context.Background())

	svc := NewService(Config{QueueSize: 2, MaxAttempts: 1, ConfirmTimeout: 50 * time.Millisecond,
		MaxPayloadSize: 4096, RejectWhenNotUsable: true}, store.NewMemoryStore(), mgr, b, log, observ.NewMetrics())
	defer svc.Stop()

	_, _, err = svc.Submit(context.Background(), SubmitRequest{
		ClientID: "erp", PrinterID: "out", ExternalID: "no-1", ZPL: "^XA^FDa^FS^XZ",
	})
	if !errors.Is(err, ErrPrinterNotUsable) {
		t.Fatalf("expected ErrPrinterNotUsable, got %v", err)
	}
}

func TestSubmitWithPageCopiesAndTailSuppression(t *testing.T) {
	h := newHarness(t, mockPrinter("p1", &domain.MockCfg{State: "idle"}))
	ctx := context.Background()

	// 2-page format: Page 1 = Full carton (Qty 12), Page 2 = Tail carton (Qty 2)
	zpl := "^XA^CI28^FDFull Carton Qty: 12^FS^XZ^XA^CI28^FDTail Carton Qty: 2^FS^XZ"

	// 1. Submit with PageCopies [4, 1] (4 full cartons + 1 tail carton = 5 total labels)
	j, replayed, err := h.svc.Submit(ctx, SubmitRequest{
		ClientID: "wms", PrinterID: "p1", ExternalID: "tail-wave-1", ZPL: zpl,
		PageCopies: []int{4, 1},
	})
	if err != nil || replayed {
		t.Fatalf("submit failed: %v (replayed=%v)", err, replayed)
	}
	if j.Copies != 5 {
		t.Fatalf("expected 5 total labels, got %d", j.Copies)
	}
	if len(j.PageCopies) != 2 || j.PageCopies[0] != 4 || j.PageCopies[1] != 1 {
		t.Fatalf("page_copies not recorded: %+v", j.PageCopies)
	}
	if !strings.Contains(j.Payload, "^PQ4^XZ") || !strings.Contains(j.Payload, "^PQ1^XZ") {
		t.Fatalf("ZPL payload missing native ^PQ injections: %s", j.Payload)
	}

	// 2. Replay with identical PageCopies: returns 200 replayed
	jReplay, replayed, err := h.svc.Submit(ctx, SubmitRequest{
		ClientID: "wms", PrinterID: "p1", ExternalID: "tail-wave-1", ZPL: zpl,
		PageCopies: []int{4, 1},
	})
	if err != nil || !replayed || jReplay.ID != j.ID {
		t.Fatalf("expected identical replay: replayed=%v, err=%v", replayed, err)
	}

	// 3. Same external_id with different PageCopies [3, 1] triggers 409 Conflict
	_, _, err = h.svc.Submit(ctx, SubmitRequest{
		ClientID: "wms", PrinterID: "p1", ExternalID: "tail-wave-1", ZPL: zpl,
		PageCopies: []int{3, 1},
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expected store.ErrConflict on changed page_copies, got %v", err)
	}

	// 4. Zero remainder test: PageCopies [4, 0] suppresses Page 2 entirely
	jZero, _, err := h.svc.Submit(ctx, SubmitRequest{
		ClientID: "wms", PrinterID: "p1", ExternalID: "tail-wave-zero", ZPL: zpl,
		PageCopies: []int{4, 0},
	})
	if err != nil {
		t.Fatalf("submit zero remainder: %v", err)
	}
	if jZero.Copies != 4 {
		t.Fatalf("expected 4 copies with tail suppressed, got %d", jZero.Copies)
	}
	if strings.Contains(jZero.Payload, "Tail Carton") {
		t.Fatalf("Page 2 must be omitted when copies == 0: %s", jZero.Payload)
	}
}

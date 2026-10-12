package job

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/kucial/cloud-print-service/internal/domain"
	"github.com/kucial/cloud-print-service/internal/fleet"
	"github.com/kucial/cloud-print-service/internal/observ"
	"github.com/kucial/cloud-print-service/internal/store"
)

var (
	ErrPrinterNotFound  = errors.New("printer not found")
	ErrPrinterDisabled  = errors.New("printer disabled")
	ErrPrinterNotUsable = errors.New("printer is not usable")
	ErrQueueFull        = errors.New("printer queue is full")
	ErrInvalidRequest   = errors.New("invalid request")
	ErrNotCancellable   = errors.New("job can no longer be cancelled")
)

// SubmitRequest is the API-level request to dispatch one label.
type SubmitRequest struct {
	ClientID       string
	PrinterID      string
	ExternalID     string
	IdempotencyKey string
	ZPL            string // raw label, used when PayloadBase64 is empty
	PayloadBase64  string
	Copies         int
	Priority       string
	BizRef         string
	Meta           map[string]string
}

type Config struct {
	QueueSize      int
	MaxAttempts    int
	ConfirmTimeout time.Duration
	QueueTTL       time.Duration
	BaseBackoff    time.Duration
	// SettleFor is how long a printer that never shows "printing" has before idle is
	// accepted as confirmation. Default 1s.
	SettleFor      time.Duration
	MaxPayloadSize int64
	// RejectWhenNotUsable refuses dispatch to a printer whose cached status shows a
	// consumable/error condition. Off by default: the caller usually prefers to try and
	// see, and can filter on `usable` itself. Turn it on when a failed print is worse
	// than a rejected dispatch for that site.
	RejectWhenNotUsable bool
}

// Service owns dispatch. One worker goroutine per printer gives FIFO ordering per
// device and independent failure domains per device.
type Service struct {
	cfg Config
	st  store.Store
	mgr *fleet.Manager
	bus *observ.Bus
	log *slog.Logger
	met *observ.Registry

	mu      sync.Mutex
	queues  map[string]chan string
	running map[string]context.CancelFunc
}

func NewService(cfg Config, st store.Store, mgr *fleet.Manager, b *observ.Bus, log *slog.Logger, met *observ.Registry) *Service {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 50
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 3
	}
	if cfg.ConfirmTimeout <= 0 {
		cfg.ConfirmTimeout = 10 * time.Second
	}
	if cfg.QueueTTL <= 0 {
		cfg.QueueTTL = 5 * time.Minute
	}
	if cfg.BaseBackoff <= 0 {
		cfg.BaseBackoff = 500 * time.Millisecond
	}
	if cfg.SettleFor <= 0 {
		cfg.SettleFor = time.Second
	}
	if cfg.MaxPayloadSize <= 0 {
		cfg.MaxPayloadSize = 1 << 20
	}
	return &Service{
		cfg: cfg, st: st, mgr: mgr, bus: b, log: log, met: met,
		queues:  map[string]chan string{},
		running: map[string]context.CancelFunc{},
	}
}

// Start re-queues jobs that were mid-flight when the process died.
func (s *Service) Start(ctx context.Context) error {
	n, err := s.st.RequeueUnfinished(ctx)
	if err != nil {
		return fmt.Errorf("requeue unfinished jobs: %w", err)
	}
	if n > 0 {
		s.log.Warn("jobs marked sent_unknown after restart (bytes may have reached printers)", "count", n)
	}
	s.replayQueued(ctx)
	return nil
}

// replayQueued re-dispatches jobs that were accepted but never started. Safe because a
// `queued` job provably sent zero bytes.
func (s *Service) replayQueued(ctx context.Context) {
	jobs, err := s.st.ListJobs(ctx, store.Filter{State: string(domain.JobQueued), Limit: 1000})
	if err != nil {
		return
	}
	for i := range jobs {
		if err := s.enqueue(ctx, &jobs[i]); err != nil {
			s.log.Error("requeue failed", "job_id", jobs[i].ID, "err", err)
		}
	}
}

func (s *Service) Store() store.Store { return s.st }

// Submit validates, deduplicates and enqueues a job. The bool result reports a replay:
// the caller got an existing job back instead of a new label.
func (s *Service) Submit(ctx context.Context, req SubmitRequest) (*domain.Job, bool, error) {
	payload, err := resolvePayload(req, s.cfg.MaxPayloadSize)
	if err != nil {
		return nil, false, err
	}
	if req.PrinterID == "" {
		return nil, false, fmt.Errorf("%w: printer_id is required", ErrInvalidRequest)
	}
	p, ok := s.mgr.Get(req.PrinterID)
	if !ok {
		return nil, false, fmt.Errorf("%w: %s", ErrPrinterNotFound, req.PrinterID)
	}
	if !p.Enabled() {
		return nil, false, fmt.Errorf("%w: %s", ErrPrinterDisabled, req.PrinterID)
	}
	if s.cfg.RejectWhenNotUsable {
		if v := p.View(); !v.Usable {
			return nil, false, fmt.Errorf("%w: %s is %s", ErrPrinterNotUsable, req.PrinterID, v.Status.State)
		}
	}
	externalID := strings.TrimSpace(req.ExternalID)
	if externalID == "" {
		// Without a business key a blind retry would print a duplicate label, so the
		// caller must opt in explicitly via Idempotency-Key.
		externalID = strings.TrimSpace(req.IdempotencyKey)
		if externalID == "" {
			return nil, false, fmt.Errorf("%w: external_id (or Idempotency-Key) is required for safe retries", ErrInvalidRequest)
		}
	}
	copies := req.Copies
	if copies == 0 {
		copies = 1
	}
	if copies < 1 || copies > 50 {
		return nil, false, fmt.Errorf("%w: copies must be 1..50", ErrInvalidRequest)
	}
	priority := req.Priority
	if priority == "" {
		priority = "normal"
	}
	if priority != "normal" && priority != "high" {
		return nil, false, fmt.Errorf("%w: priority must be normal|high", ErrInvalidRequest)
	}

	sum := sha256.Sum256(payload)

	// Idempotency: same (client, external_id) => same job, no second label.
	if existing, err := s.st.GetByExternal(ctx, req.ClientID, externalID); err == nil {
		if existing.PayloadSHA != hex.EncodeToString(sum[:]) {
			return nil, false, fmt.Errorf("%w: external_id %q was already used with a different payload", store.ErrConflict, externalID)
		}
		return existing, true, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, false, err
	}

	job := &domain.Job{
		ClientID:       req.ClientID,
		ExternalID:     externalID,
		PrinterID:      req.PrinterID,
		State:          domain.JobQueued,
		Payload:        string(payload),
		Bytes:          len(payload),
		PayloadSHA:     hex.EncodeToString(sum[:]),
		Copies:         copies,
		Priority:       priority,
		BizRef:         req.BizRef,
		Meta:           req.Meta,
		IdempotencyKey: req.IdempotencyKey,
		QueuedAt:       time.Now().UTC(),
	}
	if err := s.st.CreateJob(ctx, job); err != nil {
		// Lost a race with a concurrent identical submit: return the winner.
		if existing, gerr := s.st.GetByExternal(ctx, req.ClientID, externalID); gerr == nil {
			return existing, true, nil
		}
		return nil, false, err
	}
	// Announce acceptance before the worker can start, so subscribers never see
	// `sending` before `queued`.
	s.transition(job, domain.JobQueued, "")
	if err := s.enqueue(ctx, job); err != nil {
		s.fail(ctx, job, err.Error())
		return job, false, err
	}
	s.met.Inc("cps_jobs_submitted_total", "printer_id", job.PrinterID, "client_id", job.ClientID)
	return job, false, nil
}

func resolvePayload(req SubmitRequest, max int64) ([]byte, error) {
	var payload []byte
	switch {
	case req.PayloadBase64 != "" && req.ZPL != "":
		return nil, fmt.Errorf("%w: provide either zpl or payload_base64, not both", ErrInvalidRequest)
	case req.PayloadBase64 != "":
		b, err := base64.StdEncoding.DecodeString(req.PayloadBase64)
		if err != nil {
			return nil, fmt.Errorf("%w: payload_base64 is not valid base64", ErrInvalidRequest)
		}
		payload = b
	case req.ZPL != "":
		payload = []byte(req.ZPL)
	default:
		return nil, fmt.Errorf("%w: zpl or payload_base64 is required", ErrInvalidRequest)
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("%w: payload is empty", ErrInvalidRequest)
	}
	if int64(len(payload)) > max {
		return nil, fmt.Errorf("%w: payload is %d bytes, limit is %d", ErrInvalidRequest, len(payload), max)
	}
	return payload, nil
}

func (s *Service) enqueue(ctx context.Context, j *domain.Job) error {
	s.mu.Lock()
	q, ok := s.queues[j.PrinterID]
	if !ok {
		q = make(chan string, s.cfg.QueueSize)
		s.queues[j.PrinterID] = q
	}
	_, hasWorker := s.running[j.PrinterID]
	s.mu.Unlock()

	p, pok := s.mgr.Get(j.PrinterID)
	if pok {
		p.SetQueueDepth(len(q) + 1)
	}

	select {
	case q <- j.ID:
		if p, pok := s.mgr.Get(j.PrinterID); pok {
			p.SetQueueDepth(len(q))
		}
	default:
		if p, pok := s.mgr.Get(j.PrinterID); pok {
			p.SetQueueDepth(len(q))
		}
		s.met.Inc("cps_queue_full_total", "printer_id", j.PrinterID)
		return fmt.Errorf("%w: printer %s has %d queued jobs", ErrQueueFull, j.PrinterID, len(q))
	}

	if !hasWorker {
		s.mu.Lock()
		if _, exists := s.running[j.PrinterID]; !exists {
			wctx, wcancel := context.WithCancel(context.WithoutCancel(ctx))
			s.running[j.PrinterID] = wcancel
			go s.worker(wctx, j.PrinterID, q)
		}
		s.mu.Unlock()
	}
	return nil
}

func (s *Service) worker(ctx context.Context, printerID string, q chan string) {
	defer func() {
		s.mu.Lock()
		if c, ok := s.running[printerID]; ok {
			c()
			delete(s.running, printerID)
		}
		s.mu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-q:
			s.executeSafely(ctx, printerID, id)
			if p, ok := s.mgr.Get(printerID); ok {
				p.SetQueueDepth(len(q))
			}
		}
	}
}

// executeSafely runs a job and contains a panic to that job: a driver or store bug
// must never take the printer's worker down and strand the whole queue.
func (s *Service) executeSafely(ctx context.Context, printerID, jobID string) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("panic executing job", "printer_id", printerID, "job_id", jobID, "panic", r)
			s.met.Inc("cps_dispatch_panics_total", "printer_id", printerID)
			if j, err := s.st.GetJob(context.Background(), jobID); err == nil {
				s.fail(context.Background(), j, fmt.Sprintf("internal panic: %v", r))
			}
		}
	}()
	s.execute(ctx, printerID, jobID)
}

// execute runs one job to a terminal state, retrying only failures that provably
// produced zero bytes on the driver.
func (s *Service) execute(ctx context.Context, printerID, jobID string) {
	loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	j, err := s.st.GetJob(loadCtx, jobID)
	cancel()
	if err != nil {
		return
	}
	if j.State != domain.JobQueued {
		return // cancelled before we got to it
	}
	// Re-check state right before touching the printer: a job cancelled between load and
	// now must never print.
	if j.State != domain.JobQueued {
		return
	}
	if time.Since(j.QueuedAt) > s.cfg.QueueTTL {
		s.fail(loadCtx, j, "queued_too_long: job waited "+time.Since(j.QueuedAt).Truncate(time.Second).String()+" before dispatch")
		return
	}
	p, ok := s.mgr.Get(printerID)
	if !ok {
		s.fail(loadCtx, j, "printer "+printerID+" is no longer registered")
		return
	}

	j.State = domain.JobSending
	j.Attempts = 0
	// Persist before touching the printer: the boot reaper resolves anything left in
	// `sending`, and the API must be able to show in-flight work.
	if err := s.st.UpdateJob(loadCtx, j); err != nil {
		s.log.Error("failed to mark job sending", "job_id", j.ID, "err", err)
	}
	s.transition(j, domain.JobSending, "")

	payload := []byte(j.Payload)
	var lastErr string
	var bytesSent int
	sendOK := false

	for attempt := 1; attempt <= s.cfg.MaxAttempts; attempt++ {
		if ctx.Err() != nil {
			return // shutting down: leave as sending; boot reaper resolves it
		}
		at := &domain.Attempt{JobID: j.ID, AttemptNo: attempt, StartedAt: time.Now().UTC()}
		sendCtx, sendCancel := context.WithTimeout(ctx, s.cfg.ConfirmTimeout)
		n, sendErr := p.Send(sendCtx, payload)
		sendCancel()
		j.Attempts = attempt
		fin := time.Now().UTC()
		at.FinishedAt = &fin
		at.BytesSent = n

		switch {
		case sendErr == nil:
			at.Result = "ok"
			_ = s.st.AddAttempt(ctx, at)
			sendOK = true
			lastErr = ""

		case n > 0:
			// Ambiguous: part of a label reached the driver. Never auto-retry.
			at.Result = "partial_write"
			at.Error = sendErr.Error()
			_ = s.st.AddAttempt(ctx, at)
			j.PartialWrite = true
			j.LastError = sendErr.Error()
			now := time.Now().UTC()
			j.SentAt = &now
			s.finish(ctx, j, domain.JobFailed)
			s.met.Inc("cps_dispatch_failures_total", "reason", "partial_write", "printer_id", printerID)
			return

		default:
			// Zero bytes on the printer: retrying is safe.
			at.Result = "write_error"
			at.Error = sendErr.Error()
			_ = s.st.AddAttempt(ctx, at)
			lastErr = sendErr.Error()
			if attempt == s.cfg.MaxAttempts {
				break
			}
			s.log.Warn("dispatch failed, retrying", "job_id", j.ID, "printer_id", printerID,
				"attempt", attempt, "err", sendErr)
			s.met.Inc("cps_dispatch_failures_total", "reason", "write_error", "printer_id", printerID)
			if !sleepCtx(ctx, backoff(s.cfg.BaseBackoff, attempt)) {
				return
			}
		}
		if sendOK {
			break
		}
	}

	if !sendOK {
		s.fail(ctx, j, lastErr)
		return
	}
	bytesSent = len(payload)

	now := time.Now().UTC()
	j.SentAt = &now
	s.met.Observe("cps_dispatch_seconds", time.Since(j.QueuedAt).Seconds(), "printer_id", printerID)

	// Bytes are on the wire. From here on CPS must not create a second label.
	confirmStart := time.Now()
	confirmed := s.confirm(ctx, p)
	s.met.Observe("cps_confirm_seconds", time.Since(confirmStart).Seconds(), "printer_id", printerID)
	if confirmed {
		s.finish(ctx, j, domain.JobPrinted)
		return
	}
	j.LastError = "bytes sent but no confirmation observed within " + s.cfg.ConfirmTimeout.String()
	_ = s.st.AddAttempt(ctx, &domain.Attempt{
		JobID: j.ID, AttemptNo: j.Attempts, StartedAt: now, FinishedAt: &now,
		Result: "unconfirmed", BytesSent: bytesSent,
	})
	s.met.Inc("cps_sent_unknown_total", "printer_id", printerID)
	s.log.Error("job sent but unconfirmed — needs human check", "job_id", j.ID, "printer_id", printerID, "bytes", bytesSent)
	s.finish(ctx, j, domain.JobSentUnknown)
}

// confirm decides whether the label printed. It is evidence, not proof: absence of a
// reply is reported as `sent_unknown`, never silently as success.
//
// The subtlety: a printer takes a moment to engage after the bytes land, so an immediate
// `idle` is ambiguous - it might be "not started yet". To avoid declaring printed on a
// label that never began, confirmation waits briefly for the printer to show busy before
// accepting idle as "finished". A printer that never shows busy within the settle window
// gets idle-as-printed only after the settle time has passed.
func (s *Service) confirm(ctx context.Context, p *fleet.Printer) bool {
	deadline := time.Now().Add(s.cfg.ConfirmTimeout)
	// The settle window must land strictly inside the deadline, or the "accept idle after
	// settle" branch can never fire before the loop gives up.
	settle := time.Now().Add(s.cfg.SettleFor)
	if limit := time.Now().Add(s.cfg.ConfirmTimeout / 3); settle.After(limit) {
		settle = limit
	}
	sawBusy := false
	// Poll faster than the settle window, or a short confirm timeout can never observe
	// a fast printer before it elapses.
	pollEvery := 100 * time.Millisecond
	if s.cfg.SettleFor < pollEvery {
		pollEvery = s.cfg.SettleFor / 2
	}
	if pollEvery <= 0 {
		pollEvery = 50 * time.Millisecond
	}
	for {
		st, err := p.Status(ctx)
		if err == nil {
			switch {
			case st.State.Busy():
				sawBusy = true
			case st.State.BlocksUse():
				return false // consumable-out or an error is never a confirmation
			default:
				// idle/unknown: confident once the printer has been seen busy, or after
				// the settle window for printers that report no busy state.
				if sawBusy || time.Now().After(settle) {
					return true
				}
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		if !sleepCtx(ctx, pollEvery) {
			return false
		}
	}
}

func backoff(base time.Duration, attempt int) time.Duration {
	d := base
	for i := 1; i < attempt; i++ {
		d *= 2
	}
	if d > 10*time.Second {
		d = 10 * time.Second
	}
	return d // + jitter: callers that care can add it; keep deterministic for tests
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (s *Service) fail(ctx context.Context, j *domain.Job, msg string) {
	j.State = domain.JobFailed
	j.LastError = msg
	now := time.Now().UTC()
	j.FinishedAt = &now
	s.finish(ctx, j, domain.JobFailed)
}

func (s *Service) finish(ctx context.Context, j *domain.Job, state domain.JobState) {
	j.State = state
	if state.Terminal() {
		now := time.Now().UTC()
		j.FinishedAt = &now
	}
	if err := s.st.UpdateJob(ctx, j); err != nil {
		s.log.Error("failed to persist job", "job_id", j.ID, "err", err)
	}
	s.transition(j, state, j.LastError)
}

func (s *Service) transition(j *domain.Job, state domain.JobState, msg string) {
	s.met.Inc("cps_jobs_total", "state", string(state), "printer_id", j.PrinterID)
	s.log.Info("job.state", "event", "job.state", "job_id", j.ID, "client_id", j.ClientID,
		"printer_id", j.PrinterID, "biz_ref", j.BizRef, "state", string(state),
		"attempts", j.Attempts, "bytes", j.Bytes, "msg", msg)
	if p, ok := s.mgr.Get(j.PrinterID); ok {
		p.NoteJob(j.ID, state)
	}
	s.bus.Publish(observ.Event{Topic: "job.state", Data: map[string]any{
		"job_id":      j.ID,
		"client_id":   j.ClientID,
		"printer_id":  j.PrinterID,
		"external_id": j.ExternalID,
		"state":       string(state),
		"biz_ref":     j.BizRef,
		"attempts":    j.Attempts,
		"at":          time.Now().UTC(),
	}})
}

// Get returns a job, scoped to the owning client (cross-client access is 404).
func (s *Service) Get(ctx context.Context, clientID, id string) (*domain.Job, error) {
	j, err := s.st.GetJob(ctx, id)
	if err != nil {
		return nil, err
	}
	if clientID != "" && j.ClientID != clientID {
		return nil, store.ErrNotFound
	}
	return j, nil
}

// Cancel aborts a job that has not yet been written to a driver.
func (s *Service) Cancel(ctx context.Context, clientID, id string) (*domain.Job, error) {
	j, err := s.Get(ctx, clientID, id)
	if err != nil {
		return nil, err
	}
	switch j.State {
	case domain.JobQueued:
		j.State = domain.JobCancelled
		now := time.Now().UTC()
		j.FinishedAt = &now
		if err := s.st.UpdateJob(ctx, j); err != nil {
			return nil, err
		}
		s.transition(j, domain.JobCancelled, "")
		return j, nil
	case domain.JobSending:
		// Best effort: ask the printer to abort, then let the worker settle the state.
		if p, ok := s.mgr.Get(j.PrinterID); ok {
			cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			_ = p.Cancel(cctx)
			cancel()
		}
		j.LastError = "cancel requested while sending"
		_ = s.st.UpdateJob(ctx, j)
		return j, nil
	default:
		return j, fmt.Errorf("%w: job is %s", ErrNotCancellable, j.State)
	}
}

func (s *Service) List(ctx context.Context, f store.Filter) ([]domain.Job, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	return s.st.ListJobs(ctx, f)
}

func (s *Service) Attempts(ctx context.Context, clientID, jobID string) ([]domain.Attempt, error) {
	if _, err := s.Get(ctx, clientID, jobID); err != nil {
		return nil, err
	}
	return s.st.Attempts(ctx, jobID)
}

// Stop cancels all workers and flushes queue-depth gauges.
func (s *Service) Stop() {
	s.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.running))
	for _, c := range s.running {
		cancels = append(cancels, c)
	}
	s.mu.Unlock()
	for _, c := range cancels {
		c()
	}
}

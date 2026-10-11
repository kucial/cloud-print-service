package fleet

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kongkx/cloud-print-service/internal/domain"
	"github.com/kongkx/cloud-print-service/internal/driver"
	"github.com/kongkx/cloud-print-service/internal/observ"
)

// Manager owns every printer's driver and cached status.
//
// Status reads never touch a printer socket: the HTTP API always serves the cache,
// so a hung TCP write can never block an ERP request.
type Manager struct {
	log          *slog.Logger
	bus          *observ.Bus
	pollInterval time.Duration
	staleAfter   time.Duration
	dialTimeout  time.Duration
	writeTimeout time.Duration
	readTimeout  time.Duration

	mu       sync.RWMutex
	printers map[string]*Printer
}

// Printer is one managed device: declared spec + driver + cached status.
type Printer struct {
	spec domain.Printer
	drv  driver.Driver
	mgr  *Manager

	queueDepth atomic.Int64

	mu     sync.RWMutex
	status domain.Status
}

type ManagerOpts struct {
	PollInterval time.Duration
	StaleAfter   time.Duration
	DialTimeout  time.Duration
	WriteTimeout time.Duration
	ReadTimeout  time.Duration
}

func NewManager(specs []domain.Printer, log *slog.Logger, b *observ.Bus, o ManagerOpts) (*Manager, error) {
	if o.PollInterval <= 0 {
		o.PollInterval = 15 * time.Second
	}
	if o.StaleAfter <= 0 {
		o.StaleAfter = 3 * o.PollInterval
	}
	m := &Manager{
		log:          log,
		bus:          b,
		pollInterval: o.PollInterval,
		staleAfter:   o.StaleAfter,
		dialTimeout:  o.DialTimeout,
		writeTimeout: o.WriteTimeout,
		readTimeout:  o.ReadTimeout,
		printers:     make(map[string]*Printer, len(specs)),
	}
	for _, spec := range specs {
		if _, dup := m.printers[spec.PrinterID]; dup {
			return nil, fmt.Errorf("duplicate printer_id %q", spec.PrinterID)
		}
		p, err := m.build(spec)
		if err != nil {
			return nil, err
		}
		m.printers[spec.PrinterID] = p
	}
	return m, nil
}

func (m *Manager) build(spec domain.Printer) (*Printer, error) {
	var drv driver.Driver
	var err error
	if spec.Driver == "zebra9100" {
		drv = driver.NewZebra(spec, driver.ZebraOpts{DialTimeout: m.dialTimeout, WriteTimeout: m.writeTimeout, ReadTimeout: m.readTimeout})
	} else {
		drv, err = driver.New(spec)
		if err != nil {
			return nil, err
		}
	}
	p := &Printer{spec: spec, drv: drv, mgr: m}
	p.status = domain.Status{State: domain.StateUnknown, Detail: "not polled yet"}
	if !spec.Enabled {
		p.status.State = domain.StateDisabled
	}
	return p, nil
}

func (m *Manager) Get(id string) (*Printer, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.printers[id]
	return p, ok
}

func (m *Manager) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.printers)
}

// Views returns live views of all printers, optionally filtered.
type Filter struct {
	Site    string
	Model   string
	Enabled *bool
	Usable  *bool
}

func (m *Manager) List(f Filter) []domain.Printer {
	m.mu.RLock()
	out := make([]domain.Printer, 0, len(m.printers))
	for _, p := range m.printers {
		out = append(out, p.View())
	}
	m.mu.RUnlock()

	res := out[:0]
	for _, p := range out {
		if f.Site != "" && p.Site != f.Site {
			continue
		}
		if f.Model != "" && p.Model != f.Model {
			continue
		}
		if f.Enabled != nil && p.Enabled != *f.Enabled {
			continue
		}
		if f.Usable != nil && p.Usable != *f.Usable {
			continue
		}
		res = append(res, p)
	}
	return res
}

// ViewsByIDs returns only the requested printers (missing ids are skipped).
func (m *Manager) ViewsByIDs(ids []string) []domain.Printer {
	out := make([]domain.Printer, 0, len(ids))
	for _, id := range ids {
		if p, ok := m.Get(id); ok {
			out = append(out, p.View())
		}
	}
	return out
}

func (p *Printer) Spec() domain.Printer { return p.spec }

func (p *Printer) ID() string { return p.spec.PrinterID }

func (p *Printer) Enabled() bool { return p.spec.Enabled }

func (p *Printer) SetQueueDepth(n int) { p.queueDepth.Store(int64(n)) }

func (p *Printer) QueueDepth() int { return int(p.queueDepth.Load()) }

// Cached returns the last observed status, marked stale if it is too old.
func (p *Printer) Cached() domain.Status {
	p.mu.RLock()
	st := p.status
	p.mu.RUnlock()
	if !st.ObservedAt.IsZero() && time.Since(st.ObservedAt) > p.mgr.staleAfter {
		st.Stale = true
	}
	return st
}

// Send dispatches bytes. Returns bytes flushed; err with n>0 means no retry.
func (p *Printer) Send(ctx context.Context, data []byte) (int, error) {
	return p.drv.Send(ctx, data)
}

func (p *Printer) Status(ctx context.Context) (domain.Status, error) { return p.drv.Status(ctx) }

func (p *Printer) Cancel(ctx context.Context) error { return p.drv.Cancel(ctx) }

func (p *Printer) Identify(ctx context.Context) (map[string]string, error) {
	return p.drv.Identify(ctx)
}

func (p *Printer) Close() { _ = p.drv.Close() }

// NoteJob records the last job seen on this printer, so a dashboard opened after the fact
// still shows which document produced the current state.
func (p *Printer) NoteJob(jobID string, state domain.JobState) {
	p.mu.Lock()
	p.status.LastJob = &domain.JobRef{JobID: jobID, State: state, At: time.Now().UTC()}
	p.mu.Unlock()
}

// View renders the cached status plus usability into the API representation.
func (p *Printer) View() domain.Printer {
	v := p.spec
	v.Status = p.Cached()
	v.QueueDepth = p.QueueDepth()
	if !v.Enabled {
		v.Usable = false
	} else {
		st := v.Status
		// Usable requires positive evidence: enabled, observed at least once, fresh,
		// and not in a consumable/error state. "unknown" is never reported as usable.
		v.Usable = !st.ObservedAt.IsZero() && !st.State.BlocksUse() && !st.Stale
	}
	return v
}

func (p *Printer) publishStatus(st domain.Status) {
	if p.spec.PrinterID == "" {
		return
	}
	p.mgr.bus.Publish(observ.Event{
		Topic: "printer.status",
		Data: map[string]any{
			"printer_id":  p.spec.PrinterID,
			"state":       st.State,
			"raw":         st.Raw,
			"detail":      st.Detail,
			"observed_at": st.ObservedAt,
			"stale":       false,
			"usable":      p.View().Usable,
		},
	})
}

func (p *Printer) probe(ctx context.Context) {
	probeCtx, cancel := context.WithTimeout(ctx, p.mgr.pollInterval)
	defer cancel()

	st, err := p.Status(probeCtx)
	if err != nil {
		st.State = domain.StateOffline
		if st.Detail == "" {
			st.Detail = err.Error()
		}
		if st.ObservedAt.IsZero() {
			st.ObservedAt = time.Now().UTC()
		}
		// Keep the last good reading visible for troubleshooting.
		p.mu.Lock()
		if prev := p.status; prev.ObservedAt.After(st.ObservedAt) && prev.State != domain.StateUnknown {
			st.Raw = prev.Raw
		}
		st.LastJob = p.status.LastJob
		p.status = st
		p.mu.Unlock()
		p.mgr.log.Warn("printer status probe failed", "printer_id", p.spec.PrinterID, "err", err)
		p.publishStatus(st)
		return
	}
	if !p.spec.Enabled {
		st.State = domain.StateDisabled
	}
	p.mu.Lock()
	changed := p.status.State != st.State
	// The status probe says nothing about which job ran last; keep that pointer so a
	// dashboard opened after the fact still shows the responsible document.
	st.LastJob = p.status.LastJob
	p.status = st
	p.mu.Unlock()
	if changed {
		p.mgr.log.Info("printer state changed", "printer_id", p.spec.PrinterID, "state", st.State)
	}
	p.publishStatus(st)
}

// StartPolling probes every printer on an interval, staggered to avoid a thundering herd.
func (m *Manager) StartPolling(ctx context.Context) {
	m.mu.RLock()
	ids := make([]string, 0, len(m.printers))
	for id := range m.printers {
		ids = append(ids, id)
	}
	m.mu.RUnlock()

	for _, id := range ids {
		p, ok := m.Get(id)
		if !ok {
			continue
		}
		go func(p *Printer) {
			// Probe immediately so the first API call already has data.
			p.probe(ctx)
			h := fnv.New32a()
			_, _ = h.Write([]byte(p.spec.PrinterID))
			stagger := time.Duration(h.Sum32() % uint32(m.pollInterval))
			t := time.NewTimer(stagger)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
				p.probe(ctx)
				t.Reset(m.pollInterval)
			}
		}(p)
	}
}

// ProbeAll forces an immediate refresh of every printer (admin/maintenance).
func (m *Manager) ProbeAll(ctx context.Context) {
	m.mu.RLock()
	ps := make([]*Printer, 0, len(m.printers))
	for _, p := range m.printers {
		ps = append(ps, p)
	}
	m.mu.RUnlock()
	var wg sync.WaitGroup
	for _, p := range ps {
		wg.Add(1)
		go func(p *Printer) { defer wg.Done(); p.probe(ctx) }(p)
	}
	wg.Wait()
}

func (m *Manager) Close() {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, p := range m.printers {
		p.Close()
	}
}

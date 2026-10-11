package driver

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kongkx/cloud-print-service/internal/domain"
)

func init() {
	Register("mock", func(p domain.Printer) (Driver, error) { return NewMock(p), nil })
}

// Mock is a fake printer: no sockets, deterministic behaviour for tests and demos.
type Mock struct {
	cfg domain.MockCfg

	mu      sync.Mutex
	sends   int
	failSeq int
}

func NewMock(p domain.Printer) *Mock {
	cfg := domain.MockCfg{State: string(domain.StateIdle)}
	if p.Mock != nil {
		cfg = *p.Mock
	}
	if cfg.State == "" {
		cfg.State = string(domain.StateIdle)
	}
	return &Mock{cfg: cfg}
}

func (m *Mock) Send(ctx context.Context, data []byte) (int, error) {
	if m.cfg.Offline {
		return 0, fmt.Errorf("mock printer offline")
	}
	if d := time.Duration(m.cfg.DelayMS) * time.Millisecond; d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	m.mu.Lock()
	m.sends++
	n := m.sends
	m.mu.Unlock()

	// FailEvery=2 => the 1st send fails with 0 bytes, the 2nd succeeds.
	if m.cfg.FailEvery > 0 && (n-1)%m.cfg.FailEvery == 0 {
		return 0, fmt.Errorf("mock: simulated connect failure (every %d)", m.cfg.FailEvery)
	}
	return len(data), nil
}

func (m *Mock) Status(ctx context.Context) (domain.Status, error) {
	if m.cfg.Offline {
		return domain.Status{State: domain.StateOffline, ObservedAt: time.Now()}, fmt.Errorf("mock printer offline")
	}
	return domain.Status{
		State:      domain.PrinterState(m.cfg.State),
		Raw:        "^HS0,0,0,0^HS",
		Detail:     "mock",
		ObservedAt: time.Now(),
	}, nil
}

func (m *Mock) Cancel(ctx context.Context) error { return nil }

func (m *Mock) Identify(ctx context.Context) (map[string]string, error) {
	return map[string]string{"model": "mock", "serial": "mock-0001", "fw": "n/a"}, nil
}

func (m *Mock) Close() error { return nil }

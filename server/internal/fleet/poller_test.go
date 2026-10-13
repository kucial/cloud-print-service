package fleet

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kucial/cloud-print-service/server/internal/domain"
	"github.com/kucial/cloud-print-service/server/internal/driver"
	"github.com/kucial/cloud-print-service/server/internal/observ"
)

// countingDriver counts status probes, and how many ran at once.
type countingDriver struct {
	probes, active, maxActive atomic.Int32
}

func (c *countingDriver) Send(context.Context, []byte) (int, error) { return 0, nil }
func (c *countingDriver) Cancel(context.Context) error              { return nil }
func (c *countingDriver) Close() error                              { return nil }
func (c *countingDriver) Identify(context.Context) (map[string]string, error) {
	return nil, nil
}
func (c *countingDriver) Status(ctx context.Context) (domain.Status, error) {
	c.probes.Add(1)
	if n := c.active.Add(1); n > c.maxActive.Load() {
		c.maxActive.Store(n)
	}
	defer c.active.Add(-1)
	select {
	case <-time.After(20 * time.Millisecond): // a real printer takes a moment to answer
	case <-ctx.Done():
		return domain.Status{}, ctx.Err()
	}
	return domain.Status{State: domain.StateIdle, ObservedAt: time.Now()}, nil
}

var counting = map[string]*countingDriver{}

func init() {
	driver.Register("counting-test", func(p domain.Printer, _ driver.Options) (driver.Driver, error) {
		d := &countingDriver{}
		counting[p.PrinterID] = d
		return d, nil
	})
}

func pollManager(t *testing.T, interval time.Duration) *Manager {
	t.Helper()
	m, err := NewManager(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), observ.NewBus(),
		ManagerOpts{PollInterval: interval})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

func add(t *testing.T, m *Manager, id string) {
	t.Helper()
	if err := m.Registry().Upsert(SourceConfig, "", domain.Printer{PrinterID: id, Driver: "counting-test",
		Host: "h", Enabled: true}); err != nil {
		t.Fatal(err)
	}
}

// The server starts like this: Sync, then StartPolling. Each printer must still get
// exactly one poller — two share one status socket and read each other's replies.
func TestStartupGivesEachPrinterOnePoller(t *testing.T) {
	m := pollManager(t, 100*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	add(t, m, "p1")
	if err := m.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	m.StartPolling(ctx)
	time.Sleep(550 * time.Millisecond)

	d := counting["p1"]
	if d.maxActive.Load() > 1 {
		t.Fatalf("%d status probes ran at once on one printer", d.maxActive.Load())
	}
	// One poller: an immediate probe plus about one per interval.
	if n := d.probes.Load(); n > 8 {
		t.Fatalf("%d probes in 550ms at a 100ms interval: more than one poller", n)
	}
}

// A printer added from an API request (adopted, or edited) must keep being polled
// after the request is over.
func TestPrinterAddedDuringARequestKeepsBeingPolled(t *testing.T) {
	m := pollManager(t, 50*time.Millisecond)
	reqCtx, endRequest := context.WithCancel(context.Background())
	add(t, m, "p2")
	if err := m.Sync(reqCtx); err != nil {
		t.Fatal(err)
	}
	endRequest() // the HTTP handler returns

	time.Sleep(30 * time.Millisecond)
	before := counting["p2"].probes.Load()
	time.Sleep(300 * time.Millisecond)
	if after := counting["p2"].probes.Load(); after <= before {
		t.Fatalf("polling stopped with the request: %d probes before, %d after", before, after)
	}
	if p, _ := m.Get("p2"); p.Cached().State != domain.StateIdle {
		t.Fatalf("printer state %s", p.Cached().State)
	}
}

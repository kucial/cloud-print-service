// Package printer owns connectivity to physical printers and the status cache.
package driver

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/kucial/cloud-print-service/pkg/printer"
	"github.com/kucial/cloud-print-service/server/internal/domain"
)

// StatusUnsupported is returned by drivers whose model cannot report status.
// The service degrades to TCP-reachability-only rather than inventing a state.
var StatusUnsupported = fmt.Errorf("status query not supported by this driver")

// Driver is the only vendor-specific surface in the service.
//
// Contract that the job layer relies on:
//   - Send must report the number of bytes actually flushed.
//   - If Send returns bytes > 0 together with an error, the printer may have received a
//     partial label, so the caller must NOT retry. Zero bytes means "safe to retry".
//
// What only some printers can do lives in optional interfaces (LabelTools,
// DocumentSender, JobTracker), found by type assertion: a receipt printer has no
// label roll to calibrate, and pretending otherwise would fail at the printer instead
// of in the API.
type Driver interface {
	Send(ctx context.Context, data []byte) (int, error)
	Status(ctx context.Context) (domain.Status, error)
	Cancel(ctx context.Context) error
	Identify(ctx context.Context) (map[string]string, error)
	Close() error
}

// LabelTools is a Zebra's host-query and maintenance surface.
type LabelTools interface {
	// HostConfig returns the printer's configuration dump (^HH), raw.
	HostConfig(ctx context.Context) (string, error)
	// HostDirectory lists stored objects (^HW) — fonts, encoding tables — raw.
	HostDirectory(ctx context.Context) (string, error)
	// Control sends a maintenance command such as ~JC (calibrate).
	Control(ctx context.Context, cmd string) error
}

// Doc is one document with what a job-oriented printer needs to know about it.
type Doc struct {
	Data   []byte
	Format printer.Format
	Copies int
	Name   string // job name shown on the printer, e.g. the CPS job id
}

// Sent is a document a DocumentSender handed over: Bytes as for Send, Ref the
// printer's own job id (for JobTracker).
type Sent struct {
	Bytes int
	Ref   string
}

// DocumentSender is a printer that takes whole documents with attributes (IPP): the
// format, and copies as a number rather than repeated bytes.
type DocumentSender interface {
	SendDocument(ctx context.Context, doc Doc) (Sent, error)
}

// ErrNotSupported is returned for an operation the printer's kind does not have.
var ErrNotSupported = errors.New("not supported by this printer")

// Options are the timeouts the fleet manager runs drivers with.
type Options struct {
	DialTimeout  time.Duration
	WriteTimeout time.Duration
	ReadTimeout  time.Duration
}

func (o Options) withDefaults() Options {
	if o.DialTimeout <= 0 {
		o.DialTimeout = 3 * time.Second
	}
	if o.WriteTimeout <= 0 {
		o.WriteTimeout = 5 * time.Second
	}
	if o.ReadTimeout <= 0 {
		o.ReadTimeout = 3 * time.Second
	}
	return o
}

// Factory builds a driver for a declared printer.
type Factory func(p domain.Printer, o Options) (Driver, error)

// Capabilities names what d can do beyond printing, for the API and the panel.
func Capabilities(d Driver) []string {
	caps := []string{"test_print"}
	if _, ok := d.(LabelTools); ok {
		caps = append(caps, "diagnostics", "maintenance", "fonts")
	}
	return caps
}

var (
	regMu sync.RWMutex
	reg   = map[string]Factory{}
)

func Register(name string, f Factory) {
	regMu.Lock()
	defer regMu.Unlock()
	reg[name] = f
}

func Drivers() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(reg))
	for k := range reg {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// New builds the driver declared by p.Driver.
func New(p domain.Printer, o Options) (Driver, error) {
	regMu.RLock()
	f, ok := reg[p.Driver]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("printer %s: unknown driver %q (known: %v)", p.PrinterID, p.Driver, Drivers())
	}
	return f(p, o.withDefaults())
}

// Package printer owns connectivity to physical printers and the status cache.
package driver

import (
	"context"
	"fmt"
	"sort"
	"sync"

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
type Driver interface {
	Send(ctx context.Context, data []byte) (int, error)
	Status(ctx context.Context) (domain.Status, error)
	Cancel(ctx context.Context) error
	Identify(ctx context.Context) (map[string]string, error)
	Close() error
}

// Factory builds a driver for a declared printer.
type Factory func(p domain.Printer) (Driver, error)

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
func New(p domain.Printer) (Driver, error) {
	regMu.RLock()
	f, ok := reg[p.Driver]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("printer %s: unknown driver %q (known: %v)", p.PrinterID, p.Driver, Drivers())
	}
	return f(p)
}

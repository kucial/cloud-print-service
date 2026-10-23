package driver

import (
	"context"
	"time"

	"github.com/kucial/cloud-print-service/pkg/printer"
	"github.com/kucial/cloud-print-service/server/internal/domain"
)

func init() {
	Register(printer.DriverESCPR, func(p domain.Printer, o Options) (Driver, error) { return NewESCPR(p, o), nil })
}

// ESCPR drives an Epson inkjet on raw TCP 9100. The job is already ESC/P-R raster (the
// converter renders documents to it); the printer answers no status query on this
// port, and anything it does not understand may come out as a page, so the driver
// never sends one: status is whether the port opens.
type ESCPR struct {
	link *tcpLink
}

func NewESCPR(p domain.Printer, o Options) *ESCPR { return &ESCPR{link: newTCPLink(p.Host, p.Port, o)} }

func (d *ESCPR) Send(ctx context.Context, data []byte) (int, error) { return d.link.send(ctx, data) }

func (d *ESCPR) Status(ctx context.Context) (domain.Status, error) {
	now := time.Now()
	c, err := d.link.dial(ctx)
	if err != nil {
		return domain.Status{State: domain.StateOffline, ObservedAt: now, Detail: "printer port does not open"}, err
	}
	_ = c.Close()
	return domain.Status{State: domain.StateUnknown, ObservedAt: now,
		Detail: "reachable; ESC/P-R printers report no status over 9100"}, nil
}

// Cancel is not offered: a job already handed over is in the printer, and the only
// way to stop it is the printer's own cancel button.
func (d *ESCPR) Cancel(ctx context.Context) error { return ErrNotSupported }

func (d *ESCPR) Identify(ctx context.Context) (map[string]string, error) {
	return map[string]string{"language": "escpr"}, nil
}

func (d *ESCPR) Close() error { return d.link.close() }

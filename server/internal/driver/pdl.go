package driver

import (
	"context"
	"fmt"
	"time"

	"github.com/kucial/cloud-print-service/pkg/printer"
	"github.com/kucial/cloud-print-service/server/internal/domain"
)

func init() {
	Register(printer.DriverPDL, func(p domain.Printer, o Options) (Driver, error) { return NewPDL(p, o), nil })
}

// PDL drives an office printer on raw TCP 9100 (JetDirect): the document goes as is,
// in a language the printer reads (PCL, PostScript, PDF on printers that take it
// directly). Status comes from PJL when the printer speaks it — most HP and
// HP-compatible printers do — and is "reachable, state unknown" otherwise.
type PDL struct {
	link *tcpLink
}

func NewPDL(p domain.Printer, o Options) *PDL { return &PDL{link: newTCPLink(p.Host, p.Port, o)} }

func (d *PDL) Send(ctx context.Context, data []byte) (int, error) { return d.link.send(ctx, data) }

func (d *PDL) pjl(ctx context.Context, cmd []byte) ([]byte, error) {
	return d.link.query(ctx, cmd, 4096, printer.PJLReplyComplete)
}

func (d *PDL) Status(ctx context.Context) (domain.Status, error) {
	now := time.Now()
	raw, err := d.pjl(ctx, printer.CmdPJLStatus)
	if err != nil {
		if isTimeout(err) && d.reachable(ctx) {
			// Connected, but no PJL: printers without it still print.
			return domain.Status{State: domain.StateUnknown, ObservedAt: now,
				Detail: "printer does not answer PJL status; reachable"}, nil
		}
		return domain.Status{State: domain.StateOffline, ObservedAt: now, Detail: "status query failed"}, err
	}
	st, ok := printer.ParsePJLStatus(raw)
	if !ok {
		return domain.Status{State: domain.StateUnknown, Raw: string(raw), ObservedAt: now,
			Detail: "unrecognised PJL status reply"}, nil
	}
	return domain.Status{State: st.State(), Raw: fmt.Sprint(st.Code), Detail: st.Detail(), ObservedAt: now}, nil
}

// reachable reports whether a fresh connection opens: what is left to know about a
// printer that does not answer PJL.
func (d *PDL) reachable(ctx context.Context) bool {
	c, err := d.link.dial(ctx)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// Cancel sends the UEL, which ends the current job on PJL printers.
func (d *PDL) Cancel(ctx context.Context) error {
	return d.link.control(ctx, []byte("\x1b%-12345X"))
}

func (d *PDL) Identify(ctx context.Context) (map[string]string, error) {
	raw, err := d.pjl(ctx, printer.CmdPJLID)
	if err != nil {
		return map[string]string{"language": "pdl"}, fmt.Errorf("no PJL identification: %w", err)
	}
	info := map[string]string{"language": "pjl"}
	if model, ok := printer.ParsePJLID(raw); ok {
		info["model"] = model
	}
	return info, nil
}

// Formats asks the printer which page languages it has (@PJL INFO CONFIG).
func (d *PDL) Formats(ctx context.Context) ([]printer.Format, error) {
	raw, err := d.pjl(ctx, printer.CmdPJLConfig)
	if err != nil {
		return nil, err
	}
	return printer.ParsePJLLanguages(raw), nil
}

func (d *PDL) Close() error { return d.link.close() }

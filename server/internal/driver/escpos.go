package driver

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kucial/cloud-print-service/pkg/printer"
	"github.com/kucial/cloud-print-service/server/internal/domain"
)

func init() {
	Register(printer.DriverESCPOS, func(p domain.Printer, o Options) (Driver, error) { return NewESCPOS(p, o), nil })
}

// ESCPOS drives a receipt printer that takes ESC/POS on raw TCP 9100 — network models,
// and USB ones behind the agent.
//
// Status comes from the real-time requests (DLE EOT 1, 2, 4), which a printer answers
// with one byte each even while it is offline or printing. There is no "printing" bit,
// so a job is confirmed when the printer is still ready once the document is in.
type ESCPOS struct {
	link *tcpLink
}

func NewESCPOS(p domain.Printer, o Options) *ESCPOS {
	return &ESCPOS{link: newTCPLink(p.Host, p.Port, o)}
}

func (e *ESCPOS) Send(ctx context.Context, data []byte) (int, error) { return e.link.send(ctx, data) }

// statusByte asks one DLE EOT request and returns its one-byte answer.
func (e *ESCPOS) statusByte(ctx context.Context, cmd []byte) (byte, error) {
	raw, err := e.link.query(ctx, cmd, 1, func(acc []byte) bool { return len(acc) >= 1 })
	if err != nil {
		return 0, err
	}
	return raw[0], nil
}

func (e *ESCPOS) Status(ctx context.Context) (domain.Status, error) {
	now := time.Now()
	var b [3]byte
	for i, cmd := range [][]byte{printer.CmdESCPOSPrinterStatus, printer.CmdESCPOSOfflineCause, printer.CmdESCPOSPaperStatus} {
		v, err := e.statusByte(ctx, cmd)
		if err != nil {
			if i > 0 && isTimeout(err) {
				// It answered DLE EOT 1 but not the next: a partial implementation.
				// Reachable, state unknown — rather than calling it offline.
				return domain.Status{State: domain.StateUnknown, ObservedAt: now,
					Detail: "printer answers only part of the ESC/POS status requests"}, nil
			}
			if isTimeout(err) && e.reachable(ctx) {
				// Connected but silent: a printer that cannot report status (write-only
				// behind the agent, or one without real-time status) still prints.
				return domain.Status{State: domain.StateUnknown, ObservedAt: now,
					Detail: "printer does not answer ESC/POS status requests; reachable"}, nil
			}
			return domain.Status{State: domain.StateOffline, ObservedAt: now, Detail: "status query failed"}, err
		}
		b[i] = v
	}
	st, ok := printer.ParseESCPOSStatus(b[0], b[1], b[2])
	raw := fmt.Sprintf("%02x %02x %02x", b[0], b[1], b[2])
	if !ok {
		return domain.Status{State: domain.StateUnknown, Raw: raw, ObservedAt: now,
			Detail: "unrecognised ESC/POS status reply"}, nil
	}
	return domain.Status{State: st.State(), Raw: raw, Detail: st.Detail(), ObservedAt: now}, nil
}

// Cancel is not possible: ESC/POS has no command that drops a document already in the
// printer's buffer. A queued job is still cancelled by the job service before sending.
func (e *ESCPOS) Cancel(ctx context.Context) error {
	return fmt.Errorf("ESC/POS printers cannot cancel a receipt already sent: %w", ErrNotSupported)
}

// Identify asks for the model and maker name (GS I 67/66). Printers that do not
// implement them still identify as reachable ESC/POS printers.
func (e *ESCPOS) Identify(ctx context.Context) (map[string]string, error) {
	info := map[string]string{"language": "escpos"}
	ask := func(cmd []byte) (string, error) {
		raw, err := e.link.query(ctx, cmd, 128, printer.ESCPOSInfoComplete)
		if err != nil {
			return "", err
		}
		text, _ := printer.ParseESCPOSInfo(raw)
		return text, nil
	}
	model, err := ask(printer.CmdESCPOSModelName)
	if err != nil && !isTimeout(err) {
		return nil, err
	}
	if model != "" {
		info["model"] = model
	}
	if maker, err := ask(printer.CmdESCPOSMakerName); err == nil && maker != "" {
		info["vendor"] = maker
	}
	if _, err := e.statusByte(ctx, printer.CmdESCPOSPrinterStatus); err != nil {
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			return info, fmt.Errorf("no ESC/POS status reply: %w", err)
		}
		return info, err
	}
	return info, nil
}

// reachable reports whether a fresh connection opens.
func (e *ESCPOS) reachable(ctx context.Context) bool {
	c, err := e.link.dial(ctx)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func (e *ESCPOS) Close() error { return e.link.close() }

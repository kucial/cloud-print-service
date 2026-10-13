package driver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kucial/cloud-print-service/pkg/ipp"
	"github.com/kucial/cloud-print-service/pkg/printer"
	"github.com/kucial/cloud-print-service/server/internal/domain"
)

func init() {
	Register(printer.DriverIPP, func(p domain.Printer, o Options) (Driver, error) { return NewIPP(p, o), nil })
}

// IPP drives an office printer over the Internet Printing Protocol (IPP Everywhere,
// AirPrint and most network laser and inkjet printers). Unlike a raw socket, the
// printer answers every document with a job of its own, so CPS can follow the job to
// "completed" instead of reading status bits.
type IPP struct {
	c *ipp.Client

	mu      sync.Mutex
	lastJob int // the printer's id for the newest job, for Cancel
}

func NewIPP(p domain.Printer, o Options) *IPP {
	o = o.withDefaults()
	hc := &http.Client{Transport: &http.Transport{
		Proxy:                 nil, // printers are on the local network; never via a proxy
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		DisableCompression:    true,
	}}
	port := p.Port
	if port == 0 {
		port = 631
	}
	c := ipp.NewClient(p.Host, port, p.Path, hc)
	return &IPP{c: c}
}

// Send prints data, letting the printer recognise its format.
func (d *IPP) Send(ctx context.Context, data []byte) (int, error) {
	s, err := d.SendDocument(ctx, Doc{Data: data, Format: "", Copies: 1, Name: "cps"})
	return s.Bytes, err
}

// SendDocument is Print-Job. The byte count follows the Send contract: zero when the
// printer refused the job (it answered, and nothing will print), what left the
// network otherwise.
func (d *IPP) SendDocument(ctx context.Context, doc Doc) (Sent, error) {
	mime := "application/octet-stream"
	if doc.Format != "" {
		mime = doc.Format.MIME()
	}
	id, sent, err := d.c.PrintJob(ctx, doc.Data, mime, doc.Copies, doc.Name)
	if err != nil {
		var se *ipp.StatusError
		if errors.As(err, &se) {
			if se.Permanent() {
				return Sent{}, fmt.Errorf("%w: %v", ErrRejected, se)
			}
			return Sent{}, se // busy or not accepting jobs: retry later
		}
		return Sent{Bytes: sent}, err
	}
	d.mu.Lock()
	d.lastJob = id
	d.mu.Unlock()
	return Sent{Bytes: len(doc.Data), Ref: strconv.Itoa(id)}, nil
}

// Track is Get-Job-Attributes.
func (d *IPP) Track(ctx context.Context, sent Sent) (Tracked, error) {
	id, err := strconv.Atoi(sent.Ref)
	if err != nil {
		return Tracked{}, fmt.Errorf("no IPP job id: %q", sent.Ref)
	}
	state, reasons, err := d.c.GetJob(ctx, id)
	if err != nil {
		return Tracked{}, err
	}
	t := Tracked{Detail: fmt.Sprintf("job-state=%s reasons=%s", jobStateName(state), strings.Join(reasons, ","))}
	switch state {
	case ipp.JobCompleted:
		t.Done, t.Printed = true, true
	case ipp.JobCanceled, ipp.JobAborted:
		t.Done = true
	}
	return t, nil
}

func jobStateName(s int) string {
	switch s {
	case ipp.JobPending:
		return "pending"
	case ipp.JobPendingHeld:
		return "pending-held"
	case ipp.JobProcessing:
		return "processing"
	case ipp.JobProcessingStopped:
		return "processing-stopped"
	case ipp.JobCanceled:
		return "canceled"
	case ipp.JobAborted:
		return "aborted"
	case ipp.JobCompleted:
		return "completed"
	}
	return strconv.Itoa(s)
}

func (d *IPP) Status(ctx context.Context) (domain.Status, error) {
	now := time.Now()
	info, err := d.c.GetPrinterAttributes(ctx)
	if err != nil {
		return domain.Status{State: domain.StateOffline, ObservedAt: now, Detail: "status query failed"}, err
	}
	st, detail := ipp.State(info)
	return domain.Status{State: st, Detail: detail, Raw: strings.Join(info.Reasons, ","), ObservedAt: now}, nil
}

// Cancel cancels the newest job CPS sent, if the printer still has it.
func (d *IPP) Cancel(ctx context.Context) error {
	d.mu.Lock()
	id := d.lastJob
	d.mu.Unlock()
	if id == 0 {
		return errors.New("no job to cancel")
	}
	return d.c.CancelJob(ctx, id)
}

// Identify reports model, serial and the formats the printer takes.
func (d *IPP) Identify(ctx context.Context) (map[string]string, error) {
	info, err := d.c.GetPrinterAttributes(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]string{"language": "ipp", "model": info.MakeModel, "name": info.Name}
	if info.Serial != "" {
		out["serial"] = info.Serial
	}
	var fs []string
	for _, f := range ipp.Formats(info.Formats) {
		fs = append(fs, string(f))
	}
	out["formats"] = strings.Join(fs, ",")
	return out, nil
}

// Formats is what the printer says it takes (document-format-supported).
func (d *IPP) Formats(ctx context.Context) ([]printer.Format, error) {
	info, err := d.c.GetPrinterAttributes(ctx)
	if err != nil {
		return nil, err
	}
	return ipp.Formats(info.Formats), nil
}

func (d *IPP) Close() error {
	d.c.HTTP.CloseIdleConnections()
	return nil
}

package ipp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/kucial/cloud-print-service/pkg/printer"
)

// Client talks to one IPP printer.
type Client struct {
	// URL is where requests are POSTed: http://host:631/ipp/print.
	URL string
	// PrinterURI names the printer inside requests: ipp://host:631/ipp/print.
	PrinterURI string
	HTTP       *http.Client
	User       string // requesting-user-name, shown in the printer's job list

	nextID atomic.Uint32
}

// NewClient builds a client for host:port and resource path (e.g. "ipp/print").
func NewClient(host string, port int, path string, hc *http.Client) *Client {
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		path = "ipp/print"
	}
	hp := fmt.Sprintf("%s:%d", host, port)
	if strings.Contains(host, ":") { // IPv6 literal
		hp = fmt.Sprintf("[%s]:%d", host, port)
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{URL: "http://" + hp + "/" + path, PrinterURI: "ipp://" + hp + "/" + path, HTTP: hc, User: "cps"}
}

// StatusError is a response whose status code is not successful.
type StatusError struct {
	Op      uint16
	Code    uint16
	Message string
}

func (e *StatusError) Error() string {
	msg := fmt.Sprintf("IPP operation 0x%04x: %s 0x%04x", e.Op, StatusClass(e.Code), e.Code)
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

// Permanent reports whether retrying the same request cannot succeed (a client
// error such as an unsupported format), as opposed to a busy printer.
func (e *StatusError) Permanent() bool { return StatusClass(e.Code) == "client-error" }

// countingReader counts what the HTTP transport has taken from the body: what may
// have reached the printer.
type countingReader struct {
	r io.Reader
	n atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// Do sends a request and decodes the response. sent is how many bytes of the request
// went out, so a caller can tell "never sent" from "maybe printed".
func (c *Client) Do(ctx context.Context, req *Message) (resp *Message, sent int, err error) {
	if req.RequestID == 0 {
		req.RequestID = c.nextID.Add(1)
	}
	body := &countingReader{r: bytes.NewReader(req.Encode())}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, body)
	if err != nil {
		return nil, 0, err
	}
	hreq.Header.Set("Content-Type", "application/ipp")
	hres, err := c.HTTP.Do(hreq)
	if err != nil {
		return nil, int(body.n.Load()), err
	}
	defer hres.Body.Close()
	sent = int(body.n.Load())
	raw, err := io.ReadAll(io.LimitReader(hres.Body, 4<<20))
	if err != nil {
		return nil, sent, err
	}
	if hres.StatusCode != http.StatusOK {
		return nil, sent, fmt.Errorf("IPP over HTTP: %s", hres.Status)
	}
	resp, err = Decode(raw)
	if err != nil {
		return nil, sent, err
	}
	if StatusClass(resp.Code) != "successful" {
		return resp, sent, &StatusError{Op: req.Code, Code: resp.Code, Message: resp.String("status-message")}
	}
	return resp, sent, nil
}

func (c *Client) request(op uint16) *Message {
	m := NewRequest(op, 0, c.PrinterURI)
	m.Add(TagOperation, "requesting-user-name", Name(c.User))
	return m
}

// PrinterInfo is what CPS reads from Get-Printer-Attributes.
type PrinterInfo struct {
	State     int      // 3 idle, 4 processing, 5 stopped
	Reasons   []string // printer-state-reasons
	Message   string   // printer-state-message
	MakeModel string
	Name      string
	Accepting bool
	Formats   []string // document-format-supported (MIME types)
	Serial    string   // from printer-device-id, when it has one
}

// Printer attributes CPS asks for (asking for "all" can be hundreds of kilobytes).
var printerAttributes = []string{
	"printer-state", "printer-state-reasons", "printer-state-message", "printer-make-and-model",
	"printer-name", "printer-is-accepting-jobs", "document-format-supported", "printer-device-id",
}

// GetPrinterAttributes reads the printer's state and capabilities.
func (c *Client) GetPrinterAttributes(ctx context.Context) (PrinterInfo, error) {
	req := c.request(OpGetPrinterAttributes)
	vals := make([]Value, len(printerAttributes))
	for i, a := range printerAttributes {
		vals[i] = Keyword(a)
	}
	req.Add(TagOperation, "requested-attributes", vals...)
	resp, _, err := c.Do(ctx, req)
	if err != nil {
		return PrinterInfo{}, err
	}
	return InfoFrom(resp), nil
}

// InfoFrom reads the printer attributes out of a response.
func InfoFrom(resp *Message) PrinterInfo {
	info := PrinterInfo{
		Reasons:   resp.Strings("printer-state-reasons"),
		Message:   resp.String("printer-state-message"),
		MakeModel: resp.String("printer-make-and-model"),
		Name:      resp.String("printer-name"),
		Formats:   resp.Strings("document-format-supported"),
		Accepting: true,
	}
	info.State, _ = resp.Int("printer-state")
	if v, ok := resp.Int("printer-is-accepting-jobs"); ok {
		info.Accepting = v != 0
	}
	for _, kv := range strings.Split(resp.String("printer-device-id"), ";") {
		if k, v, ok := strings.Cut(kv, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "SN") {
			info.Serial = strings.TrimSpace(v)
		}
	}
	return info
}

// PrintJob sends a document. sent counts request bytes that left, for the "may have
// printed" rule; jobID is the printer's id for the job.
func (c *Client) PrintJob(ctx context.Context, data []byte, mime string, copies int, name string) (jobID, sent int, err error) {
	req := c.request(OpPrintJob)
	req.Add(TagOperation, "job-name", Name(name))
	req.Add(TagOperation, "document-format", MimeType(mime))
	if copies > 1 {
		req.Add(TagJob, "copies", Integer(copies))
	}
	req.Data = data
	resp, sent, err := c.Do(ctx, req)
	if err != nil {
		return 0, sent, err
	}
	id, ok := resp.Int("job-id")
	if !ok {
		return 0, sent, errors.New("printer accepted the job but returned no job-id")
	}
	return id, sent, nil
}

// Job states (job-state).
const (
	JobPending           = 3
	JobPendingHeld       = 4
	JobProcessing        = 5
	JobProcessingStopped = 6
	JobCanceled          = 7
	JobAborted           = 8
	JobCompleted         = 9
)

// GetJob reads a job's state and the reasons for it.
func (c *Client) GetJob(ctx context.Context, jobID int) (state int, reasons []string, err error) {
	req := c.request(OpGetJobAttributes)
	req.Add(TagOperation, "job-id", Integer(jobID))
	req.Add(TagOperation, "requested-attributes", Keyword("job-state"), Keyword("job-state-reasons"))
	resp, _, err := c.Do(ctx, req)
	if err != nil {
		return 0, nil, err
	}
	state, _ = resp.Int("job-state")
	return state, resp.Strings("job-state-reasons"), nil
}

// CancelJob cancels a job on the printer.
func (c *Client) CancelJob(ctx context.Context, jobID int) error {
	req := c.request(OpCancelJob)
	req.Add(TagOperation, "job-id", Integer(jobID))
	_, _, err := c.Do(ctx, req)
	return err
}

// Formats maps document-format-supported to the formats CPS names; types CPS has no
// name for are left out.
func Formats(mimes []string) []printer.Format {
	var out []printer.Format
	seen := map[printer.Format]bool{}
	for _, m := range mimes {
		if f, ok := printer.ParseFormat(m); ok && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// State maps printer-state and printer-state-reasons to the vendor-neutral state.
// Reasons ending in -report are informational; -warning ones never block, so an
// "empty" warning reads as "low".
func State(info PrinterInfo) (printer.State, string) {
	type hit struct {
		state printer.State
		why   string
	}
	var blocking, warning []hit
	for _, r := range info.Reasons {
		base, severity := r, "error"
		for _, s := range []string{"-error", "-warning", "-report"} {
			if strings.HasSuffix(r, s) {
				base, severity = strings.TrimSuffix(r, s), s[1:]
			}
		}
		if base == "none" || severity == "report" {
			continue
		}
		var st printer.State
		switch base {
		case "media-empty", "media-needed", "input-tray-missing":
			st = printer.StateMediaOut
		case "media-jam", "output-area-full":
			st = printer.StatePaperJam
		case "door-open", "cover-open", "interlock-open":
			st = printer.StateDoorOpen
		case "toner-empty", "marker-supply-empty":
			st = printer.StateTonerOut
		case "toner-low", "marker-supply-low":
			st = printer.StateTonerLow
		case "media-low":
			st = printer.StateMediaLow
		case "offline", "shutdown", "connecting-to-device":
			st = printer.StateOffline
		default:
			continue
		}
		if severity == "warning" {
			switch st {
			case printer.StateMediaOut:
				st = printer.StateMediaLow
			case printer.StateTonerOut:
				st = printer.StateTonerLow
			}
			if st.BlocksUse() {
				continue // e.g. door-open-warning: the printer still prints
			}
		}
		if st.BlocksUse() {
			blocking = append(blocking, hit{st, r})
		} else {
			warning = append(warning, hit{st, r})
		}
	}
	detail := fmt.Sprintf("printer-state=%d reasons=%s", info.State, strings.Join(info.Reasons, ","))
	if info.Message != "" {
		detail += " message=" + info.Message
	}
	switch {
	case len(blocking) > 0:
		return blocking[0].state, detail
	case info.State == 5 || !info.Accepting:
		return printer.StateError, detail // stopped for a reason it did not name
	case info.State == 4:
		return printer.StatePrinting, detail
	case len(warning) > 0:
		return warning[0].state, detail
	case info.State == 3:
		return printer.StateIdle, detail
	}
	return printer.StateUnknown, detail
}

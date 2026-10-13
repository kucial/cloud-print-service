package virtual

import (
	"io"
	"net"
	"net/http"
	"slices"
	"time"

	"github.com/kucial/cloud-print-service/pkg/ipp"
)

// IPP mode: the virtual printer behaves like an office printer that speaks IPP over
// HTTP (IPP Everywhere / AirPrint style). It takes Print-Job, follows each job through
// pending, processing and completed, and reports state reasons (media-empty,
// door-open, toner-low, …) the way real printers do.

const ippLanguage = "ipp"

// DefaultIPPFormats is what a virtual office printer accepts unless told otherwise.
var DefaultIPPFormats = []string{"application/pdf", "image/urf", "image/jpeg", "application/octet-stream"}

type ippJob struct {
	id         int
	acceptedAt time.Time
	state      int
	reasons    []string
}

// ippReasons is printer-state-reasons for the current state. Caller holds p.mu.
func (p *Printer) ippReasons() []string {
	var r []string
	if p.media == 2 {
		r = append(r, "media-empty-error")
	} else if p.media == 1 {
		r = append(r, "media-low-warning")
	}
	if p.door {
		r = append(r, "door-open-error")
	}
	if p.jam {
		r = append(r, "media-jam-error")
	}
	switch p.toner {
	case 1:
		r = append(r, "toner-low-warning")
	case 2:
		r = append(r, "toner-empty-error")
	}
	if p.device == 3 {
		r = append(r, "other-error")
	}
	if len(r) == 0 {
		r = []string{"none"}
	}
	return r
}

// ippBlocked reports whether the printer cannot print now. Caller holds p.mu.
func (p *Printer) ippBlocked() bool {
	return p.media == 2 || p.door || p.jam || p.toner == 2 || p.device == 3
}

// advanceJobs moves jobs along: a job completes once it has printed for the print
// duration while nothing blocked the printer; it waits (processing-stopped) while
// something does. Caller holds p.mu.
func (p *Printer) advanceJobs(now time.Time) {
	for _, j := range p.ippJobs {
		if j.state >= ipp.JobCanceled {
			continue
		}
		switch {
		case p.ippBlocked():
			j.state, j.reasons = ipp.JobProcessingStopped, p.ippReasons()
		case p.abortJobs:
			j.state, j.reasons = ipp.JobAborted, []string{"aborted-by-system"}
		case now.Sub(j.acceptedAt) >= p.printDuration:
			j.state, j.reasons = ipp.JobCompleted, []string{"job-completed-successfully"}
		default:
			j.state, j.reasons = ipp.JobProcessing, []string{"job-printing"}
		}
	}
}

func (p *Printer) ippState() int {
	switch {
	case p.ippBlocked():
		return 5 // stopped
	case p.sticky:
		return 4
	}
	for _, j := range p.ippJobs {
		if j.state == ipp.JobProcessing || j.state == ipp.JobPending {
			return 4 // processing
		}
	}
	return 3 // idle
}

func (p *Printer) serveIPP(ln net.Listener) {
	srv := &http.Server{Handler: http.HandlerFunc(p.handleIPP), ReadHeaderTimeout: 10 * time.Second}
	p.mu.Lock()
	p.ippSrv = srv
	p.mu.Unlock()
	_ = srv.Serve(ln)
}

// dropIPP closes the HTTP server and every connection it holds: an offline printer
// answers nothing, not even on a keep-alive connection. Caller must not hold p.mu.
func (p *Printer) dropIPP() {
	p.mu.Lock()
	srv := p.ippSrv
	p.ippSrv = nil
	p.mu.Unlock()
	if srv != nil {
		_ = srv.Close()
	}
}

func (p *Printer) handleIPP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/ipp" {
		http.Error(w, "IPP only", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	closeOnPrint := p.closeOnPrint
	p.mu.Unlock()
	raw, err := io.ReadAll(io.LimitReader(r.Body, 256<<20))
	if err != nil {
		return
	}
	p.mu.Lock()
	p.bytesIn += len(raw)
	p.mu.Unlock()
	req, err := ipp.Decode(raw)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if closeOnPrint && req.Code == ipp.OpPrintJob {
		// The printer dies after taking the document, before it answers.
		if hj, ok := w.(http.Hijacker); ok {
			if c, _, err := hj.Hijack(); err == nil {
				_ = c.Close()
			}
		}
		return
	}
	resp := p.answerIPP(req)
	w.Header().Set("Content-Type", "application/ipp")
	_, _ = w.Write(resp.Encode())
}

func (p *Printer) answerIPP(req *ipp.Message) *ipp.Message {
	resp := &ipp.Message{Major: 2, Code: ipp.StatusOK, RequestID: req.RequestID}
	resp.Add(ipp.TagOperation, "attributes-charset", ipp.Value{Tag: ipp.TagCharset, Str: "utf-8"})
	resp.Add(ipp.TagOperation, "attributes-natural-language", ipp.Value{Tag: ipp.TagLanguage, Str: "en"})
	fail := func(code uint16, msg string) *ipp.Message {
		resp.Code = code
		resp.Add(ipp.TagOperation, "status-message", ipp.Text(msg))
		return resp
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.advanceJobs(time.Now())
	formats := p.opts.IPPFormats
	if len(formats) == 0 {
		formats = DefaultIPPFormats
	}

	switch req.Code {
	case ipp.OpGetPrinterAttributes:
		resp.Add(ipp.TagPrinter, "printer-state", ipp.Enum(p.ippState()))
		reasons := p.ippReasons()
		vals := make([]ipp.Value, len(reasons))
		for i, r := range reasons {
			vals[i] = ipp.Keyword(r)
		}
		resp.Add(ipp.TagPrinter, "printer-state-reasons", vals...)
		resp.Add(ipp.TagPrinter, "printer-make-and-model", ipp.Text(p.Model))
		resp.Add(ipp.TagPrinter, "printer-name", ipp.Name(p.ID))
		resp.Add(ipp.TagPrinter, "printer-is-accepting-jobs", ipp.Bool(true))
		resp.Add(ipp.TagPrinter, "printer-device-id", ipp.Text("MFG:VPRINT;MDL:"+p.Model+";SN:"+p.Serial+";"))
		fv := make([]ipp.Value, len(formats))
		for i, f := range formats {
			fv[i] = ipp.MimeType(f)
		}
		resp.Add(ipp.TagPrinter, "document-format-supported", fv...)
		return resp

	case ipp.OpPrintJob:
		format := req.String("document-format")
		if format == "" {
			format = "application/octet-stream"
		}
		if !slices.Contains(formats, format) {
			return fail(ipp.StatusDocumentFormatUnsupported, "document-format "+format+" is not supported")
		}
		copies, _ := req.Int("copies")
		if copies < 1 {
			copies = 1
		}
		p.nextIPPJob++
		j := &ippJob{id: p.nextIPPJob, acceptedAt: time.Now(), state: ipp.JobPending}
		p.ippJobs = append(p.ippJobs, j)
		p.labelCount++
		p.labels = append(p.labels, Label{
			Index: p.labelCount, ReceivedAt: time.Now().UTC(), Bytes: len(req.Data),
			Language: ippLanguage, Format: format, Copies: copies, Text: req.String("job-name"),
			Data: append([]byte(nil), req.Data...),
		})
		resp.Add(ipp.TagJob, "job-id", ipp.Integer(j.id))
		resp.Add(ipp.TagJob, "job-state", ipp.Enum(j.state))
		return resp

	case ipp.OpGetJobAttributes, ipp.OpCancelJob:
		id, _ := req.Int("job-id")
		var j *ippJob
		for _, x := range p.ippJobs {
			if x.id == id {
				j = x
			}
		}
		if j == nil {
			return fail(ipp.StatusClientNotFound, "no such job")
		}
		if req.Code == ipp.OpCancelJob {
			if j.state >= ipp.JobCanceled {
				return fail(0x040c, "job is already finished") // client-error-not-possible
			}
			j.state, j.reasons = ipp.JobCanceled, []string{"job-canceled-by-user"}
			return resp
		}
		resp.Add(ipp.TagJob, "job-id", ipp.Integer(j.id))
		resp.Add(ipp.TagJob, "job-state", ipp.Enum(j.state))
		rv := make([]ipp.Value, len(j.reasons))
		for i, r := range j.reasons {
			rv[i] = ipp.Keyword(r)
		}
		if len(rv) > 0 {
			resp.Add(ipp.TagJob, "job-state-reasons", rv...)
		}
		return resp
	}
	return fail(0x0501, "operation not supported") // server-error-operation-not-supported
}

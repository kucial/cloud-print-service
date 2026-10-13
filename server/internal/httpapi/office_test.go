package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/kucial/cloud-print-service/pkg/printer"
	"github.com/kucial/cloud-print-service/server/internal/domain"
	"github.com/kucial/cloud-print-service/vprint/virtual"
)

// officePrinter starts a virtual IPP printer and the spec that points at it.
func officePrinter(t *testing.T, id string, o virtual.Opts) (*virtual.Printer, domain.Printer) {
	t.Helper()
	o.Addr, o.Language = "127.0.0.1:0", "ipp"
	if o.PrintDuration == 0 {
		o.PrintDuration = 50 * time.Millisecond
	}
	vp := virtual.New(id, o)
	if err := vp.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vp.Close() })
	host, port := vp.Addr()
	n, _ := strconv.Atoi(port)
	return vp, domain.Printer{PrinterID: id, Name: "Office", Site: "hq", Driver: "ipp",
		Host: host, Port: n, Enabled: true}
}

func submitDoc(t *testing.T, ts *httpServer, body string) string {
	t.Helper()
	res, b := do(t, ts, "POST", "/api/v1/jobs", erpKey, body)
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("submit %s: %d %v", body, res.StatusCode, b)
	}
	return b["job_id"].(string)
}

var pdfB64 = base64.StdEncoding.EncodeToString([]byte("%PDF-1.4\n1 0 obj<<>>endobj\n%%EOF"))

func TestOfficePrinterEndToEnd(t *testing.T) {
	vp, spec := officePrinter(t, "o1", virtual.Opts{IPPFormats: []string{"application/pdf", "image/jpeg"}})
	ts := newTestServer(t, spec)

	// The printer is learned from itself: kind, and the formats it reports.
	do(t, ts, "POST", "/api/v1/printers/probe", opsKey, "")
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, body := do(t, ts, "GET", "/api/v1/printers/o1", opsKey, "")
		if body["kind"] == "document" && len(body["formats"].([]any)) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("printer formats never learned: %v %v", body["kind"], body["formats"])
		}
		time.Sleep(50 * time.Millisecond)
	}

	// A PDF in three copies is one document with copies=3, confirmed by the printer's
	// own job state.
	id := submitDoc(t, ts, `{"printer_id":"o1","external_id":"inv-1","format":"pdf","copies":3,"payload_base64":"`+pdfB64+`"}`)
	j := waitJobState(t, ts, erpKey, id, "printed")
	got := vp.Labels()
	if len(got) != 1 || got[0].Copies != 3 || got[0].Format != "application/pdf" || got[0].Text != id {
		t.Fatalf("printer received: %+v", got)
	}
	if j["copies"] != 3.0 {
		t.Fatalf("job copies: %v", j["copies"])
	}
}

func TestOfficePrinterJobOutcomes(t *testing.T) {
	vp, spec := officePrinter(t, "o1", virtual.Opts{})
	ts := newTestServer(t, spec)

	// The printer aborts the job: a definite failure, with the printer's reason.
	vp.Control(virtual.Control{State: "abort_jobs"})
	id := submitDoc(t, ts, `{"printer_id":"o1","external_id":"a","format":"pdf","payload_base64":"`+pdfB64+`"}`)
	j := waitJobState(t, ts, erpKey, id, "failed")
	if j["attempts"] != 1.0 || j["last_error"] == "" {
		t.Fatalf("aborted job: %v", j)
	}

	// Out of paper: the printer holds the job; CPS gives up following it after the
	// track timeout and says it does not know.
	vp.Control(virtual.Control{State: "idle"})
	vp.Control(virtual.Control{State: "media_out"})
	id = submitDoc(t, ts, `{"printer_id":"o1","external_id":"b","format":"pdf","payload_base64":"`+pdfB64+`"}`)
	j = waitJobState(t, ts, erpKey, id, "sent_unknown")
	if j["state_detail"] == "" {
		t.Fatalf("a held job should say what the printer reported: %v", j)
	}
}

// A format the printer refuses fails at once: no retries that cannot succeed.
func TestOfficePrinterRejectionIsNotRetried(t *testing.T) {
	_, spec := officePrinter(t, "o1", virtual.Opts{IPPFormats: []string{"image/urf"}})
	spec.Formats = []printer.Format{printer.FormatPDF} // declared wrongly in the config
	ts := newTestServer(t, spec)
	id := submitDoc(t, ts, `{"printer_id":"o1","external_id":"r","format":"pdf","payload_base64":"`+pdfB64+`"}`)
	j := waitJobState(t, ts, erpKey, id, "failed")
	if j["attempts"] != 1.0 {
		t.Fatalf("rejected document retried: %v attempts", j["attempts"])
	}
}

func TestOfficeTestPageIsAnA4PDF(t *testing.T) {
	vp, spec := officePrinter(t, "o1", virtual.Opts{})
	spec.Formats = []printer.Format{printer.FormatPDF}
	ts := newTestServerWithPayloadLimit(t, 1<<20, spec)
	res, body := do(t, ts, "POST", "/api/v1/printers/o1/test", opsKey, "")
	if res.StatusCode != http.StatusAccepted || body["label"].(map[string]any)["paper"] != "A4" {
		t.Fatalf("test page: %d %v", res.StatusCode, body)
	}
	waitJobState(t, ts, opsKey, body["job_id"].(string), "printed")
	got := vp.Labels()
	if len(got) != 1 || got[0].Format != "application/pdf" || !bytes.HasPrefix(got[0].Data, []byte("%PDF-")) ||
		!bytes.Contains(got[0].Data, []byte("/MediaBox [0 0 595.28 841.89]")) {
		t.Fatalf("test page as received: %+v", got)
	}
}

// An office printer on raw 9100 that takes PCL only gets a PCL test page.
func TestPCLPrinterTestPage(t *testing.T) {
	vp := virtual.New("lj", virtual.Opts{Addr: "127.0.0.1:0", Language: "pdl", PrintDuration: 20 * time.Millisecond})
	if err := vp.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vp.Close() })
	host, port := vp.Addr()
	n, _ := strconv.Atoi(port)
	ts := newTestServer(t, domain.Printer{PrinterID: "lj", Name: "LaserJet", Driver: "pdl9100", Host: host, Port: n, Enabled: true})
	res, body := do(t, ts, "POST", "/api/v1/printers/lj/test", opsKey, "")
	if res.StatusCode != http.StatusAccepted || body["label"].(map[string]any)["language"] != "pcl" {
		t.Fatalf("test page: %d %v", res.StatusCode, body)
	}
	waitJobState(t, ts, opsKey, body["job_id"].(string), "printed")
	if got := vp.Labels(); len(got) != 1 || got[0].Format != "application/vnd.hp-pcl" ||
		!bytes.Contains(got[0].Data, []byte("CPS TEST PAGE")) {
		t.Fatalf("received: %+v", got)
	}
}

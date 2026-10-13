package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kucial/cloud-print-service/pkg/escpos"
	"github.com/kucial/cloud-print-service/server/internal/domain"
	"github.com/kucial/cloud-print-service/vprint/virtual"
)

// receiptPrinter starts a virtual ESC/POS printer and the spec that points at it.
func receiptPrinter(t *testing.T, id string) (*virtual.Printer, domain.Printer) {
	t.Helper()
	vp := virtual.New(id, virtual.Opts{Addr: "127.0.0.1:0", Language: "escpos", Model: "TM-T82"})
	if err := vp.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vp.Close() })
	host, port := vp.Addr()
	n, _ := strconv.Atoi(port)
	return vp, domain.Printer{PrinterID: id, Name: "Front desk", Site: "wh1", Driver: "escpos9100",
		Host: host, Port: n, Enabled: true, MediaWidthMM: 58}
}

func waitJobState(t *testing.T, ts *httpServer, key, jobID, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, j := do(t, ts, "GET", "/api/v1/jobs/"+jobID, key, "")
		if j["state"] == want {
			return j
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s is %v, want %s (%v)", jobID, j["state"], want, j["last_error"])
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestReceiptPrinterEndToEnd(t *testing.T) {
	vp, spec := receiptPrinter(t, "r1")
	ts := newTestServerWithPayloadLimit(t, 1<<20, spec)

	// The printer says what it is and takes.
	_, body := do(t, ts, "GET", "/api/v1/printers/r1", opsKey, "")
	if body["kind"] != "receipt" || body["formats"].([]any)[0] != "escpos" {
		t.Fatalf("printer view: %v", body)
	}

	// Raw ESC/POS, two copies: the receipt is sent twice, the job is one.
	receipt := escpos.New().Line("ORDER 1001").Line("合计 ¥12.50").Cut().Bytes()
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/jobs/raw?printer_id=r1&external_id=rcpt-1&format=escpos&copies=2",
		bytes.NewReader(receipt))
	req.Header.Set("Authorization", "Bearer "+erpKey)
	req.Header.Set("Content-Type", "application/octet-stream")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("raw escpos: %d %s", res.StatusCode, raw)
	}
	id := jsonField(t, raw, "job_id")
	j := waitJobState(t, ts, erpKey, id, "printed")
	if j["format"] != "escpos" || j["copies"] != 2.0 {
		t.Fatalf("job: format %v copies %v", j["format"], j["copies"])
	}
	got := vp.Labels()
	if len(got) != 2 || got[0].Text != "ORDER 1001\n合计 ¥12.50" || got[1].Text != got[0].Text {
		t.Fatalf("printed: %+v", got)
	}

	// The same receipt through the JSON API, base64.
	res2, body := do(t, ts, "POST", "/api/v1/jobs", erpKey, `{"printer_id":"r1","external_id":"rcpt-2","format":"escpos",`+
		`"payload_base64":"`+base64.StdEncoding.EncodeToString(receipt)+`"}`)
	if res2.StatusCode != http.StatusAccepted {
		t.Fatalf("json escpos: %v", body)
	}
	waitJobState(t, ts, erpKey, body["job_id"].(string), "printed")

	// ZPL is not something a receipt printer can print; page_copies are a ZPL feature.
	for _, c := range []struct{ body, reason string }{
		{`{"printer_id":"r1","external_id":"z","zpl":"^XA^XZ"}`, "format_not_supported"},
		{`{"printer_id":"r1","external_id":"x","format":"wordperfect","payload_base64":"AA=="}`, "invalid_format"},
		{`{"printer_id":"r1","external_id":"p","format":"escpos","payload_base64":"AA==","page_copies":[2]}`, "invalid_page_copies"},
	} {
		res, body := do(t, ts, "POST", "/api/v1/jobs", erpKey, c.body)
		if res.StatusCode != http.StatusUnprocessableEntity || body["error"].(map[string]any)["reason"] != c.reason {
			t.Fatalf("%s: %d %v", c.body, res.StatusCode, body)
		}
	}

	// The test page is a receipt sized for the 58 mm roll, ending in a cut.
	vp.Reset()
	res3, body := do(t, ts, "POST", "/api/v1/printers/r1/test", opsKey, "")
	if res3.StatusCode != http.StatusAccepted || body["label"].(map[string]any)["columns"] != 32.0 {
		t.Fatalf("test page: %d %v", res3.StatusCode, body)
	}
	waitJobState(t, ts, opsKey, body["job_id"].(string), "printed")
	page := vp.Labels()
	if len(page) != 1 || !strings.Contains(page[0].Text, "CPS TEST") || !strings.Contains(page[0].Text, "中文测试") ||
		page[0].Images != 2 {
		t.Fatalf("test receipt: %+v", page)
	}
}

// A receipt printer that runs out of paper stops taking jobs, like a label printer.
func TestReceiptPrinterOutOfPaperIsNotUsable(t *testing.T) {
	vp, spec := receiptPrinter(t, "r1")
	ts := newTestServer(t, spec)
	vp.Control(virtual.Control{State: "media_out"})
	do(t, ts, "POST", "/api/v1/printers/probe", opsKey, "")
	_, body := do(t, ts, "GET", "/api/v1/printers/r1", opsKey, "")
	if body["usable"] != false || body["status"].(map[string]any)["state"] != "media_out" {
		t.Fatalf("out of paper: usable=%v status=%v", body["usable"], body["status"])
	}
}

func jsonField(t *testing.T, raw []byte, key string) string {
	t.Helper()
	i := bytes.Index(raw, []byte(`"`+key+`":"`))
	if i < 0 {
		t.Fatalf("no %s in %s", key, raw)
	}
	rest := raw[i+len(key)+4:]
	return string(rest[:bytes.IndexByte(rest, '"')])
}

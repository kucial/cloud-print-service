package httpapi

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kucial/cloud-print-service/pkg/printer"
	"github.com/kucial/cloud-print-service/server/internal/domain"
	"github.com/kucial/cloud-print-service/vprint/virtual"
)

func logoPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 120, 60))
	for y := 0; y < 60; y++ {
		for x := 0; x < 120; x++ {
			if (x/10+y/10)%2 == 0 {
				img.SetGray(x, y, color.Gray{})
			} else {
				img.SetGray(x, y, color.Gray{Y: 255})
			}
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func postRaw(t *testing.T, ts *httpServer, query, contentType string, body []byte) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/jobs/raw?"+query, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+erpKey)
	req.Header.Set("Content-Type", contentType)
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(raw)
}

func TestImageGoesToEveryKindOfPrinter(t *testing.T) {
	rcpt, rspec := receiptPrinter(t, "r1")
	office, ospec := officePrinter(t, "o1", virtual.Opts{IPPFormats: []string{"application/pdf"}})
	ospec.Formats = []printer.Format{printer.FormatPDF}
	lbl := virtual.New("l1", virtual.Opts{Addr: "127.0.0.1:0", PrintDuration: 20 * time.Millisecond})
	if err := lbl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lbl.Close() })
	host, port := lbl.Addr()
	n, _ := strconv.Atoi(port)
	lspec := domain.Printer{PrinterID: "l1", Name: "Labels", Driver: "zebra9100", Host: host, Port: n, Enabled: true,
		MediaWidthMM: 60, MediaHeightMM: 40, DPMM: 8}
	ts := newTestServerWithPayloadLimit(t, 4<<20, rspec, ospec, lspec)
	logo := logoPNG(t)

	// Receipt: a dithered raster, twice.
	code, body := postRaw(t, ts, "printer_id=r1&external_id=img-r&copies=2", "image/png", logo)
	if code != http.StatusAccepted {
		t.Fatalf("receipt: %d %s", code, body)
	}
	j := waitJobState(t, ts, erpKey, jsonField(t, []byte(body), "job_id"), "printed")
	if j["format"] != "escpos" || j["source_format"] != "png" {
		t.Fatalf("receipt job: format %v from %v", j["format"], j["source_format"])
	}
	if got := rcpt.Labels(); len(got) != 2 || got[0].Images != 1 {
		t.Fatalf("receipts printed: %+v", got)
	}

	// Label: a ^GFA graphic, three labels from one ^PQ.
	code, body = postRaw(t, ts, "printer_id=l1&external_id=img-l&copies=3", "image/png", logo)
	if code != http.StatusAccepted {
		t.Fatalf("label: %d %s", code, body)
	}
	j = waitJobState(t, ts, erpKey, jsonField(t, []byte(body), "job_id"), "printed")
	labels := lbl.Labels()
	if j["format"] != "zpl" || len(labels) != 1 || lbl.LabelCount() != 3 || !strings.Contains(labels[0].ZPL, "^GFA,") {
		t.Fatalf("label job %v: %d labels, %+v", j["format"], lbl.LabelCount(), labels)
	}

	// Office printer that takes PDF: the picture on an A4 page.
	code, body = postRaw(t, ts, "printer_id=o1&external_id=img-o", "image/png", logo)
	if code != http.StatusAccepted {
		t.Fatalf("office: %d %s", code, body)
	}
	waitJobState(t, ts, erpKey, jsonField(t, []byte(body), "job_id"), "printed")
	if got := office.Labels(); len(got) != 1 || got[0].Format != "application/pdf" || !bytes.HasPrefix(got[0].Data, []byte("%PDF")) {
		t.Fatalf("office printer received: %+v", got)
	}
}

func TestConversionRefusals(t *testing.T) {
	_, rspec := receiptPrinter(t, "r1")
	ts := newTestServerWithPayloadLimit(t, 1<<20, rspec)
	// This test server has no poppler: a PDF for a receipt printer says why it cannot.
	for _, c := range []struct {
		ct     string
		body   []byte
		reason string
	}{
		{"application/pdf", []byte("%PDF-1.4\n%%EOF"), "pdf_conversion_unavailable"},
		{"image/png", []byte("not really a png"), "document_unreadable"},
	} {
		code, body := postRaw(t, ts, "printer_id=r1&external_id=x-"+c.reason, c.ct, c.body)
		if code != http.StatusUnprocessableEntity || !strings.Contains(body, `"reason":"`+c.reason+`"`) {
			t.Fatalf("%s: %d %s", c.ct, code, body)
		}
	}
}

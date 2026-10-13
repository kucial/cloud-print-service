package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/kucial/cloud-print-service/server/internal/domain"
	"github.com/kucial/cloud-print-service/vprint/virtual"
)

func TestZPLCopies(t *testing.T) {
	vp := virtual.New("z", virtual.Opts{Addr: "127.0.0.1:0", PrintDuration: 20 * time.Millisecond})
	if err := vp.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vp.Close() })
	host, port := vp.Addr()
	n, _ := strconv.Atoi(port)
	ts := newTestServer(t, domain.Printer{PrinterID: "z1", Name: "Pack", Driver: "zebra9100", Host: host, Port: n, Enabled: true})

	// No ^PQ in the label: copies=3 prints three.
	id := submitDoc(t, ts, `{"printer_id":"z1","external_id":"c1","zpl":"^XA^FO50,50^FDSKU-1^FS^XZ","copies":3}`)
	waitJobState(t, ts, erpKey, id, "printed")
	if vp.LabelCount() != 3 {
		t.Fatalf("printed %d labels, want 3", vp.LabelCount())
	}

	// Resending it is a replay; resending it with another count is a conflict.
	if res, body := do(t, ts, "POST", "/api/v1/jobs", erpKey, `{"printer_id":"z1","external_id":"c1","zpl":"^XA^FO50,50^FDSKU-1^FS^XZ","copies":3}`); res.StatusCode != http.StatusOK || body["replayed"] != true {
		t.Fatalf("replay: %d %v", res.StatusCode, body)
	}
	if res, body := do(t, ts, "POST", "/api/v1/jobs", erpKey, `{"printer_id":"z1","external_id":"c1","zpl":"^XA^FO50,50^FDSKU-1^FS^XZ","copies":5}`); res.StatusCode != http.StatusConflict {
		t.Fatalf("different copies under one external_id: %d %v", res.StatusCode, body)
	}

	// The label sets its own quantity: copies=2 is ambiguous and refused; copies=1
	// prints what the label says.
	res, body := do(t, ts, "POST", "/api/v1/jobs", erpKey, `{"printer_id":"z1","external_id":"c2","zpl":"^XA^FDx^FS^PQ2^XZ","copies":2}`)
	if res.StatusCode != http.StatusUnprocessableEntity || body["error"].(map[string]any)["reason"] != "copies_conflict" {
		t.Fatalf("conflict: %d %v", res.StatusCode, body)
	}
	vp.Reset()
	id = submitDoc(t, ts, `{"printer_id":"z1","external_id":"c3","zpl":"^XA^FDx^FS^PQ2^XZ"}`)
	waitJobState(t, ts, erpKey, id, "printed")
	if vp.LabelCount() != 2 {
		t.Fatalf("label's own ^PQ2 printed %d", vp.LabelCount())
	}
}

package driver_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/kucial/cloud-print-service/pkg/printer"
	"github.com/kucial/cloud-print-service/server/internal/domain"
	"github.com/kucial/cloud-print-service/server/internal/driver"
	"github.com/kucial/cloud-print-service/vprint/virtual"
)

// The ipp driver against a virtual office printer: real IPP over HTTP.

func ippFor(t *testing.T, vp *virtual.Printer) *driver.IPP {
	t.Helper()
	host, port := vp.Addr()
	n, _ := strconv.Atoi(port)
	d := driver.NewIPP(domain.Printer{PrinterID: "op", Host: host, Port: n}, driver.Options{})
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func trackUntilDone(t *testing.T, d *driver.IPP, sent driver.Sent) driver.Tracked {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		tr, err := d.Track(context.Background(), sent)
		if err != nil {
			t.Fatalf("track: %v", err)
		}
		if tr.Done {
			return tr
		}
		if time.Now().After(deadline) {
			t.Fatalf("job never finished: %s", tr.Detail)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestIPPPrintsAndFollowsTheJob(t *testing.T) {
	vp := startVirtual(t, virtual.Opts{Language: "ipp", Model: "HP LaserJet Pro M404", PrintDuration: 50 * time.Millisecond})
	d := ippFor(t, vp)
	ctx := context.Background()

	pdf := []byte("%PDF-1.4\n…\n%%EOF")
	sent, err := d.SendDocument(ctx, driver.Doc{Data: pdf, Format: printer.FormatPDF, Copies: 3, Name: "job_7"})
	if err != nil || sent.Bytes != len(pdf) || sent.Ref == "" {
		t.Fatalf("print: %+v %v", sent, err)
	}
	got := vp.Labels()
	if len(got) != 1 || got[0].Format != "application/pdf" || got[0].Copies != 3 || got[0].Text != "job_7" {
		t.Fatalf("printer received: %+v", got)
	}
	if tr := trackUntilDone(t, d, sent); !tr.Printed {
		t.Fatalf("job: %+v", tr)
	}

	info, err := d.Identify(ctx)
	if err != nil || info["model"] != "HP LaserJet Pro M404" || info["formats"] != "pdf,urf,jpeg" {
		t.Fatalf("identify: %v %v", info, err)
	}
}

func TestIPPRejectedFormatIsNotRetryable(t *testing.T) {
	vp := startVirtual(t, virtual.Opts{Language: "ipp", IPPFormats: []string{"image/urf"}})
	d := ippFor(t, vp)
	sent, err := d.SendDocument(context.Background(), driver.Doc{Data: []byte("%PDF"), Format: printer.FormatPDF})
	if !errors.Is(err, driver.ErrRejected) || sent.Bytes != 0 {
		t.Fatalf("a refused format must be a rejection with nothing printed: %+v %v", sent, err)
	}
	if vp.LabelCount() != 0 {
		t.Fatal("refused document recorded as printed")
	}
}

func TestIPPJobThatEndsWithoutPrinting(t *testing.T) {
	vp := startVirtual(t, virtual.Opts{Language: "ipp", PrintDuration: time.Hour})
	d := ippFor(t, vp)
	sent, err := d.SendDocument(context.Background(), driver.Doc{Data: []byte("%PDF"), Format: printer.FormatPDF})
	if err != nil {
		t.Fatal(err)
	}
	vp.Control(virtual.Control{State: "abort_jobs"})
	if tr := trackUntilDone(t, d, sent); tr.Printed || tr.Detail == "" {
		t.Fatalf("aborted job: %+v", tr)
	}
}

func TestIPPStatus(t *testing.T) {
	vp := startVirtual(t, virtual.Opts{Language: "ipp"})
	d := ippFor(t, vp)
	ctx := context.Background()
	for _, c := range []struct {
		control string
		want    printer.State
	}{
		{"idle", printer.StateIdle},
		{"media_out", printer.StateMediaOut},
		{"idle", printer.StateIdle},
		{"paper_jam", printer.StatePaperJam},
		{"idle", printer.StateIdle},
		{"door_open", printer.StateDoorOpen},
		{"idle", printer.StateIdle},
		{"toner_low", printer.StateTonerLow},
		{"toner_out", printer.StateTonerOut},
		{"idle", printer.StateIdle},
	} {
		vp.Control(virtual.Control{State: c.control})
		st, err := d.Status(ctx)
		if err != nil || st.State != c.want {
			t.Fatalf("%s: got %s (%s) %v", c.control, st.State, st.Detail, err)
		}
	}
	off := true
	vp.Control(virtual.Control{Offline: &off})
	if st, err := d.Status(ctx); err == nil || st.State != printer.StateOffline {
		t.Fatalf("unreachable printer: %s %v", st.State, err)
	}
}

// The printer took the document and died before answering: it may print, so the
// bytes count and the job layer must not retry.
func TestIPPConnectionLostAfterTheDocumentIsPartial(t *testing.T) {
	vp := startVirtual(t, virtual.Opts{Language: "ipp"})
	d := ippFor(t, vp)
	closeOnPrint := true
	vp.Control(virtual.Control{CloseOnPrint: &closeOnPrint})
	sent, err := d.SendDocument(context.Background(), driver.Doc{Data: []byte("%PDF-1.4 body"), Format: printer.FormatPDF})
	if err == nil || sent.Bytes == 0 || errors.Is(err, driver.ErrRejected) {
		t.Fatalf("lost answer must be a partial send: %+v %v", sent, err)
	}
}

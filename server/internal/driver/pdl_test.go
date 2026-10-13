package driver_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/kucial/cloud-print-service/pkg/printer"
	"github.com/kucial/cloud-print-service/server/internal/domain"
	"github.com/kucial/cloud-print-service/server/internal/driver"
	"github.com/kucial/cloud-print-service/vprint/virtual"
)

func pdlFor(t *testing.T, vp *virtual.Printer) *driver.PDL {
	t.Helper()
	host, port := vp.Addr()
	n, _ := strconv.Atoi(port)
	d := driver.NewPDL(domain.Printer{PrinterID: "lj", Host: host, Port: n},
		driver.Options{DialTimeout: time.Second, WriteTimeout: time.Second, ReadTimeout: 500 * time.Millisecond})
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestPDLPrintsAndReportsPJLStatus(t *testing.T) {
	vp := startVirtual(t, virtual.Opts{Language: "pdl", Model: "HP LaserJet 4250", PrintDuration: 100 * time.Millisecond})
	d := pdlFor(t, vp)
	ctx := context.Background()

	pcl := []byte("\x1bE\x1b&l26AHello from CPS\f\x1bE")
	if n, err := d.Send(ctx, pcl); err != nil || n != len(pcl) {
		t.Fatalf("send: %d %v", n, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for vp.LabelCount() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	got := vp.Labels()
	if len(got) != 1 || got[0].Format != "application/vnd.hp-pcl" || string(got[0].Data) != string(pcl) {
		t.Fatalf("printer received: %+v", got)
	}
	if st, _ := d.Status(ctx); st.State != printer.StatePrinting {
		t.Fatalf("while printing: %s (%s)", st.State, st.Detail)
	}

	for _, c := range []struct {
		control string
		want    printer.State
	}{
		{"idle", printer.StateIdle},
		{"media_out", printer.StateMediaOut},
		{"idle", printer.StateIdle},
		{"paper_jam", printer.StatePaperJam},
		{"idle", printer.StateIdle},
		{"toner_low", printer.StateTonerLow},
	} {
		vp.Control(virtual.Control{State: c.control})
		if st, err := d.Status(ctx); err != nil || st.State != c.want {
			t.Fatalf("%s: %s (%s) %v", c.control, st.State, st.Detail, err)
		}
	}
	if info, err := d.Identify(ctx); err != nil || info["model"] != "HP LaserJet 4250" {
		t.Fatalf("identify: %v %v", info, err)
	}
}

// A printer on 9100 that says nothing back still prints: reachable, state unknown.
func TestPDLWithoutPJLIsReachableNotOffline(t *testing.T) {
	vp := startVirtual(t, virtual.Opts{Language: "escpos"}) // answers no PJL
	d := pdlFor(t, vp)
	st, err := d.Status(context.Background())
	if err != nil || st.State != printer.StateUnknown {
		t.Fatalf("no PJL: %s %v", st.State, err)
	}
}

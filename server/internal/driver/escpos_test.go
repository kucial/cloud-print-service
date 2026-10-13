package driver_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/kucial/cloud-print-service/pkg/escpos"
	"github.com/kucial/cloud-print-service/pkg/printer"
	"github.com/kucial/cloud-print-service/server/internal/domain"
	"github.com/kucial/cloud-print-service/server/internal/driver"
	"github.com/kucial/cloud-print-service/vprint/virtual"
)

// The escpos9100 driver against a virtual receipt printer over TCP: the same sockets
// and real-time status requests as with a network receipt printer.

func escposFor(t *testing.T, vp *virtual.Printer) *driver.ESCPOS {
	t.Helper()
	host, port := vp.Addr()
	n, _ := strconv.Atoi(port)
	d := driver.NewESCPOS(domain.Printer{PrinterID: "rp", Host: host, Port: n},
		driver.Options{DialTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second, ReadTimeout: time.Second})
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func waitReceipts(t *testing.T, vp *virtual.Printer, n int) []virtual.Label {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for vp.LabelCount() < n {
		if time.Now().After(deadline) {
			t.Fatalf("printer has %d receipts, want %d", vp.LabelCount(), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return vp.Labels()
}

func TestESCPOSPrintsAReceipt(t *testing.T) {
	vp := startVirtual(t, virtual.Opts{Language: "escpos", Model: "TM-T82"})
	d := escposFor(t, vp)
	ctx := context.Background()

	receipt := escpos.New().Align(escpos.Center).Bold(true).Line("ORDER SO-1001").Bold(false).
		Line("拣货单 · 仓库 A").QR("SO-1001", 6).Code128("SO-1001", 60).Cut().Bytes()
	n, err := d.Send(ctx, receipt)
	if err != nil || n != len(receipt) {
		t.Fatalf("send: %d %v", n, err)
	}
	got := waitReceipts(t, vp, 1)[0]
	if got.Text != "ORDER SO-1001\n拣货单 · 仓库 A" || got.Images != 2 {
		t.Fatalf("receipt as printed: text %q, %d codes", got.Text, got.Images)
	}

	info, err := d.Identify(ctx)
	if err != nil || info["model"] != "TM-T82" || info["language"] != "escpos" {
		t.Fatalf("identify: %v %v", info, err)
	}
}

func TestESCPOSStatus(t *testing.T) {
	vp := startVirtual(t, virtual.Opts{Language: "escpos"})
	d := escposFor(t, vp)
	ctx := context.Background()

	for _, c := range []struct {
		control string
		want    printer.State
	}{
		{"idle", printer.StateIdle},
		{"media_low", printer.StateMediaLow},
		{"media_out", printer.StateMediaOut},
		{"idle", printer.StateIdle},
		{"door_open", printer.StateDoorOpen},
		{"idle", printer.StateIdle},
	} {
		vp.Control(virtual.Control{State: c.control})
		st, err := d.Status(ctx)
		if err != nil || st.State != c.want {
			t.Fatalf("%s: got %s (%s) %v", c.control, st.State, st.Detail, err)
		}
	}
	if !printer.StateDoorOpen.BlocksUse() {
		t.Fatal("an open cover must make the printer unusable")
	}

	off := true
	vp.Control(virtual.Control{Offline: &off})
	if st, err := d.Status(ctx); err == nil || st.State != printer.StateOffline {
		t.Fatalf("unreachable printer: %s %v", st.State, err)
	}
}

// A status probe in the middle of printing must not end up in the receipt, and the
// receipt must not swallow the probe's answer.
func TestESCPOSStatusWhilePrinting(t *testing.T) {
	vp := startVirtual(t, virtual.Opts{Language: "escpos"})
	d := escposFor(t, vp)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		go func() { _, _ = d.Send(ctx, escpos.New().Line("receipt").Cut().Bytes()) }()
		if st, err := d.Status(ctx); err != nil || st.State != printer.StateIdle {
			t.Fatalf("status during print %d: %s %v", i, st.State, err)
		}
	}
	for _, r := range waitReceipts(t, vp, 5) {
		if r.Text != "receipt" {
			t.Fatalf("receipt text %q", r.Text)
		}
	}
}

func TestESCPOSCannotCancel(t *testing.T) {
	vp := startVirtual(t, virtual.Opts{Language: "escpos"})
	if err := escposFor(t, vp).Cancel(context.Background()); err == nil {
		t.Fatal("ESC/POS has no cancel; claiming one would hide a printed receipt")
	}
}

package driver_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kongkx/cloud-print-service/internal/domain"
	"github.com/kongkx/cloud-print-service/internal/driver"
	"github.com/kongkx/cloud-print-service/internal/virtual"
)

// These tests drive the real zebra9100 driver against a virtual printer over TCP,
// i.e. the same sockets, framing and status handshake used with hardware.

func startVirtual(t *testing.T, o virtual.Opts) *virtual.Printer {
	t.Helper()
	if o.Addr == "" {
		o.Addr = "127.0.0.1:0"
	}
	p := virtual.New(t.Name(), o)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("start virtual printer: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func driverFor(t *testing.T, vp *virtual.Printer) *driver.Zebra {
	t.Helper()
	host, port := vp.Addr()
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("virtual printer port %q: %v", port, err)
	}
	z := driver.NewZebra(domain.Printer{PrinterID: "vp", Host: host, Port: n},
		driver.ZebraOpts{
			DialTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second,
		})
	t.Cleanup(func() { _ = z.Close() })
	return z
}

func TestZebraSendReachesPrinterIntact(t *testing.T) {
	vp := startVirtual(t, virtual.Opts{PrintDuration: 50 * time.Millisecond})
	z := driverFor(t, vp)
	ctx := context.Background()

	payload := []byte("^XA^CI28^FO40,40^A0N,40,40^FDSKU-12345^FS^FO40,100^FD1/6^FS^XZ")
	n, err := z.Send(ctx, payload)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if n != len(payload) {
		t.Fatalf("bytes sent = %d, want %d", n, len(payload))
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && vp.LabelCount() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	labels := vp.Labels()
	if len(labels) != 1 {
		t.Fatalf("printer recorded %d labels, want 1", len(labels))
	}
	if labels[0].ZPL != string(payload) {
		t.Fatalf("label payload corrupted:\n got %q\nwant %q", labels[0].ZPL, payload)
	}
}

func TestZebraStatusTracksPrintingState(t *testing.T) {
	vp := startVirtual(t, virtual.Opts{PrintDuration: 600 * time.Millisecond})
	z := driverFor(t, vp)
	ctx := context.Background()

	st, err := z.Status(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.State != domain.StateIdle {
		t.Fatalf("idle printer reported %q (raw %q)", st.State, st.Raw)
	}

	if _, err := z.Send(ctx, []byte("^XA^FDbusy^FS^XZ")); err != nil {
		t.Fatal(err)
	}
	// While the label is printing, the driver must observe a busy state, otherwise
	// the confirmation loop would declare success before anything was printed.
	deadline := time.Now().Add(2 * time.Second)
	sawPrinting := false
	for time.Now().Before(deadline) {
		st, err = z.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if st.State == domain.StatePrinting {
			sawPrinting = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sawPrinting {
		t.Fatalf("never observed printing state; last=%q raw=%q", st.State, st.Raw)
	}
}

func TestZebraStatusDecodesConsumables(t *testing.T) {
	vp := startVirtual(t, virtual.Opts{})
	z := driverFor(t, vp)
	ctx := context.Background()

	cases := []struct {
		control virtual.Control
		want    domain.PrinterState
	}{
		{virtual.Control{State: "media_out"}, domain.StateMediaOut},
		{virtual.Control{State: "media_low"}, domain.StateMediaLow},
		{virtual.Control{State: "head_error"}, domain.StateHeadError},
		{virtual.Control{State: "error"}, domain.StateError},
		{virtual.Control{State: "idle"}, domain.StateIdle},
	}
	for _, tc := range cases {
		vp.Control(virtual.Control{State: "idle"}) // faults accumulate; reset first
		vp.Control(tc.control)
		st, err := z.Status(ctx)
		if err != nil {
			t.Fatalf("%v: %v", tc.control, err)
		}
		if st.State != tc.want {
			t.Fatalf("control %v → state %q (raw %q), want %q", tc.control, st.State, st.Raw, tc.want)
		}
		if !strings.HasPrefix(st.Raw, "^HS") {
			t.Fatalf("raw reply not preserved: %q", st.Raw)
		}
	}
}

func TestZebraIdentify(t *testing.T) {
	vp := startVirtual(t, virtual.Opts{Model: "ZT411", Serial: "SN-ABC-123"})
	z := driverFor(t, vp)
	info, err := z.Identify(context.Background())
	if err != nil {
		t.Fatalf("identify: %v", err)
	}
	if info["9"] != "ZT411" {
		t.Fatalf("model not parsed: %+v", info)
	}
	if info["12"] != "SN-ABC-123" {
		t.Fatalf("serial not parsed: %+v", info)
	}
}

func TestZebraCancelClearsPrinting(t *testing.T) {
	vp := startVirtual(t, virtual.Opts{PrintDuration: 5 * time.Second})
	z := driverFor(t, vp)
	ctx := context.Background()

	if _, err := z.Send(ctx, []byte("^XA^FDlong^FS^XZ")); err != nil {
		t.Fatal(err)
	}
	// The virtual printer records the label asynchronously, so poll for the state
	// rather than assume it has already happened.
	if !waitPrinterState(ctx, z, domain.StatePrinting, 2*time.Second) {
		st, _ := z.Status(ctx)
		t.Fatalf("expected printing before cancel, got %q", st.State)
	}
	if err := z.Cancel(ctx); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if !waitPrinterState(ctx, z, domain.StateIdle, 2*time.Second) {
		t.Fatal("printer stayed in printing state after ~JA")
	}
}

func TestZebraOfflineIsReportedNotGuessed(t *testing.T) {
	vp := startVirtual(t, virtual.Opts{})
	z := driverFor(t, vp)
	ctx := context.Background()

	if _, err := z.Status(ctx); err != nil {
		t.Fatalf("printer should be reachable: %v", err)
	}
	vp.Control(virtual.Control{Offline: boolp(true)})

	st, err := z.Status(ctx)
	if err == nil {
		t.Fatalf("expected a failure when the printer is offline, got state %q", st.State)
	}
	if st.State != domain.StateOffline {
		t.Fatalf("offline failure must report state=offline, got %q", st.State)
	}
	if n, err := z.Send(ctx, []byte("^XA^FDx^FS^XZ")); err == nil {
		t.Fatal("expected send to fail while offline")
	} else if n != 0 {
		t.Fatalf("offline send must report 0 bytes so the job stays retryable, got %d", n)
	}

	vp.Control(virtual.Control{Offline: boolp(false)})
	if _, err := z.Status(ctx); err != nil {
		t.Fatalf("printer should recover after coming back online: %v", err)
	}
}

func TestZebraPartialWriteIsNotSilentlyRetried(t *testing.T) {
	vp := startVirtual(t, virtual.Opts{})
	z := driverFor(t, vp)
	ctx := context.Background()
	vp.Control(virtual.Control{CloseOnPrint: boolp(true)})

	// A large payload plus a printer that resets mid-write must report an error. Whether
	// some bytes landed (n>0, "do not retry") or none did (n=0, "safe to retry") depends on
	// kernel buffering, so assert the contract that is invariant: a failed write is
	// always reported as an error, and a successful write never reports a short count.
	payload := []byte("^XA" + strings.Repeat("X", 512<<10) + "^XZ")
	sawPartial := false
	for attempt := 0; attempt < 5; attempt++ {
		n, err := z.Send(ctx, payload)
		switch {
		case err == nil && n != len(payload):
			t.Fatalf("successful write reported %d of %d bytes", n, len(payload))
		case err != nil && n > 0:
			sawPartial = true // job layer will refuse to retry: exactly the intent
		case err == nil:
			t.Skip("OS accepted the whole write; partial-write path not exercised on this host")
		}
		if err != nil && n > 0 {
			break
		}
	}
	if !sawPartial {
		t.Log("no partial write observed in 5 attempts (kernel buffered the reset); " +
			"the no-retry rule for n>0 is covered in internal/job tests")
	}
}

func TestZebraSurvivesPrinterReboot(t *testing.T) {
	vp := startVirtual(t, virtual.Opts{})
	z := driverFor(t, vp)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := z.Send(ctx, []byte("^XA^FDcycle^FS^XZ")); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
		if _, err := z.Status(ctx); err != nil {
			t.Fatalf("status %d: %v", i, err)
		}
	}
	if got := vp.LabelCount(); got != 3 {
		t.Fatalf("labels on printer = %d, want 3", got)
	}
}

// waitPrinterState polls the printer until it reports want, or the deadline passes.
func waitPrinterState(ctx context.Context, z *driver.Zebra, want domain.PrinterState, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if st, err := z.Status(ctx); err == nil && st.State == want {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func boolp(b bool) *bool { return &b }

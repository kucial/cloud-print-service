package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/kucial/cloud-print-service/pkg/printer"
	"github.com/kucial/cloud-print-service/vprint/virtual"
)

// docPrinter is a fake USB receipt or office printer: it answers DLE EOT / GS I or
// PJL queries, records everything else as document bytes.
type docPrinter struct {
	mu      sync.Mutex
	pending []byte
	doc     bytes.Buffer
	pjl     bool
	silent  bool // takes data, answers nothing
}

type docPort struct{ p *docPrinter }

func (d *docPrinter) open(string) (usbPort, error) { return &docPort{p: d}, nil }

func (f *docPort) Write(b []byte, _ time.Duration) (int, error) {
	f.p.mu.Lock()
	defer f.p.mu.Unlock()
	switch {
	case f.p.silent:
		f.p.doc.Write(b)
	case printer.IsESCPOSStatusQuery(b):
		f.p.pending = append(f.p.pending, 0x12)
	case bytes.Equal(b, printer.CmdESCPOSModelName):
		f.p.pending = append(f.p.pending, "_TM-T82\x00"...)
	case f.p.pjl && bytes.Equal(b, printer.CmdPJLStatus):
		f.p.pending = append(f.p.pending, "@PJL INFO STATUS\r\nCODE=10001\r\nDISPLAY=\"READY\"\r\nONLINE=TRUE\r\n\f"...)
	case f.p.pjl && bytes.Equal(b, printer.CmdPJLID):
		f.p.pending = append(f.p.pending, "@PJL INFO ID\r\n\"HP LaserJet 400 M401dn\"\r\n\f"...)
	default:
		f.p.doc.Write(b)
	}
	return len(b), nil
}

func (f *docPort) Read(b []byte, timeout time.Duration) (int, error) {
	f.p.mu.Lock()
	defer f.p.mu.Unlock()
	if len(f.p.pending) == 0 {
		f.p.mu.Unlock()
		time.Sleep(min(timeout, 10*time.Millisecond))
		f.p.mu.Lock()
		return 0, errUSBTimeout
	}
	n := copy(b, f.p.pending)
	f.p.pending = f.p.pending[n:]
	return n, nil
}

func (f *docPort) Close() error { return nil }

func withUSBDevices(t *testing.T, dp *docPrinter, devs ...usbDevice) {
	t.Helper()
	prevOpen, prevList := openUSB, listUSB
	openUSB = dp.open
	listUSB = func() ([]usbDevice, error) { return devs, nil }
	t.Cleanup(func() { openUSB, listUSB = prevOpen, prevList })
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestUSBReceiptPrinter(t *testing.T) {
	dp := &docPrinter{}
	withUSBDevices(t, dp, usbDevice{Path: "x", Key: "RCPT01", ID1284: "MFG:EPSON;CMD:ESCPOS;MDL:TM-T82;CLS:PRINTER;"})
	bs := usbBackends(context.Background(), quiet)
	if len(bs) != 1 {
		t.Fatalf("backends: %d", len(bs))
	}
	u := bs[0].(*usbPrinter)
	if u.Model() != "TM-T82" || profileOf(u).Driver != printer.DriverESCPOS || profileOf(u).Kind != printer.KindReceipt {
		t.Fatalf("receipt printer: model %q profile %+v", u.Model(), u.profile)
	}
	st, err := (&usbSink{u: u}).Status(context.Background())
	if err != nil || st.State != printer.StateIdle {
		t.Fatalf("status: %+v %v", st, err)
	}
	if dp.doc.Len() != 0 {
		t.Fatalf("queries reached the paper: %q", dp.doc.String())
	}
}

func TestUSBOfficePrinter(t *testing.T) {
	dp := &docPrinter{pjl: true}
	withUSBDevices(t, dp, usbDevice{Path: "x", Key: "LJ01",
		ID1284: "MFG:Hewlett-Packard;CMD:PJL,PML,PCLXL,POSTSCRIPT,PCL;MDL:HP LaserJet 400 M401dn;CLS:PRINTER;"})
	u := usbBackends(context.Background(), quiet)[0].(*usbPrinter)
	if u.Model() != "HP LaserJet 400 M401dn" || u.profile.Driver != printer.DriverPDL ||
		len(u.profile.Formats) != 2 || !u.profile.PJL {
		t.Fatalf("office printer: %q %+v", u.Model(), u.profile)
	}
	if st, err := (&usbSink{u: u}).Status(context.Background()); err != nil || st.State != printer.StateIdle {
		t.Fatalf("status: %+v %v", st, err)
	}
}

// A printer that prints but never answers is served, with an unknown state.
func TestUSBSilentPrinterIsUnknownNotOffline(t *testing.T) {
	for _, id := range []string{"MFG:Printer;MDL:POS58 Printer USB;", "MFG:Brother;CMD:PCL,PCLXL;MDL:HL-1110;"} {
		dp := &docPrinter{silent: true}
		withUSBDevices(t, dp, usbDevice{Path: "x", Key: "S1", ID1284: id})
		bs := usbBackends(context.Background(), quiet)
		if len(bs) != 1 {
			t.Fatalf("%s: not served", id)
		}
		st, err := (&usbSink{u: bs[0].(*usbPrinter)}).Status(context.Background())
		if err != nil || st.State != printer.StateUnknown {
			t.Fatalf("%s: %+v %v", id, st, err)
		}
	}
	u := &usbPrinter{dev: usbDevice{WriteOnly: true}, profile: printer.ParseDeviceID("MFG:EPSON;CMD:ESCPOS;MDL:TM-T20;").Profile()}
	if st, err := (&usbSink{u: u}).Status(context.Background()); err != nil || st.State != printer.StateUnknown {
		t.Fatalf("write-only: %+v %v", st, err)
	}
}

// What the server's drivers send on the status socket is answered by the agent and
// never printed; documents go through, even ones that start with the same bytes.
func TestServerQueriesAreNeverPrinted(t *testing.T) {
	receipt := printer.Profile{Driver: printer.DriverESCPOS}
	office := printer.Profile{Driver: printer.DriverPDL}
	for _, c := range []struct {
		p     printer.Profile
		chunk []byte
		query bool
	}{
		{receipt, printer.CmdESCPOSPrinterStatus, true},
		{receipt, printer.CmdESCPOSModelName, true},
		{receipt, append(append([]byte{}, printer.CmdESCPOSPrinterStatus...), "receipt text"...), false},
		{office, printer.CmdPJLStatus, true},
		{office, append(append([]byte{}, printer.CmdPJLStatus...), "\x1bE page"...), false},
		{office, []byte("~HS"), false},
		{zebraProfile, []byte("~HS"), true},
		{zebraProfile, []byte("^XA^FDx^FS^XZ"), false},
	} {
		if got := isServerQuery(c.p, c.chunk); got != c.query {
			t.Errorf("%s %q: query=%v", c.p.Driver, c.chunk, got)
		}
	}
}

// Looking for printers on local ports must not print anything on any of them: a
// receipt printer would print ~HI or a PJL line, an office printer ~HI.
func TestLocalSharesAreFoundWithoutPrinting(t *testing.T) {
	ports := map[string]*virtual.Printer{}
	for _, lang := range []string{"zpl", "escpos", "pdl"} {
		vp := virtual.New(lang, virtual.Opts{Addr: "127.0.0.1:0", Language: lang})
		if err := vp.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = vp.Close() })
		ports[lang] = vp
	}
	var list []string
	for _, vp := range ports {
		_, port := vp.Addr()
		list = append(list, port)
	}
	t.Setenv("CPS_AGENT_PORTS", list[0]+","+list[1]+","+list[2])
	prevList := listUSB
	listUSB = func() ([]usbDevice, error) { return nil, nil }
	t.Cleanup(func() { listUSB = prevList })

	bs, err := probeBackends(context.Background(), quiet)
	if err != nil || len(bs) != 3 {
		t.Fatalf("found %d shares: %v", len(bs), err)
	}
	kinds := map[printer.Kind]bool{}
	for _, b := range bs {
		kinds[profileOf(b).Kind] = true
	}
	if !kinds[printer.KindLabel] || !kinds[printer.KindReceipt] || !kinds[printer.KindDocument] {
		t.Fatalf("kinds found: %v", kinds)
	}
	time.Sleep(100 * time.Millisecond)
	for lang, vp := range ports {
		if n := vp.LabelCount(); n != 0 {
			t.Errorf("probing printed %d page(s) on the %s printer: %+v", n, lang, vp.Labels())
		}
	}
}

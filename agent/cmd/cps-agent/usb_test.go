package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kucial/cloud-print-service/pkg/printer"
)

// fakePrinter stands in for a USB printer: it answers ~HI/~HS like a GK888t (captured
// replies), delivers them in small chunks as USB does, and records label bytes.
type fakePrinter struct {
	mu      sync.Mutex
	pending []byte // reply bytes not yet read
	labels  strings.Builder
	stale   []byte // a late reply left over from an earlier query
	opens   int
}

const (
	realHI = "\x02GK888t (EPL)-200dpi,V71.17.15Z,8,2104KB\x03\r\n"
	realHS = "\x02030,0,0,0333,000,0,0,0,000,0,0,0\x03\r\n\x02001,0,0,0,1,2,3,0,00000000,1,000\x03\r\n\x021234,0\x03\r\n"
)

type fakePort struct{ p *fakePrinter }

func (f *fakePrinter) open(string) (usbPort, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens++
	if f.stale != nil {
		f.pending = append(f.pending, f.stale...)
		f.stale = nil
	}
	return &fakePort{p: f}, nil
}

func (f *fakePort) Write(b []byte, _ time.Duration) (int, error) {
	f.p.mu.Lock()
	defer f.p.mu.Unlock()
	switch s := string(b); {
	case strings.HasPrefix(s, printer.CmdHostID):
		f.p.pending = append(f.p.pending, realHI...)
	case strings.HasPrefix(s, printer.CmdHostStatus):
		f.p.pending = append(f.p.pending, realHS...)
	default:
		f.p.labels.Write(b)
	}
	return len(b), nil
}

func (f *fakePort) Read(b []byte, timeout time.Duration) (int, error) {
	f.p.mu.Lock()
	defer f.p.mu.Unlock()
	if len(f.p.pending) == 0 {
		f.p.mu.Unlock()
		time.Sleep(min(timeout, 20*time.Millisecond))
		f.p.mu.Lock()
		return 0, errUSBTimeout
	}
	n := copy(b, f.p.pending[:min(len(f.p.pending), 17)]) // dribble: 17 bytes at a time
	f.p.pending = f.p.pending[n:]
	return n, nil
}

func (f *fakePort) Close() error { return nil }

func withFakeUSB(t *testing.T) *fakePrinter {
	t.Helper()
	fp := &fakePrinter{}
	prev := openUSB
	openUSB = fp.open
	t.Cleanup(func() { openUSB = prev })
	return fp
}

func TestUSBQueryReassemblesChunkedHostStatus(t *testing.T) {
	withFakeUSB(t)
	u := &usbPrinter{dev: usbDevice{Path: "fake"}}
	raw, err := u.query(context.Background(), printer.CmdHostStatus)
	if err != nil {
		t.Fatal(err)
	}
	if raw != realHS {
		t.Fatalf("reply not reassembled:\n got %q\nwant %q", raw, realHS)
	}
	st, err := (&usbSink{u: u}).Status(context.Background())
	if err != nil || st.State != printer.StateIdle {
		t.Fatalf("status: %+v err=%v", st, err)
	}
}

// A reply that arrives after its query gave up must not be read as the next answer.
func TestUSBQueryDrainsStaleReply(t *testing.T) {
	fp := withFakeUSB(t)
	fp.stale = []byte(realHI)
	u := &usbPrinter{dev: usbDevice{Path: "fake"}}
	raw, err := u.query(context.Background(), printer.CmdHostStatus)
	if err != nil {
		t.Fatal(err)
	}
	if raw != realHS {
		t.Fatalf("stale ~HI reply leaked into ~HS answer: %q", raw)
	}
}

func TestUSBWritePassesLabelThrough(t *testing.T) {
	fp := withFakeUSB(t)
	u := &usbPrinter{dev: usbDevice{Path: "fake"}}
	label := "^XA^FO50,50^A0N,40,40^FDhello^FS^XZ"
	n, err := (&usbSink{u: u}).Write(context.Background(), []byte(label))
	if err != nil || n != len(label) {
		t.Fatalf("write: n=%d err=%v", n, err)
	}
	if got := fp.labels.String(); got != label {
		t.Fatalf("printer received %q", got)
	}
}

func TestUSBBackendsIdentifyAndName(t *testing.T) {
	withFakeUSB(t)
	prev := listUSB
	listUSB = func() ([]usbDevice, error) {
		return []usbDevice{{Path: "fake", Key: "19J152400112", ID1284: "MFG:Zebra;MDL:ZTC GK888t (EPL);"}}, nil
	}
	t.Cleanup(func() { listUSB = prev })

	bs := usbBackends(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if len(bs) != 1 {
		t.Fatalf("want 1 backend, got %d", len(bs))
	}
	u := bs[0].(*usbPrinter)
	if u.Model() != "GK888t (EPL)-200dpi" {
		t.Fatalf("model %q", u.Model())
	}
	if got := u.Name(); got != "usb/GK888t__EPL_-200dpi-19J152400112" {
		t.Fatalf("name %q", got)
	}
}

func TestUSBNameIsStableForLocationIDs(t *testing.T) {
	a := usbName("GK888t", "6&2b4c1d&0&1")
	if a != usbName("GK888t", "6&2b4c1d&0&1") || strings.ContainsAny(a, "&#") {
		t.Fatalf("location-based id must hash to a stable, clean name: %q", a)
	}
	if model1284("MANUFACTURER:Zebra Technologies;COMMAND SET:EPL;MODEL:ZTC GK888t (EPL);") != "ZTC GK888t (EPL)" {
		t.Fatal("1284 MODEL not parsed")
	}
}

// Replies without STX/ETX framing (virtual printers, legacy ^HS) end when the line
// goes quiet rather than waiting out the whole timeout.
func TestReadReplyEndsOnQuietForUnframedReplies(t *testing.T) {
	data := []byte("^HS0,0,0,0^HS")
	read := func(p []byte, d time.Duration) (int, error) {
		if len(data) == 0 {
			time.Sleep(min(d, 10*time.Millisecond))
			return 0, errUSBTimeout
		}
		n := copy(p, data)
		data = data[n:]
		return n, nil
	}
	start := time.Now()
	raw, err := readReply(read, "^HS", 3*time.Second)
	if err != nil || raw != "^HS0,0,0,0^HS" {
		t.Fatalf("raw=%q err=%v", raw, err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("waited %v for an unframed reply", time.Since(start))
	}
}

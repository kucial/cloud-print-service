package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/kucial/cloud-print-service/pkg/printer"
)

// servedUSB runs serveConn for a fake USB printer and returns a dialer for new
// connections to it, as the server's print and status connections would be.
func servedUSB(t *testing.T) (*fakePrinter, func() net.Conn) {
	t.Helper()
	fp := withFakeUSB(t)
	u := &usbPrinter{dev: usbDevice{Path: "fake"}}
	p := &localPrinter{backend: u, sink: &usbSink{u: u}}
	a := &agent{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return fp, func() net.Conn {
		client, agentEnd := net.Pipe()
		go a.serveConn(ctx, p, agentEnd)
		t.Cleanup(func() { client.Close() })
		return client
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (f *fakePrinter) wireString() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.wire.String()
}

// queryAsync sends ~HS on its own connection and delivers the reply.
func queryAsync(conn net.Conn) <-chan string {
	got := make(chan string, 1)
	go func() {
		_, _ = conn.Write([]byte(printer.CmdHostStatus))
		buf := make([]byte, 256)
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, _ := conn.Read(buf)
		got <- string(buf[:n])
	}()
	return got
}

// A status query that arrives while a label is still coming in pieces must reach the
// printer after the whole label: inside a ^GF graphic it makes the printer drop the
// label without an error.
func TestHostQueryWaitsForTheLabelToEnd(t *testing.T) {
	fp, dial := servedUSB(t)
	jobConn, statusConn := dial(), dial()

	part1 := "^XA^FO0,0^GFA,4,4,1,FF00"
	part2 := "FF00^FS^X" // ^XZ split across reads
	part3 := "Z\n"
	if _, err := jobConn.Write([]byte(part1)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first piece", func() bool { return fp.wireString() == part1 })

	reply := queryAsync(statusConn)
	time.Sleep(50 * time.Millisecond)
	if strings.Contains(fp.wireString(), printer.CmdHostStatus) {
		t.Fatalf("query reached the printer inside the label: %q", fp.wireString())
	}

	for _, part := range []string{part2, part3} {
		if _, err := jobConn.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	if r := <-reply; r != realHS {
		t.Fatalf("status reply %q", r)
	}
	if want := part1 + part2 + part3 + printer.CmdHostStatus; fp.wireString() != want {
		t.Fatalf("printer received\n %q\nwant\n %q", fp.wireString(), want)
	}
}

// Data with no ^XZ (receipt and office printers) must not hold the printer once the
// sender pauses: the server keeps its print connection open between jobs.
func TestPausedNonZPLDataReleasesThePrinter(t *testing.T) {
	prev := labelIdle
	labelIdle = 50 * time.Millisecond
	t.Cleanup(func() { labelIdle = prev })

	fp, dial := servedUSB(t)
	jobConn, statusConn := dial(), dial()
	receipt := "\x1b@hello receipt\n\x1dV\x00"
	if _, err := jobConn.Write([]byte(receipt)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "receipt", func() bool { return fp.wireString() == receipt })

	select {
	case r := <-queryAsync(statusConn):
		if r != realHS {
			t.Fatalf("status reply %q", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("query never answered: the printer stayed held after the sender paused")
	}
}

func TestLabelEndDetection(t *testing.T) {
	for _, tc := range []struct {
		chunks []string
		want   bool
	}{
		{[]string{"^XA^FDx^FS^XZ"}, true},
		{[]string{"^XA^FDx^FS^XZ\r\n  "}, true},
		{[]string{"^XA^FDx^FS^X", "Z"}, true},
		{[]string{"^XA^FDx^FS^", "xz", "\n"}, true},
		{[]string{"^XA^FDx^FS^XZ^XA^FDy"}, false},
		{[]string{"^XA^GFA,2,2,1,FF"}, false},
	} {
		var l labelState
		var got bool
		for _, c := range tc.chunks {
			got = l.ended([]byte(c))
		}
		if got != tc.want {
			t.Errorf("%q: ended=%v, want %v", tc.chunks, got, tc.want)
		}
	}
}

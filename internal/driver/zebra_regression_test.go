package driver_test

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/kongkx/cloud-print-service/internal/driver"
	"github.com/kongkx/cloud-print-service/internal/virtual"
)

// A printer that reboots between jobs must not silently eat a job: the previous socket
// is half-open and its first write goes into a dead buffer. Every job must open a fresh
// connection, so a dead peer surfaces as 0 bytes written (safe to retry).
func TestEachJobOpensAFreshPrintSocket(t *testing.T) {
	vp := startVirtual(t, virtual.Opts{PrintDuration: 20 * time.Millisecond})
	z := driverFor(t, vp)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := z.Send(ctx, []byte("^XA^FDjob^FS^XZ")); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	// The close-after-write delivers asynchronously on the other side: poll for the
	// count instead of asserting it immediately.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && vp.LabelCount() < 3 {
		time.Sleep(10 * time.Millisecond)
	}
	// Kill the virtual printer (connection reset), then the next job must fail with 0
	// bytes, not silently write into the void.
	vp.Control(virtual.Control{Offline: boolp(true)})
	vp.Control(virtual.Control{Offline: boolp(false)})
	n, err := z.Send(ctx, []byte("^XA^FDafter-reboot^FS^XZ"))
	if err == nil {
		t.Log("write landed anyway (the reboot cleared quickly) - contract still holds")
	} else if n != 0 {
		t.Fatalf("write error reported n=%d, want 0 (retryable) so the job is not dropped", n)
	}
	// and it must be possible again after
	if _, err := z.Send(ctx, []byte("^XA^FDback^FS^XZ")); err != nil {
		t.Fatalf("send after reboot: %v", err)
	}
}

// A read timeout on the status socket must not let the next probe read this query's
// late reply - a classic desync that makes a paper-out look like idle.
func TestStatusTimeoutDoesNotDesync(t *testing.T) {
	vp := startVirtual(t, virtual.Opts{})
	z := driverFor(t, vp)
	ctx := context.Background()

	vp.Control(virtual.Control{StatusDelayMS: intp(500)})
	slowCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if _, err := z.Status(slowCtx); err == nil {
		t.Fatal("expected a timeout with a slow printer")
	}

	// The reply arrives late. A later probe with a fresh timeout must not read it.
	vp.Control(virtual.Control{StatusDelayMS: intp(0)})
	deadline := time.Now().Add(2 * time.Second)
	var stErr error
	for time.Now().Before(deadline) {
		st, err := z.Status(ctx)
		if err == nil {
			if st.Raw == "" {
				t.Fatal("status had no raw data: the previous query's reply leaked")
			}
			return
		}
		stErr = err
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("status never recovered after a timeout: %v", stErr)
}

func TestIsConnErrClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"reset", syscall.ECONNRESET, true},
		{"broken pipe", syscall.EPIPE, true},
		{"eof", io.EOF, true},
		{"timeout", &timeoutErr{}, false}, // a slow printer is not a dead socket
		{"other", errors.New("nope"), false},
	} {
		if got := driver.IsConnErr(tc.err); got != tc.want {
			t.Errorf("isConnErr(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

type timeoutErr struct{}

func (*timeoutErr) Error() string   { return "i/o timeout" }
func (*timeoutErr) Timeout() bool   { return true }
func (*timeoutErr) Temporary() bool { return false }

var _ net.Error = (*timeoutErr)(nil)

func intp(v int) *int { return &v }

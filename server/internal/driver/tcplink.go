package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// tcpLink is the raw-TCP plumbing every port-9100 printer shares, whatever language it
// speaks (ZPL, ESC/POS, PCL): a print socket opened per job and a separate, long-lived
// status socket for queries.
//
// Two independent sockets are used on purpose: printer status replies are interleaved
// into whatever the print socket is sending, so status traffic gets its own connection
// and can never corrupt document data or starve behind a large payload.
type tcpLink struct {
	host string
	port int
	opts Options

	pmu      sync.Mutex
	printCon net.Conn

	// statusMu is independent of pmu so a status probe never blocks on a print write.
	// (Printers are effectively single-connection for inbound status replies.)
	statusMu  sync.Mutex
	statusCon net.Conn
}

func newTCPLink(host string, port int, o Options) *tcpLink {
	if port == 0 {
		port = 9100
	}
	return &tcpLink{host: host, port: port, opts: o.withDefaults()}
}

func (l *tcpLink) addr() string { return net.JoinHostPort(l.host, strconv.Itoa(l.port)) }

func (l *tcpLink) dial(ctx context.Context) (net.Conn, error) {
	d := net.Dialer{Timeout: l.opts.DialTimeout}
	return d.DialContext(ctx, "tcp", l.addr())
}

func deadlineFor(ctx context.Context, d time.Duration) time.Time {
	dl := time.Now().Add(d)
	if cd, ok := ctx.Deadline(); ok && cd.Before(dl) {
		return cd
	}
	return dl
}

// send writes one document. Bytes actually flushed are returned; err != nil with n > 0
// means a partial document may exist on the printer and the caller must not retry.
func (l *tcpLink) send(ctx context.Context, data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	l.pmu.Lock()
	defer l.pmu.Unlock()

	if err := ctx.Err(); err != nil {
		return 0, err
	}
	// The print socket is per-job, not pooled across jobs: a printer that rebooted since
	// the last job would have a half-open socket whose write "succeeds" into the kernel
	// buffer, and the job layer could no longer tell whether a byte reached the printer.
	// A fresh dial keeps "0 bytes written" as the reliable, retryable signal it is.
	l.dropPrint()
	c, err := l.dial(ctx)
	if err != nil {
		return 0, err // 0 bytes -> caller may retry
	}
	l.printCon = c
	_ = l.printCon.SetWriteDeadline(deadlineFor(ctx, l.opts.WriteTimeout))
	n, err := l.printCon.Write(data)
	// Whatever happened, the socket is now single-use: the next job opens a fresh one.
	l.dropPrint()
	return n, err
}

// control writes a command on the print connection, so it is ordered with document
// data instead of racing it (cancel, calibrate, saved settings).
func (l *tcpLink) control(ctx context.Context, cmd []byte) error {
	l.pmu.Lock()
	defer l.pmu.Unlock()
	if l.printCon == nil {
		c, err := l.dial(ctx)
		if err != nil {
			return err
		}
		l.printCon = c
	}
	_ = l.printCon.SetWriteDeadline(deadlineFor(ctx, l.opts.WriteTimeout))
	if _, err := l.printCon.Write(cmd); err != nil {
		l.dropPrint()
		return err
	}
	return nil
}

func (l *tcpLink) dropPrint() {
	if l.printCon != nil {
		_ = l.printCon.Close()
		l.printCon = nil
	}
}

func (l *tcpLink) dropStatus() {
	if l.statusCon != nil {
		_ = l.statusCon.Close()
		l.statusCon = nil
	}
}

// query writes cmd on the status socket and accumulates the reply until complete says
// it is whole, maxBytes arrive, or the line goes quiet.
//
// A printer reboot (or a network blip) leaves a half-open socket that fails on write
// rather than on connect, so one transparent redial-and-retry is normal housekeeping,
// not an error the caller should see.
func (l *tcpLink) query(ctx context.Context, cmd []byte, maxBytes int, complete func([]byte) bool) ([]byte, error) {
	l.statusMu.Lock()
	defer l.statusMu.Unlock()

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			l.dropStatus()
		}
		raw, err := l.queryOnce(ctx, cmd, maxBytes, complete)
		if err == nil {
			return raw, nil
		}
		lastErr = err
		if ctx.Err() != nil || !IsConnErr(err) {
			break
		}
	}
	return nil, lastErr
}

// IsConnErr reports whether err means the socket itself is dead and the query may be
// redialed. Exported because the classification is load-bearing: timeouts must not retry.
func IsConnErr(err error) bool {
	if isTimeout(err) {
		return false // a slow printer is not a dead socket; redialing wastes a cycle
	}
	return errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE)
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func (l *tcpLink) queryOnce(ctx context.Context, cmd []byte, maxBytes int, complete func([]byte) bool) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if l.statusCon == nil {
		c, err := l.dial(ctx)
		if err != nil {
			return nil, err
		}
		l.statusCon = c
	}
	_ = l.statusCon.SetWriteDeadline(deadlineFor(ctx, l.opts.DialTimeout))
	if _, err := l.statusCon.Write(cmd); err != nil {
		l.dropStatus()
		return nil, err
	}

	buf := make([]byte, maxBytes)
	var acc []byte
	first := true
	// Read until the reply is complete (framed replies) or the line goes quiet. Large
	// replies such as ^HH arrive in many segments.
	for i := 0; i < 64; i++ {
		d := l.opts.ReadTimeout
		if !first {
			d = 400 * time.Millisecond // printers often dribble the reply in pieces
		}
		first = false
		_ = l.statusCon.SetReadDeadline(deadlineFor(ctx, d))
		n, err := l.statusCon.Read(buf)
		if n > 0 {
			acc = append(acc, buf[:n]...)
			if len(acc) >= maxBytes || (complete != nil && complete(acc)) {
				break
			}
		}
		if err != nil {
			if len(acc) > 0 {
				break // partial reply is still useful
			}
			// A read error (timeout included) means the next probe might read this
			// command's late reply as the answer to its own - drop the socket so each
			// query starts aligned.
			l.dropStatus()
			return acc, err
		}
	}
	if len(acc) == 0 {
		l.dropStatus()
		return nil, fmt.Errorf("no reply to %q from %s", cmd, l.addr())
	}
	return acc, nil
}

// close releases both sockets.
func (l *tcpLink) close() error {
	l.pmu.Lock()
	l.dropPrint()
	l.pmu.Unlock()
	l.statusMu.Lock()
	l.dropStatus()
	l.statusMu.Unlock()
	return nil
}

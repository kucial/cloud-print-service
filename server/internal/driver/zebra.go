package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kucial/cloud-print-service/server/internal/domain"
)

func init() {
	Register("zebra9100", func(p domain.Printer) (Driver, error) {
		return NewZebra(p, ZebraOpts{DialTimeout: 3 * time.Second, WriteTimeout: 5 * time.Second, ReadTimeout: 3 * time.Second}), nil
	})
}

// Zebra speaks raw ZPL over TCP 9100.
//
// Two independent sockets are used on purpose: printer status replies are interleaved
// into whatever the print socket is sending, so status traffic gets its own connection
// and can never corrupt label data or starve behind a large payload.
type Zebra struct {
	host string
	port int

	dialTimeout  time.Duration
	writeTimeout time.Duration
	readTimeout  time.Duration

	pmu      sync.Mutex
	printCon net.Conn

	// statusMu is independent of pmu so a status probe never blocks on a print write.
	// (Printers are effectively single-connection for inbound status replies.)
	statusMu  sync.Mutex
	statusCon net.Conn
}

type ZebraOpts struct {
	DialTimeout  time.Duration
	WriteTimeout time.Duration
	ReadTimeout  time.Duration
}

func NewZebra(p domain.Printer, o ZebraOpts) *Zebra {
	if o.DialTimeout <= 0 {
		o.DialTimeout = 3 * time.Second
	}
	if o.WriteTimeout <= 0 {
		o.WriteTimeout = 5 * time.Second
	}
	if o.ReadTimeout <= 0 {
		o.ReadTimeout = 3 * time.Second
	}
	port := p.Port
	if port == 0 {
		port = 9100
	}
	return &Zebra{host: p.Host, port: port, dialTimeout: o.DialTimeout, writeTimeout: o.WriteTimeout, readTimeout: o.ReadTimeout}
}

func (z *Zebra) addr() string { return net.JoinHostPort(z.host, strconv.Itoa(z.port)) }

func (z *Zebra) dial(ctx context.Context) (net.Conn, error) {
	d := net.Dialer{Timeout: z.dialTimeout}
	return d.DialContext(ctx, "tcp", z.addr())
}

func deadlineFor(ctx context.Context, d time.Duration) time.Time {
	dl := time.Now().Add(d)
	if cd, ok := ctx.Deadline(); ok && cd.Before(dl) {
		return cd
	}
	return dl
}

// Send writes raw ZPL. Bytes actually flushed are returned; err != nil with n > 0 means
// a partial label may exist on the printer and the caller must not retry.
func (z *Zebra) Send(ctx context.Context, data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	z.pmu.Lock()
	defer z.pmu.Unlock()

	if err := ctx.Err(); err != nil {
		return 0, err
	}
	// The print socket is per-job, not pooled across jobs: a printer that rebooted since
	// the last job would have a half-open socket whose write "succeeds" into the kernel
	// buffer, and the job layer could no longer tell whether a byte reached the printer.
	// A fresh dial keeps "0 bytes written" as the reliable, retryable signal it is.
	z.dropPrint()
	c, err := z.dial(ctx)
	if err != nil {
		return 0, err // 0 bytes -> caller may retry
	}
	z.printCon = c
	_ = z.printCon.SetWriteDeadline(deadlineFor(ctx, z.writeTimeout))
	n, err := z.printCon.Write(data)
	// Whatever happened, the socket is now single-use: the next job opens a fresh one.
	z.dropPrint()
	return n, err
}

func (z *Zebra) dropPrint() {
	if z.printCon != nil {
		_ = z.printCon.Close()
		z.printCon = nil
	}
}

func (z *Zebra) dropStatus() {
	if z.statusCon != nil {
		_ = z.statusCon.Close()
		z.statusCon = nil
	}
}

// query writes cmd on the status socket and accumulates the reply.
//
// A printer reboot (or a network blip) leaves a half-open socket that fails on write
// rather than on connect, so one transparent redial-and-retry is normal housekeeping,
// not an error the caller should see.
func (z *Zebra) query(ctx context.Context, cmd string, maxBytes int) (string, error) {
	z.statusMu.Lock()
	defer z.statusMu.Unlock()

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			z.dropStatus()
		}
		raw, err := z.queryOnce(ctx, cmd, maxBytes)
		if err == nil {
			return raw, nil
		}
		lastErr = err
		if ctx.Err() != nil || !IsConnErr(err) {
			break
		}
	}
	return "", lastErr
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

func (z *Zebra) queryOnce(ctx context.Context, cmd string, maxBytes int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if z.statusCon == nil {
		c, err := z.dial(ctx)
		if err != nil {
			return "", err
		}
		z.statusCon = c
	}
	_ = z.statusCon.SetWriteDeadline(deadlineFor(ctx, z.dialTimeout))
	if _, err := z.statusCon.Write([]byte(cmd)); err != nil {
		z.dropStatus()
		return "", err
	}

	buf := make([]byte, maxBytes)
	var acc []byte
	first := true
	for i := 0; i < 4; i++ {
		d := z.readTimeout
		if !first {
			d = 400 * time.Millisecond // printers often dribble the reply in pieces
		}
		first = false
		_ = z.statusCon.SetReadDeadline(deadlineFor(ctx, d))
		n, err := z.statusCon.Read(buf)
		if n > 0 {
			acc = append(acc, buf[:n]...)
			if len(acc) >= maxBytes {
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
			z.dropStatus()
			return string(acc), err
		}
	}
	if len(acc) == 0 {
		z.dropStatus()
		return "", fmt.Errorf("no reply to %s from %s", strings.TrimSpace(cmd), z.addr())
	}
	return string(acc), nil
}

// Status probes with the ZPL host status command. Falls back to reachability-only when the
// model does not answer, so the API contract stays intact (state becomes `unknown`).
func (z *Zebra) Status(ctx context.Context) (domain.Status, error) {
	raw, err := z.query(ctx, "^HS", 128)
	now := time.Now()
	if err != nil {
		st := domain.Status{State: domain.StateOffline, ObservedAt: now, Detail: "status query failed"}
		return st, err
	}
	st, ok := ParseHostStatus([]byte(raw))
	st.ObservedAt = now
	if !ok {
		st.State = domain.StateUnknown
		st.Detail = "unrecognised host status reply"
	}
	return st, nil
}

// Cancel aborts printing in progress (ZPL ~JA).
func (z *Zebra) Cancel(ctx context.Context) error {
	z.pmu.Lock()
	defer z.pmu.Unlock()
	if z.printCon == nil {
		c, err := z.dial(ctx)
		if err != nil {
			return err
		}
		z.printCon = c
	}
	_ = z.printCon.SetWriteDeadline(deadlineFor(ctx, z.writeTimeout))
	_, err := z.printCon.Write([]byte("~JA"))
	if err != nil {
		z.dropPrint()
	}
	return err
}

// Identify reads model / serial / firmware via ~HI. Used when onboarding a printer.
func (z *Zebra) Identify(ctx context.Context) (map[string]string, error) {
	raw, err := z.query(ctx, "~HI", 256)
	if err != nil {
		return map[string]string{"raw": raw}, err
	}
	out := map[string]string{"raw": strings.TrimSpace(raw)}
	// Zebra answers ~HI with e.g. ";8=;9=ZT411;10=...;11=...;12=SN;13=FW;"
	for _, kv := range splitFields(raw) {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			if v := strings.TrimSpace(parts[1]); v != "" {
				out[parts[0]] = v
			}
		}
	}
	return out, nil
}

func splitFields(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == '\n' || r == '\r' })
}

// Close releases both sockets.
func (z *Zebra) Close() error {
	z.pmu.Lock()
	z.dropPrint()
	z.pmu.Unlock()
	z.statusMu.Lock()
	z.dropStatus()
	z.statusMu.Unlock()
	return nil
}

// ParseHostStatus decodes a ZPL ^HS reply: ^HS<device>,<media>,<ribbon>,<head>^HS
//
// Raw is always preserved, so a model whose semantics differ can be fixed here alone.
// See docs/DESIGN.md §5 — verify this table against the actual fleet (milestone M1).
func ParseHostStatus(raw []byte) (domain.Status, bool) {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return domain.Status{State: domain.StateUnknown, Detail: "empty host status reply"}, false
	}
	st := domain.Status{Raw: s}

	seg := s
	if i := strings.Index(seg, "^HS"); i >= 0 {
		seg = seg[i+3:]
	}
	nums := intsIn(seg)
	if len(nums) == 0 {
		st.State = domain.StateUnknown
		st.Detail = "no numeric fields in host status reply"
		return st, false
	}
	device := nums[0]
	media := at(nums, 1)
	ribbon := at(nums, 2)
	head := at(nums, 3)

	switch {
	case head == 1:
		st.State = domain.StateHeadError
	case device == 3:
		st.State = domain.StateError
	case ribbon == 2:
		st.State = domain.StateRibbonOut
	case media == 2:
		st.State = domain.StateMediaOut
	case device == 1:
		st.State = domain.StatePrinting
	case device == 2:
		st.State = domain.StateBusy
	case media == 1 || ribbon == 1:
		st.State = domain.StateMediaLow
	default:
		st.State = domain.StateIdle
	}
	st.Detail = fmt.Sprintf("device=%d media=%d ribbon=%d head=%d", device, media, ribbon, head)
	return st, true
}

func at(s []int, i int) int {
	if i < len(s) {
		return s[i]
	}
	return 0
}

func intsIn(s string) []int {
	var out []int
	cur := strings.Builder{}
	flush := func() {
		if cur.Len() > 0 {
			if n, err := strconv.Atoi(cur.String()); err == nil {
				out = append(out, n)
			}
			cur.Reset()
		}
	}
	for _, r := range s {
		if r >= '0' && r <= '9' {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

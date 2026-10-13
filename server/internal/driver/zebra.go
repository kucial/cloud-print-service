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

	"github.com/kucial/cloud-print-service/pkg/printer"
	"github.com/kucial/cloud-print-service/server/internal/domain"
)

func init() {
	Register(printer.DriverZebra, func(p domain.Printer, o Options) (Driver, error) { return NewZebra(p, o), nil })
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

// ZebraOpts is the former name of Options.
type ZebraOpts = Options

func NewZebra(p domain.Printer, o Options) *Zebra {
	o = o.withDefaults()
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
	// Read until the reply is complete (framed replies) or the line goes quiet. Large
	// replies such as ^HH arrive in many segments.
	for i := 0; i < 64; i++ {
		d := z.readTimeout
		if !first {
			d = 400 * time.Millisecond // printers often dribble the reply in pieces
		}
		first = false
		_ = z.statusCon.SetReadDeadline(deadlineFor(ctx, d))
		n, err := z.statusCon.Read(buf)
		if n > 0 {
			acc = append(acc, buf[:n]...)
			if len(acc) >= maxBytes || printer.ReplyComplete(cmd, acc) {
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
	raw, err := z.query(ctx, printer.CmdHostStatus, 256)
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

// ParseHIResponse parses Zebra ~HI host identification string into structured hardware identity.
// Standard fields in Zebra ~HI:
//
//	 8: Vendor
//	 9: Model (e.g. "ZT411")
//	10: Resolution in dpmm or dpi (e.g. "8.0", "12.0", "203", "300")
//	11: Options / Memory / Installed fonts
//	12: Serial number
//	13: Firmware version
func ParseHIResponse(raw string) (model, serial, firmware string, dpi int, dpmm float64, cjkFont string) {
	if !strings.Contains(raw, "=") {
		// Real firmware: MODEL,Vfirmware,dpmm,memory — no serial, no font list.
		if id, ok := printer.ParseHostID(raw); ok {
			model, firmware, dpmm = id.Model, id.Firmware, id.DPMM
			dpi = printer.DPIFromDPMM(dpmm)
		}
		return
	}
	for _, kv := range splitFields(raw) {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			continue
		}
		k := strings.TrimSpace(parts[0])
		v := strings.TrimSpace(parts[1])
		switch k {
		case "9":
			model = v
		case "12":
			serial = v
		case "13":
			firmware = v
		case "10":
			if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
				if f >= 100 {
					dpi = int(f)
					dpmm = float64(int((f/25.4)*10+0.5)) / 10
				} else {
					dpmm = f
					switch int(f) {
					case 8:
						dpi = 203
					case 12:
						dpi = 300
					case 24:
						dpi = 600
					default:
						dpi = int(f*25.4 + 0.5)
					}
				}
			}
		case "11":
			upper := strings.ToUpper(v)
			if strings.Contains(upper, "SIMSUN") {
				cjkFont = "SIMSUN.FNT"
			} else if strings.Contains(upper, "ANSD") || strings.Contains(upper, "ANDALE") {
				cjkFont = "ANSD.TTF"
			} else if strings.Contains(upper, "NOTO") {
				cjkFont = "NotoSansCJK"
			} else if strings.HasSuffix(upper, ".FNT") || strings.HasSuffix(upper, ".TTF") {
				cjkFont = v
			}
		}
	}
	return
}

// Identify reads model / serial / firmware via ~HI. Used when onboarding a printer.
func (z *Zebra) Identify(ctx context.Context) (map[string]string, error) {
	raw, err := z.query(ctx, "~HI", 256)
	if err != nil {
		return map[string]string{"raw": raw}, err
	}
	out := map[string]string{"raw": strings.TrimSpace(raw)}
	for _, kv := range splitFields(raw) {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			if v := strings.TrimSpace(parts[1]); v != "" {
				out[parts[0]] = v
			}
		}
	}
	model, serial, fw, dpi, dpmm, cjk := ParseHIResponse(raw)
	if model != "" {
		out["model"] = model
	}
	if serial != "" {
		out["serial"] = serial
	}
	if fw != "" {
		out["firmware"] = fw
	}
	if dpi > 0 {
		out["dpi"] = strconv.Itoa(dpi)
		out["dpmm"] = fmt.Sprintf("%g", dpmm)
		out["dpi_resolved_from_hi"] = "true"
		out["resolution_source"] = "hi"
	}
	if cjk != "" {
		out["cjk_font"] = cjk
		out["font_resolved_from_hi"] = "true"
		out["font_source"] = "hi"
	}
	return out, nil
}

func splitFields(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == '\n' || r == '\r' })
}

// HostConfig returns the printer's ^HH configuration dump, raw.
func (z *Zebra) HostConfig(ctx context.Context) (string, error) {
	return z.query(ctx, printer.CmdHostConfig, 16<<10)
}

// HostDirectory lists the printer's stored objects (^HW), raw.
func (z *Zebra) HostDirectory(ctx context.Context) (string, error) {
	return z.query(ctx, printer.CmdHostDirectory, 32<<10)
}

// Control sends a maintenance command (~JC calibrate, ^LT…^JUS) on the print connection,
// so it is ordered with label data instead of racing it.
func (z *Zebra) Control(ctx context.Context, cmd string) error {
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
	if _, err := z.printCon.Write([]byte(cmd)); err != nil {
		z.dropPrint()
		return err
	}
	return nil
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

// ParseHostStatus decodes a host status reply: real firmware's three-line ~HS answer, or
// the legacy ^HS<device>,<media>,<ribbon>,<head>^HS form. See printer.ParseHostStatus.
func ParseHostStatus(raw []byte) (domain.Status, bool) {
	state, detail, ok := printer.ParseHostStatus(string(raw))
	return domain.Status{Raw: strings.TrimSpace(string(raw)), State: state, Detail: detail}, ok
}

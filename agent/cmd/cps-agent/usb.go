package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/kucial/cloud-print-service/pkg/agentapi"
	"github.com/kucial/cloud-print-service/pkg/printer"
)

// USB tier: talk to a printer through the operating system's own USB printer class
// driver — usbprint.sys on Windows, usblp on Linux. Both expose the printer as a
// bidirectional byte pipe, so the agent can send documents and read status replies
// (~HS for labels, DLE EOT for receipts, PJL for office printers) with no driver swap,
// no libusb and no cgo. The vendor print driver (ZDesigner) stays
// installed and keeps working, because the agent opens the device only for the length
// of one operation.

// usbDevice is a USB printer found by the platform layer.
type usbDevice struct {
	Path   string // what openUSBPort opens: an interface path or /dev/usb/lpN
	Key    string // stable per printer (USB serial, or the port it is plugged into)
	ID1284 string // IEEE-1284 device id, e.g. "MFG:Zebra;MDL:GK888t;…" (may be empty)
	// WriteOnly is a printer interface with no way back (no IN endpoint): it prints,
	// but cannot answer status queries.
	WriteOnly bool
}

// usbPort is one open handle to a device. Timeouts are per call: a read that sees no
// data returns 0, errUSBTimeout.
type usbPort interface {
	Write(p []byte, timeout time.Duration) (int, error)
	Read(p []byte, timeout time.Duration) (int, error)
	Close() error
}

var errUSBTimeout = errors.New("usb i/o timeout")

// errNoReply is a query the printer took but did not answer in time.
var errNoReply = errors.New("no reply")

// openUSB and listUSB are swapped out by tests.
var (
	openUSB = openUSBPort
	listUSB = listUSBPrinters
)

type usbPrinter struct {
	dev     usbDevice
	model   string
	key     string // USB serial, or a stable hash of the port for serial-less devices
	profile printer.Profile

	// One pipe carries both label data and status replies, so every operation holds
	// mu: a ~HS must never land in the middle of a label, nor a label eat a reply.
	mu sync.Mutex
}

func (u *usbPrinter) Name() string             { return "usb/" + u.key }
func (u *usbPrinter) Model() string            { return u.model }
func (u *usbPrinter) Key() string              { return u.key }
func (u *usbPrinter) Profile() printer.Profile { return u.profile }

func (u *usbPrinter) Open(ctx context.Context) (Sink, error) { return &usbSink{u: u}, nil }
func (u *usbPrinter) Close() error                           { return nil }

// open retries briefly when the device is busy (the spooler is printing through it);
// nothing has been sent yet, so retrying cannot duplicate a label.
func (u *usbPrinter) open(ctx context.Context) (usbPort, error) {
	var lastErr error
	for i := 0; i < 10; i++ {
		port, err := openUSB(u.dev.Path)
		if err == nil {
			return port, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("open %s: %w", u.dev.Path, lastErr)
}

// query sends a status or identity command and returns the printer's reply.
func (u *usbPrinter) query(ctx context.Context, cmd string) (string, error) {
	if u.dev.WriteOnly {
		return "", errWriteOnly
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	port, err := u.open(ctx)
	if err != nil {
		return "", err
	}
	defer port.Close()

	// A reply that arrived after an earlier query gave up would otherwise be read as
	// the answer to this one.
	drain := make([]byte, 512)
	for i := 0; i < 8; i++ {
		if n, _ := port.Read(drain, 30*time.Millisecond); n == 0 {
			break
		}
	}
	if _, err := port.Write([]byte(cmd), 3*time.Second); err != nil {
		return "", err
	}
	return readReply(port.Read, cmd, 3*time.Second)
}

type usbSink struct{ u *usbPrinter }

func (s *usbSink) Write(ctx context.Context, p []byte) (int, error) {
	s.u.mu.Lock()
	defer s.u.mu.Unlock()
	port, err := s.u.open(ctx)
	if err != nil {
		return 0, err
	}
	defer port.Close()
	timeout := 30 * time.Second
	if d, ok := ctx.Deadline(); ok {
		timeout = time.Until(d)
	}
	// n>0 with an error is reported as is: the caller must not retry a partial label.
	return port.Write(p, timeout)
}

func (s *usbSink) Status(ctx context.Context) (agentapi.Status, error) {
	return statusFor(ctx, s.u.profile, s.u.query)
}

func (s *usbSink) Close() error { return nil }

// usbBackends finds USB printers and works out what each is from its 1284 device id:
// a Zebra is confirmed with ~HI, a receipt or office printer is asked its model in its
// own language. A device the id does not place is tried as a Zebra, as before.
func usbBackends(ctx context.Context, log *slog.Logger) []Backend {
	devs, err := listUSB()
	if err != nil {
		log.Warn("cannot list USB printers", "err", err)
		return nil
	}
	var out []Backend
	for _, d := range devs {
		u := &usbPrinter{dev: d, key: usbKey(d.Key), profile: printer.ParseDeviceID(d.ID1284).Profile()}
		qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		switch u.profile.Driver {
		case printer.DriverESCPOS, printer.DriverPDL:
			u.model = identifyModel(qctx, u.profile, model1284(d.ID1284), u.query)
			cancel()
			log.Info("found USB printer", "model", u.model, "kind", u.profile.Kind, "driver", u.profile.Driver,
				"formats", u.profile.Formats, "write_only", d.WriteOnly, "key", d.Key, "device", d.ID1284)
			out = append(out, u)
			continue
		}
		raw, err := u.query(qctx, printer.CmdHostID)
		cancel()
		id, ok := printer.ParseHostID(raw)
		if err != nil || !ok {
			// Without ~HI there is no ~HS either, and the server could never tell a
			// printed label from a lost one; serving it would only look broken.
			log.Warn("USB printer did not identify itself; skipping",
				"model", model1284(d.ID1284), "device", d.ID1284, "path", d.Path, "err", err, "reply", raw,
				"hint", "a Zebra must be in ZPL mode (EPL printers do not answer ~HI/~HS); other printers are "+
					"recognised by the command set in their device id")
			continue
		}
		u.model, u.profile = id.Model, zebraProfile
		log.Info("found USB printer", "model", id.Model, "firmware", id.Firmware, "kind", u.profile.Kind,
			"key", d.Key, "device", d.ID1284)
		out = append(out, u)
	}
	return out
}

// usbKey identifies a USB printer in its printer id: the USB serial number, which
// survives restarts and moving the cable to another port. The model is not part of it
// (it has its own field).
func usbKey(key string) string {
	if key == "" || strings.ContainsAny(key, "&\\/#{}") {
		// Windows makes up location-based instance ids ("6&2b4c…&0&1") for devices
		// without a serial; those are stable per port, so hash them short.
		sum := sha256.Sum256([]byte(key))
		return "usb" + hex.EncodeToString(sum[:4])
	}
	return sanitizeID(key)
}

func sanitizeID(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

// model1284 extracts the model from an IEEE-1284 device id ("…;MDL:GK888t;…").
func model1284(id string) string {
	for _, f := range strings.Split(id, ";") {
		k, v, ok := strings.Cut(f, ":")
		if !ok {
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(k)) {
		case "MDL", "MODEL":
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// readReply collects the reply to a query: until replyComplete, or,
// for replies without framing (virtual printers), until the line goes quiet. read returns 0, errUSBTimeout (or any timeout) when no data
// arrived within its window.
func readReply(read func(p []byte, timeout time.Duration) (int, error), cmd string, total time.Duration) (string, error) {
	const quiet = 300 * time.Millisecond
	deadline := time.Now().Add(total)
	buf := make([]byte, 512)
	var acc []byte
	for time.Now().Before(deadline) {
		window := time.Until(deadline)
		if len(acc) > 0 && window > quiet {
			window = quiet
		}
		n, err := read(buf, window)
		if n > 0 {
			acc = append(acc, buf[:n]...)
			if replyComplete(cmd, acc) {
				break
			}
			continue
		}
		if len(acc) > 0 {
			break // quiet after some data: the reply is complete
		}
		if err != nil && !isTimeoutErr(err) {
			return "", err
		}
		if err == nil {
			time.Sleep(20 * time.Millisecond) // a driver that returns 0 bytes at once
		}
	}
	if len(acc) == 0 {
		return "", fmt.Errorf("%w to %q", errNoReply, strings.TrimSpace(cmd))
	}
	return string(acc), nil
}

func isTimeoutErr(err error) bool {
	if errors.Is(err, errUSBTimeout) {
		return true
	}
	var te interface{ Timeout() bool }
	return errors.As(err, &te) && te.Timeout()
}

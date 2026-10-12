package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kucial/cloud-print-service/pkg/agentapi"
	"github.com/kucial/cloud-print-service/pkg/printer"
)

// Tier 1 backend: a driver-provided local TCP share.
//
// This is the case that decides how much USB work is really needed. On a Windows packing
// PC with Zebra's driver installed, the printer is already listening on a loopback port
// and answers the same `^HI`/`^HS` commands a network printer would — so the agent can
// serve it with no privileges, no driver swap and no raw USB code.
//
// Ports we look for: the ones Zebra-family drivers are known to use, plus whatever the
// config pins. Ports are probed with a short `^HI`, so an unrelated service is ignored.
var defaultSharePorts = []int{9100, 9200, 9101, 9102, 2100, 6400}

type tcpShare struct {
	host string
	port int

	mu sync.Mutex
	// Each printer gets one control connection for ^HI/^HS and one for data, mirroring
	// the network driver: replies interleave, so mixing them corrupts both.
	ctl net.Conn
}

func (s *tcpShare) Name() string { return fmt.Sprintf("tcp-share/%s:%d", s.host, s.port) }

// Open probes the share and returns a sink. The agent does not retry here either: the
// server owns retry policy, and a failed Open simply means the printer is unavailable.
func (s *tcpShare) Open(ctx context.Context) (Sink, error) {
	d := net.Dialer{Timeout: 2 * time.Second}
	ctl, err := d.DialContext(ctx, "tcp", s.addr())
	if err != nil {
		return nil, fmt.Errorf("driver share not reachable: %w", err)
	}
	s.mu.Lock()
	s.ctl = ctl
	s.mu.Unlock()
	return &tcpSink{share: s}, nil
}

func (s *tcpShare) addr() string { return net.JoinHostPort(s.host, strconv.Itoa(s.port)) }

func (s *tcpShare) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctl != nil {
		_ = s.ctl.Close()
		s.ctl = nil
	}
	return nil
}

// query sends a short command and returns the reply, redialing once on a stale socket.
func (s *tcpShare) query(ctx context.Context, cmd string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 && s.ctl != nil {
			_ = s.ctl.Close()
			s.ctl = nil
		}
		if s.ctl == nil {
			d := net.Dialer{Timeout: 2 * time.Second}
			c, err := d.DialContext(ctx, "tcp", s.addr())
			if err != nil {
				lastErr = err
				continue
			}
			s.ctl = c
		}
		_ = s.ctl.SetWriteDeadline(time.Now().Add(3 * time.Second))
		if _, err := s.ctl.Write([]byte(cmd)); err != nil {
			lastErr = err
			continue
		}
		conn := s.ctl
		reply, err := readReply(func(p []byte, d time.Duration) (int, error) {
			_ = conn.SetReadDeadline(time.Now().Add(d))
			return conn.Read(p)
		}, cmd, 3*time.Second)
		if err == nil {
			return reply, nil
		}
		lastErr = err
	}
	return "", lastErr
}

type tcpSink struct {
	share *tcpShare
	mu    sync.Mutex
	data  net.Conn
}

func (s *tcpSink) Write(ctx context.Context, p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		d := net.Dialer{Timeout: 2 * time.Second}
		c, err := d.DialContext(ctx, "tcp", s.share.addr())
		if err != nil {
			return 0, err
		}
		s.data = c
	}
	_ = s.data.SetWriteDeadline(time.Now().Add(30 * time.Second))
	n, err := s.data.Write(p)
	if err != nil {
		// The stream position is unknown now; drop the socket and report n honestly.
		_ = s.data.Close()
		s.data = nil
	}
	return n, err
}

func (s *tcpSink) Status(ctx context.Context) (agentapi.Status, error) {
	raw, err := s.share.query(ctx, printer.CmdHostStatus)
	now := time.Now().UTC()
	if err != nil {
		return agentapi.Status{State: printer.StateOffline, ObservedAt: now, Detail: err.Error()}, err
	}
	return ParseHostStatus(raw, now), nil
}

func (s *tcpSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data != nil {
		_ = s.data.Close()
		s.data = nil
	}
	return s.share.Close()
}

// Identify reads model/serial over `~HI`.
func (s *tcpShare) Identify(ctx context.Context) (model string, serial string, err error) {
	raw, err := s.query(ctx, printer.CmdHostID)
	if err != nil {
		return "", "", err
	}
	id, ok := printer.ParseHostID(raw)
	if !ok {
		return "", "", fmt.Errorf("unrecognised ~HI reply %q", raw)
	}
	return id.Model, id.Serial, nil
}

// ParseHostStatus decodes a host status reply (real ~HS or legacy ^HS form) for a
// heartbeat. The decoding itself lives in pkg/printer, shared with the server.
func ParseHostStatus(raw string, observedAt time.Time) agentapi.Status {
	state, detail, _ := printer.ParseHostStatus(raw)
	return agentapi.Status{Raw: strings.TrimSpace(raw), State: state, Detail: detail, ObservedAt: observedAt}
}

// probeBackends finds usable printers, cheapest tier first.
//
// It is the function tests point at a virtual printer: run `vprint` on loopback and the
// agent discovers it exactly as it would a driver share, which is what makes the whole
// agent testable without hardware.
func probeBackends(ctx context.Context, log *slog.Logger) ([]Backend, error) {
	ports := defaultSharePorts
	if extra := os.Getenv("CPS_AGENT_PORTS"); extra != "" {
		ports = nil
		for _, p := range strings.Split(extra, ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
				ports = append(ports, n)
			}
		}
	}
	var out []Backend
	for _, p := range ports {
		if ctx.Err() != nil {
			break
		}
		share := &tcpShare{host: "127.0.0.1", port: p}
		model, serial, err := share.Identify(ctx)
		if err != nil {
			continue // not a printer share
		}
		log.Info("found local printer share", "port", p, "model", model, "serial", serial)
		out = append(out, share)
	}
	out = append(out, usbBackends(ctx, log)...)
	if len(out) == 0 {
		return nil, fmt.Errorf("no local printers found: no USB printer answered and nothing "+
			"identified as a printer on local ports %v", ports)
	}
	return out, nil
}

// jsonMarshal/jsonUnmarshal keep the HTTP calls terse.
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

func jsonUnmarshal(r io.Reader, v any) error {
	b, err := io.ReadAll(io.LimitReader(r, 8<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(bytes.TrimSpace(b), v)
}

func goRuntime() string { return runtime.GOOS + "/" + runtime.GOARCH }

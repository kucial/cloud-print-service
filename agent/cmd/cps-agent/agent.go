package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/kucial/cloud-print-service/pkg/agentapi"
	"github.com/kucial/cloud-print-service/pkg/printer"
)

// agent is the running process: backends for local printers, one TCP listener per
// printer, and a registration/heartbeat loop to the server.
type agent struct {
	cfg     Config
	cfgPath string
	log     *slog.Logger
	cli     *http.Client

	mu       sync.RWMutex
	printers map[string]*localPrinter // printer_id -> printer with its listener
	ctx      context.Context
}

type localPrinter struct {
	agentapi.Printer
	backend Backend
	sink    Sink
	ln      net.Listener
	status  agentapi.Status
}

// query answers a host command (`^HS`, `~HI`) on the printer's control connection and
// returns the reply for the server.
func (p *localPrinter) query(ctx context.Context, cmd string) (string, error) {
	if q, ok := p.backend.(interface {
		query(context.Context, string) (string, error)
	}); ok {
		return q.query(ctx, cmd)
	}
	return "", fmt.Errorf("backend %s cannot answer host queries", p.backend.Name())
}

// Run starts everything and blocks until ctx is cancelled.
func (a *agent) Run(ctx context.Context) {
	a.ctx = ctx

	backends, err := openBackends(ctx, a.log)
	if err != nil {
		a.log.Error("no usable printer backends", "err", err)
		return
	}
	a.log.Info("backends available", "backends", names(backends))

	if err := a.serveLocal(ctx, backends); err != nil {
		a.log.Error("cannot serve local printers", "err", err)
		return
	}

	a.registerAndHeartbeat(ctx)

	<-ctx.Done()
	a.shutdown()
}

func names(bs []Backend) []string {
	out := make([]string, 0, len(bs))
	for _, b := range bs {
		out = append(out, b.Name())
	}
	return out
}

// serveLocal opens a listening port per discovered printer.
func (a *agent) serveLocal(ctx context.Context, backends []Backend) error {
	a.mu.Lock()
	a.printers = map[string]*localPrinter{}
	a.mu.Unlock()

	port := a.cfg.BasePort
	for _, b := range backends {
		sink, err := b.Open(ctx)
		if err != nil {
			a.log.Warn("backend cannot open a printer", "backend", b.Name(), "err", err)
			continue
		}
		id := fmt.Sprintf("%s-%s", a.cfg.AgentID, strings.ReplaceAll(b.Name(), "/", "-"))
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			a.log.Error("cannot listen for the server", "port", port, "err", err)
			_ = sink.Close()
			port++
			continue
		}
		model := b.Name()
		if m, ok := b.(interface{ Model() string }); ok && m.Model() != "" {
			model = m.Model()
		}
		p := &localPrinter{
			Printer: agentapi.Printer{
				PrinterID: id, Name: b.Name(), Site: a.cfg.Site,
				Driver: "zebra9100", Host: a.cfg.Host, Port: port, Enabled: true,
				Model: model,
			},
			backend: b, sink: sink, ln: ln,
		}
		a.mu.Lock()
		a.printers[id] = p
		a.mu.Unlock()
		a.log.Info("serving printer", "printer_id", id, "backend", b.Name(),
			"listen", fmt.Sprintf("%s:%d", a.cfg.Host, port))
		go a.acceptLoop(ctx, p)
		port++
	}

	a.mu.RLock()
	n := len(a.printers)
	a.mu.RUnlock()
	if n == 0 {
		return errors.New("no printers found: nothing to serve")
	}
	return nil
}

// acceptLoop is deliberately dumb: one connection, one stream, no buffering, no retry.
// Every guarantee that matters (FIFO, at-most-once after the first byte, backpressure)
// belongs to the server; duplicating any of it here would create a second place to get
// it wrong. The agent's job is to move bytes and stop.
func (a *agent) acceptLoop(ctx context.Context, p *localPrinter) {
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			a.log.Warn("accept failed", "printer_id", p.PrinterID, "err", err)
			continue
		}
		go a.serveConn(ctx, p, conn)
	}
}

func (a *agent) serveConn(ctx context.Context, p *localPrinter, conn net.Conn) {
	defer conn.Close()
	buf := make([]byte, 32<<10)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
		n, err := conn.Read(buf)
		if n > 0 {
			if !a.handle(ctx, p, conn, buf[:n]) {
				return
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && ctx.Err() == nil {
				a.log.Debug("connection closed", "printer_id", p.PrinterID, "err", err)
			}
			return
		}
	}
}

// handle forwards one chunk and returns false when the connection should be dropped.
//
// The agent must be *transparent in both directions*: the server's driver sends `^HS` on
// its status socket and expects a reply. A write-only forwarder would leave every job
// stuck in `sending`, because confirmation could never observe a non-printing state.
func (a *agent) handle(ctx context.Context, p *localPrinter, conn net.Conn, chunk []byte) bool {
	trimmed := strings.TrimLeft(string(chunk), " \r\n\t")
	switch {
	case strings.HasPrefix(trimmed, printer.CmdHostStatus), strings.HasPrefix(trimmed, printer.CmdHostID),
		strings.HasPrefix(trimmed, "^HS"): // ^HS: servers before the switch to real ~HS
		reply, err := p.query(ctx, string(chunk))
		if err != nil {
			a.log.Warn("host query failed", "printer_id", p.PrinterID, "err", err)
			return false
		}
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write([]byte(reply)); err != nil {
			return false
		}
		return true

	case strings.HasPrefix(trimmed, "~JA"):
		wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := p.sink.Write(wctx, []byte(chunk))
		cancel()
		return err == nil

	default:
		// Label data: straight through, no buffering, no interpretation.
		wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err := p.sink.Write(wctx, chunk)
		cancel()
		if err != nil {
			// Drop the connection so the write error cannot look like success upstream.
			a.log.Warn("write to printer failed", "printer_id", p.PrinterID, "err", err)
			return false
		}
		return true
	}
}

// registerAndHeartbeat tells the server what we serve, then keeps it informed.
func (a *agent) registerAndHeartbeat(ctx context.Context) {
	if err := a.register(ctx); err != nil {
		a.log.Error("registration failed (will retry)", "err", err)
	}
	interval := time.Duration(a.cfg.PollSecs) * time.Second
	if interval <= 0 {
		interval = 15 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := a.register(ctx); err != nil {
				a.log.Warn("re-registration failed", "err", err)
			}
			a.heartbeat(ctx)
		}
	}
}

func (a *agent) register(ctx context.Context) error {
	a.mu.RLock()
	printers := make([]agentapi.Printer, 0, len(a.printers))
	for _, p := range a.printers {
		printers = append(printers, p.Printer)
	}
	a.mu.RUnlock()
	if len(printers) == 0 {
		return errors.New("no printers to register")
	}

	payload := agentapi.RegisterRequest{
		AgentID: a.cfg.AgentID, Site: a.cfg.Site, Hostname: hostname(),
		OS: goRuntime(), Version: version, AgentAddr: a.cfg.Host, Printers: printers,
	}
	var out agentapi.RegisterResponse // Token is present only on the first registration
	if err := a.post(ctx, agentapi.PathRegister, payload, &out); err != nil {
		return err
	}
	if out.Token != "" && out.Token != a.cfg.Token {
		// Bootstrap credential exchanged for the durable one: persist it now.
		a.cfg.Token = out.Token
		if a.cfgPath != "" {
			if err := a.cfg.save(a.cfgPath); err != nil {
				a.log.Error("received a new credential but cannot persist it", "err", err)
				return err
			}
			a.log.Info("agent credential exchanged and saved", "config", a.cfgPath)
		}
	}
	a.log.Info("registered with server", "agent_id", a.cfg.AgentID, "printers", len(printers))
	return nil
}

func (a *agent) heartbeat(ctx context.Context) {
	statuses := map[string]agentapi.Status{}
	a.mu.RLock()
	for id, p := range a.printers {
		stctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		st, err := p.sink.Status(stctx)
		cancel()
		if err != nil {
			st = agentapi.Status{State: printer.StateOffline, ObservedAt: time.Now().UTC(), Detail: err.Error()}
		}
		statuses[id] = st
	}
	a.mu.RUnlock()

	payload := agentapi.HeartbeatRequest{AgentID: a.cfg.AgentID, Status: statuses}
	if err := a.post(ctx, agentapi.PathHeartbeat, payload, nil); err != nil {
		a.log.Warn("heartbeat failed", "err", err)
	}
}

func (a *agent) post(ctx context.Context, path string, payload any, out any) error {
	body, err := jsonMarshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.ServerURL+path, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s -> %d: %s", path, resp.StatusCode, readAllLimited(resp))
	}
	if out != nil {
		return jsonUnmarshal(resp.Body, out)
	}
	return nil
}

func (a *agent) shutdown() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, p := range a.printers {
		if p.ln != nil {
			_ = p.ln.Close()
		}
		if p.sink != nil {
			_ = p.sink.Close()
		}
		delete(a.printers, id)
	}
}

func newAgent(cfg Config, cfgPath string, log *slog.Logger) (*agent, error) {
	cfg.defaults()
	if cfg.Token == "" {
		return nil, errors.New("agent has no credential: re-run with -enroll <token>")
	}
	if _, err := url.Parse(cfg.ServerURL); err != nil {
		return nil, fmt.Errorf("server_url %q: %w", cfg.ServerURL, err)
	}
	return &agent{
		cfg:     cfg,
		cfgPath: cfgPath,
		log:     log,
		cli:     &http.Client{Timeout: 20 * time.Second},
	}, nil
}

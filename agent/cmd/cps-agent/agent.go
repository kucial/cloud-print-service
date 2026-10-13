package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
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
	status   agentStatus
}

// Agent phases, as the tray and the local panel show them.
const (
	phaseStarting   = "starting"
	phaseNoPrinters = "no_printers"
	phaseConnected  = "connected"
	phaseError      = "error"
)

// agentStatus is what the agent knows about itself, for the people at the PC: the
// server sees the same facts through registration and heartbeats.
type agentStatus struct {
	Phase         string          `json:"phase"`
	LastRegister  *time.Time      `json:"last_register,omitempty"`
	LastHeartbeat *time.Time      `json:"last_heartbeat,omitempty"`
	LastError     string          `json:"last_error,omitempty"`
	Printers      []printerStatus `json:"printers"`
}

type printerStatus struct {
	ID     string `json:"id"`
	Model  string `json:"model"`
	Port   int    `json:"port"`
	State  string `json:"state,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Snapshot returns a copy of the agent's status, safe to hand to another goroutine.
func (a *agent) Snapshot() agentStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()
	st := a.status
	st.Printers = make([]printerStatus, 0, len(a.printers))
	for _, p := range a.printers {
		st.Printers = append(st.Printers, printerStatus{ID: p.PrinterID, Model: p.Model, Port: p.Port,
			State: string(p.status.State), Detail: p.status.Detail})
	}
	sort.Slice(st.Printers, func(i, j int) bool { return st.Printers[i].Port < st.Printers[j].Port })
	return st
}

// setStatus records a phase change and, when err is not nil, why.
func (a *agent) setStatus(phase string, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status.Phase = phase
	a.status.LastError = ""
	if err != nil {
		a.status.LastError = err.Error()
	}
}

type localPrinter struct {
	agentapi.Printer
	profile printer.Profile
	backend Backend
	sink    Sink
	ln      net.Listener
	status  agentapi.Status
	// job is held by a connection while it is inside a label, so that a host query from
	// another connection cannot reach the printer between two pieces of it.
	job sync.Mutex
}

// profileOf is what a backend's printer is; backends that cannot tell serve Zebras.
func profileOf(b Backend) printer.Profile {
	if p, ok := b.(interface{ Profile() printer.Profile }); ok && p.Profile().Driver != "" {
		return p.Profile()
	}
	return zebraProfile
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
	a.setStatus(phaseStarting, nil)

	backends, err := openBackends(ctx, a.log)
	if err != nil {
		a.log.Error("no usable printer backends", "err", err)
		a.setStatus(phaseNoPrinters, err)
		return
	}
	a.log.Info("backends available", "backends", names(backends))

	if err := a.serveLocal(ctx, backends); err != nil {
		a.log.Error("cannot serve local printers", "err", err)
		a.setStatus(phaseNoPrinters, err)
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
		// <agent>-<serial>: the host it hangs off and the device itself, nothing more.
		key := strings.ReplaceAll(b.Name(), "/", "-")
		if k, ok := b.(interface{ Key() string }); ok && k.Key() != "" {
			key = k.Key()
		}
		id := fmt.Sprintf("%s-%s", a.cfg.AgentID, key)
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
		prof := profileOf(b)
		p := &localPrinter{
			Printer: agentapi.Printer{
				PrinterID: id, Name: model, Site: a.cfg.Site,
				Driver: prof.Driver, Kind: prof.Kind, Formats: prof.Formats,
				Host: a.cfg.Host, Port: port, Enabled: true, Model: model,
			},
			profile: prof, backend: b, sink: sink, ln: ln,
		}
		a.mu.Lock()
		a.printers[id] = p
		a.mu.Unlock()
		a.log.Info("serving printer", "printer_id", id, "backend", b.Name(), "kind", prof.Kind,
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
	var label labelState
	defer label.release(p)
	// The server sends each job on a connection of its own; when it closes, the job is
	// over. Pass that on: an office printer prints a PDF or PostScript job when its
	// connection ends, not when the next job begins.
	wroteDocument := false
	defer func() {
		if e, ok := p.sink.(interface{ EndJob() }); ok && wroteDocument {
			e.EndJob()
		}
	}()
	buf := make([]byte, 32<<10)
	for {
		wait := 5 * time.Minute
		if label.held {
			wait = labelIdle
		}
		_ = conn.SetReadDeadline(time.Now().Add(wait))
		n, err := conn.Read(buf)
		if n > 0 {
			if !isServerQuery(p.profile, buf[:n]) {
				wroteDocument = true
			}
			if !a.handle(ctx, p, conn, buf[:n], &label) {
				return
			}
		}
		if err != nil {
			var ne net.Error
			if label.held && errors.As(err, &ne) && ne.Timeout() {
				// The sender paused inside what looked like a label: not ZPL, or no ^XZ.
				label.release(p)
				continue
			}
			if !errors.Is(err, io.EOF) && ctx.Err() == nil {
				a.log.Debug("connection closed", "printer_id", p.PrinterID, "err", err)
			}
			return
		}
	}
}

// handle forwards one chunk and returns false when the connection should be dropped.
//
// The agent must be *transparent in both directions*: the server's driver asks the
// printer for its status (~HS, DLE EOT, @PJL INFO) on its status socket and expects a
// reply. A write-only forwarder would leave every job stuck in `sending`, because
// confirmation could never observe a non-printing state.
func (a *agent) handle(ctx context.Context, p *localPrinter, conn net.Conn, chunk []byte, label *labelState) bool {
	trimmed := strings.TrimLeft(string(chunk), " \r\n\t")
	switch {
	case isServerQuery(p.profile, chunk):
		// The server asks for status on its own connection right after sending a job,
		// while the agent may still be passing the document on in pieces. A query between
		// two pieces lands inside the document (in a ^GF graphic, the printer then drops
		// the whole label without an error), so it waits for the document to end.
		if !label.held {
			p.job.Lock()
		}
		reply, err := p.query(ctx, string(chunk))
		if !label.held {
			p.job.Unlock()
		}
		if err != nil {
			if p.profile.Driver != printer.DriverZebra {
				// A receipt or office printer that cannot answer (write-only, no PJL)
				// gets no reply rather than a dropped connection: the server reads that
				// as "reachable, state unknown". The query is never sent on as data.
				a.log.Debug("printer cannot answer the server's query", "printer_id", p.PrinterID, "err", err)
				return true
			}
			a.log.Warn("host query failed", "printer_id", p.PrinterID, "err", err)
			return false
		}
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write([]byte(reply)); err != nil {
			return false
		}
		return true

	case p.profile.Driver == printer.DriverZebra && strings.HasPrefix(trimmed, "~JA"):
		wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := p.sink.Write(wctx, []byte(chunk))
		cancel()
		return err == nil

	default:
		// Document data: straight through, no buffering, no interpretation.
		label.hold(p)
		wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err := p.sink.Write(wctx, chunk)
		cancel()
		if err != nil {
			// Drop the connection so the write error cannot look like success upstream.
			a.log.Warn("write to printer failed", "printer_id", p.PrinterID, "err", err)
			return false
		}
		if label.ended(chunk) {
			label.release(p)
		}
		return true
	}
}

// labelIdle releases a printer held for a label the sender stopped sending: data that
// is not ZPL (receipt and office printers) has no ^XZ to end on, and the server keeps
// its print connection open between jobs.
var labelIdle = 500 * time.Millisecond

// labelState is one connection's place in its stream of label data.
type labelState struct {
	held bool
	tail string // the last non-blank bytes written, to spot a ^XZ split across reads
}

func (l *labelState) hold(p *localPrinter) {
	if !l.held {
		p.job.Lock()
		l.held = true
	}
}

func (l *labelState) release(p *localPrinter) {
	if l.held {
		p.job.Unlock()
		l.held = false
	}
	l.tail = ""
}

// ended reports whether the data written so far ends a ZPL label: ^XZ, then only blanks.
func (l *labelState) ended(chunk []byte) bool {
	if t := bytes.TrimRight(chunk, " \r\n\t"); len(t) > 0 {
		l.tail += string(t[max(0, len(t)-3):])
		l.tail = l.tail[max(0, len(l.tail)-3):]
	}
	return strings.EqualFold(l.tail, "^XZ")
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
		a.setStatus(phaseError, err)
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
	now := time.Now()
	a.mu.Lock()
	a.status.Phase, a.status.LastError, a.status.LastRegister = phaseConnected, "", &now
	a.mu.Unlock()
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

	a.mu.Lock()
	for id, st := range statuses {
		if p := a.printers[id]; p != nil {
			p.status = st
		}
	}
	a.mu.Unlock()

	payload := agentapi.HeartbeatRequest{AgentID: a.cfg.AgentID, Status: statuses}
	if err := a.post(ctx, agentapi.PathHeartbeat, payload, nil); err != nil {
		a.log.Warn("heartbeat failed", "err", err)
		a.setStatus(phaseError, err)
		return
	}
	now := time.Now()
	a.mu.Lock()
	a.status.Phase, a.status.LastError, a.status.LastHeartbeat = phaseConnected, "", &now
	a.mu.Unlock()
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
		status:  agentStatus{Phase: phaseStarting},
	}, nil
}

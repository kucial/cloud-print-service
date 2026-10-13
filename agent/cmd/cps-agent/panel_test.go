package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeRunner stands in for the agent: it runs until cancelled, or until told to exit.
type fakeRunner struct {
	cfg   Config
	exit  chan struct{}
	mu    sync.Mutex
	phase string
	ended atomic.Bool
}

func (f *fakeRunner) setPhase(p string) {
	f.mu.Lock()
	f.phase = p
	f.mu.Unlock()
}

func (f *fakeRunner) Run(ctx context.Context) {
	select {
	case <-ctx.Done():
		time.Sleep(30 * time.Millisecond) // closing ports and USB handles takes a moment
	case <-f.exit:
	}
	f.ended.Store(true)
}

func (f *fakeRunner) Snapshot() agentStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return agentStatus{Phase: f.phase}
}

type runnerLog struct {
	mu      sync.Mutex
	started []*fakeRunner
	running atomic.Int32
	overlap atomic.Bool
}

func (l *runnerLog) newRunner(cfg Config) (runner, error) {
	r := &fakeRunner{cfg: cfg, exit: make(chan struct{}), phase: phaseConnected}
	l.mu.Lock()
	l.started = append(l.started, r)
	l.mu.Unlock()
	return &countingRunner{fakeRunner: r, log: l}, nil
}

func (l *runnerLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.started)
}

func (l *runnerLog) last() *fakeRunner {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.started[len(l.started)-1]
}

type countingRunner struct {
	*fakeRunner
	log *runnerLog
}

func (c *countingRunner) Run(ctx context.Context) {
	if c.log.running.Add(1) > 1 {
		c.log.overlap.Store(true)
	}
	defer c.log.running.Add(-1)
	c.fakeRunner.Run(ctx)
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testSupervisor(t *testing.T) (*supervisor, *runnerLog, context.CancelFunc) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cps-agent", "agent.json")
	s := newSupervisor(path, quietLog(), newLogRing(10))
	s.retry = 20 * time.Millisecond
	rl := &runnerLog{}
	s.newRunner = rl.newRunner
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return s, rl, cancel
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSupervisorWaitsForSetupThenRuns(t *testing.T) {
	s, rl, _ := testSupervisor(t)
	eventually(t, "needs_setup", func() bool { return s.State().Phase == phaseNeedsSetup })
	if rl.count() != 0 {
		t.Fatal("started an agent without a credential")
	}
	if err := s.Apply(Config{ServerURL: "http://cps:8080", AgentID: "pc7", Token: "agt_x"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "agent started", func() bool { return rl.count() == 1 && s.State().Phase == phaseConnected })
}

func TestSupervisorRetriesWhenTheAgentStops(t *testing.T) {
	s, rl, _ := testSupervisor(t)
	_ = s.Apply(Config{ServerURL: "http://cps:8080", AgentID: "pc7", Token: "agt_x"})
	eventually(t, "first run", func() bool { return rl.count() == 1 })
	first := rl.last()
	first.setPhase(phaseNoPrinters)
	close(first.exit) // no printer plugged in: Run returns on its own
	eventually(t, "no_printers shown", func() bool { return rl.count() == 2 || s.State().Phase == phaseNoPrinters })
	eventually(t, "retried", func() bool { return rl.count() == 2 })
}

func TestApplyStopsTheOldAgentFirst(t *testing.T) {
	s, rl, _ := testSupervisor(t)
	_ = s.Apply(Config{ServerURL: "http://cps:8080", AgentID: "pc7", Site: "a", Token: "agt_x"})
	eventually(t, "first run", func() bool { return rl.count() == 1 })
	first := rl.last()
	if err := s.Apply(Config{ServerURL: "http://cps:8080", AgentID: "pc7", Site: "b", Token: "agt_x"}); err != nil {
		t.Fatal(err)
	}
	// The old agent may still save a credential while it runs; it must be gone before
	// Apply writes the file, or it could overwrite the new one.
	if !first.ended.Load() {
		t.Fatal("Apply returned while the old agent was still running")
	}
	eventually(t, "restarted", func() bool { return rl.count() == 2 })
	if rl.overlap.Load() {
		t.Fatal("two agents ran at once: they would fight over the printer ports")
	}
	if got := rl.last().cfg.Site; got != "b" {
		t.Fatalf("restarted with the old config: site %q", got)
	}
}

func TestSupervisorBlockedByService(t *testing.T) {
	s, rl, _ := testSupervisor(t)
	_ = s.Apply(Config{ServerURL: "http://cps:8080", AgentID: "pc7", Token: "agt_x"})
	eventually(t, "first run", func() bool { return rl.count() == 1 })
	s.mu.Lock()
	s.blocked = func() string { return "service installed" }
	s.mu.Unlock()
	_ = s.Apply(Config{ServerURL: "http://cps:8080", AgentID: "pc7", Token: "agt_x"})
	eventually(t, "blocked", func() bool { return s.State().Phase == phaseBlocked })
	if rl.count() != 1 {
		t.Fatal("started an agent while the service serves the printers")
	}
}

// ---------------------------------------------------------------- panel

type panelEnv struct {
	p       *panel
	h       http.Handler
	sup     *supervisor
	runs    *runnerLog
	server  *httptest.Server // fake CPS server
	enrolls atomic.Int32
}

func newPanelEnv(t *testing.T) *panelEnv {
	t.Helper()
	env := &panelEnv{}
	env.sup, env.runs, _ = testSupervisor(t)
	env.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/agents/enroll" || r.Header.Get("Authorization") != "Bearer enr_good" {
			http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
			return
		}
		env.enrolls.Add(1)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"agt_secret_credential","ttl":"720h"}`))
	}))
	t.Cleanup(env.server.Close)
	env.p = newPanel(env.sup, quietLog())
	env.p.host = "127.0.0.1:4711"
	env.h = env.p.Handler()
	return env
}

func (e *panelEnv) do(method, path, body string, mod func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = e.p.host
	r.AddCookie(&http.Cookie{Name: panelCookie, Value: e.p.key})
	if method != http.MethodGet {
		r.Header.Set("Origin", "http://"+e.p.host)
		r.Header.Set("Content-Type", "application/json")
	}
	if mod != nil {
		mod(r)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

func TestPanelRefusesStrangers(t *testing.T) {
	env := newPanelEnv(t)
	noCookie := func(r *http.Request) { r.Header.Del("Cookie") }
	if w := env.do("GET", "/api/state", "", noCookie); w.Code != http.StatusUnauthorized {
		t.Fatalf("no cookie: %d", w.Code)
	}
	if w := env.do("GET", "/api/state", "", func(r *http.Request) { r.Host = "evil.example:4711" }); w.Code != http.StatusForbidden {
		t.Fatalf("rebinding host: %d", w.Code)
	}
	if w := env.do("POST", "/api/config", `{}`, func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }); w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin post: %d", w.Code)
	}
	if w := env.do("POST", "/api/config", `{}`, func(r *http.Request) { r.Header.Del("Origin") }); w.Code != http.StatusForbidden {
		t.Fatalf("post without origin: %d", w.Code)
	}
	if w := env.do("GET", "/?k=wrong", "", noCookie); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong key: %d", w.Code)
	}
}

func TestPanelKeyBecomesCookie(t *testing.T) {
	env := newPanelEnv(t)
	w := env.do("GET", "/?k="+env.p.key, "", func(r *http.Request) { r.Header.Del("Cookie") })
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatalf("key exchange: %d %v", w.Code, w.Header())
	}
	c := w.Result().Cookies()
	if len(c) != 1 || c[0].Value != env.p.key || !c[0].HttpOnly || c[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie: %+v", c)
	}
	if w := env.do("GET", "/", "", nil); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte("<html")) {
		t.Fatalf("page: %d", w.Code)
	}
}

func TestPanelSetupEnrollsAndNeverShowsTheCredential(t *testing.T) {
	env := newPanelEnv(t)
	form := func(token string) string {
		b, _ := json.Marshal(map[string]any{"server_url": env.server.URL + "/", "token": token, "site": "wh1", "agent_id": "pc7"})
		return string(b)
	}
	w := env.do("POST", "/api/enroll", form("enr_bad"), nil)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "401") {
		t.Fatalf("a rejected token must say why: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(env.sup.path); err == nil {
		t.Fatal("config written after a failed enrollment")
	}

	w = env.do("POST", "/api/enroll", form("enr_good"), nil)
	if w.Code != 200 {
		t.Fatalf("enroll: %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "agt_secret") || strings.Contains(w.Body.String(), "enr_good") {
		t.Fatalf("panel leaked a credential: %s", w.Body)
	}
	var st panelState
	_ = json.Unmarshal(w.Body.Bytes(), &st)
	if !st.Enrolled || st.Config.ServerURL != env.server.URL || st.Config.Site != "wh1" {
		t.Fatalf("state after enroll: %+v", st)
	}
	cfg, err := loadConfig(env.sup.path)
	if err != nil || cfg.Token != "agt_secret_credential" || cfg.AgentID != "pc7" {
		t.Fatalf("saved config: %+v %v", cfg, err)
	}
	if fi, _ := os.Stat(env.sup.path); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("config holds a credential but is %v", fi.Mode().Perm())
	}
	eventually(t, "agent started with the credential", func() bool {
		return env.runs.count() == 1 && env.runs.last().cfg.Token == "agt_secret_credential"
	})
	if w := env.do("GET", "/api/state", "", nil); strings.Contains(w.Body.String(), "agt_secret") {
		t.Fatalf("state leaked the credential: %s", w.Body)
	}
}

func TestPanelSettings(t *testing.T) {
	env := newPanelEnv(t)
	_ = env.sup.Apply(Config{ServerURL: env.server.URL, AgentID: "pc7", Site: "wh1", Token: "agt_old"})
	post := func(fields map[string]any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(fields)
		return env.do("POST", "/api/config", string(b), nil)
	}

	// Site, port, interval: saved as they are, credential kept.
	w := post(map[string]any{"server_url": env.server.URL, "agent_id": "pc7", "site": "wh2", "base_port": 19200, "poll_seconds": 30})
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	cfg, _ := loadConfig(env.sup.path)
	if cfg.Site != "wh2" || cfg.BasePort != 19200 || cfg.PollSecs != 30 || cfg.Token != "agt_old" {
		t.Fatalf("saved: %+v", cfg)
	}

	// Moving server or agent id without a new token would keep a credential that is
	// not valid there.
	for _, f := range []map[string]any{
		{"server_url": "http://other:8080", "agent_id": "pc7"},
		{"server_url": env.server.URL, "agent_id": "pc8"},
	} {
		if w := post(f); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "token") {
			t.Fatalf("%v: %d %s", f, w.Code, w.Body)
		}
	}
	if w := post(map[string]any{"server_url": env.server.URL, "agent_id": "pc8", "token": "enr_good"}); w.Code != 200 {
		t.Fatalf("re-enroll: %d %s", w.Code, w.Body)
	}
	if cfg, _ := loadConfig(env.sup.path); cfg.AgentID != "pc8" || cfg.Token != "agt_secret_credential" {
		t.Fatalf("re-enrolled config: %+v", cfg)
	}

	for _, bad := range []map[string]any{
		{"server_url": "cps:8080", "agent_id": "pc8"},
		{"server_url": env.server.URL, "agent_id": "pc 8"},
		{"server_url": env.server.URL, "agent_id": "pc8", "base_port": 80},
		{"server_url": env.server.URL, "agent_id": "pc8", "poll_seconds": 1},
		{"server_url": env.server.URL, "agent_id": "pc8", "token_typo": "x"},
	} {
		if w := post(bad); w.Code != http.StatusBadRequest {
			t.Fatalf("%v accepted: %d", bad, w.Code)
		}
	}
}

// ---------------------------------------------------------------- icons

func TestTrayIcons(t *testing.T) {
	if _, err := png.Decode(bytes.NewReader(iconPNG(phaseConnected))); err != nil {
		t.Fatalf("png icon: %v", err)
	}
	ico := iconICO(phaseError)
	var hdr [3]uint16
	_ = binary.Read(bytes.NewReader(ico), binary.LittleEndian, &hdr)
	if hdr != [3]uint16{0, 1, 2} {
		t.Fatalf("ico header: %v", hdr)
	}
	// Each directory entry's size and offset must land inside the file.
	for i := 0; i < 2; i++ {
		e := ico[6+16*i:]
		size, off := binary.LittleEndian.Uint32(e[8:]), binary.LittleEndian.Uint32(e[12:])
		if int(off+size) > len(ico) || binary.LittleEndian.Uint32(ico[off:]) != 40 {
			t.Fatalf("entry %d: size %d offset %d of %d", i, size, off, len(ico))
		}
	}
	// The centre pixel of the 32 px image is the phase colour, opaque.
	off := binary.LittleEndian.Uint32(ico[6+16+12:])
	px := ico[int(off)+40+(16*32+16)*4:]
	if c := phaseColors[phaseError]; px[0] != c.B || px[1] != c.G || px[2] != c.R || px[3] != 0xff {
		t.Fatalf("centre pixel %v", px[:4])
	}
}

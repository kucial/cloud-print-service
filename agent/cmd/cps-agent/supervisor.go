package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Supervisor phases on top of the agent's own (see agentStatus).
const (
	phaseNeedsSetup = "needs_setup" // no credential yet: the panel asks for one
	phaseBlocked    = "blocked"     // something else serves this PC's printers
)

// runner is the part of the agent the supervisor drives; tests substitute it.
type runner interface {
	Run(ctx context.Context)
	Snapshot() agentStatus
}

// supervisor keeps an agent running for the desktop build. Where the CLI agent exits
// and lets the service manager restart it, the tray app has nobody to restart it, so
// the supervisor does: when the agent stops (no printer plugged in, a port in use) it
// tries again, and when the operator changes the config it restarts the agent with it.
//
// The config file stays the one source of truth: it is reloaded before every start,
// because the agent itself rewrites it when it exchanges its bootstrap credential.
type supervisor struct {
	path  string
	log   *slog.Logger
	logs  *logRing
	retry time.Duration

	newRunner func(cfg Config) (runner, error)
	// blocked, when set, names a reason not to start the agent at all.
	blocked func() string

	wake chan struct{}
	// applying is held while Apply swaps the config, so a restarting loop cannot read
	// the file halfway through.
	applying sync.Mutex

	mu      sync.Mutex
	current runner
	stop    context.CancelFunc
	done    chan struct{}
	last    agentStatus
	phase   string
	note    string
}

func newSupervisor(path string, log *slog.Logger, logs *logRing) *supervisor {
	s := &supervisor{path: path, log: log, logs: logs, retry: 30 * time.Second,
		wake: make(chan struct{}, 1), phase: phaseStarting}
	s.newRunner = func(cfg Config) (runner, error) { return newAgent(cfg, path, log) }
	return s
}

// supervisorState is the supervisor's view for the panel and the tray.
type supervisorState struct {
	Phase string      `json:"phase"`
	Note  string      `json:"note,omitempty"`
	Agent agentStatus `json:"agent"`
}

func (s *supervisor) State() supervisorState {
	s.mu.Lock()
	cur, st := s.current, supervisorState{Phase: s.phase, Note: s.note, Agent: s.last}
	s.mu.Unlock()
	if cur != nil {
		st.Agent = cur.Snapshot()
		st.Phase = st.Agent.Phase
	}
	if st.Agent.Printers == nil {
		st.Agent.Printers = []printerStatus{}
	}
	return st
}

func (s *supervisor) set(phase, note string) {
	s.mu.Lock()
	s.phase, s.note = phase, note
	s.mu.Unlock()
}

// Run keeps the agent going until ctx is cancelled.
func (s *supervisor) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if reason := s.blockedReason(); reason != "" {
			s.set(phaseBlocked, reason)
			s.sleep(ctx, s.retry)
			continue
		}
		s.applying.Lock()
		cfg, err := loadConfig(s.path)
		s.applying.Unlock()
		if err != nil || cfg.Token == "" {
			s.set(phaseNeedsSetup, "")
			s.sleep(ctx, 0) // until the panel saves a config
			continue
		}
		r, err := s.newRunner(cfg)
		if err != nil {
			s.set(phaseError, err.Error())
			s.sleep(ctx, 0)
			continue
		}

		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		s.mu.Lock()
		s.current, s.stop, s.done, s.note = r, cancel, done, ""
		s.mu.Unlock()
		r.Run(runCtx)
		cancel()
		snap := r.Snapshot()
		s.mu.Lock()
		s.current, s.stop, s.done, s.last, s.phase = nil, nil, nil, snap, snap.Phase
		s.mu.Unlock()
		close(done)

		if ctx.Err() != nil {
			return
		}
		select {
		case <-s.wake: // stopped on purpose for a new config: start again now
			continue
		default:
		}
		if snap.Phase == phaseStarting || snap.Phase == "" {
			s.set(phaseError, "agent stopped")
		}
		s.log.Info("agent stopped; trying again shortly", "phase", snap.Phase, "retry", s.retry)
		s.sleep(ctx, s.retry)
	}
}

func (s *supervisor) blockedReason() string {
	s.mu.Lock()
	blocked := s.blocked
	s.mu.Unlock()
	if blocked == nil {
		return ""
	}
	return blocked()
}

// sleep waits for d (0: indefinitely), a config change, or ctx.
func (s *supervisor) sleep(ctx context.Context, d time.Duration) {
	var timer <-chan time.Time
	if d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		timer = t.C
	}
	select {
	case <-ctx.Done():
	case <-s.wake:
	case <-timer:
	}
}

// Apply stops the running agent, writes cfg, and starts the agent again with it. The
// old agent is gone before the file changes, so the two never share the printer ports
// and the old one cannot overwrite the new credential.
func (s *supervisor) Apply(cfg Config) error {
	if cfg.Token == "" {
		return errors.New("no credential to save")
	}
	s.applying.Lock()
	defer s.applying.Unlock()
	s.mu.Lock()
	stop, done := s.stop, s.done
	s.mu.Unlock()
	if stop != nil {
		s.wakeUp() // tell the loop this stop is deliberate: it restarts without waiting
		stop()
		<-done
	}
	if err := cfg.save(s.path); err != nil {
		return err
	}
	s.set(phaseStarting, "")
	if stop == nil {
		s.wakeUp() // the loop is waiting for setup or a retry
	}
	return nil
}

func (s *supervisor) wakeUp() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// logRing keeps the last lines the agent logged, for the panel.
type logRing struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func newLogRing(max int) *logRing { return &logRing{max: max} }

func (r *logRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line == "" {
			continue
		}
		r.lines = append(r.lines, line)
	}
	if over := len(r.lines) - r.max; over > 0 {
		r.lines = append([]string(nil), r.lines[over:]...)
	}
	return len(p), nil
}

func (r *logRing) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.lines...)
}

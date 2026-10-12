// Package discovery finds printers on the local network and records them as
// *candidates* — devices that are not dispatchable until a human adopts them.
//
// The rule that shapes this package: discovery must never register a printer on its
// own. A warehouse LAN contains whatever else is plugged in, and silently making those
// dispatchable means printing shipping labels at the wrong box. Discovery finds; an
// operator (panel button or `cpsctl discover adopt`) decides.
package discovery

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kucial/cloud-print-service/server/internal/fleet"
)

// Probe finds devices and reports them as candidates. Implementations are per mechanism
// (mDNS today, a TCP sweep later), so a new mechanism never changes the policy above.
type Probe interface {
	Name() string
	// Scan returns everything the probe can see right now.
	Scan(ctx context.Context) ([]Found, error)
}

// Found is a device sighting before an operator has decided anything about it.
type Found struct {
	DeviceKey string            `json:"device_key"`
	Source    string            `json:"source"`
	Vendor    string            `json:"vendor,omitempty"`
	Model     string            `json:"model,omitempty"`
	Serial    string            `json:"serial,omitempty"`
	Host      string            `json:"host,omitempty"`
	Port      int               `json:"port,omitempty"`
	Detail    map[string]string `json:"detail,omitempty"`
}

// Candidate converts a sighting into the registry's representation.
func (f Found) Candidate(site string) fleet.Candidate {
	return fleet.Candidate{
		DeviceKey: f.DeviceKey,
		Source:    f.Source,
		Vendor:    f.Vendor,
		Model:     f.Model,
		Serial:    f.Serial,
		Host:      f.Host,
		Port:      f.Port,
		Site:      site,
		Detail:    f.Detail,
	}
}

// Config controls the scan loop.
type Config struct {
	// Types are the DNS-SD service types to browse, e.g. "_pdl-datastream._tcp".
	Types []string
	// Interval between sweeps. Discovery is deliberately slow: it is a convenience, and a
	// chatty mDNS browser on a warehouse LAN is noise.
	Interval time.Duration
	// CandidateTTL forgets devices that stopped answering.
	CandidateTTL time.Duration
	// Site is recorded on candidates so adoption can pre-fill it.
	Site string
	// Verify reaches each candidate and asks it who it is (~HI). Off = faster, less safe.
	Verify bool
	// BrowseTimeout bounds one browse pass over all service types.
	BrowseTimeout time.Duration
	// IdentifyTimeout bounds one verification probe.
	IdentifyTimeout time.Duration
}

// Defaults for a warehouse: browse Link-OS types, verify what we find, forget after 10m.
func (c Config) withDefaults() Config {
	if len(c.Types) == 0 {
		c.Types = DefaultTypes()
	}
	if c.Interval <= 0 {
		c.Interval = 60 * time.Second
	}
	if c.CandidateTTL <= 0 {
		c.CandidateTTL = 10 * time.Minute
	}
	if c.IdentifyTimeout <= 0 {
		c.IdentifyTimeout = 2 * time.Second
	}
	return c
}

// DefaultTypes are the DNS-SD service types Link-OS printers are expected to advertise.
//
// The primary type is what Zebra's own discovery uses; the others are generic print
// services some firmware and third-party bridges publish. Keep this list a superset -
// a device that answers `~HI` is verified as a printer anyway, so a wrong guess costs a
// little scanning, never a wrong adoption.
func DefaultTypes() []string {
	return []string{
		"_pdl-datastream._tcp",
		"_printer._tcp",
		"_ipp._tcp",
		"_pdl-datastream._tcp.local",
	}
}

// Service runs the probes on an interval and feeds the fleet registry.
type Service struct {
	cfg     Config
	probes  []Probe
	reg     *fleet.Registry
	log     Logger
	mu      sync.Mutex
	lastRun time.Time
	lastErr error
	found   int

	now func() time.Time
}

// Logger is the tiny logging surface this package needs (kept minimal so the package
// stays trivially testable).
type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Debug(msg string, args ...any)
}

func NewService(cfg Config, reg *fleet.Registry, log Logger) *Service {
	return &Service{cfg: cfg.withDefaults(), reg: reg, log: log, now: func() time.Time { return time.Now().UTC() }}
}

// WithProbes replaces the probe set (tests inject fakes).
func (s *Service) WithProbes(probes ...Probe) *Service {
	s.probes = probes
	return s
}

func (s *Service) Config() Config { return s.cfg }

// Run scans on the configured interval until ctx is cancelled.
func (s *Service) Run(ctx context.Context) {
	s.Scan(ctx)
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Scan(ctx)
		}
	}
}

// Status is what the panel and CLI show about discovery itself.
type Status struct {
	Enabled    bool      `json:"enabled"`
	Probes     []string  `json:"probes"`
	Types      []string  `json:"types"`
	Interval   string    `json:"interval"`
	LastScan   time.Time `json:"last_scan"`
	LastError  string    `json:"last_error,omitempty"`
	Found      int       `json:"found"`
	Candidates int       `json:"candidates"`
}

func (s *Service) Status() Status {
	s.mu.Lock()
	lastRun, lastErr, found := s.lastRun, s.lastErr, s.found
	s.mu.Unlock()
	names := make([]string, 0, len(s.probes))
	for _, p := range s.probes {
		names = append(names, p.Name())
	}
	sort.Strings(names)
	st := Status{
		Enabled:    len(s.probes) > 0,
		Probes:     names,
		Types:      s.cfg.Types,
		Interval:   s.cfg.Interval.String(),
		LastScan:   lastRun,
		Found:      found,
		Candidates: len(s.reg.Candidates(false)),
	}
	if lastErr != nil {
		st.LastError = lastErr.Error()
	}
	return st
}

// Scan runs every probe once and records the sightings.
//
// It reports what it found rather than creating printers: the caller (HTTP handler,
// panel) shows candidates and an operator adopts.
func (s *Service) Scan(ctx context.Context) (found int, err error) {
	defer func() {
		s.mu.Lock()
		s.lastRun = s.now()
		s.lastErr = err
		s.found = found
		s.mu.Unlock()
	}()

	var errs []string
	for _, p := range s.probes {
		select {
		case <-ctx.Done():
			return found, ctx.Err()
		default:
		}
		items, err := p.Scan(ctx)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", p.Name(), err))
			if s.log != nil {
				s.log.Warn("discovery probe failed", "probe", p.Name(), "err", err)
			}
			continue
		}
		for _, f := range items {
			if f.DeviceKey == "" {
				continue
			}
			s.reg.UpsertCandidate(f.Candidate(s.cfg.Site))
			found++
		}
		if s.log != nil && len(items) > 0 {
			s.log.Info("discovery probe found devices", "probe", p.Name(), "count", len(items))
		}
	}
	// Forget devices that stopped answering, so the list reflects reality.
	if n := s.reg.ExpireBefore(s.now().Add(-s.cfg.CandidateTTL)); n > 0 && s.log != nil {
		s.log.Info("expired stale candidates", "count", n)
	}
	if len(errs) > 0 {
		return found, fmt.Errorf("discovery: %s", strings.Join(errs, "; "))
	}
	return found, nil
}

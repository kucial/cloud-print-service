package fleet

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/kucial/cloud-print-service/pkg/agentapi"
	"github.com/kucial/cloud-print-service/server/internal/domain"
	"github.com/kucial/cloud-print-service/server/internal/observ"
)

// Agent is an enrolled edge process — typically one per packing PC — that makes local
// USB printers reachable as loopback TCP endpoints and reports their state.
//
// The server never opens an outbound connection to an agent (D2: same LAN, server pulls),
// so an agent only needs to be *listening* and to send three kinds of message:
// register, heartbeat, printer-status.
type Agent struct {
	ID        string    `json:"id"`
	Site      string    `json:"site"`
	Hostname  string    `json:"hostname"`
	OS        string    `json:"os"`
	Version   string    `json:"version"`
	AgentAddr string    `json:"agent_addr"`
	LastSeen  time.Time `json:"last_seen"`
	Printers  []string  `json:"printers"`
}

// AgentRegistry tracks enrolled agents and routes their reports into the fleet Registry.
type AgentRegistry struct {
	mu     sync.RWMutex
	agents map[string]*Agent

	fleet *Registry
	mgr   *Manager // where agent-reported status lands
	log   *observ.Log
}

// NewAgentRegistry wires agent reports into the fleet registry and status cache.
func NewAgentRegistry(fleet *Registry, mgr *Manager, log *observ.Log) *AgentRegistry {
	return &AgentRegistry{agents: map[string]*Agent{}, fleet: fleet, mgr: mgr, log: log}
}

// Register records an agent and its printers. Printers are claimed under the agent
// source, so they never shadow a config-declared printer with the same id.
func (ar *AgentRegistry) Register(p agentapi.RegisterRequest) (Agent, error) {
	if p.AgentID == "" {
		return Agent{}, fmt.Errorf("agent_id is required")
	}
	if p.AgentAddr == "" {
		return Agent{}, fmt.Errorf("agent_addr is required: the server must be able to reach the agent")
	}
	printers := make([]domain.Printer, 0, len(p.Printers))
	for _, ap := range p.Printers {
		pr := PrinterFromAgent(ap)
		pr.Enabled = true // an agent only reports printers it can serve
		if pr.Site == "" {
			pr.Site = p.Site
		}
		printers = append(printers, pr)
	}
	if err := ar.fleet.UpsertMany(SourceAgent, p.AgentID, printers); err != nil {
		return Agent{}, err
	}
	// An agent that re-registers with fewer printers has lost one (unplugged, moved).
	// Reconcile so a printer the agent no longer reports stops being dispatchable through
	// it, instead of quietly hanging around.
	ids := make([]string, 0, len(printers))
	for _, pr := range printers {
		ids = append(ids, pr.PrinterID)
	}
	if removed := ar.fleet.RemoveMissing(SourceAgent, p.AgentID, ids); len(removed) > 0 && ar.log != nil {
		ar.log.Info("agent re-registered without printers", "agent_id", p.AgentID, "removed", removed)
	}

	ar.mu.Lock()
	a := ar.agents[p.AgentID]
	if a == nil {
		a = &Agent{}
		ar.agents[p.AgentID] = a
	}
	a.ID, a.Site, a.Hostname, a.OS, a.Version, a.AgentAddr = p.AgentID, p.Site, p.Hostname, p.OS, p.Version, p.AgentAddr
	a.LastSeen = time.Now().UTC()
	sort.Strings(ids)
	a.Printers = ids
	out := *a
	ar.mu.Unlock()

	if ar.log != nil {
		ar.log.Info("agent registered", "agent_id", p.AgentID, "site", p.Site,
			"host", p.Hostname, "printers", len(ids), "addr", p.AgentAddr)
	}
	return out, nil
}

// Heartbeat refreshes liveness and, when present, status snapshots for the agent's
// printers. Agents push status on change *and* heartbeat, so a missed event heals.
func (ar *AgentRegistry) Heartbeat(p agentapi.HeartbeatRequest) error {
	ar.mu.Lock()
	a, ok := ar.agents[p.AgentID]
	if !ok {
		ar.mu.Unlock()
		return fmt.Errorf("unknown agent %q", p.AgentID)
	}
	a.LastSeen = time.Now().UTC()
	ar.mu.Unlock()

	ar.mu.RLock()
	owned := make(map[string]bool, len(a.Printers))
	for _, id := range a.Printers {
		owned[id] = true
	}
	ar.mu.RUnlock()
	for printerID, st := range p.Status {
		if !owned[printerID] {
			continue // a heartbeat can only speak for the agent's own printers
		}
		ar.mgr.ApplyStatus(printerID, StatusFromAgent(st))
	}
	return nil
}

// Status pushes a single printer status from an agent.
func (ar *AgentRegistry) Status(agentID, printerID string, st agentapi.Status) error {
	ar.mu.Lock()
	a, ok := ar.agents[agentID]
	if !ok {
		ar.mu.Unlock()
		return fmt.Errorf("unknown agent %q", agentID)
	}
	owns := false
	for _, id := range a.Printers {
		if id == printerID {
			owns = true
			break
		}
	}
	a.LastSeen = time.Now().UTC()
	ar.mu.Unlock()
	if !owns {
		// An agent reporting a printer it does not serve is either misconfigured or
		// misbehaving; accepting it would let one agent poison another's printers.
		return fmt.Errorf("agent %q does not serve printer %q", agentID, printerID)
	}
	ar.mgr.ApplyStatus(printerID, StatusFromAgent(st))
	return nil
}

// List returns enrolled agents, newest activity first.
func (ar *AgentRegistry) List() []Agent {
	ar.mu.RLock()
	out := make([]Agent, 0, len(ar.agents))
	for _, a := range ar.agents {
		out = append(out, *a)
	}
	ar.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

// Get returns one agent.
func (ar *AgentRegistry) Get(id string) (Agent, bool) {
	ar.mu.RLock()
	defer ar.mu.RUnlock()
	a, ok := ar.agents[id]
	if !ok {
		return Agent{}, false
	}
	return *a, true
}

// Healthy reports agents seen within ttl. The panel shows the rest as stale: an agent
// that stopped heartbeating means its printers are unreachable, which is operationally
// more important than the number of printers.
func (ar *AgentRegistry) Healthy(ttl time.Duration) int {
	cutoff := time.Now().UTC().Add(-ttl)
	ar.mu.RLock()
	defer ar.mu.RUnlock()
	n := 0
	for _, a := range ar.agents {
		if a.LastSeen.After(cutoff) {
			n++
		}
	}
	return n
}

// Deregister removes an agent and every printer it was the only source for.
func (ar *AgentRegistry) Deregister(id string) []string {
	ar.mu.Lock()
	_, ok := ar.agents[id]
	delete(ar.agents, id)
	ar.mu.Unlock()
	if !ok {
		return nil
	}
	gone := ar.fleet.RemoveSource(SourceAgent, id)
	if ar.log != nil {
		ar.log.Info("agent deregistered", "agent_id", id, "printers_removed", len(gone))
	}
	return gone
}

var _ = context.Background

// PrinterFromAgent maps an agent's printer report onto the server model. Live fields
// stay zero: the manager fills them once the printer is synced.
func PrinterFromAgent(p agentapi.Printer) domain.Printer {
	return domain.Printer{
		PrinterID: p.PrinterID, Name: p.Name, Site: p.Site, Model: p.Model, Driver: p.Driver,
		Host: p.Host, Port: p.Port, Enabled: p.Enabled, Remark: p.Remark, CustomLabel: p.CustomLabel,

		PaperSize: p.PaperSize, MediaWidthMM: p.MediaWidthMM, MediaHeightMM: p.MediaHeightMM,

		DPI: p.DPI, DPMM: p.DPMM, ResolutionSource: p.ResolutionSource, DPIResolvedFromHI: p.DPIResolvedFromHI,
		Orientation: p.Orientation, MediaOrientation: p.MediaOrientation, PrintDirection: p.PrintDirection,

		CJKSupported: p.CJKSupported, CJKFont: p.CJKFont, Fonts: p.Fonts,
		FontSource: p.FontSource, FontResolvedFromHI: p.FontResolvedFromHI,
	}
}

// StatusFromAgent maps an agent's status observation onto the server model. Stale and
// LastJob are the server's own bookkeeping, never taken from an agent.
func StatusFromAgent(st agentapi.Status) domain.Status {
	return domain.Status{State: st.State, Raw: st.Raw, Detail: st.Detail, ObservedAt: st.ObservedAt}
}

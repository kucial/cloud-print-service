package fleet

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/kucial/cloud-print-service/internal/domain"
)

// Registry is the fleet as *data*, with provenance.
//
// Before this existed the fleet was a config slice, which cannot express "an agent on a
// packing PC just told us this USB printer exists". A registry keeps the same
// addressability (Get/List are all dispatch needs) while allowing several sources to
// describe the same fleet:
//
//	priority: config > agent > discovery
//
// A printer_id can be claimed by more than one source; the highest-priority claim is the
// one that answers, and removing a lower-priority source never removes the printer.
type Registry struct {
	mu         sync.RWMutex
	claims     map[string][]claim // printer_id -> claims, sorted by priority
	candidates map[string]Candidate

	now func() time.Time
}

type claim struct {
	source    string
	sourceRef string
	priority  int
	printer   domain.Printer
	claimedAt time.Time
}

// Source names, in precedence order (lower number wins).
const (
	SourceConfig    = "config"
	SourceAgent     = "agent"
	SourceDiscovery = "discovery"
)

var sourcePriority = map[string]int{
	SourceConfig:    0, // declared by an operator: explicit beats implicit
	SourceAgent:     1, // reported by an enrolled agent on the network
	SourceDiscovery: 2, // merely found; candidates until adopted
}

func NewRegistry() *Registry {
	return &Registry{
		claims:     map[string][]claim{},
		candidates: map[string]Candidate{},
		now:        func() time.Time { return time.Now().UTC() },
	}
}

// Upsert records a printer from a source. It is idempotent: registering the same agent
// twice updates rather than duplicates.
func (r *Registry) Upsert(source, sourceRef string, p domain.Printer) error {
	if p.PrinterID == "" {
		return fmt.Errorf("fleet: printer_id is required")
	}
	prio, ok := sourcePriority[source]
	if !ok {
		return fmt.Errorf("fleet: unknown source %q", source)
	}
	if p.Port == 0 && source != SourceConfig {
		return fmt.Errorf("fleet: %s: port is required", p.PrinterID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	c := claim{source: source, sourceRef: sourceRef, priority: prio, printer: p, claimedAt: r.now()}
	claims := r.claims[p.PrinterID]
	for i := range claims {
		if claims[i].source == source && claims[i].sourceRef == sourceRef {
			claims[i] = c // same source reporting again
			r.claims[p.PrinterID] = claims
			return nil
		}
	}
	r.claims[p.PrinterID] = append(claims, c)
	sort.SliceStable(r.claims[p.PrinterID], func(i, j int) bool {
		return r.claims[p.PrinterID][i].priority < r.claims[p.PrinterID][j].priority
	})
	return nil
}

// UpsertMany is the batch form used by the agent registration endpoint.
func (r *Registry) UpsertMany(source, sourceRef string, ps []domain.Printer) error {
	for _, p := range ps {
		if err := r.Upsert(source, sourceRef, p); err != nil {
			return err
		}
	}
	return nil
}

// UpdateMetadata updates user-editable metadata (name, site, remark, custom_label, paper_size, dimensions, enabled)
// on all claims for the specified printer.
func (r *Registry) UpdateMetadata(printerID string, name, site, remark, customLabel, paperSize *string, mediaW, mediaH *float64, enabled *bool) (domain.Printer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	claims, ok := r.claims[printerID]
	if !ok || len(claims) == 0 {
		return domain.Printer{}, fmt.Errorf("fleet: printer %q not found", printerID)
	}
	for i := range claims {
		p := &claims[i].printer
		if name != nil {
			p.Name = *name
		}
		if site != nil {
			p.Site = *site
		}
		if remark != nil {
			p.Remark = *remark
		}
		if customLabel != nil {
			p.CustomLabel = *customLabel
		}
		if paperSize != nil {
			p.PaperSize = *paperSize
		}
		if mediaW != nil {
			p.MediaWidthMM = *mediaW
		}
		if mediaH != nil {
			p.MediaHeightMM = *mediaH
		}
		if enabled != nil {
			p.Enabled = *enabled
		}
	}
	return claims[0].printer, nil
}

// Remove drops one source's claim. The printer survives if another source still claims it,
// which is what lets an operator delete a config entry while an agent keeps reporting.
func (r *Registry) Remove(printerID, source, sourceRef string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	claims := r.claims[printerID]
	kept := claims[:0]
	found := false
	for _, c := range claims {
		if c.source == source && (sourceRef == "" || c.sourceRef == sourceRef) {
			found = true
			continue
		}
		kept = append(kept, c)
	}
	if !found {
		return false
	}
	if len(kept) == 0 {
		delete(r.claims, printerID)
	} else {
		r.claims[printerID] = kept
	}
	return true
}

// RemoveMissing drops the claims of one source that are not in keep. Used to reconcile an
// agent's re-registration: a printer it no longer reports stops being dispatchable.
func (r *Registry) RemoveMissing(source, sourceRef string, keep []string) []string {
	keepSet := make(map[string]bool, len(keep))
	for _, id := range keep {
		keepSet[id] = true
	}
	r.mu.Lock()
	removed := []string{}
	for id, claims := range r.claims {
		kept := claims[:0]
		for _, c := range claims {
			if c.source == source && (sourceRef == "" || c.sourceRef == sourceRef) && !keepSet[id] {
				removed = append(removed, id)
				continue
			}
			kept = append(kept, c)
		}
		if len(kept) == 0 {
			delete(r.claims, id)
			continue
		}
		r.claims[id] = kept
	}
	r.mu.Unlock()
	sort.Strings(removed)
	return removed
}

// RemoveSource drops every claim from one source (an agent went away).
func (r *Registry) RemoveSource(source, sourceRef string) []string {
	r.mu.Lock()
	ids := make([]string, 0, len(r.claims))
	for id, claims := range r.claims {
		kept := claims[:0]
		for _, c := range claims {
			if c.source == source && (sourceRef == "" || c.sourceRef == sourceRef) {
				continue
			}
			kept = append(kept, c)
		}
		if len(kept) == 0 {
			delete(r.claims, id)
			ids = append(ids, id)
			continue
		}
		r.claims[id] = kept
	}
	r.mu.Unlock()
	sort.Strings(ids)
	return ids
}

// Get returns the winning claim for a printer.
func (r *Registry) Get(printerID string) (domain.Printer, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	claims, ok := r.claims[printerID]
	if !ok || len(claims) == 0 {
		return domain.Printer{}, false
	}
	return claims[0].printer, true
}

// Provenance reports which sources claim a printer, highest priority first. The panel
// shows this so an operator can see "config + agent both know this printer".
func (r *Registry) Provenance(printerID string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.claims[printerID]))
	for _, c := range r.claims[printerID] {
		out = append(out, c.source)
	}
	return out
}

// List returns the winning claim for every printer, sorted by id.
func (r *Registry) List(f Filter) []domain.Printer {
	r.mu.RLock()
	out := make([]domain.Printer, 0, len(r.claims))
	for _, claims := range r.claims {
		if len(claims) == 0 {
			continue
		}
		p := claims[0].printer
		if f.Site != "" && p.Site != f.Site {
			continue
		}
		if f.Source != "" && claims[0].source != f.Source {
			continue
		}
		if f.PaperSize != "" && p.PaperSize != f.PaperSize {
			continue
		}
		out = append(out, p)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].PrinterID < out[j].PrinterID })
	return out
}

// Len is the number of addressable printers.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.claims)
}

// ------------------------------------------------------------- candidates

// Candidate is a device that discovery found but nobody has adopted. It is deliberately
// NOT a printer: auto-registering whatever answers on the LAN is how you print shipping
// labels at the wrong box.
type Candidate struct {
	DeviceKey string            `json:"device_key"`
	Source    string            `json:"source"`
	Vendor    string            `json:"vendor,omitempty"`
	Model     string            `json:"model,omitempty"`
	Serial    string            `json:"serial,omitempty"`
	Host      string            `json:"host,omitempty"`
	Port      int               `json:"port,omitempty"`
	Site      string            `json:"site,omitempty"`
	Detail    map[string]string `json:"detail,omitempty"`
	FirstSeen time.Time         `json:"first_seen"`
	LastSeen  time.Time         `json:"last_seen"`
	Dismissed bool              `json:"dismissed"`
}

// UpsertCandidate records a sighting. `adopted` marks a device that became a printer, so
// discovery stops listing it as a candidate.
func (r *Registry) UpsertCandidate(c Candidate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.DeviceKey == "" {
		return
	}
	if old, ok := r.candidates[c.DeviceKey]; ok {
		if c.FirstSeen.IsZero() {
			c.FirstSeen = old.FirstSeen
		}
		if c.Detail == nil {
			c.Detail = old.Detail
		}
		if c.Site == "" {
			c.Site = old.Site
		}
	}
	if c.FirstSeen.IsZero() {
		c.FirstSeen = r.now()
	}
	c.LastSeen = r.now()
	r.candidates[c.DeviceKey] = c
}

func (r *Registry) Candidate(deviceKey string) (Candidate, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.candidates[deviceKey]
	return c, ok
}

// Candidates lists sightings, newest first, optionally hiding dismissed ones.
func (r *Registry) Candidates(includeDismissed bool) []Candidate {
	r.mu.RLock()
	out := make([]Candidate, 0, len(r.candidates))
	for _, c := range r.candidates {
		if c.Dismissed && !includeDismissed {
			continue
		}
		out = append(out, c)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

func (r *Registry) DismissCandidate(deviceKey string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.candidates[deviceKey]
	if !ok {
		return false
	}
	c.Dismissed = true
	r.candidates[deviceKey] = c
	return true
}

// ExpireCandidates forgets sightings older than ttl and returns how many were dropped.
func (r *Registry) ExpireCandidates(ttl time.Duration) int {
	if ttl <= 0 {
		return 0
	}
	return r.ExpireBefore(r.now().Add(-ttl))
}

// ExpireBefore forgets sightings last seen before cutoff. Taking the cutoff rather than a
// duration lets the caller own the clock (discovery sweeps and tests may not share one).
func (r *Registry) ExpireBefore(cutoff time.Time) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for k, c := range r.candidates {
		if c.LastSeen.Before(cutoff) {
			delete(r.candidates, k)
			n++
		}
	}
	return n
}

// AdoptCandidate turns a candidate into a printer from the given source.
func (r *Registry) AdoptCandidate(deviceKey, source, sourceRef string, p domain.Printer) error {
	c, ok := r.Candidate(deviceKey)
	if !ok {
		return fmt.Errorf("fleet: unknown candidate %q", deviceKey)
	}
	if p.Host == "" {
		p.Host = c.Host
	}
	if p.Port == 0 {
		p.Port = c.Port
	}
	if p.Model == "" {
		p.Model = c.Model
	}
	if err := r.Upsert(source, sourceRef, p); err != nil {
		return err
	}
	// Stop listing it: it is a printer now, and it will appear in List().
	r.mu.Lock()
	delete(r.candidates, deviceKey)
	r.mu.Unlock()
	return nil
}

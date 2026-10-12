package fleet

import (
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/kucial/cloud-print-service/pkg/printer"
	"github.com/kucial/cloud-print-service/server/internal/domain"
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

	// saved holds operator edits by printer id, as persisted. They are applied to a
	// printer when it is first claimed after a restart, and kept up to date by
	// UpdateMetadata; onSave writes them to the store.
	saved  map[string]MetadataPatch
	onSave func(printerID string, merged MetadataPatch)

	now func() time.Time
}

type claim struct {
	source    string
	sourceRef string
	priority  int
	printer   domain.Printer
	claimedAt time.Time

	// overrides are operator edits (PATCH) to this claim. A source that reports again —
	// an agent re-registers every poll — must not silently undo them, so they are
	// re-applied on top of every re-report.
	overrides MetadataPatch
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
		saved:      map[string]MetadataPatch{},
		now:        func() time.Time { return time.Now().UTC() },
	}
}

// LoadOverrides restores persisted operator edits. Call it before printers are
// claimed, so a printer that comes back after a restart gets its edits straight away.
func (r *Registry) LoadOverrides(saved map[string]MetadataPatch) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, p := range saved {
		r.saved[id] = p
	}
}

// OnOverrideSaved registers where edits are persisted. fn gets every edit made so far
// to the printer, merged; it runs under the registry lock, so it must not call back in.
func (r *Registry) OnOverrideSaved(fn func(printerID string, merged MetadataPatch)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onSave = fn
}

// Upsert records a printer from a source. It is idempotent: registering the same agent
// twice updates rather than duplicates.
func (r *Registry) Upsert(source, sourceRef string, p domain.Printer) error {
	p.Normalize()
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
			// Same source reporting again: fresh facts, operator edits kept.
			c.overrides = claims[i].overrides
			c.overrides.applyTo(&c.printer)
			c.printer.Normalize()
			claims[i] = c
			r.claims[p.PrinterID] = claims
			return nil
		}
	}
	// A new claim (first sight since start) picks up the printer's saved edits.
	if saved, ok := r.saved[p.PrinterID]; ok {
		c.overrides = saved
		saved.applyTo(&c.printer)
		c.printer.Normalize()
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

// MetadataPatch specifies user-editable printer attributes.
type MetadataPatch struct {
	Name               *string   `json:"name,omitempty"`
	Site               *string   `json:"site,omitempty"`
	Remark             *string   `json:"remark,omitempty"`
	CustomLabel        *string   `json:"custom_label,omitempty"`
	PaperSize          *string   `json:"paper_size,omitempty"`
	MediaWidthMM       *float64  `json:"media_width_mm,omitempty"`
	MediaHeightMM      *float64  `json:"media_height_mm,omitempty"`
	DPI                *int      `json:"dpi,omitempty"`
	DPMM               *float64  `json:"dpmm,omitempty"`
	ResolutionSource   *string   `json:"resolution_source,omitempty"`
	DPIResolvedFromHI  *bool     `json:"dpi_resolved_from_hi,omitempty"`
	Orientation        *string   `json:"orientation,omitempty"`
	MediaOrientation   *string   `json:"media_orientation,omitempty"`
	PrintDirection     *string   `json:"print_direction,omitempty"`
	CJKSupported       *bool     `json:"cjk_supported,omitempty"`
	CJKFont            *string   `json:"cjk_font,omitempty"`
	Fonts              *[]string `json:"fonts,omitempty"`
	FontSource         *string   `json:"font_source,omitempty"`
	FontResolvedFromHI *bool     `json:"font_resolved_from_hi,omitempty"`
	Enabled            *bool     `json:"enabled,omitempty"`

	// AvailableFonts is reported by the printer, never sent by clients.
	AvailableFonts *[]printer.StoredFile `json:"-"`
}

// UpdateMetadata updates user-editable metadata (name, site, remark, custom_label, paper_size, dimensions, resolution, CJK font, enabled)
// on all claims for the specified printer.
func (r *Registry) UpdateMetadata(printerID string, patch MetadataPatch) (domain.Printer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	claims, ok := r.claims[printerID]
	if !ok || len(claims) == 0 {
		return domain.Printer{}, fmt.Errorf("fleet: printer %q not found", printerID)
	}
	for i := range claims {
		claims[i].overrides = claims[i].overrides.merge(patch)
		patch.applyTo(&claims[i].printer)
		claims[i].printer.Normalize()
	}
	r.saved[printerID] = r.saved[printerID].merge(patch)
	if r.onSave != nil {
		r.onSave(printerID, r.saved[printerID])
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
		if f.PaperSize != "" && !p.MatchesPaperSize(f.PaperSize) {
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

// applyTo writes every set field of the patch onto p.
func (patch MetadataPatch) applyTo(p *domain.Printer) {
	if patch.Name != nil {
		p.Name = *patch.Name
	}
	if patch.Site != nil {
		p.Site = *patch.Site
	}
	if patch.Remark != nil {
		p.Remark = *patch.Remark
	}
	if patch.CustomLabel != nil {
		p.CustomLabel = *patch.CustomLabel
	}
	if patch.PaperSize != nil {
		p.PaperSize = *patch.PaperSize
	}
	if patch.MediaWidthMM != nil {
		p.MediaWidthMM = *patch.MediaWidthMM
	}
	if patch.MediaHeightMM != nil {
		p.MediaHeightMM = *patch.MediaHeightMM
	}
	if patch.DPI != nil {
		p.DPI = *patch.DPI
	}
	if patch.DPMM != nil {
		p.DPMM = *patch.DPMM
	}
	if patch.ResolutionSource != nil {
		p.ResolutionSource = *patch.ResolutionSource
	}
	if patch.DPIResolvedFromHI != nil {
		p.DPIResolvedFromHI = *patch.DPIResolvedFromHI
	}
	if patch.Orientation != nil {
		p.Orientation = *patch.Orientation
	}
	if patch.MediaOrientation != nil {
		p.MediaOrientation = *patch.MediaOrientation
	}
	if patch.PrintDirection != nil {
		p.PrintDirection = *patch.PrintDirection
	}
	if patch.CJKSupported != nil {
		p.CJKSupported = *patch.CJKSupported
	}
	if patch.CJKFont != nil {
		p.CJKFont = *patch.CJKFont
	}
	if patch.Fonts != nil {
		p.Fonts = *patch.Fonts
	}
	if patch.FontSource != nil {
		p.FontSource = *patch.FontSource
	}
	if patch.FontResolvedFromHI != nil {
		p.FontResolvedFromHI = *patch.FontResolvedFromHI
	}
	if patch.Enabled != nil {
		p.Enabled = *patch.Enabled
	}
	if patch.AvailableFonts != nil {
		p.AvailableFonts = *patch.AvailableFonts
	}
}

// merge returns m with every field set in next taking next's value.
func (m MetadataPatch) merge(next MetadataPatch) MetadataPatch {
	dst, src := reflect.ValueOf(&m).Elem(), reflect.ValueOf(next)
	for i := 0; i < src.NumField(); i++ {
		if !src.Field(i).IsNil() {
			dst.Field(i).Set(src.Field(i))
		}
	}
	return m
}

package fleet

import (
	"testing"
	"time"

	"github.com/kucial/cloud-print-service/server/internal/domain"
)

func spec(id, site, host string, port int) domain.Printer {
	return domain.Printer{PrinterID: id, Name: id, Site: site, Host: host, Port: port, Enabled: true}
}

func TestRegistryPrecedenceConfigBeatsAgent(t *testing.T) {
	r := NewRegistry()
	if err := r.Upsert(SourceAgent, "agent-1", spec("wh1-pack-01", "wh1", "10.0.0.9", 19100)); err != nil {
		t.Fatal(err)
	}
	if err := r.Upsert(SourceConfig, "", spec("wh1-pack-01", "wh1", "10.0.0.9", 9100)); err != nil {
		t.Fatal(err)
	}
	got, ok := r.Get("wh1-pack-01")
	if !ok || got.Port != 9100 {
		t.Fatalf("config must win over agent: %+v", got)
	}
	if prov := r.Provenance("wh1-pack-01"); len(prov) != 2 || prov[0] != SourceConfig {
		t.Fatalf("provenance = %v, want config first", prov)
	}

	// Removing the agent claim must not remove the printer.
	r.Remove("wh1-pack-01", SourceAgent, "agent-1")
	if _, ok := r.Get("wh1-pack-01"); !ok {
		t.Fatal("printer vanished when a lower-priority claim was removed")
	}
	r.Remove("wh1-pack-01", SourceConfig, "")
	if _, ok := r.Get("wh1-pack-01"); ok {
		t.Fatal("printer should be gone once no source claims it")
	}
}

func TestRegistryUpdateMetadataAndNormalization(t *testing.T) {
	r := NewRegistry()
	if err := r.Upsert(SourceConfig, "", spec("p1", "wh1", "127.0.0.1", 9100)); err != nil {
		t.Fatal(err)
	}

	paperSize := "100x150mm"
	dpi := 203
	cjkFont := "SIMSUN.FNT"
	orientation := "normal"
	dir := "top_first"

	updated, err := r.UpdateMetadata("p1", MetadataPatch{
		PaperSize:      &paperSize,
		DPI:            &dpi,
		CJKFont:        &cjkFont,
		Orientation:    &orientation,
		PrintDirection: &dir,
	})
	if err != nil {
		t.Fatalf("UpdateMetadata failed: %v", err)
	}

	if updated.DPMM != 8 {
		t.Errorf("expected DPMM 8, got %v", updated.DPMM)
	}
	if updated.MediaWidthMM != 100 || updated.MediaHeightMM != 150 {
		t.Errorf("expected 100x150mm, got %vx%v", updated.MediaWidthMM, updated.MediaHeightMM)
	}
	if !updated.CJKSupported {
		t.Errorf("expected CJKSupported true")
	}
	if updated.CJKFont != "SIMSUN.FNT" {
		t.Errorf("expected CJKFont SIMSUN.FNT, got %q", updated.CJKFont)
	}

	// Test List with PaperSize filter (matching mm and 4x6 compatibility)
	matched := r.List(Filter{PaperSize: "100x150mm"})
	if len(matched) != 1 {
		t.Errorf("expected 1 printer matched on 100x150mm, got %d", len(matched))
	}
	matchedBare := r.List(Filter{PaperSize: "100x150"})
	if len(matchedBare) != 1 {
		t.Errorf("expected 1 printer matched on 100x150 (bare mm), got %d", len(matchedBare))
	}
	matchedCompat := r.List(Filter{PaperSize: "4x6"})
	if len(matchedCompat) != 1 {
		t.Errorf("expected 1 printer matched on 4x6 for 100x150mm roll, got %d", len(matchedCompat))
	}
}

func TestRegistryUpsertIsIdempotentPerSource(t *testing.T) {
	r := NewRegistry()
	for i := 0; i < 5; i++ {
		if err := r.Upsert(SourceAgent, "a1", spec("p1", "wh1", "127.0.0.1", 19100)); err != nil {
			t.Fatal(err)
		}
	}
	if r.Len() != 1 {
		t.Fatalf("re-registering an agent must not duplicate printers, got %d", r.Len())
	}
	// A host change from the same source updates in place.
	if err := r.Upsert(SourceAgent, "a1", spec("p1", "wh1", "127.0.0.1", 19200)); err != nil {
		t.Fatal(err)
	}
	p, _ := r.Get("p1")
	if p.Port != 19200 {
		t.Fatalf("port = %d, want the latest report", p.Port)
	}
}

func TestRegistryRemoveSourceDropsAgentPrinters(t *testing.T) {
	r := NewRegistry()
	_ = r.Upsert(SourceConfig, "", spec("cfg1", "wh1", "10.0.0.1", 9100))
	_ = r.Upsert(SourceAgent, "a1", spec("usb1", "wh1", "10.0.0.2", 19100))
	_ = r.Upsert(SourceAgent, "a1", spec("usb2", "wh1", "10.0.0.2", 19101))
	_ = r.Upsert(SourceAgent, "a2", spec("usb3", "wh1", "10.0.0.3", 19102))

	gone := r.RemoveSource(SourceAgent, "a1")
	if len(gone) != 2 {
		t.Fatalf("removed %v, want 2 agent printers", gone)
	}
	if _, ok := r.Get("cfg1"); !ok {
		t.Fatal("config printer must survive an agent going away")
	}
	if _, ok := r.Get("usb3"); !ok {
		t.Fatal("another agent's printer must survive")
	}
	if _, ok := r.Get("usb1"); ok {
		t.Fatal("the departing agent's printer should be gone")
	}
}

func TestRegistryValidates(t *testing.T) {
	r := NewRegistry()
	if err := r.Upsert(SourceAgent, "a1", domain.Printer{}); err == nil {
		t.Error("printer_id is required")
	}
	if err := r.Upsert("telepathy", "x", spec("p", "s", "h", 9100)); err == nil {
		t.Error("unknown source must be rejected")
	}
	if err := r.Upsert(SourceAgent, "a1", spec("p", "s", "h", 0)); err == nil {
		t.Error("an agent-reported printer without a port is unroutable")
	}
	if err := r.Upsert(SourceConfig, "", spec("p", "s", "h", 0)); err != nil {
		t.Errorf("config printers may omit a port (they get the default): %v", err)
	}
}

func TestCandidatesAreNotPrinters(t *testing.T) {
	r := NewRegistry()
	r.UpsertCandidate(Candidate{DeviceKey: "d1", Source: SourceDiscovery, Model: "ZT411", Host: "10.0.0.9", Port: 9100})
	if r.Len() != 0 {
		t.Fatal("a discovered device must not be a dispatchable printer yet")
	}
	cs := r.Candidates(false)
	if len(cs) != 1 || cs[0].Model != "ZT411" {
		t.Fatalf("candidate = %+v", cs)
	}

	// Re-seeing a device keeps first_seen and refreshes last_seen.
	first := cs[0].FirstSeen
	r.UpsertCandidate(Candidate{DeviceKey: "d1", Source: SourceDiscovery, Host: "10.0.0.9", Port: 9100})
	cs = r.Candidates(false)
	if cs[0].Site != "" || cs[0].FirstSeen.Before(first) {
		t.Fatalf("candidate re-seen should keep its metadata: %+v", cs[0])
	}

	if err := r.AdoptCandidate("d1", SourceDiscovery, "", domain.Printer{PrinterID: "wh1-new", Site: "wh1"}); err != nil {
		t.Fatal(err)
	}
	if r.Len() != 1 {
		t.Fatal("adoption should create exactly one printer")
	}
	p, _ := r.Get("wh1-new")
	if p.Host != "10.0.0.9" || p.Port != 9100 {
		t.Fatalf("adoption must carry over the discovered endpoint: %+v", p)
	}
	if len(r.Candidates(false)) != 0 {
		t.Fatal("an adopted device must stop being listed as a candidate")
	}
}

func TestCandidateDismissAndExpiry(t *testing.T) {
	r := NewRegistry()
	r.UpsertCandidate(Candidate{DeviceKey: "d1", Source: SourceDiscovery})
	if !r.DismissCandidate("d1") {
		t.Fatal("dismiss should succeed")
	}
	if len(r.Candidates(false)) != 0 {
		t.Fatal("dismissed candidates are hidden by default")
	}
	if len(r.Candidates(true)) != 1 {
		t.Fatal("dismissed candidates are still visible when asked for")
	}

	r.now = func() time.Time { return time.Now().UTC().Add(2 * time.Hour) }
	if n := r.ExpireCandidates(time.Hour); n != 1 {
		t.Fatalf("expired %d candidates, want 1", n)
	}
	if len(r.Candidates(true)) != 0 {
		t.Fatal("expired candidates must be forgotten")
	}
}

func TestRegistryListIsSortedAndFiltered(t *testing.T) {
	r := NewRegistry()
	for _, p := range []domain.Printer{
		spec("wh2-b", "wh2", "h", 9100),
		spec("wh1-a", "wh1", "h", 9100),
		spec("wh1-b", "wh1", "h", 9100),
	} {
		_ = r.Upsert(SourceConfig, "", p)
	}
	all := r.List(Filter{})
	if len(all) != 3 || all[0].PrinterID != "wh1-a" || all[2].PrinterID != "wh2-b" {
		t.Fatalf("list must be sorted by id: %+v", all)
	}
	if n := len(r.List(Filter{Site: "wh1"})); n != 2 {
		t.Fatalf("site filter returned %d", n)
	}
}

// An agent re-registers every poll; an operator's edits to its printer must survive.
func TestOperatorEditsSurviveAgentReRegistration(t *testing.T) {
	r := NewRegistry()
	reported := spec("pc7-usb-1", "wh1", "10.0.0.7", 19390)
	if err := r.Upsert(SourceAgent, "pc7", reported); err != nil {
		t.Fatal(err)
	}
	size, off := "60x40mm", false
	if _, err := r.UpdateMetadata("pc7-usb-1", MetadataPatch{PaperSize: &size, Enabled: &off}); err != nil {
		t.Fatal(err)
	}

	reported.Model = "GK888t" // a fresh report with new facts
	if err := r.Upsert(SourceAgent, "pc7", reported); err != nil {
		t.Fatal(err)
	}
	got, _ := r.Get("pc7-usb-1")
	if got.PaperSize != "60x40mm" || got.MediaWidthMM != 60 || got.MediaHeightMM != 40 {
		t.Fatalf("paper size lost on re-registration: %+v", got)
	}
	if got.Enabled {
		t.Fatal("an operator-disabled printer must stay disabled when its agent re-registers")
	}
	if got.Model != "GK888t" {
		t.Fatalf("fresh facts from the agent must still apply, model=%q", got.Model)
	}
}

// Edits are handed to the store merged, and restored onto a printer claimed after a
// restart — whichever source claims it.
func TestOperatorEditsArePersistedAndRestored(t *testing.T) {
	r := NewRegistry()
	var saved = map[string]MetadataPatch{}
	r.OnOverrideSaved(func(id string, p MetadataPatch) { saved[id] = p })
	if err := r.Upsert(SourceAgent, "pc7", spec("pc7-usb-1", "wh1", "10.0.0.7", 19390)); err != nil {
		t.Fatal(err)
	}
	size, name := "60x40mm", "Dock printer"
	_, _ = r.UpdateMetadata("pc7-usb-1", MetadataPatch{PaperSize: &size})
	_, _ = r.UpdateMetadata("pc7-usb-1", MetadataPatch{Name: &name})
	if p := saved["pc7-usb-1"]; p.PaperSize == nil || *p.PaperSize != size || p.Name == nil || *p.Name != name {
		t.Fatalf("store must get every edit, merged: %+v", p)
	}

	// restart: a fresh registry, edits loaded from the store, the agent re-registers
	r2 := NewRegistry()
	r2.LoadOverrides(saved)
	if err := r2.Upsert(SourceAgent, "pc7", spec("pc7-usb-1", "wh1", "10.0.0.7", 19390)); err != nil {
		t.Fatal(err)
	}
	got, _ := r2.Get("pc7-usb-1")
	if got.PaperSize != size || got.Name != name || got.MediaWidthMM != 60 {
		t.Fatalf("edits not restored after restart: %+v", got)
	}
}

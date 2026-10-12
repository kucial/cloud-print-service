package discovery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kucial/cloud-print-service/internal/domain"
	"github.com/kucial/cloud-print-service/internal/fleet"
)

type fakeProbe struct {
	name  string
	items []Found
	err   error
	calls int
}

func (p *fakeProbe) Name() string { return p.name }
func (p *fakeProbe) Scan(ctx context.Context) ([]Found, error) {
	p.calls++
	return p.items, p.err
}

type nopLogger struct{}

func (nopLogger) Info(string, ...any)  {}
func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Debug(string, ...any) {}

// Discovery finds devices; it must never make one dispatchable.
func TestScanRecordsCandidatesNotPrinters(t *testing.T) {
	reg := fleet.NewRegistry()
	probe := &fakeProbe{name: "mdns", items: []Found{
		{DeviceKey: "mdns:10.20.1.53", Source: "mdns", Host: "10.20.1.53", Port: 9100, Model: "ZT411", Serial: "SN1"},
		{DeviceKey: "mdns:10.20.1.54", Source: "mdns", Host: "10.20.1.54", Port: 9100, Model: "ZD621", Serial: "SN2"},
	}}
	svc := NewService(Config{Site: "wh1"}, reg, nopLogger{}).WithProbes(probe)

	found, err := svc.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if found != 2 {
		t.Fatalf("found = %d, want 2", found)
	}
	if reg.Len() != 0 {
		t.Fatal("discovery must not create dispatchable printers")
	}
	cs := reg.Candidates(false)
	if len(cs) != 2 {
		t.Fatalf("candidates = %d, want 2", len(cs))
	}
	if cs[0].Site != "wh1" || cs[0].FirstSeen.IsZero() || cs[0].LastSeen.IsZero() {
		t.Fatalf("candidate metadata missing: %+v", cs[0])
	}
	if st := svc.Status(); !st.Enabled || st.LastScan.IsZero() || st.Candidates != 2 {
		t.Fatalf("status = %+v", st)
	}
}

func TestAdoptionIsTheOnlyPathToAPrinter(t *testing.T) {
	reg := fleet.NewRegistry()
	svc := NewService(Config{}, reg, nopLogger{}).WithProbes(&fakeProbe{name: "mdns", items: []Found{
		{DeviceKey: "mdns:10.20.1.53", Source: "mdns", Host: "10.20.1.53", Port: 9100, Model: "ZT411", Serial: "SN1"},
	}})
	if _, err := svc.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	c := reg.Candidates(false)[0]
	if err := reg.AdoptCandidate(c.DeviceKey, fleet.SourceDiscovery, "",
		domainPrinter("wh1-pack-04")); err != nil {
		t.Fatal(err)
	}
	p, ok := reg.Get("wh1-pack-04")
	if !ok {
		t.Fatal("adopted printer is not addressable")
	}
	if p.Host != "10.20.1.53" || p.Port != 9100 || p.Model != "ZT411" {
		t.Fatalf("adoption lost the discovered endpoint: %+v", p)
	}
	if len(reg.Candidates(false)) != 0 {
		t.Fatal("adopted device must stop being a candidate")
	}
}

func TestCandidateLifecycle(t *testing.T) {
	reg := fleet.NewRegistry()
	// Same device seen repeatedly keeps first_seen and refreshes last_seen.
	probe := &fakeProbe{name: "mdns", items: []Found{
		{DeviceKey: "d1", Source: "mdns", Host: "10.0.0.9", Port: 9100},
	}}
	svc := NewService(Config{}, reg, nopLogger{}).WithProbes(probe)
	if _, err := svc.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := reg.Candidates(false)[0].FirstSeen
	time.Sleep(2 * time.Millisecond)
	if _, err := svc.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	c := reg.Candidates(false)
	if len(c) != 1 {
		t.Fatalf("re-seeing a device must not duplicate it: %d", len(c))
	}
	if c[0].FirstSeen.After(first) {
		t.Fatal("first_seen must be preserved")
	}

	// Dismissed devices disappear from the default list but stay auditable.
	reg.DismissCandidate("d1")
	if len(reg.Candidates(false)) != 0 || len(reg.Candidates(true)) != 1 {
		t.Fatal("dismiss semantics broken")
	}

	// Devices that stop answering are forgotten once past the TTL. The clock is moved
	// rather than slept through, because a TTL of nanoseconds is otherwise a race.
	reg.UpsertCandidate(fleet.Candidate{DeviceKey: "gone", Source: "mdns"})
	svc.now = func() time.Time { return time.Now().UTC().Add(time.Hour) }
	svc.cfg.CandidateTTL = time.Minute
	if _, err := svc.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range reg.Candidates(true) {
		if c.DeviceKey == "gone" {
			t.Fatal("an expired candidate must be forgotten")
		}
	}
}

func TestProbeFailureDoesNotHideWhatWasFound(t *testing.T) {
	reg := fleet.NewRegistry()
	good := &fakeProbe{name: "mdns", items: []Found{{DeviceKey: "d1", Source: "mdns", Host: "10.0.0.9"}}}
	bad := &fakeProbe{name: "tcp", err: errors.New("no route to host")}
	svc := NewService(Config{}, reg, nopLogger{}).WithProbes(bad, good)

	found, err := svc.Scan(context.Background())
	if err == nil {
		t.Fatal("a failing probe must be reported")
	}
	if found != 1 || len(reg.Candidates(false)) != 1 {
		t.Fatalf("a partial sweep must still record what it found: found=%d", found)
	}
	if st := svc.Status(); st.LastError == "" {
		t.Fatal("status must carry the error so the panel can show it")
	}
}

func TestEmptyDeviceKeyIsIgnored(t *testing.T) {
	reg := fleet.NewRegistry()
	svc := NewService(Config{}, reg, nopLogger{}).WithProbes(&fakeProbe{name: "mdns", items: []Found{
		{Host: "10.0.0.9"}, // no device key: unusable
	}})
	if _, err := svc.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(reg.Candidates(true)) != 0 {
		t.Fatal("a sighting with no device key must be dropped, not stored as a blank candidate")
	}
}

func TestDefaultTypesIncludeLinkOS(t *testing.T) {
	types := DefaultTypes()
	found := false
	for _, t := range types {
		if t == "_pdl-datastream._tcp" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the Link-OS service type must be browsed: %v", types)
	}
}

func domainPrinter(id string) domain.Printer {
	return domain.Printer{PrinterID: id}
}

// A probe with no configuration must still scan something: an empty service-type list
// that silently scans nothing is indistinguishable from an empty warehouse.
func TestMDNSDefaultsToTheLinkOSTypes(t *testing.T) {
	p := MDNS{}.withDefaults()
	if len(p.Types) == 0 {
		t.Fatal("a zero MDNS probe must default to the Link-OS service types")
	}
	if p.BrowseTimeout <= 0 || p.IdentifyTimeout <= 0 || p.DefaultPort != 9100 {
		t.Fatalf("defaults incomplete: %+v", p)
	}
}

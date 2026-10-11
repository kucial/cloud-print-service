package fleet

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/kongkx/cloud-print-service/internal/bus"
	"github.com/kongkx/cloud-print-service/internal/domain"
)

// A status probe must not erase the "which document caused this" pointer: operators
// debug a flapping printer by looking at the last job that touched it.
func TestPollPreservesLastJob(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr, err := NewManager([]domain.Printer{
		{PrinterID: "p1", Name: "p1", Driver: "mock", Host: "x", Enabled: true,
			Mock: &domain.MockCfg{State: "idle"}},
	}, log, bus.New(), ManagerOpts{PollInterval: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	p, _ := mgr.Get("p1")
	p.NoteJob("job_42", domain.JobPrinted)
	mgr.ProbeAll(context.Background())
	mgr.ProbeAll(context.Background())

	got := p.Cached()
	if got.LastJob == nil {
		t.Fatal("last_job pointer was wiped by a status probe")
	}
	if got.LastJob.JobID != "job_42" || got.LastJob.State != domain.JobPrinted {
		t.Fatalf("wrong last job: %+v", got.LastJob)
	}
	if got.State != domain.StateIdle {
		t.Fatalf("state = %q", got.State)
	}
}

// Usability requires positive evidence: never polled, offline, or consumable-out all
// mean "not usable", and unknown must never be reported as usable.
func TestUsableRequiresEvidence(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr, err := NewManager([]domain.Printer{
		{PrinterID: "unprobed", Driver: "mock", Host: "x", Enabled: true, Mock: &domain.MockCfg{}},
		{PrinterID: "out", Driver: "mock", Host: "x", Enabled: true, Mock: &domain.MockCfg{State: "media_out"}},
		{PrinterID: "off", Driver: "mock", Host: "x", Enabled: true, Mock: &domain.MockCfg{Offline: true}},
		{PrinterID: "disabled", Driver: "mock", Host: "x", Enabled: false, Mock: &domain.MockCfg{}},
		{PrinterID: "ok", Driver: "mock", Host: "x", Enabled: true, Mock: &domain.MockCfg{State: "idle"}},
	}, log, bus.New(), ManagerOpts{PollInterval: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	// Before any probe, nothing is known — "unknown" must never read as usable.
	if p, _ := mgr.Get("unprobed"); p.View().Usable {
		t.Fatal("a printer that has never been observed must not be usable")
	}
	mgr.ProbeAll(context.Background())

	want := map[string]bool{
		"unprobed": true, // it is a healthy mock, now observed
		"out":      false,
		"off":      false,
		"disabled": false,
		"ok":       true,
	}
	for id, expect := range want {
		p, _ := mgr.Get(id)
		if got := p.View().Usable; got != expect {
			t.Errorf("%s usable = %v, want %v (state %q)", id, got, expect, p.Cached().State)
		}
	}

	// The dashboard filter is what a WMS uses to raise an alarm.
	no := map[string]bool{}
	for _, v := range mgr.List(Filter{Usable: boolp(false)}) {
		no[v.PrinterID] = true
	}
	if len(no) != 3 {
		t.Fatalf("usable=false should list out/off/disabled, got %v", no)
	}
	if !no["out"] || no["ok"] {
		t.Fatalf("filter picked the wrong printers: %v", no)
	}
}

func boolp(b bool) *bool { return &b }

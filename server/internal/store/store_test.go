package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/kucial/cloud-print-service/server/internal/domain"
)

// Every store must behave the same; the job service cannot tell them apart.
func eachStore(t *testing.T, test func(t *testing.T, s Store)) {
	t.Run("memory", func(t *testing.T) { test(t, NewMemoryStore()) })
	t.Run("sqlite", func(t *testing.T) {
		s, err := OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "cps.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		test(t, s)
	})
}

func newJob(client, ext, printer string, at time.Time) *domain.Job {
	return &domain.Job{ClientID: client, ExternalID: ext, PrinterID: printer, State: domain.JobQueued,
		Payload: "^XA^FD" + ext + "^FS^XZ", IdempotencyKey: "k-" + ext, Copies: 2, PageCopies: []int{1, 3},
		Meta: map[string]string{"order": ext}, QueuedAt: at}
}

func TestJobsRoundTrip(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		t0 := time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)
		j := newJob("wms", "SO-1", "p1", t0)
		if err := s.CreateJob(ctx, j); err != nil {
			t.Fatal(err)
		}
		if j.ID != "job_1" {
			t.Fatalf("first id %q", j.ID)
		}
		got, err := s.GetJob(ctx, j.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Payload != j.Payload || got.IdempotencyKey != j.IdempotencyKey || got.Copies != 2 ||
			len(got.PageCopies) != 2 || got.Meta["order"] != "SO-1" || !got.QueuedAt.Equal(t0) {
			t.Fatalf("round trip lost data: %+v", got)
		}
		if g, err := s.GetByExternal(ctx, "wms", "SO-1"); err != nil || g.ID != j.ID {
			t.Fatalf("by external: %v %v", g, err)
		}
		if _, err := s.GetByExternal(ctx, "erp", "SO-1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("external ids are per client: %v", err)
		}
		if err := s.CreateJob(ctx, newJob("wms", "SO-1", "p1", t0)); err == nil {
			t.Fatal("duplicate external id accepted")
		}
		if err := s.CreateJob(ctx, newJob("erp", "SO-1", "p1", t0)); err != nil {
			t.Fatalf("same external id, other client: %v", err)
		}

		now := t0.Add(time.Minute)
		got.State, got.Attempts, got.SentAt = domain.JobPrinted, 1, &now
		if err := s.UpdateJob(ctx, got); err != nil {
			t.Fatal(err)
		}
		again, _ := s.GetJob(ctx, j.ID)
		if again.State != domain.JobPrinted || again.Attempts != 1 || again.SentAt == nil {
			t.Fatalf("update lost: %+v", again)
		}
		if err := s.UpdateJob(ctx, &domain.Job{ID: "job_404"}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("update unknown: %v", err)
		}
		if _, err := s.GetJob(ctx, "job_404"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("get unknown: %v", err)
		}
	})
}

func TestListJobsFiltersAndOrder(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		t0 := time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)
		for i, spec := range []struct{ client, ext, printer string }{
			{"wms", "a", "p1"}, {"wms", "b", "p2"}, {"erp", "c", "p1"}, {"wms", "d", "p1"},
		} {
			if err := s.CreateJob(ctx, newJob(spec.client, spec.ext, spec.printer, t0.Add(time.Duration(i)*time.Second))); err != nil {
				t.Fatal(err)
			}
		}
		ids := func(js []domain.Job) (out []string) {
			for _, j := range js {
				out = append(out, j.ExternalID)
			}
			return
		}
		check := func(name string, f Filter, want ...string) {
			t.Helper()
			got, err := s.ListJobs(ctx, f)
			if err != nil {
				t.Fatal(err)
			}
			if g := ids(got); len(g) != len(want) || (len(g) > 0 && func() bool {
				for i := range g {
					if g[i] != want[i] {
						return true
					}
				}
				return false
			}()) {
				t.Errorf("%s: got %v, want %v", name, g, want)
			}
		}
		check("newest first", Filter{}, "d", "c", "b", "a")
		check("client", Filter{ClientID: "wms"}, "d", "b", "a")
		check("printer", Filter{PrinterID: "p1"}, "d", "c", "a")
		check("limit", Filter{Limit: 2}, "d", "c")
		from := t0.Add(time.Second)
		check("from", Filter{From: &from}, "d", "c", "b")
		check("ids keep the caller's order", Filter{IDs: []string{"job_1", " job_3 ", "job_9"}}, "a", "c")
		check("ids with client", Filter{IDs: []string{"job_1", "job_3"}, ClientID: "erp"}, "c")
		check("state", Filter{State: string(domain.JobPrinted)})
	})
}

func TestAttemptsAndRestart(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		j := newJob("wms", "x", "p1", time.Now().UTC())
		if err := s.CreateJob(ctx, j); err != nil {
			t.Fatal(err)
		}
		for i := 1; i <= 2; i++ {
			a := &domain.Attempt{JobID: j.ID, AttemptNo: i, StartedAt: time.Now().UTC(), Result: "ok", BytesSent: 10 * i}
			if err := s.AddAttempt(ctx, a); err != nil || a.ID == 0 {
				t.Fatalf("attempt %d: id=%d err=%v", i, a.ID, err)
			}
		}
		as, err := s.Attempts(ctx, j.ID)
		if err != nil || len(as) != 2 || as[1].AttemptNo != 2 || as[1].BytesSent != 20 {
			t.Fatalf("attempts: %+v %v", as, err)
		}

		j.State = domain.JobSending
		_ = s.UpdateJob(ctx, j)
		n, err := s.RequeueUnfinished(ctx)
		if err != nil || n != 1 {
			t.Fatalf("requeue: %d %v", n, err)
		}
		got, _ := s.GetJob(ctx, j.ID)
		if got.State != domain.JobSentUnknown || got.FinishedAt == nil {
			t.Fatalf("a job in flight at restart must become sent_unknown: %+v", got)
		}
	})
}

func TestPrinterOverrides(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		_ = s.SavePrinterOverride(ctx, "p1", json.RawMessage(`{"paper_size":"60x40mm"}`))
		_ = s.SavePrinterOverride(ctx, "p1", json.RawMessage(`{"paper_size":"100x150mm"}`))
		_ = s.SavePrinterOverride(ctx, "p2", json.RawMessage(`{"name":"Dock"}`))
		got, err := s.PrinterOverrides(ctx)
		if err != nil || len(got) != 2 || string(got["p1"]) != `{"paper_size":"100x150mm"}` {
			t.Fatalf("overrides: %v %v", got, err)
		}
	})
}

// The point of SQLite: a restart (reopening the file) keeps jobs, and ids continue
// instead of colliding with the ones already issued.
func TestSQLiteSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cps.db")
	s, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	j := newJob("wms", "keep", "p1", time.Now().UTC())
	_ = s.CreateJob(ctx, j)
	_ = s.SavePrinterOverride(ctx, "p1", json.RawMessage(`{"name":"kept"}`))
	s.Close()

	s, err = OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, err := s.GetByExternal(ctx, "wms", "keep"); err != nil || got.ID != j.ID {
		t.Fatalf("job lost across restart: %v %v", got, err)
	}
	next := newJob("wms", "next", "p1", time.Now().UTC())
	if err := s.CreateJob(ctx, next); err != nil || next.ID != "job_2" {
		t.Fatalf("ids must continue after a restart: %q %v", next.ID, err)
	}
	if o, _ := s.PrinterOverrides(ctx); string(o["p1"]) != `{"name":"kept"}` {
		t.Fatalf("override lost: %v", o)
	}
}

func TestSQLiteFileIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "cps.db")
	s, err := OpenSQLite(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("database is readable by others: %v", fi.Mode().Perm())
	}
}

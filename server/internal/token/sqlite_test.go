package token

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func openTokenDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	return db
}

// The token lifecycle the API relies on, against both stores.
func TestTokenLifecycleOnEveryStore(t *testing.T) {
	stores := map[string]func(t *testing.T) Store{
		"memory": func(t *testing.T) Store { return NewMemoryStore() },
		"sqlite": func(t *testing.T) Store {
			db := openTokenDB(t, filepath.Join(t.TempDir(), "cps.db"))
			t.Cleanup(func() { db.Close() })
			st, err := NewSQLiteStore(context.Background(), db)
			if err != nil {
				t.Fatal(err)
			}
			return st
		},
	}
	for name, open := range stores {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s := NewService(open(t), time.Hour)
			tok, secret, err := s.Issue(ctx, IssueRequest{ClientID: "erp", Note: "n"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Verify(ctx, secret); err != nil {
				t.Fatalf("verify: %v", err)
			}
			// Use is recorded asynchronously by Verify.
			deadline := time.Now().Add(time.Second)
			for {
				got, _ := s.Get(ctx, tok.ID)
				if got.Uses == 1 && got.LastUsedAt != nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("use not tracked: %+v", got)
				}
				time.Sleep(5 * time.Millisecond)
			}
			_, _, _ = s.Issue(ctx, IssueRequest{ClientID: "wms"})
			if l, _ := s.List(ctx, "erp"); len(l) != 1 {
				t.Fatalf("list by client: %d", len(l))
			}
			if l, _ := s.List(ctx, ""); len(l) != 2 {
				t.Fatalf("list all: %d", len(l))
			}
			if _, err := s.Revoke(ctx, tok.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Verify(ctx, secret); err == nil {
				t.Fatal("revoked token still verifies")
			}
			if err := s.Delete(ctx, tok.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Get(ctx, tok.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("purged token still there: %v", err)
			}
		})
	}
}

// Agent credentials and integration tokens must outlive a server restart.
func TestSQLiteTokensSurviveRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cps.db")
	db := openTokenDB(t, path)
	st, _ := NewSQLiteStore(ctx, db)
	s := NewService(st, time.Hour)
	tok, secret, err := s.Issue(ctx, IssueRequest{ClientID: "agent:pc7", Kind: KindAgent, AgentID: "pc7"})
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	db = openTokenDB(t, path)
	defer db.Close()
	st, _ = NewSQLiteStore(ctx, db)
	s = NewService(st, time.Hour)
	got, err := s.Verify(ctx, secret)
	if err != nil || got.ID != tok.ID || got.AgentID != "pc7" {
		t.Fatalf("agent credential lost across restart: %+v %v", got, err)
	}
	next, _, _ := s.Issue(ctx, IssueRequest{ClientID: "erp"})
	if next.ID == tok.ID || next.ID[:6] != "tok_2_" {
		t.Fatalf("token ids must continue after restart: %q", next.ID)
	}
}

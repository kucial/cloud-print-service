package token

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func newSvc(t *testing.T) *Service {
	t.Helper()
	return NewService(NewMemoryStore(), 24*time.Hour)
}

func TestIssueAndVerify(t *testing.T) {
	ctx := context.Background()
	s := newSvc(t)

	tok, secret, err := s.Issue(ctx, IssueRequest{ClientID: "wms", Note: "pilot"})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if !strings.HasPrefix(secret, SecretPrefix) {
		t.Fatalf("secret must be prefixed, got %q", secret)
	}
	if tok.Prefix == "" || strings.Contains(Display(tok), secret) {
		t.Fatal("display must not leak the secret")
	}

	got, err := s.Verify(ctx, secret)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.ClientID != "wms" || got.Role != RoleClient || got.ID != tok.ID {
		t.Fatalf("wrong identity: %+v", got)
	}
}

func TestSecretsAreUnique(t *testing.T) {
	ctx := context.Background()
	s := newSvc(t)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		_, secret, err := s.Issue(ctx, IssueRequest{ClientID: "erp"})
		if err != nil {
			t.Fatal(err)
		}
		if seen[secret] {
			t.Fatal("issued a duplicate secret")
		}
		seen[secret] = true
	}
}

func TestOnlyHashIsStored(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore()
	s := NewService(st, time.Hour)
	_, secret, err := s.Issue(ctx, IssueRequest{ClientID: "erp"})
	if err != nil {
		t.Fatal(err)
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	for _, e := range st.tokens {
		if e.hash == secret || strings.Contains(e.hash, secret) {
			t.Fatalf("raw secret found in stored hash: %q", e.hash)
		}
		if len(e.hash) != 64 { // sha256 hex
			t.Fatalf("unexpected hash form: %q", e.hash)
		}
	}
}

func TestVerifyRejectsGarbage(t *testing.T) {
	ctx := context.Background()
	s := newSvc(t)
	for _, bad := range []string{"", "nope", SecretPrefix + "AAAAAAAAAAAAAAAA", strings.Repeat("x", 200)} {
		if _, err := s.Verify(ctx, bad); err == nil {
			t.Fatalf("expected failure for %q", bad)
		}
	}
}

func TestRevoke(t *testing.T) {
	ctx := context.Background()
	s := newSvc(t)
	tok, secret, _ := s.Issue(ctx, IssueRequest{ClientID: "erp"})
	if _, err := s.Verify(ctx, secret); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revoke(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(ctx, secret); !errors.Is(err, ErrRevoked) {
		t.Fatalf("expected ErrRevoked, got %v", err)
	}
	// A revoked token is still listed (audit trail) but inactive.
	list, _ := s.List(ctx, "")
	if len(list) != 1 || list[0].Active(time.Now()) {
		t.Fatalf("revoked token should be listed but inactive: %+v", list)
	}
}

func TestExpiry(t *testing.T) {
	ctx := context.Background()
	s := newSvc(t)
	tok, secret, err := s.Issue(ctx, IssueRequest{ClientID: "erp", TTL: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := s.Verify(ctx, secret); !errors.Is(err, ErrExpired) {
		t.Fatalf("expected ErrExpired, got %v", err)
	}
	if tok.Active(time.Now()) {
		t.Fatal("expired token must not be active")
	}
}

func TestRotateRevokesOldAndKeepsIdentity(t *testing.T) {
	ctx := context.Background()
	s := newSvc(t)
	old, oldSecret, _ := s.Issue(ctx, IssueRequest{ClientID: "wms", Role: RoleAdmin, Note: "keep me"})
	newTok, newSecret, err := s.Rotate(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(ctx, oldSecret); !errors.Is(err, ErrRevoked) {
		t.Fatalf("old secret must stop working: %v", err)
	}
	got, err := s.Verify(ctx, newSecret)
	if err != nil {
		t.Fatalf("new secret must work: %v", err)
	}
	if got.ClientID != "wms" || got.Role != RoleAdmin || got.Note != "keep me" {
		t.Fatalf("rotation must preserve identity: %+v", got)
	}
	if newTok.ID == old.ID {
		t.Fatal("rotation must mint a new id")
	}
}

func TestValidation(t *testing.T) {
	ctx := context.Background()
	s := newSvc(t)
	if _, _, err := s.Issue(ctx, IssueRequest{ClientID: ""}); err == nil {
		t.Error("client_id must be required")
	}
	if _, _, err := s.Issue(ctx, IssueRequest{ClientID: "erp", Role: "superuser"}); err == nil {
		t.Error("unknown role must be rejected")
	}
	if _, _, err := s.Issue(ctx, IssueRequest{ClientID: "erp", TTL: -time.Hour}); err == nil {
		t.Error("negative ttl must be rejected")
	}
}

func TestListFilterAndOrder(t *testing.T) {
	ctx := context.Background()
	s := newSvc(t)
	base := time.Now()
	for i, cid := range []string{"erp", "wms", "erp"} {
		s.store.(*MemoryStore).mu.Lock()
		seq := s.store.(*MemoryStore).seq
		_ = seq
		s.store.(*MemoryStore).mu.Unlock()
		_, _, err := s.Issue(ctx, IssueRequest{ClientID: cid})
		if err != nil {
			t.Fatal(err)
		}
		_ = i
		_ = base
	}
	all, _ := s.List(ctx, "")
	if len(all) != 3 {
		t.Fatalf("want 3, got %d", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i].CreatedAt.After(all[i-1].CreatedAt) {
			t.Fatal("list must be newest first")
		}
	}
	erp, _ := s.List(ctx, "erp")
	if len(erp) != 2 {
		t.Fatalf("client filter broken: %d", len(erp))
	}
}

func TestUseTracking(t *testing.T) {
	ctx := context.Background()
	s := newSvc(t)
	tok, secret, _ := s.Issue(ctx, IssueRequest{ClientID: "erp"})
	if _, err := s.Verify(ctx, secret); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	got, err := s.Get(ctx, tok.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastUsedAt == nil || got.Uses < 1 {
		t.Fatalf("usage must be tracked: %+v", got)
	}
}

func TestPurge(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore()
	s := NewService(st, time.Hour)
	tok, secret, _ := s.Issue(ctx, IssueRequest{ClientID: "erp"})
	if err := s.Delete(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(ctx, secret); !errors.Is(err, ErrNotFound) {
		t.Fatalf("purged token must be gone: %v", err)
	}
	if err := s.Delete(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// Package token issues, verifies and revokes API tokens for business systems.
//
// Secrets are shown exactly once, at creation. Only a SHA-256 hash is stored, so the
// database never holds a usable credential.
package token

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	// SecretPrefix makes tokens greppable in logs and paste-detectable in code review.
	SecretPrefix = "cps_live_"
	// prefixLen is how much of the secret is kept for human identification.
	prefixLen = 8

	RoleClient = "client"
	RoleAdmin  = "admin"

	// Token kinds. A credential's kind decides what it may do, independent of role:
	// an agent credential can report status but never dispatch a job or read business
	// data, so a compromised packing PC cannot become a printing station.
	KindBusiness = "business"
	KindAgent    = "agent"
	// KindAgentBootstrap is the one-time credential an operator hands to a packing PC.
	// It is exchanged for a durable agent credential on the agent's first registration and
	// revoked in the same breath, so a token pasted into an install script cannot be
	// replayed.
	KindAgentBootstrap = "agent-bootstrap"
	KindBootstrap      = "bootstrap"
)

var (
	ErrNotFound = errors.New("token not found")
	ErrRevoked  = errors.New("token revoked")
	ErrExpired  = errors.New("token expired")
)

// Token is the stored (non-secret) half of a credential.
type Token struct {
	ID         string     `json:"id"`
	ClientID   string     `json:"client_id"`
	Role       string     `json:"role"`
	Kind       string     `json:"kind"`
	AgentID    string     `json:"agent_id,omitempty"`
	Prefix     string     `json:"prefix"`
	Note       string     `json:"note,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	Uses       int        `json:"uses"`
}

// Active reports whether the token may still be used.
func (t *Token) Active(now time.Time) bool {
	if t.RevokedAt != nil {
		return false
	}
	if t.ExpiresAt != nil && now.After(*t.ExpiresAt) {
		return false
	}
	return true
}

func (t *Token) display() string { return SecretPrefix + t.Prefix + "…" }

// Store persists tokens. Memory is the MVP implementation; Postgres follows db/schema.sql.
type Store interface {
	Create(ctx context.Context, t *Token, hash string) error
	Get(ctx context.Context, id string) (*Token, error)
	GetByHash(ctx context.Context, hash string) (*Token, error)
	List(ctx context.Context, clientID string) ([]Token, error)
	Revoke(ctx context.Context, id string, at time.Time) error
	Touch(ctx context.Context, id string, at time.Time) error
	Delete(ctx context.Context, id string) error
}

type MemoryStore struct {
	mu     sync.RWMutex
	tokens map[string]*entry // id -> entry
	byHash map[string]string // hash -> id
	seq    int64
}

type entry struct {
	t    Token
	hash string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{tokens: map[string]*entry{}, byHash: map[string]string{}}
}

func (s *MemoryStore) Create(ctx context.Context, t *Token, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	if t.ID == "" {
		t.ID = fmt.Sprintf("tok_%d_%s", s.seq, t.Prefix)
	}
	if _, ok := s.byHash[hash]; ok {
		return errors.New("token already exists")
	}
	cp := *t
	s.tokens[t.ID] = &entry{t: cp, hash: hash}
	s.byHash[hash] = t.ID
	return nil
}

func (s *MemoryStore) Get(ctx context.Context, id string) (*Token, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.tokens[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := e.t
	return &cp, nil
}

func (s *MemoryStore) GetByHash(ctx context.Context, hash string) (*Token, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byHash[hash]
	if !ok {
		return nil, ErrNotFound
	}
	cp := s.tokens[id].t
	return &cp, nil
}

func (s *MemoryStore) List(ctx context.Context, clientID string) ([]Token, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Token{}
	for _, e := range s.tokens {
		if clientID != "" && e.t.ClientID != clientID {
			continue
		}
		out = append(out, e.t)
	}
	return out, nil
}

func (s *MemoryStore) Revoke(ctx context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.tokens[id]
	if !ok {
		return ErrNotFound
	}
	if e.t.RevokedAt == nil {
		e.t.RevokedAt = &at
	}
	return nil
}

func (s *MemoryStore) Touch(ctx context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.tokens[id]
	if !ok {
		return ErrNotFound
	}
	e.t.LastUsedAt = &at
	e.t.Uses++
	return nil
}

func (s *MemoryStore) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.tokens[id]
	if !ok {
		return ErrNotFound
	}
	delete(s.byHash, e.hash)
	delete(s.tokens, id)
	return nil
}

// Service issues and verifies tokens.
type Service struct {
	store      Store
	defaultTTL time.Duration
	now        func() time.Time
	rand       func(n int) ([]byte, error)
}

func NewService(store Store, defaultTTL time.Duration) *Service {
	if defaultTTL <= 0 {
		defaultTTL = 90 * 24 * time.Hour
	}
	return &Service{
		store:      store,
		defaultTTL: defaultTTL,
		now:        func() time.Time { return time.Now().UTC() },
		rand: func(n int) ([]byte, error) {
			b := make([]byte, n)
			if _, err := rand.Read(b); err != nil {
				return nil, err
			}
			return b, nil
		},
	}
}

func (s *Service) DefaultTTL() time.Duration { return s.defaultTTL }

// IssueRequest describes a credential to mint.
type IssueRequest struct {
	ClientID string
	Role     string
	TTL      time.Duration
	Note     string
	// Kind is business (default) or agent. Agents are issued by `cpsctl agent enroll`.
	Kind string
	// AgentID links an agent credential to its registry entry.
	AgentID string
}

// Issue mints a token and returns it with the plaintext secret (shown once).
func (s *Service) Issue(ctx context.Context, req IssueRequest) (*Token, string, error) {
	req.ClientID = strings.TrimSpace(req.ClientID)
	if req.ClientID == "" {
		return nil, "", errors.New("client_id is required")
	}
	if req.Role == "" {
		req.Role = RoleClient
	}
	if req.Role != RoleClient && req.Role != RoleAdmin {
		return nil, "", fmt.Errorf("role must be %s|%s", RoleClient, RoleAdmin)
	}
	if req.Kind == "" {
		req.Kind = KindBusiness
	}
	if req.Kind != KindBusiness && req.Kind != KindAgent && req.Kind != KindAgentBootstrap {
		return nil, "", fmt.Errorf("kind must be %s|%s|%s", KindBusiness, KindAgent, KindAgentBootstrap)
	}
	if req.Kind == KindAgent || req.Kind == KindAgentBootstrap {
		req.Role = RoleClient // an agent is never an admin
	}
	if req.TTL < 0 {
		return nil, "", errors.New("ttl must be positive (use a negative-free duration, or 0 for the default)")
	}
	if req.TTL == 0 {
		req.TTL = s.defaultTTL
	}
	raw, err := s.rand(24)
	if err != nil {
		return nil, "", fmt.Errorf("entropy failure: %w", err)
	}
	secret := SecretPrefix + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	sum := sha256.Sum256([]byte(secret))
	t := &Token{
		ClientID: req.ClientID,
		Role:     req.Role,
		Kind:     req.Kind,
		AgentID:  req.AgentID,
		// Prefix is the identifying head *after* the scheme, so listings read
		// "cps_live_5BKF7CKS…" instead of repeating the scheme.
		Prefix:    secret[len(SecretPrefix) : len(SecretPrefix)+prefixLen],
		Note:      req.Note,
		CreatedAt: s.now(),
	}
	if req.TTL > 0 {
		exp := s.now().Add(req.TTL)
		t.ExpiresAt = &exp
	}
	if err := s.store.Create(ctx, t, hex.EncodeToString(sum[:])); err != nil {
		return nil, "", err
	}
	return t, secret, nil
}

// Verify resolves a presented secret to its token, or an error explaining the refusal.
func (s *Service) Verify(ctx context.Context, secret string) (*Token, error) {
	if secret == "" {
		return nil, ErrNotFound
	}
	sum := sha256.Sum256([]byte(secret))
	t, err := s.store.GetByHash(ctx, hex.EncodeToString(sum[:]))
	if err != nil {
		return nil, ErrNotFound
	}
	now := s.now()
	if t.RevokedAt != nil {
		return t, ErrRevoked
	}
	if t.ExpiresAt != nil && now.After(*t.ExpiresAt) {
		return t, ErrExpired
	}
	// Best-effort usage tracking; never fail a request because of it.
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.store.Touch(bg, t.ID, now)
	}()
	return t, nil
}

func (s *Service) Get(ctx context.Context, id string) (*Token, error) {
	t, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if t.ID == "" {
		return nil, ErrNotFound
	}
	return t, nil
}

func (s *Service) List(ctx context.Context, clientID string) ([]Token, error) {
	out, err := s.store.List(ctx, clientID)
	if err != nil {
		return nil, err
	}
	// Most recent first, so the newest credential is at the top.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].CreatedAt.After(out[j-1].CreatedAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

// Revoke permanently disables a token (the secret keeps existing but stops working).
func (s *Service) Revoke(ctx context.Context, id string) (*Token, error) {
	if err := s.store.Revoke(ctx, id, s.now()); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// Rotate revokes a token and mints a replacement with the same identity.
func (s *Service) Rotate(ctx context.Context, id string) (*Token, string, error) {
	old, err := s.Get(ctx, id)
	if err != nil {
		return nil, "", err
	}
	ttl := s.defaultTTL
	if old.ExpiresAt != nil {
		if d := time.Until(*old.ExpiresAt); d > 0 {
			ttl = d
		}
	}
	if _, err := s.Revoke(ctx, id); err != nil {
		return nil, "", err
	}
	return s.Issue(ctx, IssueRequest{ClientID: old.ClientID, Role: old.Role, TTL: ttl,
		Note: old.Note, Kind: old.Kind, AgentID: old.AgentID})
}

// Delete removes a token entirely (used for tokens that were issued and never used).
func (s *Service) Delete(ctx context.Context, id string) error {
	return s.store.Delete(ctx, id)
}

// Display renders a token for logs without leaking the secret.
func Display(t *Token) string { return t.display() }

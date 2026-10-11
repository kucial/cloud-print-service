// Package job implements dispatch: validation, idempotency, per-printer FIFO, retry.
package job

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/kongkx/cloud-print-service/internal/domain"
)

var (
	// ErrNotFound is returned for unknown job ids, including another client's jobs
	// (we answer 404 rather than 403 so we never leak existence across clients).
	ErrNotFound = errors.New("job not found")
	// ErrConflict is returned when a replayed job carries a different payload.
	ErrConflict = errors.New("idempotency conflict")
)

// Filter is the query shape for List.
type Filter struct {
	ClientID   string
	PrinterID  string
	State      string
	ExternalID string
	From       *time.Time
	To         *time.Time
	Limit      int
}

// Store is the persistence boundary. Memory is the MVP implementation; the Postgres
// implementation (schema in db/schema.sql) is a drop-in for production.
type Store interface {
	CreateJob(ctx context.Context, j *domain.Job) error
	GetJob(ctx context.Context, id string) (*domain.Job, error)
	GetByExternal(ctx context.Context, clientID, externalID string) (*domain.Job, error)
	UpdateJob(ctx context.Context, j *domain.Job) error
	ListJobs(ctx context.Context, f Filter) ([]domain.Job, error)
	AddAttempt(ctx context.Context, a *domain.Attempt) error
	Attempts(ctx context.Context, jobID string) ([]domain.Attempt, error)
	// RequeueUnfinished is called at boot: anything left `sending` cannot be proven
	// unwritten, so it becomes `sent_unknown` rather than being printed again.
	RequeueUnfinished(ctx context.Context) (int, error)
	Ping(ctx context.Context) error
	Close() error
}

// MemoryStore is a process-local store: correct for MVP/demo, restart loses history.
type MemoryStore struct {
	mu       sync.RWMutex
	jobs     map[string]*domain.Job
	byExt    map[string]string // "clientID\x00externalID" -> job id
	attempts map[string][]domain.Attempt
	seq      int64
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		jobs:     map[string]*domain.Job{},
		byExt:    map[string]string{},
		attempts: map[string][]domain.Attempt{},
	}
}

func extKey(clientID, externalID string) string { return clientID + "\x00" + externalID }

func (s *MemoryStore) CreateJob(ctx context.Context, j *domain.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j.ID == "" {
		s.seq++
		j.ID = fmt.Sprintf("job_%d", s.seq)
	}
	if _, ok := s.jobs[j.ID]; ok {
		return fmt.Errorf("job %s already exists", j.ID)
	}
	if j.ExternalID != "" {
		k := extKey(j.ClientID, j.ExternalID)
		if _, ok := s.byExt[k]; ok {
			return fmt.Errorf("external_id %q already used by this client", j.ExternalID)
		}
		s.byExt[k] = j.ID
	}
	cp := *j
	s.jobs[j.ID] = &cp
	return nil
}

func (s *MemoryStore) GetJob(ctx context.Context, id string) (*domain.Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *j
	return &cp, nil
}

func (s *MemoryStore) GetByExternal(ctx context.Context, clientID, externalID string) (*domain.Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byExt[extKey(clientID, externalID)]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *s.jobs[id]
	return &cp, nil
}

func (s *MemoryStore) UpdateJob(ctx context.Context, j *domain.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[j.ID]; !ok {
		return ErrNotFound
	}
	cp := *j
	s.jobs[j.ID] = &cp
	return nil
}

func (s *MemoryStore) ListJobs(ctx context.Context, f Filter) ([]domain.Job, error) {
	s.mu.RLock()
	out := []domain.Job{}
	for _, j := range s.jobs {
		if f.ClientID != "" && j.ClientID != f.ClientID {
			continue
		}
		if f.PrinterID != "" && j.PrinterID != f.PrinterID {
			continue
		}
		if f.State != "" && string(j.State) != f.State {
			continue
		}
		if f.ExternalID != "" && j.ExternalID != f.ExternalID {
			continue
		}
		if f.From != nil && j.QueuedAt.Before(*f.From) {
			continue
		}
		if f.To != nil && j.QueuedAt.After(*f.To) {
			continue
		}
		out = append(out, *j)
	}
	s.mu.RUnlock()

	sort.Slice(out, func(a, b int) bool { return out[a].QueuedAt.After(out[b].QueuedAt) })
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

func (s *MemoryStore) AddAttempt(ctx context.Context, a *domain.Attempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	a.ID = s.seq
	cp := *a
	s.attempts[a.JobID] = append(s.attempts[a.JobID], cp)
	return nil
}

func (s *MemoryStore) Attempts(ctx context.Context, jobID string) ([]domain.Attempt, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]domain.Attempt(nil), s.attempts[jobID]...), nil
}

func (s *MemoryStore) RequeueUnfinished(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	now := time.Now().UTC()
	for _, j := range s.jobs {
		if j.State != domain.JobSending {
			continue
		}
		j.State = domain.JobSentUnknown
		j.LastError = "cps restarted while the job was in flight; printed state unknown"
		j.FinishedAt = &now
		n++
	}
	return n, nil
}

func (s *MemoryStore) Ping(ctx context.Context) error { return nil }

func (s *MemoryStore) Close() error { return nil }

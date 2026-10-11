package httpapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kongkx/cloud-print-service/internal/metrics"
	"github.com/kongkx/cloud-print-service/internal/token"
)

type ctxKey int

const (
	ctxClient ctxKey = iota
)

// Client is the authenticated caller identity.
type Client struct {
	ID      string
	Role    string // client | admin
	TokenID string // set when authenticated with an issued token ("" for bootstrap keys)
}

func (c Client) IsAdmin() bool { return c.Role == "admin" }

type keyEntry struct {
	token  string
	client Client
}

// Auth authenticates bearer tokens from two sources, checked in order:
//
//  1. bootstrap keys from config — break-glass access, works even with an empty store
//  2. issued tokens from the token service — what business systems actually use
//
// Only a SHA-256 hash of an issued token is stored, so the database never holds a
// usable credential.
type Auth struct {
	keys   []keyEntry
	tokens *token.Service
	rl     *rateLimiter
	met    *metrics.Registry
}

// KeySpec is one bootstrap API key identity.
type KeySpec struct {
	ClientID string
	Role     string // client | admin
}

func NewAuth(keys map[string]KeySpec, tokens *token.Service, rps float64, burst int, met *metrics.Registry) *Auth {
	a := &Auth{met: met, tokens: tokens}
	for secret, k := range keys {
		a.keys = append(a.keys, keyEntry{token: secret, client: Client{ID: k.ClientID, Role: k.Role}})
	}
	if rps > 0 {
		a.rl = newRateLimiter(rps, burst)
	}
	return a
}

// authenticate resolves a client from the Authorization header.
func (a *Auth) authenticate(r *http.Request) (Client, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return Client{}, false
	}
	secret := strings.TrimSpace(h[len(prefix):])

	// 1. Bootstrap keys: constant-time comparison against every configured key.
	found := Client{}
	ok := false
	for _, k := range a.keys {
		if subtle.ConstantTimeCompare([]byte(secret), []byte(k.token)) == 1 {
			found = k.client
			ok = true
		}
	}
	if ok {
		return found, true
	}

	// 2. Issued tokens.
	if a.tokens != nil {
		t, err := a.tokens.Verify(r.Context(), secret)
		if err == nil {
			return Client{ID: t.ClientID, Role: t.Role, TokenID: t.ID}, true
		}
		// Metrics distinguish revoked/expired/unknown; the HTTP response does not,
		// so probing cannot tell a dead token from a nonexistent one.
		a.met.Inc("cps_auth_failures_total", "reason", tokenErrReason(err))
	}
	return Client{}, false
}

func tokenErrReason(err error) string {
	switch {
	case errors.Is(err, token.ErrRevoked):
		return "revoked"
	case errors.Is(err, token.ErrExpired):
		return "expired"
	default:
		return "unknown"
	}
}

// Middleware returns a handler chain: rate limit → auth → metrics.
// It takes the mux so it can resolve the matched route pattern for a low-cardinality
// metric label (r.Pattern is empty before the mux runs).
func (a *Auth) Middleware(mux *http.ServeMux) http.Handler {
	guarded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client, ok := a.authenticate(r)
		if !ok {
			a.met.Inc("cps_http_requests_total", "route", routeLabel(mux, r), "status", "401")
			writeErr(w, http.StatusUnauthorized, "unauthorized", "a valid bearer API key is required")
			return
		}
		if a.rl != nil && !a.rl.allow(client.ID) {
			a.met.Inc("cps_http_requests_total", "route", routeLabel(mux, r), "status", "429")
			w.Header().Set("Retry-After", "1")
			writeErr(w, http.StatusTooManyRequests, "rate_limited", "too many requests for client "+client.ID)
			return
		}
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxClient, client)))
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		start := time.Now()
		guarded.ServeHTTP(rec, r)
		a.met.Inc("cps_http_requests_total", "route", routeLabel(mux, r), "status", itoa(rec.status))
		a.met.Observe("cps_http_request_seconds", time.Since(start).Seconds(), "route", routeLabel(mux, r))
	})
}

// RequireAdmin wraps admin-only handlers.
func RequireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !ClientFrom(r.Context()).IsAdmin() {
			writeErr(w, http.StatusForbidden, "forbidden", "this endpoint requires an admin API key")
			return
		}
		next(w, r)
	}
}

func ClientFrom(ctx context.Context) Client {
	c, _ := ctx.Value(ctxClient).(Client)
	return c
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Flush lets SSE work through the wrapping ResponseWriter.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// routeLabel returns a low-cardinality route label for metrics.
func routeLabel(mux *http.ServeMux, r *http.Request) string {
	if r.Pattern != "" { // already matched
		return r.Pattern
	}
	if _, pattern := mux.Handler(r); pattern != "" {
		return pattern
	}
	return "unmatched"
}

// rateLimiter is a per-client token bucket.
type rateLimiter struct {
	mu      sync.Mutex
	rps     float64
	burst   float64
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(rps float64, burst int) *rateLimiter {
	if burst <= 0 {
		burst = int(rps) * 3
	}
	return &rateLimiter{rps: rps, burst: float64(burst), buckets: map[string]*bucket{}}
}

func (l *rateLimiter) allow(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.buckets[id]
	if !ok {
		l.buckets[id] = &bucket{tokens: l.burst - 1, last: now}
		return true
	}
	b.tokens += now.Sub(b.last).Seconds() * l.rps
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

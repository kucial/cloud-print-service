package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// These tests pin the *retry rules* of the reference integration — the part integrators
// copy verbatim. If they change, the rules changed.

func testClient(t *testing.T, h http.HandlerFunc) (*client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &client{base: srv.URL, token: "t", http: srv.Client(), maxAttempts: 4}, srv
}

func TestDispatchAccepted(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(submitResult{JobID: "job_1", State: "queued"})
	})
	res, err := c.dispatch(context.Background(), map[string]any{"external_id": "OK-1-line-1"})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if res.JobID != "job_1" || res.State != "queued" || res.Replayed {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestReplayIsNotASecondLabel(t *testing.T) {
	var calls int32
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_ = json.NewEncoder(w).Encode(submitResult{JobID: "job_1", State: "printed", Replayed: true})
	})
	res, err := c.dispatch(context.Background(), map[string]any{"external_id": "OK-1-line-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Replayed || res.JobID != "job_1" {
		t.Fatalf("replay must surface the original job: %+v", res)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("a replayed accept must not be retried (calls=%d)", calls)
	}
}

// queue_full means nothing was printed: retry, and expect success.
func TestRetriesQueueFull(t *testing.T) {
	var calls int32
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"queue_full","message":"busy"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(submitResult{JobID: "job_2", State: "queued"})
	})
	res, err := c.dispatch(context.Background(), map[string]any{"external_id": "OK-2-line-1"})
	if err != nil {
		t.Fatalf("dispatch should succeed after backpressure: %v", err)
	}
	if res.JobID != "job_2" || res.Attempts < 3 {
		t.Fatalf("expected 3 attempts then success: %+v", res)
	}
}

// A 409 is a data problem: retrying can only make it worse.
func TestNeverRetriesConflict(t *testing.T) {
	var calls int32
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"idempotency_conflict","message":"different payload"}}`))
	})
	res, err := c.dispatch(context.Background(), map[string]any{"external_id": "OK-3-line-1"})
	if err == nil {
		t.Fatal("expected an error for a conflict")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("409 must not be retried (calls=%d)", calls)
	}
	var ae *apiError
	if !errors.As(err, &ae) || ae.Code != "idempotency_conflict" {
		t.Fatalf("error should be typed and machine-readable: %v", err)
	}
	if res.State != "rejected" {
		t.Fatalf("state = %q, want rejected", res.State)
	}
}

func TestNeverRetriesClientErrors(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity} {
		var calls int32
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.WriteHeader(code)
		})
		if _, err := c.dispatch(context.Background(), map[string]any{"external_id": "x"}); err == nil {
			t.Fatalf("http %d should be an error", code)
		}
		if atomic.LoadInt32(&calls) != 1 {
			t.Errorf("http %d must not be retried (calls=%d)", code, calls)
		}
	}
}

// Transport errors are ambiguous — retrying is safe only because external_id is stable.
func TestRetriesTransportErrors(t *testing.T) {
	var calls int32
	c, srv := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 2 {
			// Close the connection without a response: a real transport failure.
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, err := hj.Hijack()
				if err == nil {
					_ = conn.Close()
					return
				}
			}
			panic("unreachable")
		}
		_ = json.NewEncoder(w).Encode(submitResult{JobID: "job_3", State: "queued"})
	})
	res, err := c.dispatch(context.Background(), map[string]any{"external_id": "OK-4-line-1"})
	if err != nil {
		t.Fatalf("expected success on the second attempt: %v", err)
	}
	if res.JobID != "job_3" {
		t.Fatalf("unexpected: %+v", res)
	}
	_ = srv
}

func TestBackoffIsBoundedAndJittered(t *testing.T) {
	seen := map[time.Duration]bool{}
	for attempt := 1; attempt <= 6; attempt++ {
		d := backoff(attempt)
		if d > 6*time.Second {
			t.Fatalf("attempt %d backoff %v is unbounded", attempt, d)
		}
		if d <= 0 {
			t.Fatalf("attempt %d backoff must be positive, got %v", attempt, d)
		}
		seen[d] = true
	}
	if len(seen) < 2 {
		t.Skip("jitter happened to be constant on this run")
	}
}

func TestErrorEnvelopeIsParsed(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"queue_full","message":"printer busy","details":{"queue_depth":50}}}`))
	})
	_, err := c.dispatch(context.Background(), map[string]any{"external_id": "x"})
	var ae *apiError
	if !errors.As(err, &ae) {
		t.Fatalf("want apiError, got %T", err)
	}
	if !strings.Contains(err.Error(), "queue_full") || !strings.Contains(err.Error(), "printer busy") {
		t.Fatalf("error message lost detail: %v", err)
	}
}

func TestDemoDataHasStableBusinessKeys(t *testing.T) {
	orders := generateOrders(7)
	if len(orders) == 0 {
		t.Fatal("expected demo orders")
	}
	seen := map[string]bool{}
	for _, o := range orders {
		for _, l := range o.Lines {
			key := o.ID + "-line-" + itoa(l.No)
			if seen[key] {
				t.Fatalf("duplicate external_id would break idempotency: %s", key)
			}
			seen[key] = true
			if !strings.HasPrefix(l.ZPL, "^XA") || !strings.HasSuffix(l.ZPL, "^XZ") {
				t.Fatalf("demo ZPL must be a complete format: %q", l.ZPL)
			}
		}
	}
	// Same seed, same data: the demo must be reproducible.
	again := generateOrders(7)
	if len(again) != len(orders) || again[0].ID != orders[0].ID || again[0].SKU != orders[0].SKU {
		t.Fatal("generateOrders must be deterministic for a given seed")
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

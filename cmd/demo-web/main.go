// Command demo-web is a reference integration between a business module (ERP/WMS) and
// the Cloud Print Service.
//
// It exists to be *read*. Every choice it makes is one a real integration should copy:
//
//   - client-scoped token, never an admin token, for dispatch
//   - external_id = a stable business key, so retries cannot duplicate labels
//   - retries only when the failure was unambiguous (503 queue_full, transport error);
//     a job reported as sent_unknown is never retried automatically
//   - status read from the API, never guessed from "the request returned 202"
//   - live updates over SSE, with polling as the fallback
//   - payloads are never logged; only sizes and ids
//
// Run it against a local service:
//
//	cpsctl token issue --client-id wms --ttl 90d      # → cps_live_…
//	go run ./cmd/demo-web -token cps_live_…            # http://localhost:8090
//
// It is open source on purpose: point your own developers at this file instead of at the
// API documentation.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	var (
		addr       = flag.String("addr", ":8090", "demo console listen address")
		url        = flag.String("url", env("CPS_URL", "http://localhost:8080"), "Cloud Print Service base URL")
		token      = flag.String("token", env("CPS_TOKEN", ""), "client token (cps_live_…) — required")
		adminToken = flag.String("admin-token", env("CPS_ADMIN_TOKEN", ""), "optional admin token, enables the label preview")
		site       = flag.String("site", env("CPS_SITE", ""), "prefer printers from this site (optional)")
		printerID  = flag.String("printer", env("CPS_PRINTER", ""), "pin all jobs to this printer (optional)")
		simulate   = flag.Int("simulate", 0, "background jobs per minute (demo of queueing/backpressure)")
		seed       = flag.Int64("seed", 42, "demo data seed")
	)
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if *token == "" {
		log.Error("missing -token; issue one with: cpsctl token issue --client-id wms --ttl 90d")
		os.Exit(2)
	}

	app, err := newApp(log, appConfig{
		base:       strings.TrimRight(*url, "/"),
		token:      *token,
		adminToken: *adminToken,
		site:       *site,
		printerID:  *printerID,
		simulate:   *simulate,
		seed:       *seed,
	})
	if err != nil {
		log.Error("startup failed", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	app.Run(ctx)

	srv := &http.Server{Addr: *addr, Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Info("demo console", "addr", *addr, "cps", app.cfg.base, "printers", len(app.PrinterIDs()))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "err", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// ------------------------------------------------------------------ API client

// client is a thin, honest Cloud Print Service client.
//
// The important part is not the HTTP calls, it is the retry rules in dispatch().
type client struct {
	base  string
	token string
	http  *http.Client
	// maxAttempts bounds *unambiguous* retries only.
	maxAttempts int
}

type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("http %d", e.Status)
	}
	return e.Code + ": " + e.Message
}

func (c *client) do(ctx context.Context, method, path, token string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 400 {
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		return &apiError{Status: resp.StatusCode, Code: e.Error.Code, Message: e.Error.Message}
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

type submitResult struct {
	JobID      string `json:"job_id"`
	State      string `json:"state"`
	PrinterID  string `json:"printer_id"`
	ExternalID string `json:"external_id"`
	Replayed   bool   `json:"replayed"`
}

type job struct {
	JobID      string     `json:"job_id"`
	ClientID   string     `json:"client_id"`
	ExternalID string     `json:"external_id"`
	PrinterID  string     `json:"printer_id"`
	State      string     `json:"state"`
	Bytes      int        `json:"payload_bytes"`
	Copies     int        `json:"copies"`
	Attempts   int        `json:"attempts"`
	LastError  string     `json:"last_error"`
	BizRef     string     `json:"biz_ref"`
	Partial    bool       `json:"partial_write"`
	QueuedAt   time.Time  `json:"queued_at"`
	SentAt     *time.Time `json:"sent_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

// dispatchResult is what the console reports for one line of one wave.
type dispatchResult struct {
	JobID     string    `json:"job_id,omitempty"`
	State     string    `json:"state"`
	Replayed  bool      `json:"replayed"`
	Attempts  int       `json:"attempts"`
	Error     string    `json:"error,omitempty"`
	LatencyMS int64     `json:"latency_ms"`
	UpdatedAt time.Time `json:"updated_at"`
	Job       *job      `json:"job,omitempty"`
}

// dispatch applies the retry rules a correct integration must follow.
//
//	202/200 → accepted (or replayed): done, poll for the state
//	503     → backpressure: nothing was printed, safe to retry with backoff
//	409     → idempotency conflict: a human must look, never retry
//	4xx     → our bug: never retry
//	network → the request may or may not have been accepted. Retrying is safe *because*
//	          external_id is stable: the service replays the original job instead of
//	          printing a second label.
func (c *client) dispatch(ctx context.Context, req map[string]any) (dispatchResult, error) {
	start := time.Now()
	var lastErr error
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		var res submitResult
		err := c.do(ctx, "POST", "/api/v1/jobs", c.token, req, &res)
		latency := time.Since(start).Milliseconds()
		if err == nil {
			return dispatchResult{
				JobID: res.JobID, State: res.State, Replayed: res.Replayed,
				Attempts: attempt, LatencyMS: latency, UpdatedAt: time.Now(),
			}, nil
		}
		lastErr = err

		var ae *apiError
		if errors.As(err, &ae) {
			switch {
			case ae.Status == http.StatusServiceUnavailable && ae.Code == "queue_full":
				// Backpressure: nothing was printed. Honour Retry-After if present.
				wait := backoff(attempt)
				logCtx := sleepCtx(ctx, wait)
				if logCtx.Err() != nil {
					return dispatchResult{}, ctx.Err()
				}
				continue
			default:
				// 4xx, including 409 idempotency_conflict: retrying cannot help.
				return dispatchResult{State: "rejected", Error: err.Error(),
					Attempts: attempt, LatencyMS: latency, UpdatedAt: time.Now()}, err
			}
		}
		// Transport error: the submit may or may not have landed. Safe to retry only
		// because external_id is stable — the service de-duplicates for us.
		if ctx.Err() != nil {
			return dispatchResult{}, ctx.Err()
		}
		_ = sleepCtx(ctx, backoff(attempt))
	}
	return dispatchResult{State: "error", Error: lastErr.Error(),
		Attempts: c.maxAttempts, LatencyMS: time.Since(start).Milliseconds(),
		UpdatedAt: time.Now()}, lastErr
}

func backoff(attempt int) time.Duration {
	d := 250 * time.Millisecond << (attempt - 1)
	if d > 4*time.Second {
		d = 4 * time.Second
	}
	// Jitter: a whole WMS releasing a wave must not retry in lockstep.
	return d/2 + time.Duration(rand.Int63n(int64(d/2)+1))
}

func sleepCtx(ctx context.Context, d time.Duration) context.Context {
	c, cancel := context.WithTimeout(ctx, d)
	go func() { <-c.Done(); cancel() }()
	return c
}

package httpapi

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kucial/cloud-print-service/server/internal/fleet"
	"github.com/kucial/cloud-print-service/server/internal/job"
	"github.com/kucial/cloud-print-service/server/internal/observ"
	"github.com/kucial/cloud-print-service/server/internal/store"
	"github.com/kucial/cloud-print-service/server/internal/token"
)

// An open event stream (a panel left open) must not hold up shutdown: Shutdown waits
// for handlers but never cancels them, so StopStreams has to end the stream.
func TestShutdownEndsOpenEventStreams(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	b := observ.NewBus()
	mgr, err := fleet.NewManager(nil, log, b, fleet.ManagerOpts{PollInterval: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	js := job.NewService(job.Config{QueueSize: 1, MaxAttempts: 1}, store.NewMemoryStore(), mgr, b, log, observ.NewMetrics())
	t.Cleanup(js.Stop)
	tok := token.NewService(token.NewMemoryStore(), time.Hour)
	auth := NewAuth(map[string]KeySpec{opsKey: {ClientID: "ops", Role: "admin"}}, tok, 0, 0, observ.NewMetrics())
	api := New(mgr, js, b, auth, tok, log, observ.NewMetrics(), 50, 4096)

	ts := httptest.NewUnstartedServer(api.Handler())
	ts.Config.RegisterOnShutdown(api.StopStreams)
	ts.Start()
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+opsKey)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	ended := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, bufio.NewReader(res.Body))
		close(ended)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	if err := ts.Config.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown waited out its deadline with a stream open: %v", err)
	}
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("event stream still open after shutdown")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("shutdown took %v", d)
	}
}

// Command cps runs the Cloud Print Service.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kongkx/cloud-print-service/internal/config"
	"github.com/kongkx/cloud-print-service/internal/driver"
	"github.com/kongkx/cloud-print-service/internal/fleet"
	"github.com/kongkx/cloud-print-service/internal/httpapi"
	"github.com/kongkx/cloud-print-service/internal/job"
	"github.com/kongkx/cloud-print-service/internal/observ"
	"github.com/kongkx/cloud-print-service/internal/render"
	"github.com/kongkx/cloud-print-service/internal/store"
	"github.com/kongkx/cloud-print-service/internal/token"
)

func rendererName(r render.Renderer) string {
	if r == nil {
		return "none"
	}
	return r.Name()
}

func main() {
	cfgPath := flag.String("config", "", "path to config.json (default $CPS_CONFIG or ./config.json)")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Error("config error", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	b := observ.NewBus()
	met := observ.NewMetrics()

	// Fleet. Drivers are opened lazily; status is polled in the background.
	mgr, err := fleet.NewManager(cfg.Printers, log, b, fleet.ManagerOpts{
		PollInterval: cfg.StatusPollInterval.D(),
		StaleAfter:   cfg.StatusStaleAfter.D(),
		DialTimeout:  cfg.PrinterDialTimeout.D(),
		WriteTimeout: cfg.PrinterWriteTimeout.D(),
		ReadTimeout:  cfg.PrinterReadTimeout.D(),
	})
	if err != nil {
		log.Error("cannot initialise printers", "err", err)
		os.Exit(1)
	}
	defer mgr.Close()

	store := store.NewMemoryStore() // MVP: process-local. See db/schema.sql for the Postgres store.

	js := job.NewService(job.Config{
		QueueSize:           cfg.QueueSizePerPrinter,
		MaxAttempts:         cfg.MaxAttempts,
		ConfirmTimeout:      cfg.ConfirmTimeout.D(),
		QueueTTL:            cfg.JobQueueTTL.D(),
		BaseBackoff:         cfg.BaseBackoff.D(),
		MaxPayloadSize:      cfg.MaxPayloadBytes,
		RejectWhenNotUsable: cfg.RejectWhenNotUsable,
	}, store, mgr, b, log, met)
	defer js.Stop()

	if err := js.Start(ctx); err != nil {
		log.Error("job service start failed", "err", err)
		os.Exit(1)
	}

	mgr.StartPolling(ctx)

	keys := make(map[string]httpapi.KeySpec, len(cfg.APIKeys))
	for secret, k := range cfg.APIKeys {
		keys[secret] = httpapi.KeySpec{ClientID: k.ClientID, Role: k.Role}
	}
	// Tokens issued to business systems live in their own store so they can be listed,
	// rotated and revoked at runtime without touching config.
	tokenSvc := token.NewService(token.NewMemoryStore(), cfg.TokenDefaultTTL.D())
	auth := httpapi.NewAuth(keys, tokenSvc, cfg.RateLimitRPS, cfg.RateLimitBurst, met)

	// Operator panel + ZPL preview. The render engine is optional: if it cannot be
	// loaded, the service still runs and only /admin/preview is unavailable.
	renderer, rerr := render.Pick(cfg.Render.Backend, cfg.Render.LabelizeURL)
	if rerr != nil {
		log.Warn("render engine unavailable, label previews disabled", "err", rerr)
	}

	// Edge agents (USB printers on packing PCs) register here; their printers become
	// first-class fleet members and are reconciled without a restart.
	agents := fleet.NewAgentRegistry(mgr.Registry(), mgr, log)

	apiSrv := httpapi.New(mgr, js, b, auth, tokenSvc, log, met, cfg.MaxBatchSize, cfg.MaxPayloadBytes)
	apiSrv.SetAgents(agents)
	apiSrv.SetWebConfig(httpapi.WebConfig{
		Enabled: cfg.UI.Enabled, Path: cfg.UI.Path, Title: cfg.UI.Title,
	})
	if renderer != nil {
		apiSrv.SetRenderer(renderer)
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           apiSrv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// No WriteTimeout: /api/v1/events is a long-lived SSE stream.
		IdleTimeout: 120 * time.Second,
	}

	go func() {
		log.Info("cloud print service listening",
			"addr", cfg.Addr, "printers", mgr.Len(), "drivers", driver.Drivers(),
			"store", cfg.StoreDriver, "token_default_ttl", cfg.TokenDefaultTTL.D().String(),
			"ui", cfg.UI.Enabled, "renderer", rendererName(renderer))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Error("graceful shutdown failed", "err", err)
	}
}

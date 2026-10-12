// Command cps runs the Cloud Print Service.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kucial/cloud-print-service/pkg/render"
	"github.com/kucial/cloud-print-service/server/internal/config"
	"github.com/kucial/cloud-print-service/server/internal/discovery"
	"github.com/kucial/cloud-print-service/server/internal/driver"
	"github.com/kucial/cloud-print-service/server/internal/fleet"
	"github.com/kucial/cloud-print-service/server/internal/httpapi"
	"github.com/kucial/cloud-print-service/server/internal/job"
	"github.com/kucial/cloud-print-service/server/internal/observ"
	"github.com/kucial/cloud-print-service/server/internal/store"
	"github.com/kucial/cloud-print-service/server/internal/token"
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

	// Persistence first: the fleet restores operator edits from it before any printer
	// is claimed.
	st, tokenStore, err := openStores(ctx, cfg)
	if err != nil {
		log.Error("cannot open the data store", "driver", cfg.StoreDriver, "path", cfg.DataPath, "err", err)
		os.Exit(1)
	}
	defer st.Close()

	// Fleet. Drivers are opened lazily; status is polled in the background.
	reg := fleet.NewRegistry()
	if err := restoreOverrides(ctx, reg, st, log); err != nil {
		log.Error("cannot restore printer edits", "err", err)
		os.Exit(1)
	}
	if err := reg.UpsertMany(fleet.SourceConfig, "", cfg.Printers); err != nil {
		log.Error("cannot initialise printers", "err", err)
		os.Exit(1)
	}
	mgr, err := fleet.NewManagerWithRegistry(reg, log, b, fleet.ManagerOpts{
		PollInterval: cfg.StatusPollInterval.D(),
		StaleAfter:   cfg.StatusStaleAfter.D(),
		DialTimeout:  cfg.PrinterDialTimeout.D(),
		WriteTimeout: cfg.PrinterWriteTimeout.D(),
		ReadTimeout:  cfg.PrinterReadTimeout.D(),
	})
	if err == nil {
		err = mgr.Sync(ctx)
	}
	if err != nil {
		log.Error("cannot initialise printers", "err", err)
		os.Exit(1)
	}
	defer mgr.Close()

	js := job.NewService(job.Config{
		QueueSize:           cfg.QueueSizePerPrinter,
		MaxAttempts:         cfg.MaxAttempts,
		ConfirmTimeout:      cfg.ConfirmTimeout.D(),
		QueueTTL:            cfg.JobQueueTTL.D(),
		BaseBackoff:         cfg.BaseBackoff.D(),
		MaxPayloadSize:      cfg.MaxPayloadBytes,
		MaxCopies:           cfg.MaxCopies,
		RejectWhenNotUsable: cfg.RejectWhenNotUsable,
	}, st, mgr, b, log, met)
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
	tokenSvc := token.NewService(tokenStore, cfg.TokenDefaultTTL.D())
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

	// Discovery finds printers on the LAN. It only produces *candidates*: adoption is a
	// human decision, because a warehouse LAN contains things that are not printers.
	var disco *discovery.Service
	if cfg.Discovery.Enabled {
		verify := true
		if cfg.Discovery.Verify != nil {
			verify = *cfg.Discovery.Verify
		}
		dcfg := discovery.Config{
			Types: cfg.Discovery.Types, Interval: cfg.Discovery.Interval.D(),
			CandidateTTL: cfg.Discovery.CandidateTTL.D(), Site: cfg.Discovery.Site,
			Verify: verify, BrowseTimeout: cfg.Discovery.BrowseTimeout.D(),
		}
		disco = discovery.NewService(dcfg, mgr.Registry(), log).WithProbes(discovery.MDNS{
			Types: cfg.Discovery.Types, Identify: verify,
			BrowseTimeout:   cfg.Discovery.BrowseTimeout.D(),
			IdentifyTimeout: cfg.Discovery.IdentifyTimeout.D(),
			Logf:            func(f string, a ...any) { log.Debug(f, a...) },
		})
		go disco.Run(ctx)
		log.Info("discovery enabled", "types", cfg.Discovery.Types, "interval", cfg.Discovery.Interval.D())
	}

	apiSrv := httpapi.New(mgr, js, b, auth, tokenSvc, log, met, cfg.MaxBatchSize, cfg.MaxPayloadBytes)
	apiSrv.SetAgents(agents)
	apiSrv.SetDiscovery(disco)
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
			"store", cfg.StoreDriver, "data_path", dataPathFor(cfg), "token_default_ttl", cfg.TokenDefaultTTL.D().String(),
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

// openStores opens the job store and the token store for the configured driver. With
// sqlite both live in one file, so a restart keeps jobs, issued tokens and agent
// credentials.
func openStores(ctx context.Context, cfg config.Config) (store.Store, token.Store, error) {
	if cfg.StoreDriver == "memory" {
		return store.NewMemoryStore(), token.NewMemoryStore(), nil
	}
	sq, err := store.OpenSQLite(ctx, cfg.DataPath)
	if err != nil {
		return nil, nil, err
	}
	ts, err := token.NewSQLiteStore(ctx, sq.DB())
	if err != nil {
		sq.Close()
		return nil, nil, err
	}
	return sq, ts, nil
}

// restoreOverrides loads persisted panel edits into the registry and saves every new
// edit back, so names, paper sizes and directions outlive restarts.
func restoreOverrides(ctx context.Context, reg *fleet.Registry, st store.Store, log *slog.Logger) error {
	raw, err := st.PrinterOverrides(ctx)
	if err != nil {
		return err
	}
	saved := make(map[string]fleet.MetadataPatch, len(raw))
	for id, b := range raw {
		var p fleet.MetadataPatch
		if err := json.Unmarshal(b, &p); err != nil {
			log.Warn("ignoring unreadable saved printer edits", "printer_id", id, "err", err)
			continue
		}
		saved[id] = p
	}
	reg.LoadOverrides(saved)
	reg.OnOverrideSaved(func(id string, merged fleet.MetadataPatch) {
		b, err := json.Marshal(merged)
		if err == nil {
			err = st.SavePrinterOverride(context.Background(), id, b)
		}
		if err != nil {
			log.Error("cannot save printer edits; they will not survive a restart", "printer_id", id, "err", err)
		}
	})
	if len(saved) > 0 {
		log.Info("restored printer edits", "printers", len(saved))
	}
	return nil
}

func dataPathFor(cfg config.Config) string {
	if cfg.StoreDriver == "memory" {
		return "(memory: lost on restart)"
	}
	return cfg.DataPath
}

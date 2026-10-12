// Command cps-agent is the edge agent for label printers that are not on the network.
//
// It runs on a packing PC and does four things, no more:
//
//  1. Enumerate locally attached printers (USB, or a driver-provided local TCP share)
//  2. Listen on one TCP port per printer so the server can push ZPL exactly as it would
//     to a network printer — the server needs no USB code at all
//  3. Register itself and its printers with the server, then heartbeat
//  4. Push printer status changes
//
// Everything else — queueing, retries, idempotency, label policy — stays on the server,
// where it is testable without hardware.
//
//	cpsctl agent enroll --site wh1 --id pc7-pack01        # → one-time token
//	cps-agent -config agent.json -enroll <token>
//
// On Windows the agent also installs itself as a service:
//
//	cps-agent.exe -install -config C:\\ProgramData\\cps-agent\\agent.json
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
)

// version is stamped by the build; shown in every heartbeat so the fleet view can spot
// machines that were never upgraded.
var version = "dev"

func main() {
	var (
		configPath = flag.String("config", defaultConfigPath(), "agent config file")
		enroll     = flag.String("enroll", "", "one-time enrollment token (first run only)")
		site       = flag.String("site", "", "override the site from config")
		id         = flag.String("id", "", "override the agent id (default: hostname)")
		listenBase = flag.Int("listen-base", 0, "override the first local port (default: config/base_port)")
		version    = flag.Bool("version", false, "print version and exit")
		install    = flag.Bool("install", false, "Windows: install as a service (requires admin)")
		uninstall  = flag.Bool("uninstall", false, "Windows: remove the service")
		service    = flag.Bool("service", false, "internal: run under the Windows service manager")
	)
	flag.Parse()

	if *version {
		fmt.Println("cps-agent", version, runtime.GOOS, runtime.GOARCH)
		return
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Windows service install/uninstall needs no config and no hardware.
	if *install || *uninstall {
		os.Exit(runServiceCommand(log, *install, *configPath))
	}

	cfg, err := loadConfig(*configPath)
	if err != nil && *enroll == "" {
		log.Error("cannot load config", "err", err, "hint", "run once with -enroll <token> to create it")
		os.Exit(1)
	}
	cfg.defaults()
	if *site != "" {
		cfg.Site = *site
	}
	if *id != "" {
		cfg.AgentID = *id
	}
	if *listenBase > 0 {
		cfg.BasePort = *listenBase
	}
	if *enroll != "" {
		if err := cfg.enrollWith(context.Background(), *enroll, log); err != nil {
			log.Error("enrollment failed", "err", err)
			os.Exit(1)
		}
		if err := cfg.save(*configPath); err != nil {
			log.Error("cannot write config", "err", err)
			os.Exit(1)
		}
		log.Info("enrolled", "agent_id", cfg.AgentID, "config", *configPath)
	}
	if cfg.Token == "" {
		log.Error("no credential: re-run with -enroll <token>")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	a, err := newAgent(cfg, *configPath, log)
	if err != nil {
		log.Error("startup failed", "err", err)
		os.Exit(1)
	}

	if *service {
		// Windows SCM entry point: the real work happens in Run so the service control
		// handler can report status instead of just starting a goroutine.
		if err := runAsService(ctx, a, log); err != nil && err != errStopped {
			log.Error("service failed", "err", err)
			os.Exit(1)
		}
		return
	}

	a.Run(ctx)
	log.Info("agent stopped")
}

func defaultConfigPath() string {
	if v := os.Getenv("CPS_AGENT_CONFIG"); v != "" {
		return v
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("ProgramData"), "cps-agent", "agent.json")
	}
	return "/etc/cps-agent/agent.json"
}

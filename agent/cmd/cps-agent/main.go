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
// The desktop build (-tags gui, shipped as cps-agent-gui.exe) needs no command line:
// double-click it, and a tray icon opens a local page that asks for the server and the
// token, then shows the agent's status.
//
// On Windows, enroll and install as a service in one step (elevated prompt):
//
//	cps-agent.exe -enroll <token> -site wh1 -install
//	cps-agent.exe -uninstall
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"
)

// version is stamped by the build; shown in every heartbeat so the fleet view can spot
// machines that were never upgraded.
var version = "dev"

// guiMain is set by the desktop build (-tags gui): started without an action flag
// (double-clicked), the agent runs as a tray app with a local settings page instead.
var guiMain func(configPath string) int

func main() {
	var (
		configPath = flag.String("config", defaultConfigPath(), "agent config file")
		enroll     = flag.String("enroll", "", "one-time enrollment token (first run only)")
		site       = flag.String("site", "", "override the site from config")
		id         = flag.String("id", "", "override the agent id (default: hostname)")
		listenBase = flag.Int("listen-base", 0, "override the first local port (default: config/base_port)")
		showVer    = flag.Bool("version", false, "print version and exit")
		install    = flag.Bool("install", false, "Windows: install as a service (requires admin)")
		uninstall  = flag.Bool("uninstall", false, "Windows: remove the service")
		service    = flag.Bool("service", false, "internal: run under the Windows service manager")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("cps-agent", version, runtime.GOOS, runtime.GOARCH)
		return
	}
	if guiMain != nil && *enroll == "" && !*install && !*uninstall && !*service {
		os.Exit(guiMain(*configPath))
	}

	var logOut io.Writer = os.Stdout
	if !*service {
		// On a console the agent must never wait for its own output: Windows pauses a
		// program's writes while text is selected in its window (one click does it), and
		// a paused log line would stop the heartbeat with it.
		disableQuickEdit()
		logOut = newNonBlockingWriter(os.Stdout, 1024)
	}
	if *service {
		// Under the service manager there is no console; log next to the config instead.
		f, err := openServiceLog(serviceLogPath(*configPath))
		if err == nil {
			defer f.Close()
			logOut = f
		}
	}
	log := slog.New(slog.NewTextHandler(logOut, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Uninstall needs no config and no hardware. Install comes after enrollment so the
	// two can run as one step.
	if *uninstall {
		os.Exit(runServiceCommand(log, false, *configPath))
	}

	cfg, err := loadConfig(*configPath)
	if err != nil && *enroll == "" && !*install {
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
	if *install {
		os.Exit(runServiceCommand(log, true, *configPath))
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

func serviceLogPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "agent.log")
}

// maxServiceLog caps the service log; one previous file is kept as agent.log.1.
const maxServiceLog = 10 << 20

func openServiceLog(path string) (*os.File, error) {
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxServiceLog {
		_ = os.Rename(path, path+".1")
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

// nonBlockingWriter hands lines to a background writer and drops them, rather than
// waiting, when that writer is stuck. Losing log lines is better than losing heartbeats.
type nonBlockingWriter struct {
	lines   chan []byte
	dropped atomic.Int64
}

func newNonBlockingWriter(w io.Writer, buffer int) *nonBlockingWriter {
	nb := &nonBlockingWriter{lines: make(chan []byte, buffer)}
	go func() {
		for line := range nb.lines {
			if n := nb.dropped.Swap(0); n > 0 {
				fmt.Fprintf(w, "(%d log lines dropped while the console was paused)\n", n)
			}
			_, _ = w.Write(line)
		}
	}()
	return nb
}

func (nb *nonBlockingWriter) Write(p []byte) (int, error) {
	line := append([]byte(nil), p...) // slog reuses its buffer
	select {
	case nb.lines <- line:
	default:
		nb.dropped.Add(1)
	}
	return len(p), nil
}

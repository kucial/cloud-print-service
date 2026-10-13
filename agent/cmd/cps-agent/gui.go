//go:build gui

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"fyne.io/systray"
)

// The desktop build: a tray icon that shows the agent's state and opens the local
// panel. Built with `-tags gui`; the plain build stays a console program and service.
func init() { guiMain = runGUI }

func runGUI(configPath string) int {
	if !acquireSingleInstance() {
		return 1
	}

	ring := newLogRing(200)
	outs := []io.Writer{ring}
	if runtime.GOOS != "windows" { // a windowsgui program has no console to write to
		outs = append(outs, os.Stderr)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err == nil {
		if f, err := openServiceLog(serviceLogPath(configPath)); err == nil {
			defer f.Close()
			outs = append(outs, f)
		}
	}
	log := slog.New(slog.NewTextHandler(teeWriter(outs), &slog.HandlerOptions{Level: slog.LevelInfo}))
	log.Info("cps-agent desktop starting", "version", version, "config", configPath)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	sup := newSupervisor(configPath, log, ring)
	sup.blocked = serviceBlocks
	p := newPanel(sup, log)
	panelURL, err := p.Listen(ctx)
	if err != nil {
		log.Error("cannot start the panel", "err", err)
		showError("CPS agent cannot start its settings page: " + err.Error())
		return 1
	}
	if runtime.GOOS != "windows" {
		// Development builds run from a terminal: say where the panel is, there only
		// (the key is not for the log file).
		fmt.Fprintln(os.Stderr, "panel:", panelURL)
	}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		sup.Run(ctx)
	}()
	go func() {
		<-ctx.Done()
		systray.Quit()
	}()

	open := func() {
		if err := openBrowser(panelURL); err != nil {
			log.Warn("cannot open the browser", "err", err, "url", panelURL)
		}
	}
	systray.Run(func() { trayReady(ctx, sup, open, configPath) }, cancel)

	cancel()
	select { // let the agent close its ports before the process goes
	case <-stopped:
	case <-time.After(5 * time.Second):
	}
	log.Info("cps-agent desktop stopped")
	return 0
}

func trayReady(ctx context.Context, sup *supervisor, open func(), configPath string) {
	setIcon := func(phase string) {
		if runtime.GOOS == "windows" {
			systray.SetIcon(iconICO(phase))
		} else {
			systray.SetIcon(iconPNG(phase))
		}
	}
	setIcon(phaseStarting)
	systray.SetTooltip("CPS agent")

	mStatus := systray.AddMenuItem("CPS agent", "")
	mStatus.Disable()
	systray.AddSeparator()
	mOpen := systray.AddMenuItem("Open panel…", "Status and settings")
	var mLogin *systray.MenuItem
	if autostartSupported() {
		mLogin = systray.AddMenuItemCheckbox("Start at login", "Start the agent when you sign in", autostartEnabled())
	}
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Stop the agent")
	if runtime.GOOS == "windows" {
		systray.SetOnTapped(open) // left click opens the panel; right click shows the menu
	}

	var loginClicks <-chan struct{}
	if mLogin != nil {
		loginClicks = mLogin.ClickedCh
	}

	go func() {
		// Give the supervisor a moment to read the config, then open the panel on a
		// first run so the operator lands straight on the setup form.
		time.Sleep(300 * time.Millisecond)
		if sup.State().Phase == phaseNeedsSetup {
			open()
		}
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		last := ""
		for {
			st := sup.State()
			if line := trayLine(st); line != last {
				last = line
				setIcon(st.Phase)
				systray.SetTooltip("CPS agent: " + line)
				mStatus.SetTitle(line)
			}
			select {
			case <-ctx.Done():
				return
			case <-mOpen.ClickedCh:
				open()
			case <-loginClicks:
				on := !mLogin.Checked()
				if err := setAutostart(on, configPath); err != nil {
					showError("Cannot change the sign-in setting: " + err.Error())
				} else if on {
					mLogin.Check()
				} else {
					mLogin.Uncheck()
				}
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			case <-t.C:
			}
		}
	}()
}

// trayLine is the one-line status for the tooltip and the menu.
func trayLine(st supervisorState) string {
	n := len(st.Agent.Printers)
	switch st.Phase {
	case phaseConnected:
		if n == 1 {
			return "1 printer, connected"
		}
		return strconv.Itoa(n) + " printers, connected"
	case phaseNoPrinters:
		return "no printer found"
	case phaseNeedsSetup:
		return "not set up: open the panel"
	case phaseBlocked:
		return "not running: the Windows service serves this PC"
	case phaseError:
		return "cannot reach the server"
	default:
		return "starting…"
	}
}

// teeWriter writes to every writer, ignoring failures so one bad sink (a full disk)
// does not silence the others.
type teeWriter []io.Writer

func (t teeWriter) Write(p []byte) (int, error) {
	for _, w := range t {
		_, _ = w.Write(p)
	}
	return len(p), nil
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// The agent runs as a LocalSystem service so it survives logoff and starts before
// anyone signs in at the packing station.
const (
	serviceName    = "cps-agent"
	serviceDisplay = "Cloud Print Service agent"
	serviceDesc    = "Makes locally attached label printers reachable by the Cloud Print Service."
	firewallRule   = "Cloud Print Service agent"
)

var errStopped = errors.New("service stopped")

func runServiceCommand(log *slog.Logger, install bool, configPath string) int {
	var err error
	if install {
		err = installService(log, configPath)
	} else {
		err = uninstallService(log)
	}
	if err != nil {
		log.Error("service command failed", "install", install, "err", err,
			"hint", "run from an elevated (Administrator) prompt")
		return 1
	}
	return 0
}

func installService(log *slog.Logger, configPath string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if _, err := os.Stat(configPath); err != nil {
		return fmt.Errorf("config %s: %w (enroll first: cps-agent -enroll <token> -config %s)", configPath, err, configPath)
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	if s, err := m.OpenService(serviceName); err == nil {
		s.Close()
		return fmt.Errorf("service %q already exists; run -uninstall first", serviceName)
	}
	s, err := m.CreateService(serviceName, exe, mgr.Config{
		DisplayName:      serviceDisplay,
		Description:      serviceDesc,
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: true, // let the network and printer drivers come up first
	}, "-service", "-config", configPath)
	if err != nil {
		return err
	}
	defer s.Close()

	// The agent exits when it finds no printer (unplugged at boot); restart it so a
	// printer that comes back is picked up without a visit to the PC.
	restart := mgr.RecoveryAction{Type: mgr.ServiceRestart, Delay: 30 * time.Second}
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{restart, restart, restart}, 24*60*60); err != nil {
		log.Warn("could not set service recovery actions", "err", err)
	}

	// The server dials in to the agent's per-printer ports; allow that for this program
	// only, rather than opening a port range.
	if out, err := exec.Command("netsh", "advfirewall", "firewall", "add", "rule",
		"name="+firewallRule, "dir=in", "action=allow", "program="+exe, "enable=yes",
		"profile=domain,private").CombinedOutput(); err != nil {
		log.Warn("could not add firewall rule; the server may not reach this PC", "err", err, "output", string(out))
	}

	if err := s.Start(); err != nil {
		return fmt.Errorf("installed but not started: %w", err)
	}
	log.Info("service installed and started", "name", serviceName, "config", configPath, "log", serviceLogPath(configPath))
	return nil
}

func uninstallService(log *slog.Logger) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("service %q is not installed", serviceName)
	}
	defer s.Close()

	if st, err := s.Control(svc.Stop); err == nil {
		for i := 0; i < 20 && st.State != svc.Stopped; i++ {
			time.Sleep(500 * time.Millisecond)
			if st, err = s.Query(); err != nil {
				break
			}
		}
	}
	if err := s.Delete(); err != nil {
		return err
	}
	_ = exec.Command("netsh", "advfirewall", "firewall", "delete", "rule", "name="+firewallRule).Run()
	log.Info("service removed", "name", serviceName)
	return nil
}

// runAsService hands control to the service manager and runs the agent until it is
// told to stop.
func runAsService(ctx context.Context, a *agent, log *slog.Logger) error {
	if err := svc.Run(serviceName, &handler{ctx: ctx, a: a, log: log}); err != nil {
		return err
	}
	return errStopped
}

type handler struct {
	ctx context.Context
	a   *agent
	log *slog.Logger
}

func (h *handler) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()

	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(done)
		h.a.Run(ctx)
	}()

	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case <-done:
			// Run returned on its own (no printers, cannot listen): report a failure so
			// the recovery actions restart us.
			h.log.Error("agent exited; the service manager will restart it")
			status <- svc.Status{State: svc.StopPending}
			return true, 1
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				h.log.Info("service stop requested")
				status <- svc.Status{State: svc.StopPending}
				cancel()
				wg.Wait()
				return false, 0
			}
		}
	}
}

// restrictToOwner limits the config — it holds the agent credential — to SYSTEM (the
// service), Administrators, and the account running the agent now. Without that last
// entry an agent enrolled from a non-elevated prompt locks itself out of the file it
// just wrote, and cannot save the credential the server exchanges on first contact.
// SIDs, not names, so it works on localized Windows too.
func restrictToOwner(path string) error {
	sddl := "D:P(A;;FA;;;SY)(A;;FA;;;BA)"
	if user, err := windows.GetCurrentProcessToken().GetTokenUser(); err == nil {
		sddl += "(A;;FA;;;" + user.User.Sid.String() + ")"
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

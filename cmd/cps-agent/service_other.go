//go:build !windows

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
)

// Windows-only surface. On other platforms the agent runs as a normal process and is
// supervised by systemd/launchd.

var errStopped = errors.New("service stopped")

func runServiceCommand(log *slog.Logger, install bool, configPath string) int {
	log.Info("service install is Windows-only", "install", install)
	return 2
}

func runAsService(ctx context.Context, a *agent, log *slog.Logger) error {
	a.Run(ctx)
	return errStopped
}

func restrictToOwner(path string) error { return os.Chmod(path, 0o600) }

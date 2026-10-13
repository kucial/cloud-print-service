//go:build gui && !windows

package main

import (
	"fmt"
	"os"
)

// Elsewhere the desktop build is for development; the service manager (systemd,
// launchd) already provides single-instance and start-at-login.

func acquireSingleInstance() bool { return true }

func serviceBlocks() string { return "" }

func autostartSupported() bool { return false }

func autostartEnabled() bool { return false }

func setAutostart(bool, string) error { return fmt.Errorf("not supported on this platform") }

func showError(msg string) { fmt.Fprintln(os.Stderr, msg) }

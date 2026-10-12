//go:build !usb

package main

import (
	"context"
	"log/slog"
)

// usbBackends returns raw-USB backends for attached printers.
//
// Tier 2 (WinUSB via golang.org/x/sys/windows) and tier 3 (libusb) are build-tagged so
// that the default build of cps-agent stays pure Go with no native dependencies — see
// docs/PLAN-agent-discovery.md §6.3. Until a tier is built for this platform the agent
// simply reports no USB printers instead of pretending it can reach them.
func usbBackends(ctx context.Context, log *slog.Logger) []Backend {
	log.Debug("raw USB backend not built for this binary",
		"hint", "the default build ships the driver-share tier only; USB requires the WinUSB or usb_libusb build")
	return nil
}

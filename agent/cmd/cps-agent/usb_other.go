//go:build !windows && !linux && !(darwin && cgo && usb_libusb)

package main

import (
	"errors"
)

// macOS and the BSDs have no kernel USB printer device the agent could open without
// libusb (and so cgo). Printers there are reached through a driver TCP share only.

func listUSBPrinters() ([]usbDevice, error) { return nil, nil }

func openUSBPort(string) (usbPort, error) {
	return nil, errors.New("USB printers are supported on Windows and Linux only")
}

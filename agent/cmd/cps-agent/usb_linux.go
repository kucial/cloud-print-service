//go:build linux

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Linux: the usblp kernel driver exposes each USB printer as /dev/usb/lpN, readable and
// writable. The agent's user needs access to it (group lp on most distributions).

func listUSBPrinters() ([]usbDevice, error) {
	paths, err := filepath.Glob("/dev/usb/lp*")
	if err != nil {
		return nil, err
	}
	var out []usbDevice
	for _, p := range paths {
		base := filepath.Base(p)
		d := usbDevice{Path: p, Key: sysfsSerial(base)}
		if d.Key == "" {
			d.Key = base // lpN follows plug order: stable only while nothing else changes
		}
		if port, err := openUSBPort(p); err == nil {
			d.ID1284 = port.(*lpPort).deviceID()
			port.Close()
		}
		out = append(out, d)
	}
	return out, nil
}

// sysfsSerial reads the USB serial number of the device behind lpN, if it has one.
func sysfsSerial(lp string) string {
	b, err := os.ReadFile(filepath.Join("/sys/class/usbmisc", lp, "device", "..", "serial"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

type lpPort struct{ f *os.File }

func openUSBPort(path string) (usbPort, error) {
	// Non-blocking so the runtime poller can apply deadlines to reads and writes.
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return &lpPort{f: os.NewFile(uintptr(fd), path)}, nil
}

func (l *lpPort) Write(p []byte, timeout time.Duration) (int, error) {
	_ = l.f.SetWriteDeadline(time.Now().Add(timeout))
	n, err := l.f.Write(p)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return n, errUSBTimeout
	}
	return n, err
}

func (l *lpPort) Read(p []byte, timeout time.Duration) (int, error) {
	_ = l.f.SetReadDeadline(time.Now().Add(timeout))
	n, err := l.f.Read(p)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return n, errUSBTimeout
	}
	return n, err
}

func (l *lpPort) Close() error { return l.f.Close() }

// deviceID asks usblp for the IEEE-1284 id: LPIOC_GET_DEVICE_ID(len), whose first two
// bytes are a big-endian length.
func (l *lpPort) deviceID() string {
	const size = 1024
	buf := make([]byte, size)
	req := uintptr(2<<30 | size<<16 | 'P'<<8 | 1) // _IOC(_IOC_READ, 'P', 1, size)
	raw, err := l.f.SyscallConn()
	if err != nil {
		return ""
	}
	var errno unix.Errno
	_ = raw.Control(func(fd uintptr) {
		_, _, errno = unix.Syscall(unix.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(&buf[0])))
	})
	if errno != 0 {
		return ""
	}
	n := int(buf[0])<<8 | int(buf[1])
	if n < 2 || n > size {
		return ""
	}
	return strings.TrimSpace(string(buf[2:n]))
}

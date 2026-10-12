//go:build windows

package main

import (
	"errors"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// GUID_DEVINTERFACE_USBPRINT: every printer bound to usbprint.sys — which is every USB
// printer with a normal vendor driver installed — exposes this device interface.
var guidUSBPrint = windows.GUID{
	Data1: 0x28d78fad, Data2: 0x5a12, Data3: 0x11d1,
	Data4: [8]byte{0xae, 0x5b, 0x00, 0x00, 0xf8, 0x03, 0xa8, 0xc2},
}

// IOCTL_USBPRINT_GET_1284_ID = CTL_CODE(FILE_DEVICE_UNKNOWN, 13, METHOD_BUFFERED, FILE_ANY_ACCESS)
const ioctlUSBPrintGet1284ID = 0x220034

func listUSBPrinters() ([]usbDevice, error) {
	paths, err := windows.CM_Get_Device_Interface_List("", &guidUSBPrint, windows.CM_GET_DEVICE_INTERFACE_LIST_PRESENT)
	if err != nil {
		return nil, err
	}
	var out []usbDevice
	for _, p := range paths {
		if p == "" {
			continue
		}
		d := usbDevice{Path: p, Key: instanceKey(p)}
		if port, err := openUSBPort(p); err == nil {
			d.ID1284 = port.(*winPort).deviceID()
			port.Close()
		}
		out = append(out, d)
	}
	return out, nil
}

// instanceKey pulls the instance id out of an interface path:
// \\?\usb#vid_0a5f&pid_0080#<instance>#{28d78fad-…}. With a USB serial number the
// instance is that serial; without one Windows derives it from the port.
func instanceKey(path string) string {
	parts := strings.Split(path, "#")
	if len(parts) >= 3 {
		return parts[2]
	}
	return path
}

type winPort struct{ h windows.Handle }

func openUSBPort(path string) (usbPort, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return nil, err
	}
	return &winPort{h: h}, nil
}

func (w *winPort) Write(p []byte, timeout time.Duration) (int, error) {
	total := 0
	deadline := time.Now().Add(timeout)
	for total < len(p) {
		left := time.Until(deadline)
		if left <= 0 {
			return total, errUSBTimeout
		}
		n, err := w.io(false, p[total:], left)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func (w *winPort) Read(p []byte, timeout time.Duration) (int, error) {
	return w.io(true, p, timeout)
}

// io runs one overlapped read or write and gives up after timeout, cancelling the
// request so the handle can be closed cleanly.
func (w *winPort) io(read bool, p []byte, timeout time.Duration) (int, error) {
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(ev)
	ov := windows.Overlapped{HEvent: ev}

	// done is unreliable for overlapped I/O (GetOverlappedResult has the count), but
	// x/sys dereferences it under the race detector, so it must not be nil.
	var done uint32
	if read {
		err = windows.ReadFile(w.h, p, &done, &ov)
	} else {
		err = windows.WriteFile(w.h, p, &done, &ov)
	}
	if err != nil && !errors.Is(err, windows.ERROR_IO_PENDING) {
		return 0, err
	}
	ms := uint32(timeout / time.Millisecond)
	if ms == 0 {
		ms = 1
	}
	var n uint32
	if r, _ := windows.WaitForSingleObject(ev, ms); r == uint32(windows.WAIT_TIMEOUT) {
		_ = windows.CancelIoEx(w.h, &ov)
		// Wait for the cancel to land: the kernel still owns ov and p until it does.
		_ = windows.GetOverlappedResult(w.h, &ov, &n, true)
		if n > 0 {
			return int(n), nil
		}
		return 0, errUSBTimeout
	}
	if err := windows.GetOverlappedResult(w.h, &ov, &n, true); err != nil {
		return int(n), err
	}
	return int(n), nil
}

// deviceID returns the IEEE-1284 id; the first two bytes are a big-endian length.
func (w *winPort) deviceID() string {
	buf := make([]byte, 1024)
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(ev)
	ov := windows.Overlapped{HEvent: ev}
	var n uint32
	err = windows.DeviceIoControl(w.h, ioctlUSBPrintGet1284ID, nil, 0, &buf[0], uint32(len(buf)), nil, &ov)
	if err != nil && !errors.Is(err, windows.ERROR_IO_PENDING) {
		return ""
	}
	if r, _ := windows.WaitForSingleObject(ev, 2000); r == uint32(windows.WAIT_TIMEOUT) {
		_ = windows.CancelIoEx(w.h, &ov)
		_ = windows.GetOverlappedResult(w.h, &ov, &n, true)
		return ""
	}
	if err := windows.GetOverlappedResult(w.h, &ov, &n, true); err != nil || n < 2 {
		return ""
	}
	l := int(buf[0])<<8 | int(buf[1])
	if l > int(n) || l < 2 {
		l = int(n)
	}
	return strings.TrimSpace(string(buf[2:l]))
}

func (w *winPort) Close() error { return windows.CloseHandle(w.h) }

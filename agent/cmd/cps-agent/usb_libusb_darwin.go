//go:build darwin && cgo && usb_libusb

package main

// macOS has no kernel USB printer device, so this opt-in backend goes through libusb:
//
//	brew install libusb
//	go build -tags usb_libusb ./agent/cmd/cps-agent
//
// It exists to run the agent against a real printer on a developer Mac; packing PCs use
// the pure-Go Windows/Linux backends.

/*
#cgo pkg-config: libusb-1.0
#include <libusb.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

// find_printer opens the n-th USB printer-class interface whose key matches (or any,
// when key is empty) and claims it. key is the serial number, or "bus-port.port…".
static int find_printer(libusb_context *ctx, const char *want, libusb_device_handle **out,
                        int *iface, unsigned char *ep_out, unsigned char *ep_in,
                        char *key, int keylen, char *id1284, int idlen, int index) {
	libusb_device **list;
	ssize_t cnt = libusb_get_device_list(ctx, &list);
	int seen = 0, rc = LIBUSB_ERROR_NOT_FOUND;
	for (ssize_t i = 0; i < cnt && rc == LIBUSB_ERROR_NOT_FOUND; i++) {
		struct libusb_config_descriptor *cfg;
		if (libusb_get_active_config_descriptor(list[i], &cfg) != 0) continue;
		for (int f = 0; f < cfg->bNumInterfaces; f++) {
			const struct libusb_interface_descriptor *alt = &cfg->interface[f].altsetting[0];
			if (alt->bInterfaceClass != 7) continue; // printer class
			unsigned char o = 0, in = 0;
			for (int e = 0; e < alt->bNumEndpoints; e++) {
				const struct libusb_endpoint_descriptor *ep = &alt->endpoint[e];
				if ((ep->bmAttributes & 3) != LIBUSB_TRANSFER_TYPE_BULK) continue;
				if (ep->bEndpointAddress & 0x80) in = ep->bEndpointAddress; else o = ep->bEndpointAddress;
			}
			if (!o || !in) continue; // write-only printers cannot answer ~HS
			libusb_device_handle *h;
			if (libusb_open(list[i], &h) != 0) continue;
			char k[128] = {0};
			struct libusb_device_descriptor d;
			libusb_get_device_descriptor(list[i], &d);
			if (d.iSerialNumber) libusb_get_string_descriptor_ascii(h, d.iSerialNumber, (unsigned char *)k, sizeof k);
			if (!k[0]) {
				uint8_t ports[8]; int np = libusb_get_port_numbers(list[i], ports, 8);
				int w = snprintf(k, sizeof k, "%d", libusb_get_bus_number(list[i]));
				for (int p = 0; p < np && w < (int)sizeof k; p++) w += snprintf(k + w, sizeof k - w, "%s%d", p ? "." : "-", ports[p]);
			}
			int match = want[0] ? strcmp(want, k) == 0 : seen++ == index;
			if (!match) { libusb_close(h); continue; }
			libusb_set_auto_detach_kernel_driver(h, 1);
			if (libusb_claim_interface(h, alt->bInterfaceNumber) != 0) { libusb_close(h); rc = LIBUSB_ERROR_BUSY; break; }
			unsigned char buf[1024];
			int n = libusb_control_transfer(h, 0xA1, 0, cfg->bConfigurationValue - 1,
			                                (alt->bInterfaceNumber << 8) | alt->bAlternateSetting, buf, sizeof buf - 1, 1000);
			if (n > 2 && id1284) { buf[n] = 0; strncpy(id1284, (char *)buf + 2, idlen - 1); }
			strncpy(key, k, keylen - 1);
			*out = h; *iface = alt->bInterfaceNumber; *ep_out = o; *ep_in = in;
			rc = 0;
			break;
		}
		libusb_free_config_descriptor(cfg);
	}
	libusb_free_device_list(list, 1);
	return rc;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"
)

var (
	usbCtxOnce sync.Once
	usbCtx     *C.libusb_context
	usbCtxErr  error
)

func libusbContext() (*C.libusb_context, error) {
	usbCtxOnce.Do(func() {
		if rc := C.libusb_init(&usbCtx); rc != 0 {
			usbCtxErr = fmt.Errorf("libusb_init: %s", C.GoString(C.libusb_error_name(rc)))
		}
	})
	return usbCtx, usbCtxErr
}

type libusbPort struct {
	h       *C.libusb_device_handle
	iface   C.int
	out, in C.uchar
	key     string
	id1284  string
}

func openLibusb(want string, index int) (*libusbPort, error) {
	ctx, err := libusbContext()
	if err != nil {
		return nil, err
	}
	cwant := C.CString(want)
	defer C.free(unsafe.Pointer(cwant))
	p := &libusbPort{}
	var key, id [512]C.char
	rc := C.find_printer(ctx, cwant, &p.h, &p.iface, &p.out, &p.in, &key[0], C.int(len(key)), &id[0], C.int(len(id)), C.int(index))
	if rc != 0 {
		return nil, fmt.Errorf("usb printer %q: %s", want, C.GoString(C.libusb_error_name(rc)))
	}
	p.key, p.id1284 = C.GoString(&key[0]), C.GoString(&id[0])
	return p, nil
}

func listUSBPrinters() ([]usbDevice, error) {
	var out []usbDevice
	for i := 0; i < 16; i++ {
		p, err := openLibusb("", i)
		if err != nil {
			break
		}
		out = append(out, usbDevice{Path: p.key, Key: p.key, ID1284: p.id1284})
		p.Close()
	}
	return out, nil
}

func openUSBPort(path string) (usbPort, error) { return openLibusb(path, 0) }

func (p *libusbPort) transfer(ep C.uchar, b []byte, timeout time.Duration) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	ms := C.uint(timeout / time.Millisecond)
	if ms == 0 {
		ms = 1
	}
	// cgo may not pass Go memory the C side keeps; libusb_bulk_transfer is synchronous,
	// so a C copy per call is enough.
	cbuf := C.malloc(C.size_t(len(b)))
	defer C.free(cbuf)
	if ep&0x80 == 0 {
		C.memcpy(cbuf, unsafe.Pointer(&b[0]), C.size_t(len(b)))
	}
	var n C.int
	rc := C.libusb_bulk_transfer(p.h, ep, (*C.uchar)(cbuf), C.int(len(b)), &n, ms)
	if ep&0x80 != 0 && n > 0 {
		C.memcpy(unsafe.Pointer(&b[0]), cbuf, C.size_t(n))
	}
	switch rc {
	case 0:
		return int(n), nil
	case C.LIBUSB_ERROR_TIMEOUT:
		return int(n), errUSBTimeout
	}
	return int(n), errors.New(C.GoString(C.libusb_error_name(rc)))
}

func (p *libusbPort) Write(b []byte, timeout time.Duration) (int, error) {
	return p.transfer(p.out, b, timeout)
}

func (p *libusbPort) Read(b []byte, timeout time.Duration) (int, error) {
	n, err := p.transfer(p.in, b, timeout)
	if n > 0 && errors.Is(err, errUSBTimeout) {
		err = nil
	}
	return n, err
}

func (p *libusbPort) Close() error {
	C.libusb_release_interface(p.h, p.iface)
	C.libusb_close(p.h)
	return nil
}

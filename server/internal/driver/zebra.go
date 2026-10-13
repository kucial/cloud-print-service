package driver

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kucial/cloud-print-service/pkg/printer"
	"github.com/kucial/cloud-print-service/server/internal/domain"
)

func init() {
	Register(printer.DriverZebra, func(p domain.Printer, o Options) (Driver, error) { return NewZebra(p, o), nil })
}

// Zebra speaks raw ZPL over TCP 9100 (see tcpLink for the two-socket design).
type Zebra struct {
	link *tcpLink
}

// ZebraOpts is the former name of Options.
type ZebraOpts = Options

func NewZebra(p domain.Printer, o Options) *Zebra {
	return &Zebra{link: newTCPLink(p.Host, p.Port, o)}
}

// Send writes raw ZPL. Bytes actually flushed are returned; err != nil with n > 0 means
// a partial label may exist on the printer and the caller must not retry.
func (z *Zebra) Send(ctx context.Context, data []byte) (int, error) { return z.link.send(ctx, data) }

// query asks a host query on the status socket; ZPL replies are STX/ETX framed.
func (z *Zebra) query(ctx context.Context, cmd string, maxBytes int) (string, error) {
	raw, err := z.link.query(ctx, []byte(cmd), maxBytes, func(acc []byte) bool { return printer.ReplyComplete(cmd, acc) })
	return string(raw), err
}

// Status probes with the ZPL host status command. Falls back to reachability-only when the
// model does not answer, so the API contract stays intact (state becomes `unknown`).
func (z *Zebra) Status(ctx context.Context) (domain.Status, error) {
	raw, err := z.query(ctx, printer.CmdHostStatus, 256)
	now := time.Now()
	if err != nil {
		st := domain.Status{State: domain.StateOffline, ObservedAt: now, Detail: "status query failed"}
		return st, err
	}
	st, ok := ParseHostStatus([]byte(raw))
	st.ObservedAt = now
	if !ok {
		st.State = domain.StateUnknown
		st.Detail = "unrecognised host status reply"
	}
	return st, nil
}

// Cancel aborts printing in progress (ZPL ~JA).
func (z *Zebra) Cancel(ctx context.Context) error { return z.link.control(ctx, []byte("~JA")) }

// ParseHIResponse parses Zebra ~HI host identification string into structured hardware identity.
// Standard fields in Zebra ~HI:
//
//	 8: Vendor
//	 9: Model (e.g. "ZT411")
//	10: Resolution in dpmm or dpi (e.g. "8.0", "12.0", "203", "300")
//	11: Options / Memory / Installed fonts
//	12: Serial number
//	13: Firmware version
func ParseHIResponse(raw string) (model, serial, firmware string, dpi int, dpmm float64, cjkFont string) {
	if !strings.Contains(raw, "=") {
		// Real firmware: MODEL,Vfirmware,dpmm,memory — no serial, no font list.
		if id, ok := printer.ParseHostID(raw); ok {
			model, firmware, dpmm = id.Model, id.Firmware, id.DPMM
			dpi = printer.DPIFromDPMM(dpmm)
		}
		return
	}
	for _, kv := range splitFields(raw) {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			continue
		}
		k := strings.TrimSpace(parts[0])
		v := strings.TrimSpace(parts[1])
		switch k {
		case "9":
			model = v
		case "12":
			serial = v
		case "13":
			firmware = v
		case "10":
			if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
				if f >= 100 {
					dpi = int(f)
					dpmm = float64(int((f/25.4)*10+0.5)) / 10
				} else {
					dpmm = f
					switch int(f) {
					case 8:
						dpi = 203
					case 12:
						dpi = 300
					case 24:
						dpi = 600
					default:
						dpi = int(f*25.4 + 0.5)
					}
				}
			}
		case "11":
			upper := strings.ToUpper(v)
			if strings.Contains(upper, "SIMSUN") {
				cjkFont = "SIMSUN.FNT"
			} else if strings.Contains(upper, "ANSD") || strings.Contains(upper, "ANDALE") {
				cjkFont = "ANSD.TTF"
			} else if strings.Contains(upper, "NOTO") {
				cjkFont = "NotoSansCJK"
			} else if strings.HasSuffix(upper, ".FNT") || strings.HasSuffix(upper, ".TTF") {
				cjkFont = v
			}
		}
	}
	return
}

// Identify reads model / serial / firmware via ~HI. Used when onboarding a printer.
func (z *Zebra) Identify(ctx context.Context) (map[string]string, error) {
	raw, err := z.query(ctx, "~HI", 256)
	if err != nil {
		return map[string]string{"raw": raw}, err
	}
	out := map[string]string{"raw": strings.TrimSpace(raw)}
	for _, kv := range splitFields(raw) {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			if v := strings.TrimSpace(parts[1]); v != "" {
				out[parts[0]] = v
			}
		}
	}
	model, serial, fw, dpi, dpmm, cjk := ParseHIResponse(raw)
	if model != "" {
		out["model"] = model
	}
	if serial != "" {
		out["serial"] = serial
	}
	if fw != "" {
		out["firmware"] = fw
	}
	if dpi > 0 {
		out["dpi"] = strconv.Itoa(dpi)
		out["dpmm"] = fmt.Sprintf("%g", dpmm)
		out["dpi_resolved_from_hi"] = "true"
		out["resolution_source"] = "hi"
	}
	if cjk != "" {
		out["cjk_font"] = cjk
		out["font_resolved_from_hi"] = "true"
		out["font_source"] = "hi"
	}
	return out, nil
}

func splitFields(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == '\n' || r == '\r' })
}

// HostConfig returns the printer's ^HH configuration dump, raw.
func (z *Zebra) HostConfig(ctx context.Context) (string, error) {
	return z.query(ctx, printer.CmdHostConfig, 16<<10)
}

// HostDirectory lists the printer's stored objects (^HW), raw.
func (z *Zebra) HostDirectory(ctx context.Context) (string, error) {
	return z.query(ctx, printer.CmdHostDirectory, 32<<10)
}

// Control sends a maintenance command (~JC calibrate, ^LT…^JUS) on the print connection,
// so it is ordered with label data instead of racing it.
func (z *Zebra) Control(ctx context.Context, cmd string) error {
	return z.link.control(ctx, []byte(cmd))
}

// Close releases both sockets.
func (z *Zebra) Close() error { return z.link.close() }

// ParseHostStatus decodes a host status reply: real firmware's three-line ~HS answer, or
// the legacy ^HS<device>,<media>,<ribbon>,<head>^HS form. See printer.ParseHostStatus.
func ParseHostStatus(raw []byte) (domain.Status, bool) {
	state, detail, ok := printer.ParseHostStatus(string(raw))
	return domain.Status{Raw: strings.TrimSpace(string(raw)), State: state, Detail: detail}, ok
}

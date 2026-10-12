package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brutella/dnssd"
)

// MDNS browses DNS-SD service types and (optionally) verifies each hit by asking the
// device who it is with `~HI`.
//
// Verifying is the part that matters. An advertisement is a claim; `~HI` returning a model
// and serial is evidence. Everything that reaches a candidate list has been asked to
// identify itself, so adopting a device is a decision about something that answered.
type MDNS struct {
	// Types are the DNS-SD service types to browse.
	Types []string
	// BrowseTimeout bounds one full browse pass.
	BrowseTimeout time.Duration
	// Identify asks each hit who it is. Turning this off makes candidates cheaper to
	// produce and weaker evidence; leave it on unless you are on a huge flat network.
	Identify bool
	// IdentifyTimeout bounds one identification probe.
	IdentifyTimeout time.Duration
	// DefaultPort is used when the advertisement does not name one.
	DefaultPort int
	// Logf receives probe-level diagnostics (optional).
	Logf func(format string, args ...any)
}

func (p MDNS) Name() string { return "mdns" }

// withDefaults is not optional here: a probe built with zero types would scan nothing
// and report success, which looks exactly like "no printers on this network".
func (p MDNS) withDefaults() MDNS {
	if len(p.Types) == 0 {
		p.Types = DefaultTypes()
	}
	if p.BrowseTimeout <= 0 {
		p.BrowseTimeout = 2 * time.Second
	}
	if p.IdentifyTimeout <= 0 {
		p.IdentifyTimeout = 2 * time.Second
	}
	if p.DefaultPort == 0 {
		p.DefaultPort = 9100
	}
	return p
}

// Scan browses each service type and merges the results by device key.
func (p MDNS) Scan(ctx context.Context) ([]Found, error) {
	p = p.withDefaults()
	timeout := p.BrowseTimeout
	identifyTimeout := p.IdentifyTimeout

	byKey := map[string]Found{}
	var errs []string

	for _, svcType := range p.Types {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		normType := normalizeServiceType(svcType)
		bctx, cancel := context.WithTimeout(ctx, timeout)
		entries, err := p.browse(bctx, normType)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				errs = append(errs, fmt.Sprintf("%s: %v", svcType, err))
			}
			continue
		}
		for _, e := range entries {
			f := Found{
				DeviceKey: p.deviceKey(e),
				Source:    "mdns",
				Host:      hostOf(e),
				Detail: map[string]string{
					"service":    e.ServiceInstanceName(),
					"adverts":    strings.Join(p.advertisedTypes(e), ","),
					"discovered": time.Now().UTC().Format(time.RFC3339),
				},
			}
			f.Port = p.portFor(e)
			if f.Port == 0 {
				f.Port = p.DefaultPort
			}
			if host := f.Host; host != "" {
				f.DeviceKey = fmt.Sprintf("mdns:%s:%d", host, f.Port)
			}

			if p.Identify && f.Host != "" {
				probeCtx, pcancel := context.WithTimeout(ctx, identifyTimeout)
				id, ierr := identify(prCtx{ctx: probeCtx, host: f.Host, port: f.Port}, p.Logf)
				pcancel()
				if ierr == nil {
					f.Model, f.Serial, f.Vendor = id.Model, id.Serial, id.Vendor
					f.Detail["identified"] = "true"
				} else {
					// Not answering `~HI` on the advertised port: keep it as an unverified
					// sighting, clearly labelled so an operator can judge.
					f.Detail["identified"] = "false"
					f.Detail["identify_error"] = ierr.Error()
					if p.Logf != nil {
						p.Logf("mdns: %s did not answer ~HI: %v", f.Host, ierr)
					}
				}
			}
			if old, ok := byKey[f.DeviceKey]; ok {
				// Same device seen under several service types: merge the evidence.
				if f.Model == "" {
					f.Model = old.Model
				}
				if f.Serial == "" {
					f.Serial = old.Serial
				}
				if f.Vendor == "" {
					f.Vendor = old.Vendor
				}
			}
			byKey[f.DeviceKey] = f
		}
	}

	out := make([]Found, 0, len(byKey))
	for _, f := range byKey {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DeviceKey < out[j].DeviceKey })
	if len(errs) > 0 {
		return out, fmt.Errorf("mdns browse: %s", strings.Join(errs, "; "))
	}
	return out, nil
}

func (p MDNS) browse(ctx context.Context, svcType string) ([]dnssd.BrowseEntry, error) {
	var (
		mu      sync.Mutex
		entries []dnssd.BrowseEntry
	)
	add := func(e dnssd.BrowseEntry) {
		mu.Lock()
		entries = append(entries, e)
		mu.Unlock()
	}
	rmv := func(dnssd.BrowseEntry) {}

	bctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- dnssd.LookupType(bctx, svcType, add, rmv) }()

	select {
	case err := <-errCh:
		mu.Lock()
		res := append([]dnssd.BrowseEntry(nil), entries...)
		mu.Unlock()
		if err != nil && ctx.Err() == nil {
			return res, err
		}
		return res, nil
	case <-ctx.Done():
		// A browse pass that fills its window is the normal path; LookupType blocks
		// until cancelled, so a timeout here is expected, not a failure.
		cancel()
		<-errCh
		mu.Lock()
		res := append([]dnssd.BrowseEntry(nil), entries...)
		mu.Unlock()
		return res, nil
	}
}

func normalizeServiceType(t string) string {
	t = strings.TrimSpace(t)
	if !strings.HasSuffix(t, ".") {
		t += "."
	}
	if !strings.HasSuffix(t, ".local.") {
		t = strings.TrimSuffix(t, ".") + ".local."
	}
	return t
}

// advertisedTypes reports the TXT keys a device publishes. Zebra firmware commonly
// advertises identity in TXT records, so these are a useful hint before `~HI` is asked.
func (p MDNS) advertisedTypes(e dnssd.BrowseEntry) []string {
	out := make([]string, 0, len(e.Text))
	for k := range e.Text {
		if k == "" {
			continue
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (p MDNS) portFor(e dnssd.BrowseEntry) int {
	if e.Port != 0 {
		return e.Port
	}
	if p.DefaultPort != 0 {
		return p.DefaultPort
	}
	return 9100
}

func (p MDNS) deviceKey(e dnssd.BrowseEntry) string {
	if h := hostOf(e); h != "" {
		return "mdns:" + h
	}
	sum := sha256.Sum256([]byte(e.ServiceInstanceName()))
	return "mdns:" + hex.EncodeToString(sum[:6])
}

// hostOf picks the address the server should actually dial: a v4 address from the
// announcement, then the interface address, then whatever the entry carries.
func hostOf(e dnssd.BrowseEntry) string {
	for _, ip := range e.IPs {
		if v4 := ip.To4(); v4 != nil {
			return v4.String()
		}
	}
	for _, ip := range e.IPs {
		if !ip.IsLoopback() {
			return ip.String()
		}
	}
	if e.IfaceName != "" {
		if iface, err := net.InterfaceByName(e.IfaceName); err == nil {
			addrs, _ := iface.Addrs()
			for _, a := range addrs {
				if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil {
					return ipnet.IP.String()
				}
			}
		}
	}
	return ""
}

// identity is what a device says it is.
type identity struct {
	Vendor string
	Model  string
	Serial string
}

// identify asks a device for `~HI` and parses the model/serial fields.
func identify(p prCtx, logf func(string, ...any)) (identity, error) {
	var id identity
	timeout := time.Second
	if dl, ok := p.ctx.Deadline(); ok {
		if left := time.Until(dl) / 2; left > 0 {
			timeout = left
		}
	}
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(p.ctx, "tcp", net.JoinHostPort(p.host, strconv.Itoa(p.port)))
	if err != nil {
		return id, err
	}
	defer conn.Close()

	deadline := time.Now().Add(time.Second)
	if d, ok := p.ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	if _, err := conn.Write([]byte("~HI")); err != nil {
		return id, err
	}
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil && n == 0 {
		return id, err
	}
	raw := string(buf[:n])
	if !strings.Contains(raw, "=") {
		return id, fmt.Errorf("no ~HI fields in %q", strings.TrimSpace(raw))
	}
	for _, kv := range strings.FieldsFunc(raw, func(r rune) bool { return r == ';' || r == '\n' || r == '\r' }) {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			continue
		}
		switch parts[0] {
		case "9":
			id.Model = strings.TrimSpace(parts[1])
		case "12":
			id.Serial = strings.TrimSpace(parts[1])
		case "8":
			id.Vendor = strings.TrimSpace(parts[1])
		}
	}
	if id.Model == "" && id.Serial == "" {
		return id, fmt.Errorf("~HI carried no model or serial: %q", strings.TrimSpace(raw))
	}
	if logf != nil && id.Model != "" {
		logf("mdns: %s:%d identified as %s (serial %s)", p.host, p.port, id.Model, id.Serial)
	}
	return id, nil
}

type prCtx struct {
	ctx  context.Context
	host string
	port int
}

package virtual

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/brutella/dnssd"
)

// Advertise publishes the virtual printer over DNS-SD, exactly as a Link-OS printer does.
//
// This is what makes discovery testable: `vprint -mdns` + the server's mDNS probe is the
// whole printer-appears-on-the-network flow, with no hardware and no manual IP entry.
// Without it, discovery tests would need a real printer on the LAN.
//
//	go run ./cmd/vprint -render -mdns -mdns-name "Packing 1"
func (p *Printer) Advertise(ctx context.Context, log *slog.Logger, name string) (func(), error) {
	_, portStr := p.Addr()
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		return func() {}, fmt.Errorf("vprint advertise: bad port %q", portStr)
	}
	if name == "" {
		name = p.ID
	}
	cfg := dnssd.Config{
		Name:   name,
		Type:   mdnsServiceType,
		Domain: "local",
		Host:   p.ID,
		Port:   port,
		// A real printer advertises its identity here; keep the TXT records honest so
		// discovery can be tested against what firmware actually publishes.
		Text: map[string]string{
			"txtvers": "1",
			"ty":      "Zebra",
			"mfg":     p.Model,
			"sn":      p.Serial,
		},
	}
	svc, err := dnssd.NewService(cfg)
	if err != nil {
		return func() {}, err
	}
	rp, err := dnssd.NewResponder()
	if err != nil {
		return func() {}, err
	}
	if _, err := rp.Add(svc); err != nil {
		return func() {}, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		rp.Respond(ctx)
	}()
	if log != nil {
		log.Info("advertising over mDNS", "name", name, "type", mdnsServiceType, "port", port)
	}
	return func() { <-done }, nil
}

// mdnsServiceType is the DNS-SD type Link-OS printers use for their data stream.
const mdnsServiceType = "_pdl-datastream._tcp"

// Command vprint runs one or more virtual Zebra-style label printers.
//
//	go run ./vprint/cmd/vprint -id pack-03 -listen :19100 -admin :19101
//	go run ./vprint/cmd/vprint -count 3 -base-port 19100 -render -render-dir /tmp/labels
//
// Printers speak raw ZPL on the printer port (answering ^HS/~HI/~JA) and render every
// received label to an image for debugging:
//
//	go run ./vprint/cmd/vprint -render              # built-in zebrash engine, no container
//	go run ./vprint/cmd/vprint -render-backend labelize -labelize-url http://localhost:8090
//
// Then open http://localhost:19201/ for the label gallery, or use the control API:
//
//	curl -XPOST localhost:19201/control -d '{"state":"media_out"}'
//	curl -XPOST localhost:19201/control -d '{"offline":true}'
//	curl localhost:19201/labels/1/image -o label.png
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kucial/cloud-print-service/pkg/render"
	"github.com/kucial/cloud-print-service/vprint/virtual"
)

func main() {
	var (
		listen    = flag.String("listen", "", "printer listen address, e.g. :19100 (default one port per printer)")
		admin     = flag.String("admin", "", "control/gallery address, e.g. :19101")
		id        = flag.String("id", "vprint-01", "virtual printer id")
		count     = flag.Int("count", 1, "how many virtual printers to run")
		basePort  = flag.Int("base-port", 19100, "first printer port when -listen is not given")
		baseAdmin = flag.Int("base-admin-port", 19200, "first gallery port when -admin is not given")
		language  = flag.String("language", "zpl", "printer language: zpl (label printer) or escpos (receipt printer)")
		model     = flag.String("model", "", "model reported by ~HI / GS I (default ZT411, or TM-T82 for escpos)")
		serial    = flag.String("serial", "", "serial reported by ~HI (default derived from id)")
		state     = flag.String("state", "idle", "initial state: idle|printing|media_low|media_out|door_open|error")
		dur       = flag.Duration("print-duration", 150*time.Millisecond, "how long one label takes to print")

		renderOn       = flag.Bool("render", false, "render each received label to PNG for debugging")
		renderDir      = flag.String("render-dir", "", "also write rendered labels to this directory")
		backend        = flag.String("render-backend", "auto", "auto|zebrash|labelize")
		labelizeURL    = flag.String("labelize-url", env("LABELIZE_URL", ""), "labelize microservice base URL")
		renderW        = flag.Float64("render-width-mm", 101.6, "label width used for rendering")
		renderH        = flag.Float64("render-height-mm", 152.4, "label height used for rendering")
		renderDPMM     = flag.Int("render-dpmm", 8, "dots per mm for rendering: 6|8|12|24")
		renderDPI      = flag.Int("render-dpi", 0, "resolution in DPI (203|300|600); overrides -render-dpmm")
		dpiFlag        = flag.Int("dpi", 0, "alias for -render-dpi")
		paperSize      = flag.String("paper-size", "", "paper roll size (e.g. 100x150mm, 4x6, 100x50mm)")
		cjkFont        = flag.String("cjk-font", "", "CJK font loaded, e.g. SIMSUN.FNT, ANSD.TTF")
		orientation    = flag.String("orientation", "normal", "media orientation: normal|inverted|portrait|landscape")
		printDirection = flag.String("print-direction", "top_first", "feed direction: top_first|bottom_first")
		renderGray     = flag.Bool("render-grayscale", false, "antialiased greyscale instead of 1-bit")
		renderUp       = flag.Bool("render-upright", false, "do not rotate ^POI labels 180 (physically faithful by default)")
		printStartup   = flag.String("label", "", "send this ZPL once at startup (smoke check)")
		mdns           = flag.Bool("mdns", false, "advertise over mDNS so the server can discover this printer")
		mdnsName       = flag.String("mdns-name", "", "mDNS instance name (default: the printer id)")
	)
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if *renderOn {
		// Fail fast on a misconfigured backend instead of silently dropping previews.
		if _, err := render.Pick(*backend, *labelizeURL); err != nil {
			log.Error("renderer unavailable", "err", err)
			os.Exit(1)
		}
	}

	if *dpiFlag > 0 && *renderDPI == 0 {
		*renderDPI = *dpiFlag
	}

	if *renderDPI > 0 {
		switch *renderDPI {
		case 203:
			*renderDPMM = 8
		case 300:
			*renderDPMM = 12
		case 600:
			*renderDPMM = 24
		default:
			*renderDPMM = int(float64(*renderDPI)/25.4 + 0.5)
		}
	} else if *renderDPMM > 0 {
		switch *renderDPMM {
		case 8:
			*renderDPI = 203
		case 12:
			*renderDPI = 300
		case 24:
			*renderDPI = 600
		default:
			*renderDPI = int(float64(*renderDPMM)*25.4 + 0.5)
		}
	}
	if *paperSize != "" {
		cleaned := strings.ToLower(strings.TrimSpace(*paperSize))
		switch cleaned {
		case "4x6", "4x6in":
			*renderW, *renderH = 101.6, 152.4
		case "4x2", "4x2in":
			*renderW, *renderH = 101.6, 50.8
		case "2x1", "2x1in":
			*renderW, *renderH = 50.8, 25.4
		default:
			cleaned = strings.TrimSuffix(cleaned, "mm")
			if parts := strings.Split(cleaned, "x"); len(parts) == 2 {
				if w, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64); err == nil {
					if h, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64); err == nil && w > 0 && h > 0 {
						*renderW, *renderH = w, h
					}
				}
			}
		}
	} else if *renderW > 0 && *renderH > 0 {
		*paperSize = fmt.Sprintf("%gx%gmm", *renderW, *renderH)
	}

	if *language != "zpl" && *language != "escpos" {
		log.Error("-language must be zpl or escpos", "got", *language)
		os.Exit(2)
	}
	if *model == "" {
		*model = "ZT411"
		if *language == "escpos" {
			*model = "TM-T82"
		}
	}

	var printers []*virtual.Printer
	for i := 0; i < *count; i++ {
		pid := *id
		port := *basePort + i
		adminPort := *baseAdmin + i
		if *count > 1 {
			pid = fmt.Sprintf("%s-%d", *id, i+1)
		}
		addr := net.JoinHostPort("0.0.0.0", strconv.Itoa(port))
		if *listen != "" && i == 0 {
			addr = *listen
		}
		aAddr := net.JoinHostPort("0.0.0.0", strconv.Itoa(adminPort))
		if *admin != "" && i == 0 {
			aAddr = *admin
		}
		sn := *serial
		if sn == "" {
			sn = "VPRT" + strings.ToUpper(strings.ReplaceAll(pid, "-", ""))
		}

		p := virtual.New(pid, virtual.Opts{
			Addr: addr, Model: *model, Serial: sn, PrintDuration: *dur,
			DPI: *renderDPI, DPMM: *renderDPMM, PaperSize: *paperSize,
			Orientation: *orientation, PrintDirection: *printDirection,
			CJKFont: *cjkFont, Language: *language,
		})
		if err := p.Start(ctx); err != nil {
			log.Error("cannot start virtual printer", "id", pid, "err", err)
			os.Exit(1)
		}
		if *state != "" && *state != "idle" {
			p.Control(virtual.Control{State: *state})
		}
		if *renderOn {
			dir := *renderDir
			if dir != "" && *count > 1 {
				dir = dir + "/" + pid
			}
			if err := p.EnableRendering(ctx, virtual.RenderOpts{
				Backend: *backend, LabelizeURL: *labelizeURL, Dir: dir,
				Options: render.Options{
					WidthMM: *renderW, HeightMM: *renderH, DPMM: *renderDPMM,
					Grayscale: *renderGray, Format: "png", Label: pid, Upright: *renderUp,
				},
			}); err != nil {
				log.Error("cannot enable rendering", "id", pid, "err", err)
				os.Exit(1)
			}
		}
		printers = append(printers, p)

		_, pPort := p.Addr()
		gallery := virtual.NewGallery(p, pid)
		srv := &http.Server{Addr: aAddr, Handler: gallery, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Error("control API failed", "id", pid, "err", err)
			}
		}()
		defer func(s *http.Server) { _ = s.Close() }(srv)

		if *mdns {
			if _, err := p.Advertise(ctx, log, *mdnsName); err != nil {
				log.Warn("mDNS advertise failed", "id", pid, "err", err)
			}
		}
		log.Info("virtual printer up",
			"id", pid, "print", pPort, "gallery", aAddr,
			"language", *language, "model", *model, "serial", sn, "renderer", p.RendererName(), "mdns", *mdns)
	}

	if *printStartup != "" {
		time.Sleep(200 * time.Millisecond)
		host, port := printers[0].Addr()
		if conn, err := net.Dial("tcp", net.JoinHostPort(host, port)); err == nil {
			_, _ = conn.Write([]byte(*printStartup))
			time.Sleep(500 * time.Millisecond)
			_ = conn.Close()
			log.Info("startup label sent", "labels", printers[0].LabelCount())
		}
	}

	<-ctx.Done()
	log.Info("stopping virtual printers")
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

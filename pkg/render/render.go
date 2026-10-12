// Package render turns ZPL into an image so a virtual printer can show what it printed.
//
// Two backends, one interface:
//
//	ZEBrash  — in-process Go library (github.com/swiftpush/zebrash, MIT). Default.
//	          Handles the commands real carrier labels use (UPS/FedEx/DHL/Amazon/USPS),
//	          1-bit or greyscale PNG, plus PDF and SVG. No container, no network.
//	labelize — optional self-hosted microservice (docker run goodboy008/labelize).
//	          Broader command coverage and calibrated against the Labelary reference,
//	          so use it when you need exact parity. Costs a container.
//
// Rendering is best-effort by design: the simulator prints regardless, and a renderer
// that fails or is slow produces a warning, never a dropped label.
package render

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Options controls label geometry and output.
type Options struct {
	WidthMM   float64 // default 101.6 (4")
	HeightMM  float64 // default 152.4 (6")
	DPMM      int     // dots per mm: 6, 8 (203 dpi), 12, 24
	Grayscale bool    // antialiased 8-bit output instead of 1-bit monochrome
	Format    string  // png (default) | pdf | svg — svg/pdf only where supported
	Label     string  // printer id, for gallery headers

	// Upright renders a ^POI (inverted orientation) label the way most preview tools
	// do: not rotated. The default (false) is physically faithful — the label appears
	// in the orientation it actually comes out of the printer.
	//
	// Verified against zebrash's own reference (testdata/ups_grayscale.png): the two
	// settings produce identical pixels apart from that 180° rotation.
	Upright bool
}

// WithDefaults fills in the 4"x6" 203dpi defaults.
func (o Options) WithDefaults() Options { return o.withDefaults() }

func (o Options) withDefaults() Options {
	if o.WidthMM <= 0 {
		o.WidthMM = 101.6
	}
	if o.HeightMM <= 0 {
		o.HeightMM = 152.4
	}
	switch o.DPMM {
	case 0, 6, 8, 12, 24:
		if o.DPMM == 0 {
			o.DPMM = 8
		}
	default:
		o.DPMM = 8
	}
	if o.Format == "" {
		o.Format = "png"
	}
	return o
}

// Image is a rendered label plus provenance, so a caller can explain a missing image
// instead of silently showing nothing.
type Image struct {
	Data        []byte    `json:"-"`
	ContentType string    `json:"content_type"`
	Format      string    `json:"format"`
	WidthDots   int       `json:"width_dots"`
	HeightDots  int       `json:"height_dots"`
	Backend     string    `json:"backend"`
	DurationMS  int64     `json:"duration_ms"`
	Warnings    []string  `json:"warnings,omitempty"`
	Source      string    `json:"-"`
	RenderedAt  time.Time `json:"rendered_at"`
}

// guard converts a renderer panic into an error.
//
// The renderer is third-party code on a debugging path: a malformed label must produce
// a warning, never a dead service. (Observed upstream: zebrash divide-by-zero on some
// ^GF graphic fields.)
func guard(backend string, err *error) {
	if rec := recover(); rec != nil {
		*err = fmt.Errorf("%s renderer panicked: %v", backend, rec)
	}
}

// Renderer converts ZPL into an image.
type Renderer interface {
	Name() string
	Render(ctx context.Context, zpl string, opts Options) (Image, error)
}

// Pick returns the requested backend.
//
//	"" | "auto"   → labelize when --labelize-url answers /health, otherwise zebrash
//	"zebrash"     → the in-process library
//	"labelize"    → the microservice (fails loudly if the URL is unreachable)
func Pick(backend, labelizeURL string) (Renderer, error) {
	switch backend {
	case "", "auto":
		if labelizeURL != "" {
			lz := NewLabelize(labelizeURL)
			if lz.Healthy(context.Background()) {
				return lz, nil
			}
		}
		return NewZebrash(), nil
	case "zebrash":
		return NewZebrash(), nil
	case "labelize":
		if labelizeURL == "" {
			return nil, fmt.Errorf("backend %q requires --labelize-url", backend)
		}
		return NewLabelize(labelizeURL), nil
	default:
		return nil, fmt.Errorf("unknown render backend %q (want auto|zebrash|labelize)", backend)
	}
}

// ContentTypeFor maps a format to its HTTP content type.
func ContentTypeFor(format string) string {
	switch format {
	case "pdf":
		return "application/pdf"
	case "svg":
		return "image/svg+xml"
	default:
		return "image/png"
	}
}

// Zebrash wraps the in-process renderer. Drawers are safe for concurrent use, so one
// instance serves the whole printer.
type Zebrash struct {
	mu     sync.Mutex
	drawer zebrashDrawer
}

func NewZebrash() *Zebrash { return &Zebrash{} }

func (z *Zebrash) Name() string { return "zebrash" }

func (z *Zebrash) Render(ctx context.Context, zpl string, opts Options) (img Image, err error) {
	defer guard(z.Name(), &err)
	opts = opts.withDefaults()
	start := time.Now()

	if err := ctx.Err(); err != nil {
		return Image{}, err
	}
	labels, err := parseZebrash(zpl)
	if err != nil {
		return Image{}, fmt.Errorf("parse: %w", err)
	}
	if len(labels) == 0 {
		return Image{}, fmt.Errorf("no label found: payload has no ^XA…^XZ")
	}

	z.mu.Lock()
	if z.drawer == nil {
		z.drawer = newZebrashDrawer()
	}
	d := z.drawer
	z.mu.Unlock()

	// A payload may carry several formats (^XA…^XZ repeated). Render the first and
	// say so, rather than silently dropping the rest.
	res := Image{
		Format:     opts.Format,
		Backend:    z.Name(),
		Source:     zpl,
		RenderedAt: time.Now().UTC(),
	}
	dopts := zebrushOptions(opts)
	var buf sliceWriter

	switch opts.Format {
	case "pdf":
		err = d.drawPDF(ctx, labels, &buf, dopts)
		res.ContentType = "application/pdf"
	case "svg":
		err = d.drawSVG(ctx, labels, &buf, dopts)
		res.ContentType = "image/svg+xml"
	default:
		err = d.drawPNG(ctx, labels, &buf, dopts)
		res.ContentType = "image/png"
		res.Format = "png"
	}
	res.WidthDots = int(opts.WidthMM * float64(opts.DPMM))
	res.HeightDots = int(opts.HeightMM * float64(opts.DPMM))
	if err != nil {
		return Image{}, fmt.Errorf("draw %s: %w", opts.Format, err)
	}
	res.Data = buf.b
	res.DurationMS = time.Since(start).Milliseconds()
	if len(labels) > 1 {
		res.Warnings = append(res.Warnings,
			fmt.Sprintf("payload contains %d labels; rendered the first", len(labels)))
	}
	return res, nil
}

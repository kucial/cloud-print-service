// Command zplpng renders a ZPL file to PNG/SVG/PDF without a printer.
//
//	zplpng label.zpl -o label.png
//	cat label.zpl | zplpng - -o - --format svg > label.svg
//	zplpng label.zpl --backend labelize --labelize-url http://localhost:8090
//
// Handy for checking what the virtual printer will show before wiring up a fleet.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/kucial/cloud-print-service/pkg/render"
)

func main() {
	// Pull the input path out of the argument list before flag parsing, so flags may
	// appear before or after it. Values belonging to a flag are skipped, otherwise
	// `-o out.png` would be mistaken for the input file.
	valueFlags := map[string]bool{
		"-o": true, "--o": true, "-format": true, "--format": true,
		"-backend": true, "--backend": true, "-labelize-url": true, "--labelize-url": true,
		"-width-mm": true, "--width-mm": true, "-height-mm": true, "--height-mm": true,
		"-dpmm": true, "--dpmm": true, "-timeout": true, "--timeout": true,
	}
	args := os.Args[1:]
	var rest []string
	var input string
	skipNext := false
	for _, a := range args {
		if skipNext {
			rest = append(rest, a)
			skipNext = false
			continue
		}
		if valueFlags[a] {
			rest = append(rest, a)
			skipNext = true
			continue
		}
		if input == "" && (a == "-" || !strings.HasPrefix(a, "-")) {
			input = a
			continue
		}
		rest = append(rest, a)
	}
	os.Args = append([]string{os.Args[0]}, rest...)

	var (
		out      = flag.String("o", "-", "output file, or - for stdout")
		format   = flag.String("format", "png", "png|svg|pdf")
		backend  = flag.String("backend", "zebrash", "zebrash|labelize")
		labelize = flag.String("labelize-url", os.Getenv("LABELIZE_URL"), "labelize microservice base URL")
		width    = flag.Float64("width-mm", 101.6, "label width")
		height   = flag.Float64("height-mm", 152.4, "label height")
		dpmm     = flag.Int("dpmm", 8, "dots per mm: 6|8|12|24")
		gray     = flag.Bool("grayscale", false, "antialiased greyscale output")
		upright  = flag.Bool("upright", false, "do not rotate ^POI labels 180 (physically faithful by default)")
		timeout  = flag.Duration("timeout", 30*time.Second, "render timeout")
	)
	flag.Parse()

	// Allow the input file anywhere: `zplpng label.zpl -o out.png` and
	// `zplpng -o out.png label.zpl` both work (Go's flag stops at the first
	// positional argument).
	if input == "" {
		fmt.Fprintln(os.Stderr, "usage: zplpng <label.zpl|-> [flags]")
		os.Exit(2)
	}

	var zpl []byte
	var err error
	if input == "-" {
		zpl, err = io.ReadAll(os.Stdin)
	} else {
		zpl, err = os.ReadFile(input)
	}
	if err != nil {
		fail(err)
	}

	r, err := render.Pick(*backend, *labelize)
	if err != nil {
		fail(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	start := time.Now()
	img, err := r.Render(ctx, string(zpl), render.Options{
		WidthMM: *width, HeightMM: *height, DPMM: *dpmm,
		Grayscale: *gray, Format: *format, Upright: *upright,
	})
	if err != nil {
		fail(fmt.Errorf("render: %w", err))
	}
	for _, w := range img.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}

	if *out == "-" {
		if _, err := os.Stdout.Write(img.Data); err != nil {
			fail(err)
		}
		fmt.Fprintf(os.Stderr, "rendered %s via %s in %s\n",
			img.Format, img.Backend, time.Since(start).Round(time.Millisecond))
		return
	}
	if err := os.WriteFile(*out, img.Data, 0o644); err != nil {
		fail(err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d bytes, %s, %dx%d dots, %s, %s)\n",
		*out, len(img.Data), img.Backend, img.WidthDots, img.HeightDots,
		img.Format, time.Since(start).Round(time.Millisecond))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

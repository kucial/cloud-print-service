package httpapi

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kucial/cloud-print-service/pkg/printer"
	"github.com/kucial/cloud-print-service/server/internal/fleet"
)

// The test label is an alignment target, not a greeting: printed on the real roll it
// shows at a glance whether the print sits on the label, and by how much it is off.
//
//   - a border inset 1 mm from every edge: a missing or clipped side means the print is
//     offset towards that side
//   - mm rulers along the top and left edges, starting at the label edge: the first
//     visible tick is how many mm were cut off — multiply by dots/mm for ^LT / ^LS
//   - a centre crosshair, a TOP marker and the size/dpi line, to check centring,
//     feed direction and scale
//   - a line of Chinese in the printer's CJK font, to check the font prints (blanks
//     or boxes mean the font or its encoding table is missing)
//   - a QR code (printer id + print time) and a Code 128 barcode (print time as
//     MMDDHHMM), to check both scan — the printed digits say what the barcode should read

type testLabelGeometry struct {
	WidthMM, HeightMM float64
	DPMM              float64
	Source            string // declared | printer | default
}

// testLabelCJK is the font the CJK line uses; Setup maps it to font slot 1.
type testLabelCJK struct {
	Font  string // device-qualified, e.g. E:SIMSUN.FNT; "" when the printer has none
	Setup string
	Note  string // why there is no CJK line, when Font is ""
}

// testLabelFont picks the CJK font: the declared one if the printer has it, else the
// printer's best CJK font (^HW).
func testLabelFont(ctx context.Context, p *fleet.Printer) testLabelCJK {
	qctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	raw, err := p.HostDirectory(qctx)
	cancel()
	if err != nil {
		return testLabelCJK{Note: "font list unavailable"}
	}
	files := printer.ParseHostDirectory(raw)
	if want := p.Spec().CJKFont; want != "" {
		if f, ok := printer.FindFile(files, want); ok {
			return testLabelCJK{Font: f.Name, Setup: printer.CJKFieldPrefix(f, files)}
		}
		return testLabelCJK{Note: "declared " + want + " not installed"}
	}
	if cjk := printer.CJKFonts(files); len(cjk) > 0 {
		return testLabelCJK{Font: cjk[0].Name, Setup: printer.CJKFieldPrefix(cjk[0], files)}
	}
	return testLabelCJK{Note: "no CJK font on printer"}
}

// testLabelSize picks the media to draw for: what the fleet declares, else what the
// printer reports (^HH), else 4x6in.
func testLabelSize(ctx context.Context, p *fleet.Printer) testLabelGeometry {
	spec := p.Spec()
	g := testLabelGeometry{WidthMM: spec.MediaWidthMM, HeightMM: spec.MediaHeightMM, DPMM: spec.DPMM, Source: "declared"}
	if g.DPMM <= 0 && spec.DPI > 0 {
		g.DPMM = float64(spec.DPI) / 25.4
	}
	if g.WidthMM <= 0 || g.HeightMM <= 0 || g.DPMM <= 0 {
		qctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		raw, err := p.HostConfig(qctx)
		cancel()
		if c, ok := printer.ParseHostConfig(raw); err == nil && ok && c.DPMM > 0 {
			if g.DPMM <= 0 {
				g.DPMM = c.DPMM
			}
			if g.WidthMM <= 0 || g.HeightMM <= 0 {
				// Label length from ^HH is the pitch (label + gap); a typical gap is ~2 mm.
				g.WidthMM, g.HeightMM, g.Source = c.PrintWidthMM, c.LabelLengthMM-2, "printer"
			}
		}
	}
	if g.DPMM <= 0 {
		g.DPMM = 8
	}
	if g.WidthMM <= 0 || g.HeightMM <= 0 {
		g.WidthMM, g.HeightMM, g.Source = 101.6, 152.4, "default"
	}
	return g
}

func testLabelZPL(name, printerID string, g testLabelGeometry, cjk testLabelCJK, now time.Time) string {
	d := func(mm float64) int { return int(mm*g.DPMM + 0.5) }
	w, h := d(g.WidthMM), d(g.HeightMM)
	m := d(1) // border inset
	short := min(w, h)
	fs := max(short/14, 16) // body text height, dots
	small := max(fs*2/3, 14)

	var b strings.Builder
	f := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	f("^XA^CI28^PW%d^LL%d^LH0,0", w, h)

	// Border, 1 mm in from every edge.
	f("^FO%d,%d^GB%d,%d,3^FS", m, m, w-2*m, h-2*m)

	// Rulers from the label edge: a tick per mm, longer every 5 mm, numbered every 5.
	tick := func(mm int) int {
		if mm%5 == 0 {
			return d(2.5)
		}
		return d(1.2)
	}
	// Offsets are a few mm, so 20 mm of ruler is plenty and keeps the label small.
	rulerX, rulerY := min(g.WidthMM*0.6, 20), min(g.HeightMM*0.6, 20)
	for mm := 0; float64(mm) <= rulerX; mm++ {
		x := d(float64(mm))
		f("^FO%d,0^GB1,%d,2^FS", x, tick(mm))
		if mm%5 == 0 && mm > 0 {
			f("^FO%d,%d^A0N,%d,%d^FD%d^FS", x-small/4, d(2.8), small, small*3/4, mm)
		}
	}
	for mm := 0; float64(mm) <= rulerY; mm++ {
		y := d(float64(mm))
		f("^FO0,%d^GB%d,1,2^FS", y, tick(mm))
		if mm%5 == 0 && mm > 0 {
			f("^FO%d,%d^A0N,%d,%d^FD%d^FS", d(2.8), y-small/3, small, small*3/4, mm)
		}
	}

	// Centre crosshair.
	cx, cy, arm := w/2, h/2, d(3)
	f("^FO%d,%d^GB%d,1,2^FS", cx-arm, cy, 2*arm)
	f("^FO%d,%d^GB1,%d,2^FS", cx, cy-arm, 2*arm)

	// Text block, right of the left ruler and below the top one.
	tx, ty := d(9), d(7)
	f("^FO%d,%d^A0N,%d,%d^FDTOP  CPS alignment test^FS", tx, ty, fs, fs)
	f("^FO%d,%d^A0N,%d,%d^FD%s^FS", tx, ty+fs+4, small, small, zplText(name))
	f("^FO%d,%d^A0N,%d,%d^FD%g x %g mm  %d dpi (%s)^FS", tx, ty+fs+small+10, small, small,
		g.WidthMM, g.HeightMM, printer.DPIFromDPMM(g.DPMM), g.Source)
	f("^FO%d,%d^A0N,%d,%d^FD%s^FS", tx, ty+fs+2*small+16, small, small, now.Format("2006-01-02 15:04:05"))

	// Codes in the right-hand column, clear of the text block and the crosshair: QR at
	// the top, Code 128 under it. Eight digits encode as four Code 128 C symbols, so
	// the bars stay 2 dots wide (reliably scannable at 203 dpi) on a 60 mm label.
	qrData := "CPS " + zplText(printerID) + " " + now.Format("20060102150405")
	qrMag := max(2, short/90)
	qrSide := qrModules(len(qrData)) * qrMag
	qx, qy := w-m-d(2)-qrSide, m+d(2)
	f("^FO%d,%d^BQN,2,%d^FDMA,%s^FS", qx, qy, qrMag, qrData)
	by := max(2, w/240)
	barW := (4+3)*11*by + 2*by
	barH := max(40, h/8)
	f("^BY%d^FO%d,%d^BCN,%d,Y,N,N,A^FD%s^FS", by, w-m-d(2)-barW, qy+qrSide+d(3), barH, now.Format("01021504"))

	// CJK check, bottom left, clear of the crosshair. Its font setup comes last so the
	// ASCII fields above are unaffected.
	cjkH := max(fs, 24)
	cy2 := h - m - d(2) - cjkH
	if cjk.Font != "" {
		f("^FO%d,%d^A0N,%d,%d^FDCJK %s^FS", tx, cy2-small-6, small, small, cjk.Font)
		f("%s^FO%d,%d^A1N,%d,%d^FD中文测试 简体 繁體^FS", cjk.Setup, tx, cy2, cjkH, cjkH)
	} else {
		f("^FO%d,%d^A0N,%d,%d^FDCJK: %s^FS", tx, cy2, small, small, zplText(cjk.Note))
	}
	f("^XZ")
	return b.String()
}

// qrModules is the side, in modules, of the smallest QR code holding n bytes at error
// correction M (the "MA" in ^FD): versions 1–6 hold 14, 26, 42, 62, 84, 106 bytes.
func qrModules(n int) int {
	for v, limit := range []int{14, 26, 42, 62, 84, 106} {
		if n <= limit {
			return 17 + 4*(v+1)
		}
	}
	return 17 + 4*7
}

// zplText keeps field data from closing its own field.
func zplText(s string) string {
	return strings.NewReplacer("^", " ", "~", " ").Replace(s)
}

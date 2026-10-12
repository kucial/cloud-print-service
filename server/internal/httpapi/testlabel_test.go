package httpapi

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kucial/cloud-print-service/pkg/render"
)

// The alignment label must fit the media exactly and render cleanly. Set
// CPS_TESTLABEL_PNG=/path.png to look at it.
func TestAlignmentLabelRendersAtMediaSize(t *testing.T) {
	for _, g := range []testLabelGeometry{
		{WidthMM: 60, HeightMM: 40, DPMM: 8, Source: "declared"},
		{WidthMM: 101.6, HeightMM: 152.4, DPMM: 12, Source: "default"},
	} {
		zpl := testLabelZPL("GK888t (USB)", "mac-usb-19J152400112", g, testLabelCJK{Font: "E:SIMSUN.FNT",
			Setup: "^CW1,E:SIMSUN.FNT^CI28^SEE:GB18030.DAT"}, time.Date(2026, 10, 12, 23, 0, 0, 0, time.UTC))
		if !strings.Contains(zpl, "^BQN,2,") || !strings.Contains(zpl, "^BCN,") {
			t.Fatalf("no QR code or barcode: %s", zpl)
		}
		if !strings.Contains(zpl, "^CW1,E:SIMSUN.FNT^CI28^SEE:GB18030.DAT") || !strings.Contains(zpl, "中文") {
			t.Fatalf("no CJK line: %s", zpl)
		}
		if len(zpl) > 4096 {
			t.Fatalf("%gx%g: test label is %d bytes; keep it small enough for tight payload limits", g.WidthMM, g.HeightMM, len(zpl))
		}
		if !strings.Contains(zpl, "^GB") || !strings.HasPrefix(zpl, "^XA") || !strings.HasSuffix(zpl, "^XZ") {
			t.Fatalf("not a bordered label: %s", zpl)
		}
		img, err := render.NewZebrash().Render(context.Background(), zpl, render.Options{
			WidthMM: g.WidthMM, HeightMM: g.HeightMM, DPMM: int(g.DPMM), Upright: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		wantW, wantH := int(g.WidthMM*g.DPMM+0.5), int(g.HeightMM*g.DPMM+0.5)
		near := func(a, b int) bool { return a-b <= 1 && b-a <= 1 } // renderer truncates, we round
		if !near(img.WidthDots, wantW) || !near(img.HeightDots, wantH) {
			t.Fatalf("%gx%g: rendered %dx%d dots, want %dx%d", g.WidthMM, g.HeightMM, img.WidthDots, img.HeightDots, wantW, wantH)
		}
		if p := os.Getenv("CPS_TESTLABEL_PNG"); p != "" && g.WidthMM == 60 {
			_ = os.WriteFile(p, img.Data, 0o644)
		}
	}
}

func TestZPLTextCannotBreakOutOfAField(t *testing.T) {
	if got := zplText("a^XZ~JA"); strings.ContainsAny(got, "^~") {
		t.Fatalf("control characters survived: %q", got)
	}
}

func TestAlignmentLabelWithoutCJKFontSaysWhy(t *testing.T) {
	g := testLabelGeometry{WidthMM: 60, HeightMM: 40, DPMM: 8}
	zpl := testLabelZPL("p", "p", g, testLabelCJK{Note: "no CJK font on printer"}, time.Now())
	if strings.Contains(zpl, "^A1") || !strings.Contains(zpl, "CJK: no CJK font on printer") {
		t.Fatalf("want an ASCII note instead of CJK glyphs: %s", zpl)
	}
}

func TestQRModules(t *testing.T) {
	for n, want := range map[int]int{14: 21, 15: 25, 39: 29, 42: 29, 43: 33, 106: 41} {
		if got := qrModules(n); got != want {
			t.Errorf("qrModules(%d) = %d, want %d", n, got, want)
		}
	}
}

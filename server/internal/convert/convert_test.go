package convert

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/kucial/cloud-print-service/pkg/escpr"
	"github.com/kucial/cloud-print-service/pkg/printer"
	"github.com/kucial/cloud-print-service/server/internal/domain"
)

// testImage is a 200x100 picture: a black bar on the left half, white on the right,
// and a transparent strip at the bottom (which must print as paper, not black).
func testImage(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			switch {
			case y >= 80:
				img.Set(x, y, color.NRGBA{}) // transparent
			case x < 100:
				img.Set(x, y, color.Black)
			default:
				img.Set(x, y, color.White)
			}
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

var (
	receipt58 = domain.Printer{Kind: printer.KindReceipt, Formats: []printer.Format{printer.FormatESCPOS}, MediaWidthMM: 58}
	label60   = domain.Printer{Kind: printer.KindLabel, Formats: []printer.Format{printer.FormatZPL}, MediaWidthMM: 60, DPMM: 8}
	ippPDF    = domain.Printer{Kind: printer.KindDocument, Formats: []printer.Format{printer.FormatPDF, printer.FormatURF}}
	pclOnly   = domain.Printer{Kind: printer.KindDocument, Formats: []printer.Format{printer.FormatPCL}}
	inkjet    = domain.Printer{Kind: printer.KindDocument, Formats: []printer.Format{printer.FormatESCPR}}
)

func TestTargets(t *testing.T) {
	images := Converter{}
	withPoppler := Converter{PDFToPPM: "/x/pdftoppm", PDFToPS: "/x/pdftops"}
	cases := []struct {
		c    Converter
		src  printer.Format
		p    domain.Printer
		want printer.Format
	}{
		{images, printer.FormatPNG, receipt58, printer.FormatESCPOS},
		{images, printer.FormatJPEG, label60, printer.FormatZPL},
		{images, printer.FormatPNG, ippPDF, printer.FormatPDF},
		{images, printer.FormatPNG, pclOnly, printer.FormatPCL},
		{images, printer.FormatPDF, ippPDF, printer.FormatPDF}, // native
		{images, printer.FormatPDF, receipt58, ""},             // needs poppler
		{withPoppler, printer.FormatPDF, receipt58, printer.FormatESCPOS},
		{withPoppler, printer.FormatPDF, label60, printer.FormatZPL},
		{withPoppler, printer.FormatPDF, pclOnly, printer.FormatPCL},
		{withPoppler, printer.FormatPDF, domain.Printer{Kind: printer.KindDocument,
			Formats: []printer.Format{printer.FormatPCL, printer.FormatPostScript}}, printer.FormatPostScript},
		{images, printer.FormatPNG, inkjet, printer.FormatESCPR},
		{images, printer.FormatPDF, inkjet, ""}, // needs poppler
		{withPoppler, printer.FormatPDF, inkjet, printer.FormatESCPR},
		{withPoppler, printer.FormatZPL, receipt58, ""}, // languages do not convert
		{withPoppler, printer.FormatESCPOS, label60, ""},
	}
	for _, c := range cases {
		got, ok := c.c.Target(c.src, c.p)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("%s to %s printer %v (poppler %v): got %q %v", c.src, c.p.Kind, c.p.Formats, c.c.PDFToPPM != "", got, ok)
		}
	}
}

func TestImageToESCPOS(t *testing.T) {
	r, err := Converter{}.Convert(context.Background(), testImage(t), printer.FormatPNG, printer.FormatESCPOS, receipt58, 1)
	if err != nil {
		t.Fatal(err)
	}
	b := r.Data
	i := bytes.Index(b, []byte{0x1d, 'v', '0', 0})
	if i < 0 {
		t.Fatalf("no GS v 0 raster in %x", b[:min(40, len(b))])
	}
	// 58 mm roll: 384 dots = 48 bytes a row. The 200 px picture is centred at 2x
	// (400 > 384, so it is scaled to fit: 384 wide), the transparent strip trimmed.
	xL, xH, yL, yH := int(b[i+4]), int(b[i+5]), int(b[i+6]), int(b[i+7])
	rb, h := xL+xH*256, yL+yH*256
	if rb != 48 || h < 150 || h > 160 {
		t.Fatalf("raster %d bytes x %d rows", rb, h)
	}
	first := b[i+8 : i+8+rb]
	if first[0] != 0xff || first[rb-1] != 0x00 {
		t.Fatalf("left half should be black, right half white: % x", first)
	}
	if !bytes.HasSuffix(b, []byte{0x1d, 'V', 66, 3}) {
		t.Fatal("receipt must end with a cut")
	}
}

func TestImageToZPL(t *testing.T) {
	r, err := Converter{}.Convert(context.Background(), testImage(t), printer.FormatPNG, printer.FormatZPL, label60, 3)
	if err != nil {
		t.Fatal(err)
	}
	z := string(r.Data)
	// 60 mm at 8 dpmm = 480 dots = 60 bytes a row. No label length declared: the
	// picture is fitted to the width (at most 2x, so 400 dots, centred).
	if !strings.HasPrefix(z, "^XA^PW480^LL200^LH0,0^FO0,0^GFA,12000,12000,60,") || !strings.HasSuffix(z, "^FS^PQ3^XZ\n") {
		t.Fatalf("ZPL: %.80s … %s", z, z[len(z)-20:])
	}
	if !strings.Contains(z[:200], "FF") {
		t.Fatal("expected black dots")
	}

	// A declared 60x40 label: the picture fits inside it, the label is 320 dots long.
	l := label60
	l.MediaHeightMM = 40
	r, _ = Converter{}.Convert(context.Background(), testImage(t), printer.FormatPNG, printer.FormatZPL, l, 1)
	if !strings.HasPrefix(string(r.Data), "^XA^PW480^LL320^") {
		t.Fatalf("fitted label: %.40s", r.Data)
	}
}

func TestImageToPDFAndPCL(t *testing.T) {
	ctx := context.Background()
	r, err := Converter{}.Convert(ctx, testImage(t), printer.FormatPNG, printer.FormatPDF, ippPDF, 1)
	if err != nil || !bytes.HasPrefix(r.Data, []byte("%PDF-")) {
		t.Fatalf("pdf: %v %q", err, r.Data[:min(8, len(r.Data))])
	}
	r, err = Converter{}.Convert(ctx, testImage(t), printer.FormatPNG, printer.FormatPCL, pclOnly, 1)
	if err != nil || !bytes.HasPrefix(r.Data, []byte("\x1bE")) || !bytes.Contains(r.Data, []byte("\x1b*r1A")) ||
		!bytes.HasSuffix(r.Data, []byte("\f\x1bE")) {
		t.Fatalf("pcl: %v", err)
	}
}

func TestBadImageAndMissingTools(t *testing.T) {
	_, err := Converter{}.Convert(context.Background(), []byte("not a png"), printer.FormatPNG, printer.FormatESCPOS, receipt58, 1)
	if !errors.Is(err, ErrBadDocument) {
		t.Fatalf("bad image: %v", err)
	}
	_, err = Converter{}.Convert(context.Background(), []byte("%PDF-1.4"), printer.FormatPDF, printer.FormatESCPOS, receipt58, 1)
	if !errors.Is(err, ErrNoPDFTools) {
		t.Fatalf("pdf without poppler: %v", err)
	}
}

// With poppler installed, a real PDF goes to a receipt printer as raster.
func TestPDFToReceiptWithPoppler(t *testing.T) {
	c := FindTools("")
	if c.PDFToPPM == "" {
		t.Skip("pdftoppm not installed")
	}
	pdf, err := Converter{}.Convert(context.Background(), testImage(t), printer.FormatPNG, printer.FormatPDF, ippPDF, 1)
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Convert(context.Background(), pdf.Data, printer.FormatPDF, printer.FormatESCPOS, receipt58, 1)
	if err != nil || r.Pages != 1 || !bytes.Contains(r.Data, []byte{0x1d, 'v', '0'}) {
		t.Fatalf("pdf to receipt: %v pages=%d", err, r.Pages)
	}
}

func TestImageToESCPR(t *testing.T) {
	r, err := Converter{}.Convert(context.Background(), testImage(t), printer.FormatPNG, printer.FormatESCPR, inkjet, 1)
	if err != nil || r.Pages != 1 || r.Format != printer.FormatESCPR {
		t.Fatalf("escpr: %v pages=%d format=%s", err, r.Pages, r.Format)
	}
	if !bytes.Contains(r.Data, []byte("ESCPR")) || !bytes.Contains(r.Data, []byte("dsnd")) {
		t.Fatal("job carries no ESC/P-R raster")
	}
}

// A picture fills the printable width; a whole page keeps its own margins, so the
// sheet's margin is cut from it rather than added to it.
func TestOnPaper(t *testing.T) {
	pw, ph := escpr.A4.Printable()
	// 2:1 picture, black: scaled to the printable width, half as tall.
	pic := image.NewGray(image.Rect(0, 0, 200, 100))
	out := onPaper(pic, escpr.A4, false)
	if b := out.Bounds(); b.Dx() != pw || b.Dy() != ph {
		t.Fatalf("canvas %v, want printable %dx%d", b, pw, ph)
	}
	if !isBlack(out.At(0, 0)) || !isBlack(out.At(pw-1, pw/2-2)) || isBlack(out.At(pw/2, pw/2+2)) {
		t.Error("picture does not fill the printable width at its aspect ratio")
	}
	// A whole A4 page at 360 dpi with ink only from 100 dots in: printed 100 dots from
	// the paper edge, which is 100 - margin into the printable area.
	page := image.NewRGBA(image.Rect(0, 0, escpr.A4.Width, escpr.A4.Height))
	for y := range escpr.A4.Height {
		for x := range escpr.A4.Width {
			if x < 100 || y < 100 {
				page.Set(x, y, color.White)
			} else {
				page.Set(x, y, color.Black)
			}
		}
	}
	out = onPaper(page, escpr.A4, true)
	m := escpr.A4.Margin
	if isBlack(out.At(100-m-1, 100-m)) || !isBlack(out.At(100-m, 100-m)) {
		t.Error("a whole page shifted: its ink does not start at its own place on the sheet")
	}
}

func isBlack(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return r < 0x1000 && g < 0x1000 && b < 0x1000
}

// With poppler installed, a PDF goes to an inkjet as one ESC/P-R page per PDF page,
// and a black-and-white document prints with black ink only.
func TestPDFToInkjetWithPoppler(t *testing.T) {
	c := FindTools("")
	if c.PDFToPPM == "" {
		t.Skip("pdftoppm not installed")
	}
	pdf, err := Converter{}.Convert(context.Background(), testImage(t), printer.FormatPNG, printer.FormatPDF, ippPDF, 1)
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Convert(context.Background(), pdf.Data, printer.FormatPDF, printer.FormatESCPR, inkjet, 1)
	if err != nil || r.Pages != 1 || bytes.Count(r.Data, []byte("sttp")) != 1 {
		t.Fatalf("pdf to inkjet: %v pages=%d", err, r.Pages)
	}
	i := bytes.Index(r.Data, []byte("setq"))
	if i < 0 || r.Data[i+4+2] != 1 {
		t.Fatal("a black-and-white PDF does not print black ink only")
	}
}

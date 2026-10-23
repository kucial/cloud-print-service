// Package convert turns what a business system sends (PDF, PNG, JPEG) into what a
// printer takes: an ESC/POS raster for a receipt printer, a ZPL graphic for a label
// printer, PDF/JPEG/PostScript/PCL for an office printer, ESC/P-R colour raster for an
// Epson inkjet. Conversion happens once, when
// the job is accepted, so the stored payload is exactly what the printer gets and a
// retry resends the same bytes.
package convert

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"slices"
	"strings"

	"github.com/go-pdf/fpdf"

	"github.com/kucial/cloud-print-service/pkg/escpos"
	"github.com/kucial/cloud-print-service/pkg/escpr"
	"github.com/kucial/cloud-print-service/pkg/printer"
	"github.com/kucial/cloud-print-service/server/internal/domain"
)

var (
	// ErrNoConversion is a format CPS cannot turn into anything the printer takes.
	ErrNoConversion = errors.New("no conversion to a format this printer takes")
	// ErrNoPDFTools is a PDF that needs rasterising on a server without poppler.
	ErrNoPDFTools = errors.New("this server cannot convert PDF: install poppler-utils (pdftoppm, pdftops)")
	// ErrBadDocument is a document that does not decode.
	ErrBadDocument = errors.New("document cannot be read")
)

// MaxPages bounds a PDF converted to raster: a receipt or label run of hundreds of
// pages is far more likely a mistake than an intent.
const MaxPages = 50

// Converter holds the external tools; its zero value converts images only.
type Converter struct {
	PDFToPPM string // path to pdftoppm, "" when not installed
	PDFToPS  string // path to pdftops
}

// Result is a converted document.
type Result struct {
	Data   []byte
	Format printer.Format
	Pages  int
}

// Target picks the format to send a src document as, or reports none. Native formats
// always win; otherwise the printer kind decides.
func (c Converter) Target(src printer.Format, p domain.Printer) (printer.Format, bool) {
	if slices.Contains(p.Formats, src) {
		return src, true
	}
	takes := func(f printer.Format) bool { return slices.Contains(p.Formats, f) }
	if !src.IsImage() && src != printer.FormatPDF {
		return "", false // printer languages are not converted into one another
	}
	pdfOK := src != printer.FormatPDF || c.PDFToPPM != ""
	switch p.Kind {
	case printer.KindReceipt:
		if takes(printer.FormatESCPOS) && pdfOK {
			return printer.FormatESCPOS, true
		}
	case printer.KindLabel:
		if takes(printer.FormatZPL) && pdfOK {
			return printer.FormatZPL, true
		}
	case printer.KindDocument:
		if takes(printer.FormatESCPR) && pdfOK {
			return printer.FormatESCPR, true // the only thing such a printer takes
		}
		if src.IsImage() {
			for _, f := range []printer.Format{printer.FormatPDF, printer.FormatJPEG, printer.FormatPCL} {
				if takes(f) {
					return f, true
				}
			}
			return "", false
		}
		if takes(printer.FormatPostScript) && c.PDFToPS != "" {
			return printer.FormatPostScript, true
		}
		if takes(printer.FormatPCL) && pdfOK {
			return printer.FormatPCL, true
		}
	}
	return "", false
}

// Convert turns data (in src) into the target format for printer p. copies is folded
// into the document where the format carries it (ZPL ^PQ).
func (c Converter) Convert(ctx context.Context, data []byte, src, target printer.Format, p domain.Printer, copies int) (Result, error) {
	if src == target {
		return Result{Data: data, Format: target, Pages: 1}, nil
	}
	// Office printers that take PostScript get the PDF as PostScript: vector, small,
	// and every page.
	if src == printer.FormatPDF && target == printer.FormatPostScript {
		ps, err := c.pdfToPS(ctx, data)
		return Result{Data: ps, Format: target, Pages: 1}, err
	}
	if target == printer.FormatESCPR {
		return c.toESCPR(ctx, data, src)
	}
	pages, err := c.pages(ctx, data, src, rasterDPI(target, p))
	if err != nil {
		return Result{}, err
	}
	var out []byte
	switch target {
	case printer.FormatESCPOS:
		out = toESCPOS(pages, receiptDots(p))
	case printer.FormatZPL:
		out = toZPL(pages, labelDots(p), labelLengthDots(p), copies)
	case printer.FormatPCL:
		out = toPCL(pages, 300)
	case printer.FormatPDF:
		out, err = imagesToPDF(pages)
	case printer.FormatJPEG:
		if len(pages) != 1 {
			return Result{}, fmt.Errorf("%w: a %d-page document cannot be sent as one JPEG", ErrNoConversion, len(pages))
		}
		var b bytes.Buffer
		err = jpeg.Encode(&b, pages[0], &jpeg.Options{Quality: 90})
		out = b.Bytes()
	default:
		return Result{}, fmt.Errorf("%w: %s to %s", ErrNoConversion, src, target)
	}
	if err != nil {
		return Result{}, err
	}
	return Result{Data: out, Format: target, Pages: len(pages)}, nil
}

// pages decodes an image, or rasterises a PDF at dpi, into one image per page.
func (c Converter) pages(ctx context.Context, data []byte, src printer.Format, dpi int) ([]image.Image, error) {
	if src.IsImage() {
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBadDocument, err)
		}
		return []image.Image{img}, nil
	}
	if src != printer.FormatPDF {
		return nil, ErrNoConversion
	}
	return c.rasterisePDF(ctx, data, dpi)
}

// rasterDPI is the resolution a PDF is rendered at for the target: the printer's own
// for thermal printers, 300 for office raster.
func rasterDPI(target printer.Format, p domain.Printer) int {
	switch target {
	case printer.FormatESCPOS, printer.FormatZPL:
		if p.DPI > 0 {
			return p.DPI
		}
		return 203
	}
	return 150 // images wrapped in PDF/JPEG: plenty for a scanned page, a sane size
}

// receiptDots is the printable width of a receipt roll: 384 dots on 58 mm paper, 576
// on 80 mm, at 203 dpi.
func receiptDots(p domain.Printer) int {
	dpmm := 8.0
	if p.DPMM > 0 {
		dpmm = p.DPMM
	}
	printable := 72.0
	if p.MediaWidthMM > 0 && p.MediaWidthMM < 70 {
		printable = 48
	}
	return int(printable * dpmm)
}

// labelDots is the label's width in dots (the whole label width; ^PW).
func labelDots(p domain.Printer) int {
	dpmm := p.DPMM
	if dpmm <= 0 {
		dpmm = 8
	}
	w := p.MediaWidthMM
	if w <= 0 {
		w = 101.6 // 4 in
	}
	return int(w * dpmm)
}

// toESCPOS prints each page as a raster image (GS v 0) in bands, and cuts once at the
// end. Bands keep each command within the size every printer accepts.
func toESCPOS(pages []image.Image, width int) []byte {
	b := escpos.New().Align(escpos.Left).Bytes()
	var out bytes.Buffer
	out.Write(b)
	const band = 256
	for i, pg := range pages {
		bm := dither(fitWidth(pg, width))
		bm.trimBottom()
		for y := 0; y < bm.h; y += band {
			h := min(band, bm.h-y)
			rb := bm.rowBytes()
			out.Write([]byte{0x1d, 'v', '0', 0, byte(rb), byte(rb >> 8), byte(h), byte(h >> 8)})
			for _, row := range bm.rows[y : y+h] {
				out.Write(row)
			}
		}
		if i < len(pages)-1 {
			out.Write([]byte{0x1b, 'd', 2}) // a little space between pages
		}
	}
	out.Write([]byte{0x1b, 'd', 4, 0x1d, 'V', 66, 3}) // feed past the cutter, cut
	return out.Bytes()
}

// labelLengthDots is the label's length in dots, 0 when not declared.
func labelLengthDots(p domain.Printer) int {
	dpmm := p.DPMM
	if dpmm <= 0 {
		dpmm = 8
	}
	return int(p.MediaHeightMM * dpmm)
}

// toZPL makes one label per page, the picture as a ^GFA graphic, copies as ^PQ. With
// a known label length the picture is fitted inside the label; without one, to its
// width.
func toZPL(pages []image.Image, width, length, copies int) []byte {
	if copies < 1 {
		copies = 1
	}
	var out bytes.Buffer
	for _, pg := range pages {
		var g *image.Gray
		if length > 0 {
			g = fitBox(pg, width, length)
		} else {
			g = fitWidth(pg, width)
		}
		bm := dither(g)
		rb := bm.rowBytes()
		total := rb * bm.h
		fmt.Fprintf(&out, "^XA^PW%d^LL%d^LH0,0^FO0,0^GFA,%d,%d,%d,", width, bm.h, total, total, rb)
		for _, row := range bm.rows {
			out.WriteString(strings.ToUpper(hex.EncodeToString(row)))
		}
		fmt.Fprintf(&out, "^FS^PQ%d^XZ\n", copies)
	}
	return out.Bytes()
}

// toPCL is PCL 5 raster graphics, one page per image, at dpi: what every PCL printer
// can print without fonts or vector support.
func toPCL(pages []image.Image, dpi int) []byte {
	var out bytes.Buffer
	out.WriteString("\x1bE\x1b&l26A\x1b&l0O") // reset, A4, portrait
	fmt.Fprintf(&out, "\x1b*t%dR", dpi)       // raster resolution
	widthDots := int(8.27 * float64(dpi))     // A4 width
	for i, pg := range pages {
		bm := dither(fitWidth(pg, widthDots))
		fmt.Fprintf(&out, "\x1b*r%dS\x1b*p0x0Y\x1b*r1A", bm.w) // width, top left, start
		for _, row := range bm.rows {
			fmt.Fprintf(&out, "\x1b*b%dW", len(row))
			out.Write(row)
		}
		out.WriteString("\x1b*rB") // end raster
		if i < len(pages)-1 {
			out.WriteString("\f")
		}
	}
	out.WriteString("\f\x1bE")
	return out.Bytes()
}

// imagesToPDF puts each image on its own A4 page, scaled to fit inside 10 mm margins.
func imagesToPDF(pages []image.Image) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(0, 0, 0)
	pdf.SetAutoPageBreak(false, 0)
	for i, pg := range pages {
		var j bytes.Buffer
		if err := jpeg.Encode(&j, pg, &jpeg.Options{Quality: 90}); err != nil {
			return nil, err
		}
		name := fmt.Sprintf("p%d", i)
		pdf.RegisterImageOptionsReader(name, fpdf.ImageOptions{ImageType: "JPG"}, &j)
		b := pg.Bounds()
		maxW, maxH := 190.0, 277.0
		w, h := maxW, maxW*float64(b.Dy())/float64(b.Dx())
		if h > maxH {
			h, w = maxH, maxH*float64(b.Dx())/float64(b.Dy())
		}
		pdf.AddPage()
		pdf.ImageOptions(name, (210-w)/2, 10, w, h, false, fpdf.ImageOptions{ImageType: "JPG"}, 0, "")
	}
	var out bytes.Buffer
	if err := pdf.Output(&out); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Preview renders the first page of a document the way the printer would print it:
// the dots a receipt or label printer gets, or the page an office printer gets. It
// returns a PNG and the number of pages.
func (c Converter) Preview(ctx context.Context, data []byte, src printer.Format, p domain.Printer) ([]byte, int, error) {
	if !src.IsImage() && src != printer.FormatPDF {
		return nil, 0, fmt.Errorf("%w: only PDF and images can be previewed", ErrNoConversion)
	}
	target := printer.FormatPDF
	switch p.Kind {
	case printer.KindReceipt:
		target = printer.FormatESCPOS
	case printer.KindLabel:
		target = printer.FormatZPL
	}
	var (
		pages []image.Image
		err   error
	)
	inkjet := p.Kind == printer.KindDocument && slices.Contains(p.Formats, printer.FormatESCPR)
	if inkjet && src == printer.FormatPDF {
		// An inkjet prints colour: preview the page in colour.
		err = c.eachPDFPage(ctx, data, rasterDPI(target, p), false, func(img image.Image) error {
			pages = append(pages, img)
			return nil
		})
	} else {
		pages, err = c.pages(ctx, data, src, rasterDPI(target, p))
	}
	if err != nil {
		return nil, 0, err
	}
	var img image.Image
	switch p.Kind {
	case printer.KindReceipt:
		bm := dither(fitWidth(pages[0], receiptDots(p)))
		bm.trimBottom()
		img = bm.image()
	case printer.KindLabel:
		var g *image.Gray
		if l := labelLengthDots(p); l > 0 {
			g = fitBox(pages[0], labelDots(p), l)
		} else {
			g = fitWidth(pages[0], labelDots(p))
		}
		img = dither(g).image()
	default:
		img = pages[0] // office printers print greys
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, 0, err
	}
	return out.Bytes(), len(pages), nil
}

// toESCPR renders a document for an Epson inkjet, in colour at the printer's 360 dpi,
// one page at a time.
func (c Converter) toESCPR(ctx context.Context, data []byte, src printer.Format) (Result, error) {
	w := escpr.NewWriter(escpr.Options{Quality: escpr.Normal})
	n := 0
	if src.IsImage() {
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return Result{}, fmt.Errorf("%w: %v", ErrBadDocument, err)
		}
		w.Page(onPaper(img, escpr.A4, false))
		n = 1
	} else {
		err := c.eachPDFPage(ctx, data, escpr.DPI, false, func(img image.Image) error {
			w.Page(onPaper(img, escpr.A4, true))
			n++
			return nil
		})
		if err != nil {
			return Result{}, err
		}
	}
	return Result{Data: w.Bytes(), Format: printer.FormatESCPR, Pages: n}, nil
}

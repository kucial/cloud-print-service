// Package escpr writes print jobs in ESC/P-R, the raster language of Epson's consumer
// and small-office inkjets (EcoTank L-series, Expression, WorkForce). Those printers
// take no PDF, PostScript or PCL: the host renders each page and sends it as RGB
// raster lines, which is what this package encodes.
//
// A job is: leave packet mode, reset, a few "remote mode" settings, enter ESC/P-R, then
// the quality (setq) and page geometry (setj) commands, and per page a start (sttp),
// one data command (dsnd) per printed line and an end (endp); then the job end (endj)
// and a reset. ESC/P-R commands are framed ESC, a class letter, a 4-byte little-endian
// parameter length and a 4-byte name; numbers inside the parameters are big-endian.
//
// The command layout follows Epson's published ESC/P-R driver
// (epson-inkjet-printer-escpr); nothing of its code is used.
package escpr

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
)

// DPI is the resolution pages are sent at: 360 dpi is the one every ESC/P-R printer
// takes.
const DPI = 360

// Paper is a sheet size in dots at DPI, with the margin the printer needs on each side.
type Paper struct {
	Name          string
	Width, Height int
	Margin        int
}

// A4 is the sheet the office path prints on, with Epson's standard 3 mm margins.
var A4 = Paper{Name: "A4", Width: 2976, Height: 4209, Margin: 42}

// Printable is the area inside the margins: the largest page image a job can carry.
func (p Paper) Printable() (w, h int) { return p.Width - 2*p.Margin, p.Height - 2*p.Margin }

// Quality is the print mode.
type Quality byte

const (
	Draft  Quality = 0
	Normal Quality = 1
	High   Quality = 2
)

// Options are a job's settings.
type Options struct {
	Paper   Paper
	Quality Quality
	// Mono prints with black ink only. A job whose pages are all grey prints that way
	// anyway, sparing the colour cartridges; Mono forces it for colour pages too.
	Mono bool
}

// Encode returns a whole job printing pages, in order. Each page is placed at the top
// left of the printable area; anything beyond it is cut off, so callers fit pages to
// Paper.Printable first.
func Encode(pages []image.Image, o Options) []byte {
	w := NewWriter(o)
	for _, pg := range pages {
		w.Page(pg)
	}
	return w.Bytes()
}

// Writer builds a job one page at a time, so a long document never has to be held as
// images all at once.
type Writer struct {
	o      Options
	body   bytes.Buffer // pages so far; the job header goes in front at the end
	pages  int
	colour bool // some page has a pixel that is not grey
	line   []byte
	enc    []byte
}

func NewWriter(o Options) *Writer {
	if o.Paper.Width == 0 {
		o.Paper = A4
	}
	pw, _ := o.Paper.Printable()
	return &Writer{o: o, line: make([]byte, 0, pw*3), enc: make([]byte, 0, pw*3+pw/128+2)}
}

// Page adds a page, placed as Encode places it.
func (w *Writer) Page(pg image.Image) {
	b := &w.body
	if w.pages > 0 {
		cmd(b, 'p', "endp", []byte{1}) // the previous page, with more to come
	}
	w.pages++
	cmd(b, 'p', "sttp", nil)
	pw, ph := w.o.Paper.Printable()
	r := pg.Bounds()
	for y := 0; y < min(r.Dy(), ph); y++ {
		x0, px, colour := rgbLine(w.line[:0], pg, r.Min.Y+y, min(r.Dx(), pw))
		if len(px) == 0 {
			continue // a blank line is paper: nothing to send
		}
		w.colour = w.colour || colour
		data, comp := px, byte(0)
		if e, ok := packBits(w.enc[:0], px); ok {
			data, comp = e, 1
		}
		p := make([]byte, 7, 7+len(data))
		binary.BigEndian.PutUint16(p[0:], uint16(x0))
		binary.BigEndian.PutUint16(p[2:], uint16(y))
		p[4] = comp
		binary.BigEndian.PutUint16(p[5:], uint16(len(data)))
		cmd(b, 'd', "dsnd", append(p, data...))
	}
}

// Bytes ends the job and returns it. The Writer is done after this.
func (w *Writer) Bytes() []byte {
	if w.pages > 0 {
		cmd(&w.body, 'p', "endp", []byte{0}) // the last page
	}
	var b bytes.Buffer
	b.Grow(w.body.Len() + 256)
	startJob(&b, w.o, w.o.Mono || !w.colour)
	b.Write(w.body.Bytes())
	endJob(&b)
	return b.Bytes()
}

// startJob writes everything up to the first page.
func startJob(b *bytes.Buffer, o Options, mono bool) {
	// Leave IEEE 1284.4 packet mode, in case a previous host left the printer in it.
	b.Write([]byte{0x00, 0x00, 0x00, 0x1b, 0x01})
	b.WriteString("@EJL 1284.4\n@EJL     \n")
	b.Write([]byte{0x1b, '@'})
	remote(b,
		remoteCmd("JS", 0, 0, 0, 0),                                // job start
		remoteCmd("JH", append(make([]byte, 6), "ESCPRLib"...)...), // job header: who sends
		remoteCmd("HD", 0, 0x03, 0x04),                             // host: Linux
		remoteCmd("PP", 0, 0x01, 0xff),                             // paper source: automatic
	)
	b.Write([]byte{0x1b, '(', 'R', 0x06, 0x00, 0x00})
	b.WriteString("ESCPR")

	colorMode := byte(0)
	if mono {
		colorMode = 1
	}
	// media (plain paper), quality, colour mode, brightness, contrast, saturation,
	// colour plane (full-colour RGB), palette size (none).
	cmd(b, 'q', "setq", []byte{0, byte(o.Quality), colorMode, 0, 0, 0, 0, 0, 0})

	pw, ph := o.Paper.Printable()
	j := make([]byte, 22)
	binary.BigEndian.PutUint32(j[0:], uint32(o.Paper.Width))
	binary.BigEndian.PutUint32(j[4:], uint32(o.Paper.Height))
	binary.BigEndian.PutUint16(j[8:], uint16(o.Paper.Margin))  // top
	binary.BigEndian.PutUint16(j[10:], uint16(o.Paper.Margin)) // left
	binary.BigEndian.PutUint32(j[12:], uint32(pw))
	binary.BigEndian.PutUint32(j[16:], uint32(ph))
	j[20] = 0 // input resolution: 360 x 360
	j[21] = 0 // print direction: bidirectional
	cmd(b, 'j', "setj", j)
}

func endJob(b *bytes.Buffer) {
	cmd(b, 'j', "endj", nil)
	b.Write([]byte{0x1b, '@'})
	remote(b, remoteCmd("JE", 0)) // job end
}

// cmd writes one ESC/P-R command.
func cmd(b *bytes.Buffer, class byte, name string, params []byte) {
	var h [6]byte
	h[0], h[1] = 0x1b, class
	binary.LittleEndian.PutUint32(h[2:], uint32(len(params)))
	b.Write(h[:])
	b.WriteString(name)
	b.Write(params)
}

// remote writes a block of remote-mode commands.
func remote(b *bytes.Buffer, cmds ...[]byte) {
	b.Write([]byte{0x1b, '(', 'R', 0x08, 0x00, 0x00})
	b.WriteString("REMOTE1")
	for _, c := range cmds {
		b.Write(c)
	}
	b.Write([]byte{0x1b, 0x00, 0x00, 0x00})
}

// remoteCmd is a two-letter remote command with a 2-byte little-endian length.
func remoteCmd(name string, params ...byte) []byte {
	c := []byte{name[0], name[1], byte(len(params)), byte(len(params) >> 8)}
	return append(c, params...)
}

// rgbLine appends row y of img, w pixels wide, as RGB to buf, and returns the part
// between the first and last non-white pixel with the x it starts at, and whether any
// pixel has colour (is not grey). Transparent pixels are paper. A blank row returns no
// pixels.
func rgbLine(buf []byte, img image.Image, y, w int) (int, []byte, bool) {
	r := img.Bounds()
	first, last := -1, -1
	colour := false
	rgba, fast := img.(*image.RGBA)
	for x := 0; x < w; x++ {
		var c color.RGBA
		if fast {
			i := rgba.PixOffset(r.Min.X+x, y)
			c = color.RGBA{rgba.Pix[i], rgba.Pix[i+1], rgba.Pix[i+2], rgba.Pix[i+3]}
		} else {
			c = color.RGBAModel.Convert(img.At(r.Min.X+x, y)).(color.RGBA)
		}
		// Composite on white: premultiplied colour plus the paper showing through.
		k := 255 - c.A
		px := [3]byte{c.R + k, c.G + k, c.B + k}
		buf = append(buf, px[:]...)
		if px != [3]byte{255, 255, 255} {
			if first < 0 {
				first = x
			}
			last = x
			colour = colour || px[0] != px[1] || px[1] != px[2]
		}
	}
	if first < 0 {
		return 0, nil, false
	}
	return first, buf[first*3 : (last+1)*3], colour
}

// packBits run-length encodes RGB pixels: a run of 2 to 129 equal pixels is one count
// byte (257 - n) and the pixel; up to 128 differing pixels are a count byte (n - 1)
// and the pixels. It reports false when that is no smaller than the line itself, which
// is then sent as is.
func packBits(dst, px []byte) ([]byte, bool) {
	n := len(px) / 3
	same := func(i, j int) bool {
		return px[i*3] == px[j*3] && px[i*3+1] == px[j*3+1] && px[i*3+2] == px[j*3+2]
	}
	for i := 0; i < n; {
		if i+1 < n && same(i, i+1) {
			run := 2
			for i+run < n && run < 129 && same(i, i+run) {
				run++
			}
			dst = append(dst, byte(257-run))
			dst = append(dst, px[i*3:i*3+3]...)
			i += run
		} else {
			lit := 1
			for i+lit < n && lit < 128 && !(i+lit+1 < n && same(i+lit, i+lit+1)) {
				lit++
			}
			dst = append(dst, byte(lit-1))
			dst = append(dst, px[i*3:(i+lit)*3]...)
			i += lit
		}
		if len(dst) >= len(px) {
			return nil, false
		}
	}
	return dst, true
}

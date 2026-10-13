// Package escpos builds ESC/POS receipts: text (with Chinese), QR codes, barcodes,
// raster images and the paper cut. It writes the subset that Epson TM printers and the
// common Chinese receipt printers (Xprinter, Gprinter, Rongta, …) all accept.
package escpos

import (
	"bytes"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// Builder accumulates one receipt.
type Builder struct {
	buf     bytes.Buffer
	chinese bool // FS & sent: double-byte characters are GB18030
}

// New starts a receipt with the printer reset to its defaults (ESC @).
func New() *Builder {
	b := &Builder{}
	b.raw(0x1b, '@')
	return b
}

func (b *Builder) raw(p ...byte) *Builder {
	b.buf.Write(p)
	return b
}

// Bytes is the receipt so far.
func (b *Builder) Bytes() []byte { return append([]byte(nil), b.buf.Bytes()...) }

// Alignment for Align.
const (
	Left   = 0
	Center = 1
	Right  = 2
)

// Align sets the justification of the following lines (ESC a n).
func (b *Builder) Align(n byte) *Builder { return b.raw(0x1b, 'a', n) }

// Bold turns emphasis on or off (ESC E n).
func (b *Builder) Bold(on bool) *Builder { return b.raw(0x1b, 'E', boolByte(on)) }

// Size sets the character magnification, 1–8 in each direction (GS ! n).
func (b *Builder) Size(width, height int) *Builder {
	clamp := func(v int) byte {
		if v < 1 {
			v = 1
		}
		if v > 8 {
			v = 8
		}
		return byte(v - 1)
	}
	return b.raw(0x1d, '!', clamp(width)<<4|clamp(height))
}

// Text prints s. ASCII goes as is; anything else is sent in the printer's Chinese mode
// (FS &) as GB18030, which is what Chinese receipt printers expect. Characters GB18030
// cannot encode print as "?".
func (b *Builder) Text(s string) *Builder {
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if r < 0x80 {
			if b.chinese {
				b.raw(0x1c, '.') // FS .: back to single-byte characters
				b.chinese = false
			}
			b.buf.WriteByte(s[0])
			s = s[1:]
			continue
		}
		if !b.chinese {
			b.raw(0x1c, '&') // FS &: Chinese character mode
			b.chinese = true
		}
		enc, err := simplifiedchinese.GB18030.NewEncoder().String(s[:size])
		if err != nil {
			enc = "?"
		}
		b.buf.WriteString(enc)
		s = s[size:]
	}
	return b
}

// Line prints s and a line feed.
func (b *Builder) Line(s string) *Builder { return b.Text(s).raw('\n') }

// Feed advances the paper n lines (ESC d n).
func (b *Builder) Feed(n int) *Builder { return b.raw(0x1b, 'd', byte(clampInt(n, 0, 255))) }

// QR prints a QR code (GS ( k), module size 1–16 dots, error correction M.
func (b *Builder) QR(data string, module int) *Builder {
	fn := func(f byte, params ...byte) {
		n := len(params) + 2
		b.raw(0x1d, '(', 'k', byte(n), byte(n>>8), 49, f)
		b.raw(params...)
	}
	fn(65, 50, 0)                          // model 2
	fn(67, byte(clampInt(module, 1, 16)))  // module size
	fn(69, 49)                             // error correction M
	fn(80, append([]byte{48}, data...)...) // store the data
	fn(81, 48)                             // print it
	return b
}

// Code128 prints a Code 128 barcode with its text below (GS k 73), height in dots.
func (b *Builder) Code128(data string, height int) *Builder {
	payload := append([]byte("{B"), data...) // code set B: printable ASCII
	b.raw(0x1d, 'h', byte(clampInt(height, 1, 255)))
	b.raw(0x1d, 'w', 2) // module width
	b.raw(0x1d, 'H', 2) // text below
	b.raw(0x1d, 'k', 73, byte(len(payload)))
	return b.raw(payload...)
}

// Cut feeds past the cutter and cuts, leaving a small hinge (GS V 66 n).
func (b *Builder) Cut() *Builder {
	if b.chinese {
		b.raw(0x1c, '.')
		b.chinese = false
	}
	return b.raw(0x1d, 'V', 66, 3)
}

func boolByte(v bool) byte {
	if v {
		return 1
	}
	return 0
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

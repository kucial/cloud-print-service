package virtual

import (
	"bytes"
	"net"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// ESC/POS mode: the virtual printer behaves like a receipt printer. It answers the
// real-time status requests (DLE EOT n) and the model query (GS I n) wherever they
// appear in the stream, as real printers do, and records one receipt per cut
// (GS V …), or per connection when nothing cuts.

const (
	escposLanguage = "escpos"
	escposOK       = 0x12 // fixed bits of every DLE EOT reply
)

// escposStatus returns the DLE EOT n reply for the printer's current state. Caller
// holds p.mu.
func (p *Printer) escposStatus(n byte) byte {
	paperEnd := p.media == 2
	paperLow := p.media == 1
	errored := p.device == 3 || p.head == 1
	b := byte(escposOK)
	switch n {
	case 1:
		if paperEnd || p.door || errored {
			b |= 0x08 // offline
		}
	case 2:
		if p.door {
			b |= 0x04
		}
		if paperEnd {
			b |= 0x20
		}
		if errored {
			b |= 0x40
		}
	case 4:
		if paperLow {
			b |= 0x0c
		}
		if paperEnd {
			b |= 0x60
		}
	}
	return b
}

// escposStream splits an ESC/POS byte stream into replies to send and receipts to
// record. It keeps what it cannot parse yet (a command cut across reads) for the next
// call.
type escposStream struct {
	pending []byte
	doc     []byte
	text    []byte // printable bytes of the current receipt, still GB18030
	images  int
}

// escCmdLen is the length of the fixed-size ESC commands receipts commonly use.
var escCmdLen = map[byte]int{
	'@': 2, 'a': 3, '!': 3, 'E': 3, 'G': 3, '-': 3, 'd': 3, 'J': 3, 't': 3, 'M': 3, 'R': 3,
	'2': 2, '3': 3, 'V': 3, '{': 3, 'p': 5, 'c': 4, 'D': 0,
}

// gsCmdLen is the same for GS commands without a data block.
var gsCmdLen = map[byte]int{'!': 3, 'B': 3, 'H': 3, 'h': 3, 'w': 3, 'f': 3, 'L': 4, 'W': 4, 'a': 3, 'r': 3}

// need reports whether b holds at least n bytes, for parsing a command.
func need(b []byte, n int) bool { return len(b) >= n }

// feed consumes data. reply is called for each query answer; receipt for each
// finished receipt.
func (s *escposStream) feed(data []byte, reply func(query []byte), receipt func(doc, text []byte, images int)) {
	b := append(s.pending, data...)
	s.pending = nil
	for len(b) > 0 {
		c := b[0]
		switch {
		case c == 0x10 && need(b, 2) && b[1] == 0x04: // DLE EOT n: real-time status
			if !need(b, 3) {
				s.pending = b
				return
			}
			reply(b[:3])
			b = b[3:]
		case c == 0x10 && !need(b, 2):
			s.pending = b
			return
		case c == 0x1d: // GS
			if !need(b, 2) {
				s.pending = b
				return
			}
			n, done := s.gs(b)
			if n == 0 {
				s.pending = b
				return
			}
			if b[1] == 'I' {
				reply(b[:n]) // model query: answered, not printed
			} else {
				s.doc = append(s.doc, b[:n]...)
			}
			b = b[n:]
			if done {
				receipt(s.doc, s.text, s.images)
				s.doc, s.text, s.images = nil, nil, 0
			}
		case c == 0x1b: // ESC
			if !need(b, 2) {
				s.pending = b
				return
			}
			n := s.esc(b)
			if n == 0 {
				s.pending = b
				return
			}
			s.doc = append(s.doc, b[:n]...)
			b = b[n:]
		case c == 0x1c: // FS: & . (Chinese mode on/off) and friends are two bytes
			if !need(b, 2) {
				s.pending = b
				return
			}
			s.doc = append(s.doc, b[:2]...)
			b = b[2:]
		default:
			s.doc = append(s.doc, c)
			if c == '\n' || c >= 0x20 {
				s.text = append(s.text, c)
			}
			b = b[1:]
		}
	}
}

// gs returns the length of the GS command at b[0] (0: not all here yet), and whether
// it cuts the paper.
func (s *escposStream) gs(b []byte) (int, bool) {
	switch b[1] {
	case 'V': // cut: GS V m, or GS V m n for m in 65, 66, 97, 98, 103, 104
		if !need(b, 3) {
			return 0, false
		}
		if b[2] >= 65 {
			if !need(b, 4) {
				return 0, false
			}
			return 4, true
		}
		return 3, true
	case 'I':
		if !need(b, 3) {
			return 0, false
		}
		return 3, false
	case 'v': // GS v 0 m xL xH yL yH d…: raster image
		if !need(b, 8) {
			return 0, false
		}
		n := 8 + (int(b[4])+int(b[5])*256)*(int(b[6])+int(b[7])*256)
		if !need(b, n) {
			return 0, false
		}
		s.images++
		return n, false
	case '(': // GS ( k pL pH …: QR codes and other 2D symbols
		if !need(b, 5) {
			return 0, false
		}
		n := 5 + int(b[3]) + int(b[4])*256
		if !need(b, n) {
			return 0, false
		}
		if b[2] == 'k' && n >= 7 && b[5] == 49 && b[6] == 81 { // print the stored symbol
			s.images++
		}
		return n, false
	case 'k': // GS k m d… NUL (m < 65), or GS k m n d…: barcode
		if !need(b, 4) {
			return 0, false
		}
		if b[2] < 65 {
			i := bytes.IndexByte(b[3:], 0)
			if i < 0 {
				return 0, false
			}
			s.images++
			return 3 + i + 1, false
		}
		n := 4 + int(b[3])
		if !need(b, n) {
			return 0, false
		}
		s.images++
		return n, false
	}
	if n, ok := gsCmdLen[b[1]]; ok {
		if !need(b, n) {
			return 0, false
		}
		return n, false
	}
	return 2, false
}

// esc returns the length of the ESC command at b[0] (0: not all here yet).
func (s *escposStream) esc(b []byte) int {
	if b[1] == '*' { // ESC * m nL nH d…: bit image, 1 or 3 bytes per column
		if !need(b, 5) {
			return 0
		}
		cols := int(b[3]) + int(b[4])*256
		per := 1
		if b[2] >= 32 {
			per = 3
		}
		if !need(b, 5+cols*per) {
			return 0
		}
		s.images++
		return 5 + cols*per
	}
	n, ok := escCmdLen[b[1]]
	if !ok || n == 0 {
		n = 3 // most ESC commands take one parameter byte
	}
	if !need(b, n) {
		return 0
	}
	return n
}

// flush records what is left when the connection closes, if it printed anything.
func (s *escposStream) flush(receipt func(doc, text []byte, images int)) {
	if len(bytes.TrimSpace(s.text)) > 0 || s.images > 0 {
		receipt(s.doc, s.text, s.images)
	}
	s.doc, s.text, s.images, s.pending = nil, nil, 0, nil
}

// receiptText decodes the printable bytes: UTF-8 if they are, else GB18030 (what
// Chinese receipt printers take in their Chinese mode).
func receiptText(b []byte) string {
	if utf8.Valid(b) {
		return strings.TrimSpace(string(b))
	}
	if d, err := simplifiedchinese.GB18030.NewDecoder().Bytes(b); err == nil {
		return strings.TrimSpace(string(d))
	}
	return strings.TrimSpace(strings.ToValidUTF8(string(b), "?"))
}

func (p *Printer) serveESCPOS(c net.Conn) {
	var st escposStream
	reply := func(q []byte) {
		var out []byte
		p.mu.Lock()
		switch {
		case q[0] == 0x10:
			out = []byte{p.escposStatus(q[2])}
		case q[2] == 67:
			out = append(append([]byte{'_'}, p.Model...), 0)
		case q[2] == 66:
			out = []byte("_VPRINT\x00")
		}
		p.mu.Unlock()
		if out != nil {
			p.writeReply(c, out)
		}
	}
	receipt := func(doc, text []byte, images int) {
		p.recordReceipt(doc, receiptText(text), images)
	}
	chunk := make([]byte, 8192)
	for {
		_ = c.SetReadDeadline(time.Now().Add(60 * time.Second))
		n, err := c.Read(chunk)
		if n > 0 {
			p.mu.Lock()
			p.bytesIn += n
			closeOnPrint := p.closeOnPrint
			p.mu.Unlock()
			if closeOnPrint && !isESCPOSQueryOnly(chunk[:n]) {
				_ = c.Close()
				return
			}
			st.feed(chunk[:n], reply, receipt)
		}
		if err != nil {
			st.flush(receipt)
			return
		}
	}
}

// isESCPOSQueryOnly reports whether a read holds only status or model queries (the
// driver's status socket), which must keep working while printing is set to fail.
func isESCPOSQueryOnly(b []byte) bool {
	for len(b) >= 3 && (b[0] == 0x10 && b[1] == 0x04 || b[0] == 0x1d && b[1] == 'I') {
		b = b[3:]
	}
	return len(b) == 0
}

func (p *Printer) recordReceipt(doc []byte, text string, images int) {
	p.mu.Lock()
	p.labelCount++
	p.labels = append(p.labels, Label{
		Index: p.labelCount, ReceivedAt: time.Now().UTC(), Bytes: len(doc),
		Language: escposLanguage, Text: text, Images: images,
	})
	p.mu.Unlock()
}

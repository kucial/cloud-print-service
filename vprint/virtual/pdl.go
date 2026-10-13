package virtual

import (
	"bytes"
	"net"
	"time"
)

// PDL mode: an office printer on raw TCP 9100 (JetDirect). It answers PJL INFO STATUS
// and INFO ID with HP's codes, and records each connection's document — PCL,
// PostScript or PDF, whatever arrives.

const pdlLanguage = "pdl"

var pjlUEL = []byte("\x1b%-12345X")

// pjlStatusCode is the HP status code for the printer's state. Caller holds p.mu.
func (p *Printer) pjlStatusCode() (int, string) {
	switch {
	case p.media == 2:
		return 41302, "LOAD TRAY 2 PLAIN A4"
	case p.jam:
		return 40022, "PAPER JAM"
	case p.door:
		return 40021, "PRINTER OPEN"
	case p.toner == 2:
		return 40010, "INSTALL TONER CARTRIDGE"
	case p.device == 3:
		return 50001, "ERROR 50.1"
	case p.printing || p.device == 1:
		return 10023, "PRINTING"
	case p.toner == 1:
		return 10006, "TONER LOW"
	}
	return 10001, "READY"
}

// pjlAnswer returns the reply to the PJL query at the start of q, and its length.
func (p *Printer) pjlAnswer(q []byte) ([]byte, int, bool) {
	for _, c := range []struct {
		cmd   string
		reply func() string
	}{
		{"@PJL INFO STATUS", func() string {
			code, display := p.pjlStatusCode()
			return "@PJL INFO STATUS\r\nCODE=" + itoa(code) + "\r\nDISPLAY=\"" + display + "\"\r\nONLINE=TRUE\r\n\f"
		}},
		{"@PJL INFO ID", func() string { return "@PJL INFO ID\r\n\"" + p.Model + "\"\r\n\f" }},
		{"@PJL INFO CONFIG", func() string {
			return "@PJL INFO CONFIG\r\nLANGUAGES [3 ENUMERATED]\r\n\tPCL\r\n\tPCLXL\r\n\tPOSTSCRIPT\r\n\f"
		}},
	} {
		if bytes.HasPrefix(q, []byte(c.cmd)) {
			end := bytes.IndexByte(q, '\n')
			if end < 0 {
				return nil, 0, false // the rest of the line has not arrived yet
			}
			p.mu.Lock()
			r := c.reply()
			p.mu.Unlock()
			return []byte(r), end + 1, true
		}
	}
	return nil, 0, false
}

func itoa(n int) string {
	b := []byte{}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if len(b) == 0 {
		return "0"
	}
	return string(b)
}

func (p *Printer) servePDL(c net.Conn) {
	var doc, buf []byte
	chunk := make([]byte, 32<<10)
	for {
		_ = c.SetReadDeadline(time.Now().Add(60 * time.Second))
		n, err := c.Read(chunk)
		if n > 0 {
			p.mu.Lock()
			p.bytesIn += n
			closeOnPrint := p.closeOnPrint
			p.mu.Unlock()
			buf = append(buf, chunk[:n]...)
			// Answer PJL queries wherever they appear between UELs; keep the rest.
			for {
				buf = bytes.TrimPrefix(buf, pjlUEL)
				reply, used, ok := p.pjlAnswer(buf)
				if !ok {
					break
				}
				p.writeReply(c, reply)
				buf = buf[used:]
			}
			if len(buf) > 0 && closeOnPrint && !bytes.HasPrefix(buf, []byte("@PJL")) {
				_ = c.Close()
				return
			}
			if i := bytes.Index(buf, []byte("@PJL INFO")); i < 0 {
				doc, buf = append(doc, buf...), nil
			} else {
				doc, buf = append(doc, buf[:i]...), buf[i:]
			}
		}
		if err != nil {
			p.recordPDL(doc)
			return
		}
	}
}

func (p *Printer) recordPDL(doc []byte) {
	body := bytes.TrimSpace(bytes.ReplaceAll(doc, pjlUEL, nil))
	if len(body) == 0 || onlyControl(body) {
		// A status connection, or bytes that make no marks (a real printer ejects a
		// page only when something is on it).
		return
	}
	format := "application/octet-stream"
	switch {
	case bytes.HasPrefix(body, []byte("%PDF")):
		format = "application/pdf"
	case bytes.HasPrefix(body, []byte("%!")):
		format = "application/postscript"
	case bytes.HasPrefix(body, []byte("\x1bE")):
		format = "application/vnd.hp-pcl"
	}
	p.mu.Lock()
	p.labelCount++
	p.labels = append(p.labels, Label{Index: p.labelCount, ReceivedAt: time.Now().UTC(), Bytes: len(doc),
		Language: pdlLanguage, Format: format, Data: append([]byte(nil), doc...)})
	dur := p.printDuration
	p.printing = true
	p.mu.Unlock()
	go func() {
		time.Sleep(dur)
		p.mu.Lock()
		if !p.sticky {
			p.printing = false
		}
		p.mu.Unlock()
	}()
}

func onlyControl(b []byte) bool {
	for _, c := range b {
		if c >= 0x20 || c == 0x1b || c == '\f' {
			return false
		}
	}
	return true
}

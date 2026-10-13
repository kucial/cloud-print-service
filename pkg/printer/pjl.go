package printer

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// PJL (Printer Job Language) is how office printers on a raw port (HP and the many
// printers that copy it) report status and identity. A query is wrapped in the
// Universal Exit Language sequence; the answer is text ending in a form feed.
const pjlUEL = "\x1b%-12345X"

var (
	CmdPJLStatus = []byte(pjlUEL + "@PJL INFO STATUS\r\n" + pjlUEL)
	CmdPJLID     = []byte(pjlUEL + "@PJL INFO ID\r\n" + pjlUEL)
	CmdPJLConfig = []byte(pjlUEL + "@PJL INFO CONFIG\r\n" + pjlUEL)
)

// IsPJLQuery reports whether p is a PJL INFO request (after any UEL prefix).
func IsPJLQuery(p []byte) bool {
	p = bytes.TrimPrefix(p, []byte(pjlUEL))
	return bytes.HasPrefix(p, []byte("@PJL INFO"))
}

// PJLReplyComplete reports whether a PJL answer has arrived in full: it ends in a form
// feed.
func PJLReplyComplete(reply []byte) bool { return bytes.IndexByte(reply, '\f') >= 0 }

// PJLStatus is the answer to @PJL INFO STATUS.
type PJLStatus struct {
	Code    int
	Display string
	Online  bool
}

// ParsePJLStatus reads CODE, DISPLAY and ONLINE from an INFO STATUS answer.
func ParsePJLStatus(reply []byte) (PJLStatus, bool) {
	st := PJLStatus{Online: true}
	found := false
	for _, line := range strings.Split(string(reply), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(strings.Trim(line, "\r\f")), "=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"`)
		switch strings.ToUpper(strings.TrimSpace(k)) {
		case "CODE":
			if n, err := strconv.Atoi(v); err == nil {
				st.Code, found = n, true
			}
		case "DISPLAY":
			st.Display = v
		case "ONLINE":
			st.Online = !strings.EqualFold(v, "FALSE")
		}
	}
	return st, found
}

// State maps the PJL status code (HP's numbering, which most PJL printers follow).
func (s PJLStatus) State() State {
	c := s.Code
	switch {
	case c == 10006 || c == 40038:
		return StateTonerLow
	case c == 40010:
		return StateTonerOut // no cartridge
	case c == 40021:
		return StateDoorOpen
	case c == 40022 || c/1000 == 42 || c/1000 == 44:
		return StatePaperJam
	case c/1000 == 41 || c == 40019:
		return StateMediaOut
	case c/10000 == 5:
		return StateError // hardware error
	case c/10000 == 4:
		return StateError // other operator intervention
	case !s.Online:
		return StateOffline
	case c == 10023 || c == 10024:
		return StatePrinting
	case c == 10003 || c == 10004 || c == 10005 || c == 10007 || c == 10013 || c == 10014:
		return StateBusy // warming up, self test, resetting, cancelling
	case c/10000 == 1, c/1000 == 30, c/1000 == 35:
		return StateIdle // ready, or a condition the printer continues past
	}
	return StateUnknown
}

// Detail is the printer's own words, for the panel.
func (s PJLStatus) Detail() string {
	return fmt.Sprintf("code=%d display=%q online=%v", s.Code, s.Display, s.Online)
}

// ParsePJLID reads the model from an @PJL INFO ID answer: the quoted line after the
// echoed command.
func ParsePJLID(reply []byte) (string, bool) {
	for _, line := range strings.Split(string(reply), "\n") {
		line = strings.TrimSpace(strings.Trim(line, "\r\f"))
		if line == "" || strings.HasPrefix(line, "@PJL") {
			continue
		}
		line = strings.Trim(line, `"`)
		return line, line != ""
	}
	return "", false
}

// ParsePJLLanguages reads the page languages from an @PJL INFO CONFIG answer:
//
//	LANGUAGES [3 ENUMERATED]
//		PCL
//		PCLXL
//		POSTSCRIPT
//
// in the formats CPS names (PCL and PCL XL are both "pcl").
func ParsePJLLanguages(reply []byte) []Format {
	var out []Format
	seen := map[Format]bool{}
	in := false
	for _, line := range strings.Split(string(reply), "\n") {
		raw := strings.TrimRight(line, "\r\f")
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(raw)), "LANGUAGES") {
			in = true
			continue
		}
		if !in {
			continue
		}
		if raw == "" || (raw[0] != ' ' && raw[0] != '\t') {
			break // the next setting
		}
		var f Format
		switch v := strings.ToUpper(strings.TrimSpace(raw)); {
		case strings.HasPrefix(v, "PCL"):
			f = FormatPCL
		case v == "POSTSCRIPT":
			f = FormatPostScript
		case v == "PDF":
			f = FormatPDF
		default:
			continue
		}
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

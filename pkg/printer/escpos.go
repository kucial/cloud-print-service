package printer

import (
	"fmt"
	"strings"
)

// ESC/POS real-time status requests (DLE EOT n). Each is answered with one byte, even
// while the printer is busy or offline, which is what makes them usable as a status
// probe. Printers that implement only part of ESC/POS commonly still answer these.
var (
	CmdESCPOSPrinterStatus = []byte{0x10, 0x04, 1} // offline
	CmdESCPOSOfflineCause  = []byte{0x10, 0x04, 2} // cover open, paper end, error
	CmdESCPOSPaperStatus   = []byte{0x10, 0x04, 4} // roll near-end, roll end

	// GS I n: printer information. 67 is the model name, 66 the maker; both answer
	// "_<text>\x00". Many non-Epson printers answer neither.
	CmdESCPOSModelName = []byte{0x1d, 0x49, 67}
	CmdESCPOSMakerName = []byte{0x1d, 0x49, 66}
)

// IsESCPOSStatusQuery reports whether p starts with a DLE EOT status request.
func IsESCPOSStatusQuery(p []byte) bool {
	return len(p) >= 3 && p[0] == 0x10 && p[1] == 0x04 && p[2] >= 1 && p[2] <= 4
}

// IsESCPOSInfoQuery reports whether p starts with a GS I information request.
func IsESCPOSInfoQuery(p []byte) bool {
	return len(p) >= 3 && p[0] == 0x1d && p[1] == 0x49
}

// validStatusByte checks the bits ESC/POS fixes in every DLE EOT reply (bit 1 and 4
// set, bit 0 and 7 clear), so a stray byte is not taken for a status.
func validStatusByte(b byte) bool { return b&0x93 == 0x12 }

// ESCPOSStatus is the decoded answer to DLE EOT 1, 2 and 4.
type ESCPOSStatus struct {
	Offline   bool
	CoverOpen bool
	PaperEnd  bool
	PaperLow  bool
	Error     bool
}

// ParseESCPOSStatus decodes the three status bytes, in the order of the commands
// above. ok is false when any byte is not a status reply.
func ParseESCPOSStatus(printerStatus, offlineCause, paper byte) (ESCPOSStatus, bool) {
	if !validStatusByte(printerStatus) || !validStatusByte(offlineCause) || !validStatusByte(paper) {
		return ESCPOSStatus{}, false
	}
	return ESCPOSStatus{
		Offline:   printerStatus&0x08 != 0,
		CoverOpen: offlineCause&0x04 != 0,
		PaperEnd:  offlineCause&0x20 != 0 || paper&0x60 != 0,
		PaperLow:  paper&0x0c != 0,
		Error:     offlineCause&0x40 != 0,
	}, true
}

// State is the vendor-neutral state; the most blocking condition wins.
func (s ESCPOSStatus) State() State {
	switch {
	case s.PaperEnd:
		return StateMediaOut
	case s.CoverOpen:
		return StateDoorOpen
	case s.Error:
		return StateError
	case s.Offline:
		return StateOffline
	case s.PaperLow:
		return StateMediaLow
	}
	return StateIdle
}

// Detail is the raw flags, for the panel and the logs.
func (s ESCPOSStatus) Detail() string {
	b := func(v bool) int {
		if v {
			return 1
		}
		return 0
	}
	return fmt.Sprintf("offline=%d cover_open=%d paper_end=%d paper_low=%d error=%d",
		b(s.Offline), b(s.CoverOpen), b(s.PaperEnd), b(s.PaperLow), b(s.Error))
}

// ParseESCPOSInfo reads a GS I 66/67 reply: "_" + text + NUL.
func ParseESCPOSInfo(reply []byte) (string, bool) {
	if len(reply) < 2 || reply[0] != '_' {
		return "", false
	}
	text, _, _ := strings.Cut(string(reply[1:]), "\x00")
	text = strings.TrimSpace(text)
	return text, text != ""
}

// ESCPOSInfoComplete reports whether a GS I reply has arrived in full.
func ESCPOSInfoComplete(reply []byte) bool {
	return len(reply) > 0 && reply[len(reply)-1] == 0
}

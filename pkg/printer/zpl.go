package printer

import (
	"fmt"
	"strconv"
	"strings"
)

// Zebra host commands. Both are tilde commands: the firmware acts on them as soon as
// they arrive, even while a label is printing, and answers on the same connection.
const (
	CmdHostStatus = "~HS"
	CmdHostID     = "~HI"
)

// ParseHostStatus decodes a host status reply into a coarse state.
//
// Real firmware answers ~HS with three STX…ETX framed lines (ZPL II manual, ~HS):
//
//	line 1: aaa,b,c,dddd,eee,f,g,h,iii,j,k,l
//	        b paper out · c paused · eee formats in receive buffer · f buffer full
//	        j corrupt RAM · k under temperature · l over temperature
//	line 2: mmm,n,o,p,q,r,s,t,uuuuuuuu,v,www
//	        o head up · p ribbon out · t label waiting (peel) · uuuuuuuu labels remaining
//	line 3: xxxx,y   password, static RAM
//
// ~HS carries no "low" warnings, so media/ribbon low never appear from real firmware.
// Older virtual printers replied `^HS<device>,<media>,<ribbon>,<head>^HS`; that form is
// still accepted so a mixed fleet keeps working.
func ParseHostStatus(raw string) (st State, detail string, ok bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return StateUnknown, "empty host status reply", false
	}
	if strings.Contains(s, "\x02") || !strings.Contains(s, "^HS") {
		if l1, l2, found := hostStatusLines(s); found {
			return decodeHS(l1, l2)
		}
	}
	return decodeLegacyHS(s)
}

// hostStatusLines splits a ~HS reply into its first two lines of numeric fields.
func hostStatusLines(s string) (l1, l2 []int, ok bool) {
	var lines [][]int
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == '\x02' || r == '\x03' || r == '\r' || r == '\n' }) {
		if f := csvInts(part); len(f) > 0 {
			lines = append(lines, f)
		}
	}
	// Line 1 has 12 fields and line 2 has 11; demand most of them so a short or foreign
	// reply is not misread as "idle".
	if len(lines) < 2 || len(lines[0]) < 10 || len(lines[1]) < 9 {
		return nil, nil, false
	}
	return lines[0], lines[1], true
}

func decodeHS(l1, l2 []int) (State, string, bool) {
	at := func(f []int, i int) int {
		if i < len(f) {
			return f[i]
		}
		return 0
	}
	paperOut, paused, formats, bufFull := at(l1, 1), at(l1, 2), at(l1, 4), at(l1, 5)
	corruptRAM, underTemp, overTemp := at(l1, 9), at(l1, 10), at(l1, 11)
	headUp, ribbonOut, labelWaiting, remaining := at(l2, 2), at(l2, 3), at(l2, 7), at(l2, 8)

	detail := fmt.Sprintf("paper_out=%d paused=%d head_up=%d ribbon_out=%d formats=%d remaining=%d",
		paperOut, paused, headUp, ribbonOut, formats, remaining)
	switch {
	case headUp == 1:
		return StateHeadError, detail + " (print head open)", true
	case corruptRAM == 1 || underTemp == 1 || overTemp == 1:
		return StateError, detail, true
	case ribbonOut == 1:
		return StateRibbonOut, detail, true
	case paperOut == 1:
		return StateMediaOut, detail, true
	case paused == 1:
		// A paused printer accepts data but prints nothing until someone presses feed.
		return StateError, detail + " (paused)", true
	case formats > 0 || remaining > 0:
		return StatePrinting, detail, true
	case bufFull == 1 || labelWaiting == 1:
		return StateBusy, detail, true
	}
	return StateIdle, detail, true
}

func decodeLegacyHS(s string) (State, string, bool) {
	seg := s
	if i := strings.Index(seg, "^HS"); i >= 0 {
		seg = seg[i+3:]
	}
	if j := strings.Index(seg, "^FS"); j >= 0 {
		seg = seg[:j]
	}
	nums := csvInts(seg)
	if len(nums) == 0 {
		return StateUnknown, "no numeric fields in host status reply", false
	}
	at := func(i int) int {
		if i < len(nums) {
			return nums[i]
		}
		return 0
	}
	device, media, ribbon, head := nums[0], at(1), at(2), at(3)
	detail := fmt.Sprintf("device=%d media=%d ribbon=%d head=%d", device, media, ribbon, head)
	switch {
	case head == 1:
		return StateHeadError, detail, true
	case device == 3:
		return StateError, detail, true
	case ribbon == 2:
		return StateRibbonOut, detail, true
	case media == 2:
		return StateMediaOut, detail, true
	case device == 1:
		return StatePrinting, detail, true
	case device == 2:
		return StateBusy, detail, true
	case media == 1 || ribbon == 1:
		return StateMediaLow, detail, true
	}
	return StateIdle, detail, true
}

// HostID is what a printer says about itself in reply to ~HI.
type HostID struct {
	Model    string  `json:"model"`
	Firmware string  `json:"firmware,omitempty"`
	Serial   string  `json:"serial,omitempty"` // only virtual printers report it; real ~HI has no serial
	DPMM     float64 `json:"dpmm,omitempty"`   // dots per millimetre, 0 when not reported
	Options  string  `json:"options,omitempty"`
}

// ParseHostID decodes a ~HI reply.
//
// Real firmware: STX `MODEL,Vfirmware,dpmm,memory[,options]` ETX, e.g.
// "\x02GK888t,V61.17.17Z,8,8192KB\x03". Virtual printers reply `;9=model;10=dpmm;
// 12=serial;13=firmware;` — both are accepted.
func ParseHostID(raw string) (HostID, bool) {
	s := strings.Trim(strings.TrimSpace(raw), "\x02\x03\r\n ")
	if s == "" {
		return HostID{}, false
	}
	if strings.Contains(s, "=") {
		var id HostID
		for _, kv := range strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == '\n' || r == '\r' }) {
			k, v, found := strings.Cut(kv, "=")
			if !found {
				continue
			}
			v = strings.TrimSpace(v)
			switch strings.TrimSpace(k) {
			case "9":
				id.Model = v
			case "10":
				id.DPMM, _ = strconv.ParseFloat(v, 64)
			case "11":
				id.Options = v
			case "12":
				id.Serial = v
			case "13":
				id.Firmware = v
			}
		}
		return id, id.Model != ""
	}
	f := strings.Split(s, ",")
	if len(f) < 2 {
		return HostID{}, false
	}
	id := HostID{Model: strings.TrimSpace(f[0]), Firmware: strings.TrimSpace(f[1])}
	if len(f) > 2 {
		id.DPMM, _ = strconv.ParseFloat(strings.TrimSpace(f[2]), 64)
	}
	if len(f) > 4 {
		id.Options = strings.TrimSpace(strings.Join(f[4:], ","))
	}
	return id, id.Model != ""
}

// csvInts returns the integers in s, in order, ignoring any separators.
func csvInts(s string) []int {
	var out []int
	start := -1
	for i := 0; i <= len(s); i++ {
		if i < len(s) && s[i] >= '0' && s[i] <= '9' {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			if n, err := strconv.Atoi(s[start:i]); err == nil {
				out = append(out, n)
			}
			start = -1
		}
	}
	return out
}

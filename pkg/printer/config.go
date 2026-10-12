package printer

import (
	"regexp"
	"strconv"
	"strings"
)

// CmdHostConfig asks for the printer's configuration as text — the same content the
// printer puts on its configuration label, returned instead of printed.
const CmdHostConfig = "^XA^HH^XZ"

// IsHostQuery reports whether data starts with a host command the printer answers.
// Everything else on a printer connection is label data and gets no reply.
func IsHostQuery(data string) bool {
	s := strings.TrimLeft(data, " \r\n\t")
	for _, p := range []string{CmdHostStatus, CmdHostID, "^XA^HH", "^HH", "^XA^HW", "^HW", "^HS"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// ReplyComplete reports whether reply holds the whole answer to cmd: ~HS is three
// STX…ETX frames, ~HI and ^HH one. Replies without framing (virtual printers, the
// legacy ^HS form) are never "complete" here; callers end them on a quiet line.
func ReplyComplete(cmd string, reply []byte) bool {
	want := 1
	if strings.HasPrefix(strings.TrimLeft(cmd, " \r\n\t"), CmdHostStatus) {
		want = 3
	}
	n := 0
	for _, b := range reply {
		if b == 0x03 {
			n++
		}
	}
	return n >= want
}

// HostConfig is the decoded ^HH reply. Settings keeps every line as printed, so a
// model-specific field is never lost to parsing.
type HostConfig struct {
	Settings map[string]string `json:"settings"`

	DPMM            float64 `json:"dpmm,omitempty"`
	DPI             int     `json:"dpi,omitempty"`
	PrintWidthDots  int     `json:"print_width_dots,omitempty"`
	PrintWidthMM    float64 `json:"print_width_mm,omitempty"`
	LabelLengthDots int     `json:"label_length_dots,omitempty"`
	LabelLengthMM   float64 `json:"label_length_mm,omitempty"`
	LabelTopDots    int     `json:"label_top_dots"`
	LeftPosDots     int     `json:"left_position_dots"`
	Darkness        string  `json:"darkness,omitempty"`
	PrintSpeed      string  `json:"print_speed,omitempty"`
	PrintMethod     string  `json:"print_method,omitempty"` // THERMAL-TRANS. | DIRECT-THERMAL
	PrintMode       string  `json:"print_mode,omitempty"`   // TEAR OFF | PEEL OFF | CUTTER …
	MediaType       string  `json:"media_type,omitempty"`   // GAP/NOTCH | MARK | CONTINUOUS
	Language        string  `json:"language,omitempty"`     // ZPL MODE: ZPL II | ZPL
	Firmware        string  `json:"firmware,omitempty"`
	HeadUsage       string  `json:"head_usage,omitempty"`
}

var multiSpace = regexp.MustCompile(`\s{2,}`)

// ParseHostConfig decodes a ^HH reply. Each line is "<value>  <KEY>", padded into two
// columns; values may themselves contain spaces ("39.0IN   989MM"), so the key is the
// last column and the value is everything before it.
func ParseHostConfig(raw string) (HostConfig, bool) {
	c := HostConfig{Settings: map[string]string{}}
	for _, line := range strings.FieldsFunc(raw, func(r rune) bool { return r == '\r' || r == '\n' }) {
		line = strings.Trim(line, "\x02\x03 \t")
		parts := multiSpace.Split(line, -1)
		if len(parts) < 2 {
			continue
		}
		key := strings.TrimSpace(parts[len(parts)-1])
		val := strings.TrimSpace(strings.Join(parts[:len(parts)-1], " "))
		if key == "" || val == "" {
			continue
		}
		c.Settings[key] = val
	}
	if len(c.Settings) < 3 {
		return HostConfig{}, false
	}

	get := func(k string) string { return c.Settings[k] }
	c.Darkness, c.PrintSpeed = get("DARKNESS"), get("PRINT SPEED")
	c.PrintMethod, c.PrintMode, c.MediaType = get("PRINT METHOD"), get("PRINT MODE"), get("MEDIA TYPE")
	c.Language, c.HeadUsage = get("ZPL MODE"), get("HEAD USAGE")
	c.Firmware = strings.TrimSpace(strings.TrimSuffix(get("FIRMWARE"), "<-"))
	c.PrintWidthDots = leadingInt(get("PRINT WIDTH"))
	c.LabelLengthDots = leadingInt(get("LABEL LENGTH"))
	c.LabelTopDots = leadingInt(get("LABEL TOP"))
	c.LeftPosDots = leadingInt(get("LEFT POSITION"))

	// RESOLUTION reads like "832 8/MM FULL": the dpmm is the number before "/MM".
	if m := regexp.MustCompile(`(\d+(?:\.\d+)?)/MM`).FindStringSubmatch(get("RESOLUTION")); m != nil {
		c.DPMM, _ = strconv.ParseFloat(m[1], 64)
	}
	if c.DPMM > 0 {
		c.DPI = DPIFromDPMM(c.DPMM)
		c.PrintWidthMM = round1(float64(c.PrintWidthDots) / c.DPMM)
		c.LabelLengthMM = round1(float64(c.LabelLengthDots) / c.DPMM)
	}
	return c, true
}

// DPIFromDPMM maps the dots-per-mm a printer reports to its nominal resolution.
func DPIFromDPMM(dpmm float64) int {
	switch int(dpmm) {
	case 0:
		return 0
	case 6:
		return 152
	case 8:
		return 203
	case 12:
		return 300
	case 24:
		return 600
	}
	return int(dpmm*25.4 + 0.5)
}

// leadingInt parses a signed integer at the start of s ("+000", "0333", "480").
func leadingInt(s string) int {
	s = strings.TrimSpace(s)
	end := 0
	for end < len(s) && (s[end] >= '0' && s[end] <= '9' || end == 0 && (s[end] == '+' || s[end] == '-')) {
		end++
	}
	n, _ := strconv.Atoi(s[:end])
	return n
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }

// FormatHostConfig renders settings the way firmware answers ^HH: one STX…ETX frame,
// each line "  <value padded to 20>  <KEY>". Used by virtual and mock printers so the
// diagnostics path sees real-shaped data.
func FormatHostConfig(settings [][2]string) string {
	var b strings.Builder
	b.WriteByte(0x02)
	for _, kv := range settings {
		b.WriteString("  ")
		b.WriteString(kv[0])
		for i := len(kv[0]); i < 20; i++ {
			b.WriteByte(' ')
		}
		b.WriteString(kv[1])
		b.WriteString("\r\n")
	}
	b.WriteByte(0x03)
	return b.String()
}

// SimulatedHostConfig is a plausible ^HH for a printer loaded with widthMM x lengthMM
// gap media at dpmm, for virtual and mock printers.
func SimulatedHostConfig(model, firmware string, dpmm, widthMM, lengthMM float64) string {
	if dpmm <= 0 {
		dpmm = 8
	}
	dots := func(mm float64) string { return strconv.Itoa(int(mm*dpmm + 0.5)) }
	return FormatHostConfig([][2]string{
		{"15.0", "DARKNESS"},
		{"4 IPS", "PRINT SPEED"},
		{"+000", "TEAR OFF"},
		{"TEAR OFF", "PRINT MODE"},
		{"GAP/NOTCH", "MEDIA TYPE"},
		{"DIRECT-THERMAL", "PRINT METHOD"},
		{dots(widthMM), "PRINT WIDTH"},
		{"0" + dots(lengthMM), "LABEL LENGTH"},
		{"ZPL II", "ZPL MODE"},
		{"+000", "LABEL TOP"},
		{"+0000", "LEFT POSITION"},
		{dots(widthMM) + " " + strconv.Itoa(int(dpmm)) + "/MM FULL", "RESOLUTION"},
		{firmware + " <-", "FIRMWARE"},
		{model, "MODEL"},
	})
}

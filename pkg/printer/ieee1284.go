package printer

import (
	"regexp"
	"strings"
)

// DeviceID is a parsed IEEE 1284 device id, the "MFG:…;CMD:…;MDL:…;" string a USB
// printer reports about itself.
type DeviceID struct {
	Manufacturer string
	Model        string
	Commands     []string // CMD / COMMAND SET, upper-cased: "PJL", "PCL", "ESC/POS", "ZPL", …
	Raw          string
}

// ParseDeviceID reads the fields CPS uses. Keys come in short and long forms.
func ParseDeviceID(s string) DeviceID {
	d := DeviceID{Raw: s}
	for _, f := range strings.Split(s, ";") {
		k, v, ok := strings.Cut(f, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.ToUpper(strings.TrimSpace(k)) {
		case "MFG", "MANUFACTURER":
			d.Manufacturer = v
		case "MDL", "MODEL":
			d.Model = v
		case "CMD", "COMMAND SET", "COMMANDSET":
			for _, c := range strings.Split(v, ",") {
				if c = strings.ToUpper(strings.TrimSpace(c)); c != "" {
					d.Commands = append(d.Commands, c)
				}
			}
		}
	}
	return d
}

func (d DeviceID) has(cmd ...string) bool {
	for _, c := range d.Commands {
		for _, want := range cmd {
			if c == want || strings.HasPrefix(c, want) {
				return true
			}
		}
	}
	return false
}

// receiptModel matches the model names of receipt printers that report no command set
// ("POS58", "POS-80C", "XP-58IIH", "TM-T82").
var receiptModel = regexp.MustCompile(`(?i)(^|[^A-Z])(POS[- ]?(58|80)|XP-?(58|80)|TM-[TMUPLH])`)

// Profile is what a printer is, as far as CPS needs to know to drive it.
type Profile struct {
	Driver  string // DriverZebra, DriverESCPOS, DriverPDL; "" when unknown
	Kind    Kind
	Formats []Format
	PJL     bool // answers PJL status and identity
}

// Profile classifies the printer. Zebra wins by maker, because Zebra printers often
// report only the language they were shipped in (a GK888t says "EPL" yet runs ZPL).
func (d DeviceID) Profile() Profile {
	mfg := strings.ToUpper(d.Manufacturer)
	switch {
	case strings.Contains(mfg, "ZEBRA") || d.has("ZPL", "EPL"):
		return Profile{Driver: DriverZebra, Kind: KindLabel, Formats: []Format{FormatZPL}}
	case d.has("ESC/POS", "ESCPOS", "ESC_POS") || receiptModel.MatchString(d.Model):
		return Profile{Driver: DriverESCPOS, Kind: KindReceipt, Formats: []Format{FormatESCPOS}}
	}
	var formats []Format
	if d.has("PCL") { // PCL, PCL5, PCL6, PCLXL; not PCL3GUI-only inkjets, see below
		formats = append(formats, FormatPCL)
	}
	if d.has("POSTSCRIPT", "PS") {
		formats = append(formats, FormatPostScript)
	}
	if d.has("PDF") {
		formats = append(formats, FormatPDF)
	}
	if len(formats) > 0 && !(len(d.Commands) > 0 && onlyPCL3GUI(d.Commands)) {
		return Profile{Driver: DriverPDL, Kind: KindDocument, Formats: formats, PJL: d.has("PJL")}
	}
	return Profile{}
}

// onlyPCL3GUI reports an inkjet that speaks only HP's host-based PCL3GUI, which needs
// a driver to render pages and cannot print PCL sent as is.
func onlyPCL3GUI(cmds []string) bool {
	for _, c := range cmds {
		if strings.HasPrefix(c, "PCL") && c != "PCL3GUI" {
			return false
		}
		if c == "POSTSCRIPT" || c == "PDF" {
			return false
		}
	}
	return true
}

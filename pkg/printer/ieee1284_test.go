package printer

import (
	"slices"
	"testing"
)

func TestDeviceIDProfile(t *testing.T) {
	cases := []struct {
		name, id string
		driver   string
		formats  []Format
		pjl      bool
	}{
		{"GK888t says EPL but is a Zebra", "MANUFACTURER:Zebra Technologies;COMMAND SET:EPL;MODEL:ZTC GK888t (EPL);",
			DriverZebra, []Format{FormatZPL}, false},
		{"ZT411", "MFG:Zebra Technologies;CMD:ZPL;MDL:ZTC ZT411-203dpi ZPL;CLS:PRINTER;", DriverZebra, []Format{FormatZPL}, false},
		{"Epson TM-T88V", "MFG:EPSON;CMD:ESCPOS;MDL:TM-T88V;CLS:PRINTER;DES:EPSON TM-T88V Receipt;",
			DriverESCPOS, []Format{FormatESCPOS}, false},
		{"Xprinter", "MFG:Xprinter;CMD:ESC/POS;MDL:XP-58IIH;CLS:PRINTER;", DriverESCPOS, []Format{FormatESCPOS}, false},
		{"POS58 with no command set", "MFG:Printer;MDL:POS58 Printer USB;CLS:PRINTER;", DriverESCPOS, []Format{FormatESCPOS}, false},
		{"HP LaserJet", "MFG:Hewlett-Packard;CMD:PJL,PML,PCLXL,POSTSCRIPT,PCL;MDL:HP LaserJet 400 M401dn;CLS:PRINTER;",
			DriverPDL, []Format{FormatPCL, FormatPostScript}, true},
		{"Brother", "MFG:Brother;CMD:PJL,PCL,PCLXL,URF;MDL:HL-L2350DW series;CLS:PRINTER;", DriverPDL, []Format{FormatPCL}, true},
		{"host-based inkjet", "MFG:HP;CMD:PCL3GUI,PJL,Automatic;MDL:DeskJet 2130 series;", "", nil, false},
		{"unknown", "MFG:Acme;MDL:Thing;", "", nil, false},
		{"empty", "", "", nil, false},
	}
	for _, c := range cases {
		p := ParseDeviceID(c.id).Profile()
		if p.Driver != c.driver || !slices.Equal(p.Formats, c.formats) || p.PJL != c.pjl {
			t.Errorf("%s: %+v", c.name, p)
		}
	}
	if m := ParseDeviceID("MFG:EPSON;MDL:TM-T82;").Model; m != "TM-T82" {
		t.Fatalf("model %q", m)
	}
}

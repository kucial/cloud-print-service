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
		// Captured from real printers.
		{"Epson L355 (ESC/P-R inkjet)", "MFG:EPSON;CMD:ESCPL2,BDC,D4,D4PX,ESCPR1;MDL:L355 Series;CLS:PRINTER;DES:EPSON L355 Series;CID:EpsonRGB;FID:FXN,DPN,WFA,ETN,AFN,DAN;RID:00;",
			DriverESCPR, []Format{FormatESCPR}, false},
		{"Canon LBP7010C (CAPT, host-based)", "MFG:Canon;MDL:LBP7010C/7018C;CMD:CAPT;VER:3.0;CLS:PRINTER;DES:Canon LBP7010C/7018C",
			"", nil, false},
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

func TestMayBeZebra(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"MANUFACTURER:Zebra Technologies;COMMAND SET:EPL;MODEL:ZTC GK888t (EPL);", true},
		{"MFG:Acme Labels;CMD:ZPL;MDL:LP-1;", true}, // a ZPL clone that says so
		{"", true}, // says nothing: worth asking
		{"MFG:Acme;MDL:Thing;", true},
		{"MFG:Canon;MDL:LBP7010C/7018C;CMD:CAPT;VER:3.0;CLS:PRINTER;", false},
		{"MFG:EPSON;CMD:ESCPL2,BDC,D4,D4PX,ESCPR1;MDL:L355 Series;", false},
		{"MFG:Hewlett-Packard;MDL:LaserJet;", false}, // office maker, no command set
		{"MFG:Brother Industries;MDL:HL-1110;", false},
	}
	for _, c := range cases {
		if got := ParseDeviceID(c.id).MayBeZebra(); got != c.want {
			t.Errorf("%q: MayBeZebra = %v, want %v", c.id, got, c.want)
		}
	}
}

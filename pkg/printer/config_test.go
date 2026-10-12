package printer

import (
	"os"
	"testing"
)

// testdata/gk888t_hh.txt is a raw ^HH reply captured over USB from the GK888t
// (60 x 40 mm gap media; label length is the measured pitch, label + gap).
func TestParseHostConfigRealGK888t(t *testing.T) {
	raw, err := os.ReadFile("testdata/gk888t_hh.txt")
	if err != nil {
		t.Fatal(err)
	}
	c, ok := ParseHostConfig(string(raw))
	if !ok {
		t.Fatal("not parsed")
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"dpmm", c.DPMM, 8.0},
		{"dpi", c.DPI, 203},
		{"print width dots", c.PrintWidthDots, 480},
		{"print width mm", c.PrintWidthMM, 60.0},
		{"label length dots", c.LabelLengthDots, 334},
		{"label length mm", c.LabelLengthMM, 41.8},
		{"label top", c.LabelTopDots, 0},
		{"darkness", c.Darkness, "15.0"},
		{"speed", c.PrintSpeed, "2 IPS"},
		{"method", c.PrintMethod, "THERMAL-TRANS."},
		{"media", c.MediaType, "GAP/NOTCH"},
		{"language", c.Language, "ZPL II"},
		{"firmware", c.Firmware, "V71.17.15Z"},
		{"value with inner spaces", c.Settings["MAXIMUM LENGTH"], "39.0IN 989MM"},
	}
	for _, ch := range checks {
		if ch.got != ch.want {
			t.Errorf("%s = %v, want %v", ch.name, ch.got, ch.want)
		}
	}
}

func TestHostQueryFraming(t *testing.T) {
	for _, q := range []string{"~HS", " ~HI", "^XA^HH^XZ", CmdHostDirectory, "^HS"} {
		if !IsHostQuery(q) {
			t.Errorf("%q should be a host query", q)
		}
	}
	for _, d := range []string{"^XA^FDhi^FS^XZ", "~JC", "~JA"} {
		if IsHostQuery(d) {
			t.Errorf("%q is not a query", d)
		}
	}
	if ReplyComplete("~HS", []byte("\x02a\x03\x02b\x03")) || !ReplyComplete("~HS", []byte(gk888tHS)) {
		t.Error("~HS needs three frames")
	}
	if !ReplyComplete("~HI", []byte(gk888tHI)) {
		t.Error("~HI is one frame")
	}
}

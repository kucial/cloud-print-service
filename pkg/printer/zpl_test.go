package printer

import "testing"

// hs builds a real-firmware ~HS reply: three STX…ETX framed lines.
func hs(line1, line2 string) string {
	return "\x02" + line1 + "\x03\r\n\x02" + line2 + "\x03\r\n\x021234,0\x03\r\n"
}

const (
	l1Idle = "030,0,0,0245,000,0,0,0,000,0,0,0"
	l2Idle = "000,0,0,0,0,2,4,0,00000000,1,000"
)

func TestParseHostStatusRealFirmware(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		want   State
		wantOK bool
	}{
		{"idle", hs(l1Idle, l2Idle), StateIdle, true},
		{"paper out", hs("030,1,0,0245,000,0,0,0,000,0,0,0", l2Idle), StateMediaOut, true},
		{"paused", hs("030,0,1,0245,000,0,0,0,000,0,0,0", l2Idle), StateError, true},
		{"formats queued", hs("030,0,0,0245,002,0,0,0,000,0,0,0", l2Idle), StatePrinting, true},
		{"labels remaining", hs(l1Idle, "000,0,0,0,0,2,4,0,00000003,1,000"), StatePrinting, true},
		{"buffer full", hs("030,0,0,0245,000,1,0,0,000,0,0,0", l2Idle), StateBusy, true},
		{"head open", hs(l1Idle, "000,0,1,0,0,2,4,0,00000000,1,000"), StateHeadError, true},
		{"ribbon out", hs(l1Idle, "000,0,0,1,1,2,4,0,00000000,1,000"), StateRibbonOut, true},
		{"over temperature", hs("030,0,0,0245,000,0,0,0,000,0,0,1", l2Idle), StateError, true},
		{"head open beats paper out", hs("030,1,1,0245,000,0,0,0,000,0,0,0", "000,0,1,0,0,2,4,0,00000000,1,000"), StateHeadError, true},
		// Some firmware sends the lines without framing.
		{"unframed", l1Idle + "\r\n" + l2Idle + "\r\n1234,0\r\n", StateIdle, true},
		{"empty", "", StateUnknown, false},
		{"garbage", "hello", StateUnknown, false},
	}
	for _, c := range cases {
		got, detail, ok := ParseHostStatus(c.raw)
		if got != c.want || ok != c.wantOK {
			t.Errorf("%s: got %s ok=%v (%s), want %s ok=%v", c.name, got, ok, detail, c.want, c.wantOK)
		}
	}
}

func TestParseHostStatusLegacy(t *testing.T) {
	cases := map[string]State{
		"^HS0,0,0,0^HS": StateIdle,
		"^HS1,0,0,0^HS": StatePrinting,
		"^HS0,1,0,0^HS": StateMediaLow,
		"^HS0,2,0,0^HS": StateMediaOut,
		"^HS0,0,0,1^HS": StateHeadError,
		"^HS3,0,0,0^HS": StateError,
	}
	for raw, want := range cases {
		if got, _, ok := ParseHostStatus(raw); got != want || !ok {
			t.Errorf("%q: got %s ok=%v, want %s", raw, got, ok, want)
		}
	}
}

func TestParseHostID(t *testing.T) {
	id, ok := ParseHostID("\x02GK888t,V61.17.17Z,8,8192KB\x03")
	if !ok || id.Model != "GK888t" || id.Firmware != "V61.17.17Z" || id.DPMM != 8 || id.Serial != "" {
		t.Fatalf("real ~HI: %+v ok=%v", id, ok)
	}
	id, ok = ParseHostID(";8=Zebra;9=ZT411;10=12.0;11=STD;12=VPRT01;13=V1;")
	if !ok || id.Model != "ZT411" || id.Serial != "VPRT01" || id.DPMM != 12 || id.Firmware != "V1" {
		t.Fatalf("virtual ~HI: %+v ok=%v", id, ok)
	}
	if _, ok := ParseHostID(""); ok {
		t.Fatal("empty reply must not identify")
	}
}

// Captured over USB from a Zebra GK888t (EPL firmware, 1284 COMMAND SET:EPL), which
// still answers the ZPL tilde host commands.
const (
	gk888tHI = "\x02GK888t (EPL)-200dpi,V71.17.15Z,8,2104KB\x03\r\n"
	gk888tHS = "\x02030,0,0,0333,000,0,0,0,000,0,0,0\x03\r\n\x02001,0,0,0,1,2,3,0,00000000,1,000\x03\r\n\x021234,0\x03\r\n"
)

func TestRealGK888t(t *testing.T) {
	id, ok := ParseHostID(gk888tHI)
	if !ok || id.Model != "GK888t (EPL)-200dpi" || id.Firmware != "V71.17.15Z" || id.DPMM != 8 {
		t.Fatalf("~HI: %+v ok=%v", id, ok)
	}
	if st, detail, ok := ParseHostStatus(gk888tHS); st != StateIdle || !ok {
		t.Fatalf("~HS: %s ok=%v (%s)", st, ok, detail)
	}
}

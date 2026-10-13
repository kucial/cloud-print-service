package printer

import (
	"strconv"
	"testing"
)

func pjl(code int, display, online string) []byte {
	return []byte("@PJL INFO STATUS\r\nCODE=" + strconv.Itoa(code) + "\r\nDISPLAY=\"" + display + "\"\r\nONLINE=" + online + "\r\n\f")
}

func TestParsePJLStatus(t *testing.T) {
	cases := []struct {
		code   int
		online string
		want   State
	}{
		{10001, "TRUE", StateIdle},
		{10023, "TRUE", StatePrinting},
		{10003, "TRUE", StateBusy},
		{10006, "TRUE", StateTonerLow},
		{40021, "TRUE", StateDoorOpen},
		{40022, "TRUE", StatePaperJam},
		{42102, "TRUE", StatePaperJam},
		{41302, "TRUE", StateMediaOut},
		{40010, "TRUE", StateTonerOut},
		{50001, "TRUE", StateError},
		{10001, "FALSE", StateOffline},
		{30016, "TRUE", StateIdle}, // memory overflow: the printer continues
	}
	for _, c := range cases {
		st, ok := ParsePJLStatus(pjl(c.code, "x", c.online))
		if !ok || st.State() != c.want {
			t.Errorf("code %d online %s: %+v → %s, want %s", c.code, c.online, st, st.State(), c.want)
		}
	}
	if _, ok := ParsePJLStatus([]byte("garbage")); ok {
		t.Fatal("garbage parsed")
	}
	if !PJLReplyComplete(pjl(10001, "Ready", "TRUE")) || PJLReplyComplete([]byte("@PJL INFO STATUS\r\nCODE=1")) {
		t.Fatal("completion")
	}
}

func TestParsePJLID(t *testing.T) {
	if m, ok := ParsePJLID([]byte("@PJL INFO ID\r\n\"HP LaserJet 4250\"\r\n\f")); !ok || m != "HP LaserJet 4250" {
		t.Fatalf("model %q %v", m, ok)
	}
	if !IsPJLQuery(CmdPJLStatus) || !IsPJLQuery(CmdPJLID) || IsPJLQuery([]byte("%!PS")) {
		t.Fatal("query detection")
	}
}

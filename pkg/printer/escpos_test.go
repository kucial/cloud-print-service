package printer

import "testing"

func TestParseESCPOSStatus(t *testing.T) {
	const ok = 0x12 // the fixed bits of every DLE EOT reply, nothing set
	cases := []struct {
		name       string
		s1, s2, s4 byte
		want       State
	}{
		{"ready", ok, ok, ok, StateIdle},
		{"roll near end", ok, ok, ok | 0x0c, StateMediaLow},
		{"roll end (paper sensor)", ok | 0x08, ok, ok | 0x60, StateMediaOut},
		{"stopped at paper end", ok | 0x08, ok | 0x20, ok, StateMediaOut},
		{"cover open", ok | 0x08, ok | 0x04, ok, StateDoorOpen},
		{"cutter error", ok | 0x08, ok | 0x40, ok, StateError},
		{"offline, no cause given", ok | 0x08, ok, ok, StateOffline},
		{"paper end wins over cover", ok | 0x08, ok | 0x24, ok, StateMediaOut},
	}
	for _, c := range cases {
		st, valid := ParseESCPOSStatus(c.s1, c.s2, c.s4)
		if !valid || st.State() != c.want {
			t.Errorf("%s: %+v valid=%v, want %s", c.name, st, valid, c.want)
		}
	}
	// A ZPL reply or line noise is not a status byte.
	for _, b := range []byte{0x02, '0', 0xff, 0x00} {
		if _, valid := ParseESCPOSStatus(b, ok, ok); valid {
			t.Errorf("%#x accepted as a status byte", b)
		}
	}
}

func TestParseESCPOSInfo(t *testing.T) {
	if m, ok := ParseESCPOSInfo([]byte("_TM-T88V\x00")); !ok || m != "TM-T88V" {
		t.Fatalf("model: %q %v", m, ok)
	}
	if _, ok := ParseESCPOSInfo([]byte{0x12}); ok {
		t.Fatal("a status byte is not a model name")
	}
	if !ESCPOSInfoComplete([]byte("_EPSON\x00")) || ESCPOSInfoComplete([]byte("_EPS")) {
		t.Fatal("completion")
	}
}

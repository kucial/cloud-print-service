package driver

import (
	"errors"
	"testing"
)

func TestApplyCopiesZPL(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		copies int
		want   string
		total  int
		err    error
	}{
		{"one label, no ^PQ: added before ^XZ",
			"^XA^FO50,50^FDSKU-1^FS^XZ", 3, "^XA^FO50,50^FDSKU-1^FS^PQ3^XZ", 3, nil},
		{"^PQ1 is replaced, its other parameters kept",
			"^XA^FDx^FS^PQ1,0,1,Y^XZ", 4, "^XA^FDx^FS^PQ4,0,1,Y^XZ", 4, nil},
		{"two labels: each printed copies times",
			"^XA^FDa^FS^XZ\n^XA^FDb^FS^XZ", 2, "^XA^FDa^FS^PQ2^XZ\n^XA^FDb^FS^PQ2^XZ", 4, nil},
		{"setup blocks are not multiplied",
			"^XA^LT20^JUS^XZ^XA^FDa^FS^XZ", 2, "^XA^LT20^JUS^XZ^XA^FDa^FS^PQ2^XZ", 2, nil},
		{"a graphic-only label counts",
			"^XA^FO0,0^GFA,4,4,1,FFFFFFFF^FS^XZ", 2, "^XA^FO0,0^GFA,4,4,1,FFFFFFFF^FS^PQ2^XZ", 2, nil},
		{"lower-case commands", "^xa^fdx^fs^xz", 2, "^xa^fdx^fs^PQ2^xz", 2, nil},
		{"downloads and trailing commands are kept as they are",
			"~DGR:LOGO.GRF,4,1,FFFFFFFF\n^XA^XGR:LOGO.GRF^FS^XZ\n~JS", 2,
			"~DGR:LOGO.GRF,4,1,FFFFFFFF\n^XA^XGR:LOGO.GRF^FS^PQ2^XZ\n~JS", 2, nil},
		{"the label asks for 3 already", "^XA^FDx^FS^PQ3^XZ", 2, "", 0, ErrCopiesConflict},
		{"one of two labels asks for more", "^XA^FDa^FS^XZ^XA^FDb^FS^PQ2,0,0,N^XZ", 2, "", 0, ErrCopiesConflict},
		{"changed command prefix", "^XA^CC+^XA+FDx+FS+XZ", 2, "", 0, ErrCopiesUnreadable},
		{"no label at all", "~JA", 2, "", 0, ErrCopiesUnreadable},
		{"only setup", "^XA^JUS^XZ", 2, "", 0, ErrCopiesUnreadable},
	}
	for _, c := range cases {
		got, total, err := ApplyCopiesZPL(c.in, c.copies)
		if c.err != nil {
			if !errors.Is(err, c.err) {
				t.Errorf("%s: err %v, want %v", c.name, err, c.err)
			}
			continue
		}
		if err != nil || got != c.want || total != c.total {
			t.Errorf("%s:\n got %q (%d) %v\nwant %q (%d)", c.name, got, total, err, c.want, c.total)
		}
	}
	// One copy changes nothing, whatever the label says.
	if got, total, err := ApplyCopiesZPL("^XA^FDx^FS^PQ3^XZ", 1); err != nil || got != "^XA^FDx^FS^PQ3^XZ" || total != 3 {
		t.Fatalf("copies=1: %q %d %v", got, total, err)
	}
}

// The ^PQ pattern takes the whole command: the override flag must not be left behind.
func TestInjectPQKeepsNoStrayParameters(t *testing.T) {
	if got := InjectPQ("^XA^FDx^FS^PQ5,0,1,Y^XZ", 2); got != "^XA^FDx^FS^PQ2^XZ" {
		t.Fatalf("InjectPQ: %q", got)
	}
}

// page_copies keeps what sits between and after the labels.
func TestApplyPageCopiesKeepsBytesAroundLabels(t *testing.T) {
	in := "~DGR:L.GRF,4,1,FFFFFFFF\n^XA^FDa^FS^XZ\n^XA^FDtail^FS^XZ\n~JS"
	got, total, err := ApplyPageCopies(in, []int{4, 0})
	want := "~DGR:L.GRF,4,1,FFFFFFFF\n^XA^FDa^FS^PQ4^XZ\n\n~JS"
	if err != nil || got != want || total != 4 {
		t.Fatalf("got %q (%d) %v\nwant %q", got, total, err, want)
	}
}

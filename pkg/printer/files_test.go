package printer

import (
	"os"
	"testing"
)

// testdata/gk888t_hw.txt is a raw ^HW reply from the GK888t (China model).
func TestHostDirectoryRealGK888t(t *testing.T) {
	raw, err := os.ReadFile("testdata/gk888t_hw.txt")
	if err != nil {
		t.Fatal(err)
	}
	files := ParseHostDirectory(string(raw))
	if len(files) < 40 {
		t.Fatalf("parsed %d files", len(files))
	}
	if f, ok := FindFile(files, "SIMSUN.FNT"); !ok || f.Name != "E:SIMSUN.FNT" || f.Size != 1668928 {
		t.Fatalf("SIMSUN.FNT: %+v ok=%v", f, ok)
	}
	cjk := CJKFonts(files)
	if len(cjk) != 2 || cjk[0].Name != "E:SIMSUN.FNT" || cjk[1].Name != "E:CHS.FNT" {
		t.Fatalf("CJK fonts: %+v", cjk)
	}
	fonts := Fonts(files)
	if len(fonts) < 10 || !fonts[0].CJK || fonts[0].Name != "E:SIMSUN.FNT" || !fonts[1].CJK || fonts[2].CJK {
		t.Fatalf("fonts: want CJK first and flagged, got %+v", fonts[:3])
	}
	for _, f := range fonts {
		if !isFont(f.Name) {
			t.Fatalf("non-font %s in font list", f.Name)
		}
	}
	// SIMSUN is a GB-indexed bitmap font: UTF-8 needs the GB18030 table.
	if got := CJKFieldPrefix(cjk[0], files); got != "^CW1,E:SIMSUN.FNT^CI28^SEE:GB18030.DAT" {
		t.Fatalf("setup %q", got)
	}
}

func TestCJKFieldPrefixTrueType(t *testing.T) {
	files := []StoredFile{{Name: "E:ANMDS.TTF", Size: 9 << 20}, {Name: "E:GB18030.DAT", Size: 95760}}
	if got := CJKFieldPrefix(files[0], files); got != "^CW1,E:ANMDS.TTF^CI28" {
		t.Fatalf("TrueType fonts are Unicode, no encoding table: %q", got)
	}
}

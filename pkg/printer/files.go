package printer

import (
	"sort"
	"strconv"
	"strings"
)

// CmdHostDirectory lists every stored object on every device (fonts, encodings,
// graphics) without printing anything.
const CmdHostDirectory = "^XA^HW*:*.*^XZ"

// StoredFile is one object in the printer's memory, e.g. E:SIMSUN.FNT.
type StoredFile struct {
	Name string `json:"name"` // device-qualified: "E:SIMSUN.FNT"
	Size int    `json:"size"`
	CJK  bool   `json:"cjk,omitempty"` // set by Fonts: the font has CJK glyphs
}

// ParseHostDirectory decodes a ^HW reply: lines "* E:SIMSUN.FNT  1668928  P …".
func ParseHostDirectory(raw string) []StoredFile {
	var out []StoredFile
	for _, line := range strings.FieldsFunc(raw, func(r rune) bool { return r == '\r' || r == '\n' }) {
		f := strings.Fields(strings.Trim(line, "\x02\x03 "))
		if len(f) < 2 || f[0] != "*" || !strings.Contains(f[1], ":") {
			continue
		}
		sf := StoredFile{Name: f[1]}
		if len(f) > 2 {
			sf.Size, _ = strconv.Atoi(f[2])
		}
		out = append(out, sf)
	}
	return out
}

// Fonts that carry CJK glyphs, by file name. Zebra's Chinese/Japanese/Korean options
// ship under these names; anything large enough to be a CJK font also counts.
var cjkFontNames = []string{"SIMSUN", "CHS", "ANMDS", "ANMDJ", "ANMDK", "ANSD", "ANDALE", "NOTO", "MSUNG", "MHEI",
	"HANSANS", "GB", "BIG5", "SJIS", "KOREAN", "JAPAN"}

// CJKFonts returns the stored fonts that can print CJK text, best-known first
// (SIMSUN before CHS, …), then largest.
func CJKFonts(files []StoredFile) []StoredFile {
	var out []StoredFile
	for _, f := range files {
		up := strings.ToUpper(f.Name)
		if !strings.HasSuffix(up, ".FNT") && !strings.HasSuffix(up, ".TTF") && !strings.HasSuffix(up, ".TTE") {
			continue
		}
		base := up[strings.Index(up, ":")+1:]
		known := false
		for _, n := range cjkFontNames {
			if strings.HasPrefix(base, n) {
				known = true
				break
			}
		}
		// Latin bitmap fonts are tens of KB; a CJK glyph set is megabytes.
		if known || f.Size > 1<<20 {
			out = append(out, f)
		}
	}
	rank := func(f StoredFile) int {
		up := strings.ToUpper(f.Name)
		base := up[strings.Index(up, ":")+1:]
		for i, n := range cjkFontNames {
			if strings.HasPrefix(base, n) {
				return i
			}
		}
		return len(cjkFontNames)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := rank(out[i]), rank(out[j]); ri != rj {
			return ri < rj
		}
		return out[i].Size > out[j].Size
	})
	return out
}

func isFont(name string) bool {
	up := strings.ToUpper(name)
	return strings.HasSuffix(up, ".FNT") || strings.HasSuffix(up, ".TTF") || strings.HasSuffix(up, ".TTE")
}

// Fonts returns the stored fonts with their CJK flag set: CJK fonts first, in
// CJKFonts order, then the rest by name. This is what the API reports as a printer's
// available fonts, so business systems pick a font the printer really has.
func Fonts(files []StoredFile) []StoredFile {
	out := []StoredFile{}
	cjk := CJKFonts(files)
	isCJK := map[string]bool{}
	for _, f := range cjk {
		f.CJK = true
		isCJK[f.Name] = true
		out = append(out, f)
	}
	var rest []StoredFile
	for _, f := range files {
		if isFont(f.Name) && !isCJK[f.Name] {
			rest = append(rest, f)
		}
	}
	sort.SliceStable(rest, func(i, j int) bool { return rest[i].Name < rest[j].Name })
	return append(out, rest...)
}

// FindFile returns the stored file whose name (with or without device prefix) matches.
func FindFile(files []StoredFile, name string) (StoredFile, bool) {
	want := strings.ToUpper(strings.TrimSpace(name))
	for _, f := range files {
		up := strings.ToUpper(f.Name)
		if up == want || strings.HasSuffix(up, ":"+want) {
			return f, true
		}
	}
	return StoredFile{}, false
}

// CJKFieldPrefix is the ZPL that sets up font slot 1 for UTF-8 CJK text with font, and
// the font command to use in each field. Bitmap CJK fonts (.FNT) are indexed by a
// multibyte encoding, so the printer needs the matching table (^SE) to map UTF-8 to it;
// TrueType fonts are Unicode already.
func CJKFieldPrefix(font StoredFile, files []StoredFile) (setup string) {
	setup = "^CW1," + font.Name + "^CI28"
	if strings.HasSuffix(strings.ToUpper(font.Name), ".FNT") {
		for _, table := range []string{"GB18030.DAT", "GB.DAT", "BIG5.DAT", "SJIS.DAT", "KSC.DAT"} {
			if t, ok := FindFile(files, table); ok {
				setup += "^SE" + t.Name
				break
			}
		}
	}
	return setup
}

// FormatHostDirectory renders files the way firmware answers ^HW, for virtual and mock
// printers.
func FormatHostDirectory(files []StoredFile) string {
	var b strings.Builder
	b.WriteString("\x02\r\n- DIR *:*.*\r\n")
	for _, f := range files {
		b.WriteString("* " + f.Name + "   " + strconv.Itoa(f.Size) + "  P\r\n")
	}
	b.WriteString("\r\n\x03")
	return b.String()
}

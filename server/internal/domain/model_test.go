package domain

import (
	"testing"
)

func TestParsePaperSize(t *testing.T) {
	cases := []struct {
		input     string
		wantW     float64
		wantH     float64
		wantNorm  string
		wantValid bool
	}{
		{"4x6", 101.6, 152.4, "4x6", true},
		{"4x6in", 101.6, 152.4, "4x6", true},
		{"4x2", 101.6, 50.8, "4x2", true},
		{"2x1", 50.8, 25.4, "2x1", true},
		{"100x150mm", 100, 150, "100x150mm", true},
		{"100x150 mm", 100, 150, "100x150mm", true},
		{"100*150mm", 100, 150, "100x150mm", true},
		{"100x150", 100, 150, "100x150mm", true},
		{"80x50mm", 80, 50, "80x50mm", true},
		{"76x130mm", 76, 130, "76x130mm", true},
		{"A6", 105.0, 148.0, "A6", true},
		{"invalid-text", 0, 0, "invalid-text", false},
	}

	for _, tc := range cases {
		w, h, norm, ok := ParsePaperSize(tc.input)
		if ok != tc.wantValid {
			t.Errorf("ParsePaperSize(%q) ok = %v, want %v", tc.input, ok, tc.wantValid)
			continue
		}
		if ok {
			if w != tc.wantW || h != tc.wantH || norm != tc.wantNorm {
				t.Errorf("ParsePaperSize(%q) = (%v, %v, %q), want (%v, %v, %q)",
					tc.input, w, h, norm, tc.wantW, tc.wantH, tc.wantNorm)
			}
		}
	}
}

func TestPrinterNormalize(t *testing.T) {
	// 1. DPI -> DPMM & mm paper size parsing
	p1 := Printer{
		PrinterID: "test-1",
		PaperSize: "100x150mm",
		DPI:       203,
		CJKFont:   "SIMSUN.FNT",
	}
	p1.Normalize()
	if p1.DPMM != 8 {
		t.Errorf("expected DPMM 8, got %v", p1.DPMM)
	}
	if p1.MediaWidthMM != 100 || p1.MediaHeightMM != 150 {
		t.Errorf("expected 100x150mm, got %vx%v", p1.MediaWidthMM, p1.MediaHeightMM)
	}
	if !p1.CJKSupported {
		t.Errorf("expected CJKSupported true")
	}
	if len(p1.Fonts) == 0 || p1.Fonts[0] != "SIMSUN.FNT" {
		t.Errorf("expected SIMSUN.FNT in fonts, got %v", p1.Fonts)
	}
	if p1.Orientation != "normal" || p1.MediaOrientation != "normal" {
		t.Errorf("expected normal orientation, got %q", p1.Orientation)
	}
	if p1.PrintDirection != "top_first" {
		t.Errorf("expected top_first print direction, got %q", p1.PrintDirection)
	}
	if p1.ResolutionSource != "config" || p1.DPIResolvedFromHI != false {
		t.Errorf("expected config resolution source without HI verification")
	}
	if p1.FontSource != "config" || p1.FontResolvedFromHI != false {
		t.Errorf("expected config font source without HI verification")
	}

	// 1b. ~HI verified printer
	p1HI := Printer{
		PrinterID:          "test-1-hi",
		DPI:                300,
		DPIResolvedFromHI:  true,
		CJKFont:            "ANSD.TTF",
		FontResolvedFromHI: true,
	}
	p1HI.Normalize()
	if p1HI.ResolutionSource != "hi" || !p1HI.DPIResolvedFromHI {
		t.Errorf("expected hi resolution source")
	}
	if p1HI.FontSource != "hi" || !p1HI.FontResolvedFromHI {
		t.Errorf("expected hi font source")
	}

	// 2. DPMM -> DPI & 4x6 paper size
	p2 := Printer{
		PrinterID: "test-2",
		PaperSize: "4x6",
		DPMM:      12,
	}
	p2.Normalize()
	if p2.DPI != 300 {
		t.Errorf("expected DPI 300, got %v", p2.DPI)
	}
	if p2.MediaWidthMM != 101.6 || p2.MediaHeightMM != 152.4 {
		t.Errorf("expected 101.6x152.4mm, got %vx%v", p2.MediaWidthMM, p2.MediaHeightMM)
	}

	// 3. Media dimensions -> PaperSize formatted with mm
	p3 := Printer{
		PrinterID:     "test-3",
		MediaWidthMM:  76,
		MediaHeightMM: 130,
	}
	p3.Normalize()
	if p3.PaperSize != "76x130mm" {
		t.Errorf("expected 76x130mm, got %q", p3.PaperSize)
	}
}

func TestMatchesPaperSize(t *testing.T) {
	p := Printer{
		PrinterID:     "p1",
		PaperSize:     "100x150mm",
		MediaWidthMM:  100,
		MediaHeightMM: 150,
	}

	if !p.MatchesPaperSize("100x150mm") {
		t.Errorf("expected match on 100x150mm")
	}
	if !p.MatchesPaperSize("100x150") {
		t.Errorf("expected match on 100x150 (bare mm)")
	}
	if !p.MatchesPaperSize("4x6") {
		// 100x150mm is within 2.5mm of 101.6x152.4mm
		t.Errorf("expected match on 4x6 for 100x150mm roll")
	}
	if p.MatchesPaperSize("4x2") {
		t.Errorf("unexpected match on 4x2")
	}
}

package httpapi

import (
	"bytes"
	"fmt"
	"time"

	"github.com/go-pdf/fpdf"
)

// testPagePDF is an A4 page that shows whether an office printer prints the whole sheet
// where it should: a border 10 mm inside the edge, millimetre rulers along the top and
// left, corner marks, a grey ramp for toner, and the printer's identity.
//
// It uses the PDF core fonts, so it is ASCII only: a CJK test needs a TrueType font the
// server does not ship.
func testPagePDF(name, printerID string, formats []string, now time.Time) ([]byte, error) {
	const w, h, margin = 210.0, 297.0, 10.0
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetTitle("CPS test page", false)
	pdf.SetCreator("Cloud Print Service", false)
	pdf.SetMargins(0, 0, 0)
	pdf.SetAutoPageBreak(false, 0)
	pdf.AddPage()

	// Border and corner marks.
	pdf.SetLineWidth(0.4)
	pdf.Rect(margin, margin, w-2*margin, h-2*margin, "D")
	pdf.SetLineWidth(0.2)
	for _, c := range [][2]float64{{0, 0}, {w, 0}, {0, h}, {w, h}} {
		dx, dy := 1.0, 1.0
		if c[0] > 0 {
			dx = -1
		}
		if c[1] > 0 {
			dy = -1
		}
		pdf.Line(c[0]+dx*3, c[1]+dy*3, c[0]+dx*8, c[1]+dy*3)
		pdf.Line(c[0]+dx*3, c[1]+dy*3, c[0]+dx*3, c[1]+dy*8)
	}

	// Rulers: a tick every millimetre inside the border, longer every 5 and 10.
	pdf.SetFont("Helvetica", "", 6)
	for x := 0.0; x <= w-2*margin; x++ {
		l := tick(x)
		pdf.Line(margin+x, margin, margin+x, margin+l)
		if int(x)%10 == 0 && x > 0 {
			pdf.Text(margin+x-1.5, margin+l+2.5, fmt.Sprint(int(x)))
		}
	}
	for y := 0.0; y <= h-2*margin; y++ {
		l := tick(y)
		pdf.Line(margin, margin+y, margin+l, margin+y)
		if int(y)%10 == 0 && y > 0 {
			pdf.Text(margin+l+1, margin+y+1, fmt.Sprint(int(y)))
		}
	}

	// Identity.
	pdf.SetFont("Helvetica", "B", 28)
	pdf.Text(30, 50, "CPS TEST PAGE")
	pdf.SetFont("Helvetica", "", 12)
	lines := []string{
		"Printer:  " + name,
		"ID:       " + printerID,
		fmt.Sprintf("Formats:  %v", formats),
		"Printed:  " + now.Format("2006-01-02 15:04:05 MST"),
		"",
		"The border is 10 mm from every edge. If a side is missing or the",
		"rulers do not start at the border, the page is cut off or shifted.",
	}
	for i, l := range lines {
		pdf.Text(30, 65+float64(i)*7, l)
	}

	// Grey ramp: ten steps from white to black.
	for i := 0; i < 10; i++ {
		g := 255 - i*255/9
		pdf.SetFillColor(g, g, g)
		pdf.Rect(30+float64(i)*15, 130, 15, 15, "FD")
	}
	pdf.SetFillColor(0, 0, 0)
	pdf.Text(30, 152, "0%")
	pdf.Text(165, 152, "100%")

	var out bytes.Buffer
	if err := pdf.Output(&out); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func tick(v float64) float64 {
	switch {
	case int(v)%10 == 0:
		return 5
	case int(v)%5 == 0:
		return 3.5
	}
	return 2
}

// testPagePCL is the test page for an office printer that takes PCL but not PDF: plain
// text in the printer's own font, with a frame of rules to show the printable area.
func testPagePCL(name, printerID string, now time.Time) []byte {
	var b bytes.Buffer
	b.WriteString("\x1bE")                // reset
	b.WriteString("\x1b&l26A")            // A4
	b.WriteString("\x1b&l0O")             // portrait
	b.WriteString("\x1b(s0p12h10v0s0b3T") // Courier 12 cpi
	// A frame: horizontal and vertical rules (PCL rectangle fill), in decipoints.
	rule := func(x, y, w, h int) {
		fmt.Fprintf(&b, "\x1b&a%dh%dV\x1b*c%dh%dV\x1b*c0P", x, y, w, h)
	}
	rule(0, 0, 5400, 10) // top, 7.5 in
	rule(0, 0, 10, 7600)
	rule(5390, 0, 10, 7600)
	rule(0, 7590, 5400, 10)
	b.WriteString("\x1b&a720h720V")
	for _, l := range []string{
		"CPS TEST PAGE",
		"",
		"Printer:  " + name,
		"ID:       " + printerID,
		"Language: PCL",
		"Printed:  " + now.Format("2006-01-02 15:04:05 MST"),
		"",
		"The frame marks the area the printer printed.",
	} {
		b.WriteString(l + "\r\n")
	}
	b.WriteString("\f\x1bE")
	return b.Bytes()
}

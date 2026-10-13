package ipp

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/kucial/cloud-print-service/pkg/printer"
)

func TestRoundTrip(t *testing.T) {
	req := NewRequest(OpPrintJob, 7, "ipp://10.0.0.5:631/ipp/print")
	req.Add(TagOperation, "requesting-user-name", Name("cps"))
	req.Add(TagOperation, "document-format", MimeType("application/pdf"))
	req.Add(TagJob, "copies", Integer(3))
	req.Add(TagOperation, "requested-attributes", Keyword("printer-state"), Keyword("printer-state-reasons"))
	req.Data = []byte("%PDF-1.4 …")

	got, err := Decode(req.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if got.Code != OpPrintJob || got.RequestID != 7 || got.Major != 2 {
		t.Fatalf("header: %+v", got)
	}
	if got.String("printer-uri") != "ipp://10.0.0.5:631/ipp/print" || got.String("document-format") != "application/pdf" {
		t.Fatalf("operation attributes: %+v", got.Groups)
	}
	if n, _ := got.Int("copies"); n != 3 {
		t.Fatalf("copies: %d", n)
	}
	if s := got.Strings("requested-attributes"); len(s) != 2 || s[1] != "printer-state-reasons" {
		t.Fatalf("multi-valued: %v", s)
	}
	if !bytes.Equal(got.Data, req.Data) {
		t.Fatalf("document: %q", got.Data)
	}
	// Operation attributes come first, as RFC 8010 requires.
	if got.Groups[0].Tag != TagOperation || got.Groups[0].Attrs[0].Name != "attributes-charset" {
		t.Fatalf("group order: %+v", got.Groups)
	}
}

// A Get-Printer-Attributes response as a printer sends it: a collection
// (media-col-default) in the middle, which must be skipped without losing the
// attributes after it, and a textWithLanguage value.
func TestDecodePrinterResponse(t *testing.T) {
	resp := &Message{Major: 2, Code: StatusOK, RequestID: 1}
	resp.Add(TagOperation, "attributes-charset", Value{Tag: TagCharset, Str: "utf-8"})
	resp.Add(TagPrinter, "printer-state", Enum(5))
	raw := resp.Encode()
	raw = raw[:len(raw)-1] // reopen: append a collection and more attributes by hand
	var b bytes.Buffer
	b.Write(raw)
	attr := func(tag byte, name, val string) {
		b.WriteByte(tag)
		b.Write([]byte{byte(len(name) >> 8), byte(len(name))})
		b.WriteString(name)
		b.Write([]byte{byte(len(val) >> 8), byte(len(val))})
		b.WriteString(val)
	}
	attr(TagBeginCollection, "media-col-default", "")
	attr(TagMemberName, "", "media-size")
	attr(TagBeginCollection, "", "")
	attr(TagMemberName, "", "x-dimension")
	attr(TagInteger, "", "\x00\x00\x52\x08")
	attr(TagEndCollection, "", "")
	attr(TagEndCollection, "", "")
	attr(TagKeyword, "printer-state-reasons", "media-empty-error")
	attr(TagKeyword, "", "toner-low-warning")
	lang, text := "en", "HP LaserJet Pro M404"
	attr(TagTextLang, "printer-make-and-model",
		string([]byte{0, byte(len(lang))})+lang+string([]byte{0, byte(len(text))})+text)
	attr(TagMimeType, "document-format-supported", "application/pdf")
	attr(TagMimeType, "", "image/urf")
	attr(TagMimeType, "", "application/vnd.hp-pcl")
	attr(TagMimeType, "", "application/octet-stream")
	b.WriteByte(TagEnd)

	m, err := Decode(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	info := InfoFrom(m)
	if info.State != 5 || info.MakeModel != text || len(info.Reasons) != 2 {
		t.Fatalf("info: %+v", info)
	}
	if f := Formats(info.Formats); len(f) != 3 || f[0] != printer.FormatPDF || f[1] != printer.FormatURF || f[2] != printer.FormatPCL {
		t.Fatalf("formats: %v", f)
	}
	if st, _ := State(info); st != printer.StateMediaOut {
		t.Fatalf("state: %s", st)
	}
}

func TestState(t *testing.T) {
	cases := []struct {
		state     int
		reasons   string
		accepting bool
		want      printer.State
	}{
		{3, "none", true, printer.StateIdle},
		{4, "none", true, printer.StatePrinting},
		{3, "toner-low-warning", true, printer.StateTonerLow},
		{3, "media-empty-warning", true, printer.StateMediaLow}, // a warning never blocks
		{5, "media-jam-error", true, printer.StatePaperJam},
		{5, "door-open", true, printer.StateDoorOpen},
		{5, "toner-empty-error,media-low-report", true, printer.StateTonerOut},
		{4, "marker-supply-low-warning", true, printer.StatePrinting}, // printing says more
		{5, "paused", true, printer.StateError},                       // stopped, reason unknown to CPS
		{3, "none", false, printer.StateError},                        // not accepting jobs
		{3, "door-open-warning", true, printer.StateIdle},
	}
	for _, c := range cases {
		st, detail := State(PrinterInfo{State: c.state, Reasons: strings.Split(c.reasons, ","), Accepting: c.accepting})
		if st != c.want {
			t.Errorf("state %d %q: got %s, want %s (%s)", c.state, c.reasons, st, c.want, detail)
		}
	}
}

func TestDecodeRejectsTruncated(t *testing.T) {
	full := NewRequest(OpGetPrinterAttributes, 1, "ipp://x/ipp/print").Encode()
	for _, n := range []int{3, 9, len(full) - 1} {
		if _, err := Decode(full[:n]); err == nil {
			t.Errorf("decoded %d of %d bytes: %s", n, len(full), hex.EncodeToString(full[:n]))
		}
	}
}

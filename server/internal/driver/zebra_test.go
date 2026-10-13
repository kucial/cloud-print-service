package driver

import (
	"testing"

	"github.com/kucial/cloud-print-service/server/internal/domain"
)

func TestParseHostStatus(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		want  domain.PrinterState
		valid bool
	}{
		{"idle", "^HS0,0,0,0^HS", domain.StateIdle, true},
		{"printing", "^HS1,0,0,0^HS", domain.StatePrinting, true},
		{"busy", "^HS2,0,0,0^HS", domain.StateBusy, true},
		{"media out wins over printing", "^HS1,2,0,0^HS", domain.StateMediaOut, true},
		{"ribbon out", "^HS0,0,2,0^HS", domain.StateRibbonOut, true},
		{"media low", "^HS0,1,0,0^HS", domain.StateMediaLow, true},
		{"printhead error", "^HS0,0,0,1^HS", domain.StateHeadError, true},
		{"device error", "^HS3,0,0,0^HS", domain.StateError, true},
		{"single field", "^HS0^HS", domain.StateIdle, true},
		{"leading noise", "garbage^HS0,0,0,0^HS", domain.StateIdle, true},
		{"empty", "", domain.StateUnknown, false},
		{"non numeric", "^HSabc^HS", domain.StateUnknown, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, ok := ParseHostStatus([]byte(tc.raw))
			if ok != tc.valid {
				t.Fatalf("valid = %v, want %v", ok, tc.valid)
			}
			if st.State != tc.want {
				t.Fatalf("state = %q, want %q", st.State, tc.want)
			}
			if st.Raw != tc.raw {
				t.Fatalf("raw must be preserved verbatim, got %q", st.Raw)
			}
		})
	}
}

func TestPrinterStatePredicates(t *testing.T) {
	for _, s := range []domain.PrinterState{domain.StateMediaOut, domain.StateOffline, domain.StateHeadError, domain.StateError, domain.StateDisabled} {
		if !s.BlocksUse() {
			t.Errorf("%s should block use", s)
		}
	}
	for _, s := range []domain.PrinterState{domain.StateIdle, domain.StatePrinting, domain.StateMediaLow, domain.StateUnknown} {
		if s.BlocksUse() {
			t.Errorf("%s should not block use", s)
		}
	}
	if !domain.StatePrinting.Busy() || !domain.StateBusy.Busy() || domain.StateIdle.Busy() {
		t.Error("Busy() mismatch")
	}
}

func TestDriverRegistry(t *testing.T) {
	if _, err := New(domain.Printer{PrinterID: "x", Driver: "nope"}, Options{}); err == nil {
		t.Fatal("expected error for unknown driver")
	}
	d, err := New(domain.Printer{PrinterID: "x", Driver: "mock"}, Options{})
	if err != nil || d == nil {
		t.Fatalf("mock driver: %v", err)
	}
	drs := Drivers()
	if len(drs) < 2 {
		t.Fatalf("expected mock+zebra registered, got %v", drs)
	}
}

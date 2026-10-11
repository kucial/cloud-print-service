package printer

import (
	"testing"

	"github.com/kongkx/cloud-print-service/internal/model"
)

func TestParseHostStatus(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		want  model.PrinterState
		valid bool
	}{
		{"idle", "^HS0,0,0,0^HS", model.StateIdle, true},
		{"printing", "^HS1,0,0,0^HS", model.StatePrinting, true},
		{"busy", "^HS2,0,0,0^HS", model.StateBusy, true},
		{"media out wins over printing", "^HS1,2,0,0^HS", model.StateMediaOut, true},
		{"ribbon out", "^HS0,0,2,0^HS", model.StateRibbonOut, true},
		{"media low", "^HS0,1,0,0^HS", model.StateMediaLow, true},
		{"printhead error", "^HS0,0,0,1^HS", model.StateHeadError, true},
		{"device error", "^HS3,0,0,0^HS", model.StateError, true},
		{"single field", "^HS0^HS", model.StateIdle, true},
		{"leading noise", "garbage^HS0,0,0,0^HS", model.StateIdle, true},
		{"empty", "", model.StateUnknown, false},
		{"non numeric", "^HSabc^HS", model.StateUnknown, false},
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
	for _, s := range []model.PrinterState{model.StateMediaOut, model.StateOffline, model.StateHeadError, model.StateError, model.StateDisabled} {
		if !s.BlocksUse() {
			t.Errorf("%s should block use", s)
		}
	}
	for _, s := range []model.PrinterState{model.StateIdle, model.StatePrinting, model.StateMediaLow, model.StateUnknown} {
		if s.BlocksUse() {
			t.Errorf("%s should not block use", s)
		}
	}
	if !model.StatePrinting.Busy() || !model.StateBusy.Busy() || model.StateIdle.Busy() {
		t.Error("Busy() mismatch")
	}
}

func TestDriverRegistry(t *testing.T) {
	if _, err := New(model.Printer{PrinterID: "x", Driver: "nope"}); err == nil {
		t.Fatal("expected error for unknown driver")
	}
	d, err := New(model.Printer{PrinterID: "x", Driver: "mock"})
	if err != nil || d == nil {
		t.Fatalf("mock driver: %v", err)
	}
	drs := Drivers()
	if len(drs) < 2 {
		t.Fatalf("expected mock+zebra registered, got %v", drs)
	}
}

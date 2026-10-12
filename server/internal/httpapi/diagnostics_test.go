package httpapi

import (
	"net/http"
	"testing"

	"github.com/kucial/cloud-print-service/server/internal/domain"
)

func TestDiagnosticsReportsPrinterSetup(t *testing.T) {
	declared := mockSpec("p1", &domain.MockCfg{})
	declared.PaperSize = "100x150mm"
	declared.PrintDirection = "bottom_first"
	ts := newTestServer(t, declared, mockSpec("bare", &domain.MockCfg{}))

	res, body := do(t, ts, "GET", "/api/v1/printers/p1/diagnostics", opsKey, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %v", res.StatusCode, body)
	}
	cfg, _ := body["config"].(map[string]any)
	if cfg == nil || cfg["print_width_mm"] != 100.0 || cfg["label_length_mm"] != 150.0 || cfg["dpi"] != 203.0 {
		t.Fatalf("config not decoded: %v", body["config"])
	}
	if ws := body["warnings"].([]any); len(ws) != 0 {
		t.Fatalf("declared media matches the printer, want no warnings: %v", ws)
	}

	// No declared paper size: the report says what the printer is set to.
	_, body = do(t, ts, "GET", "/api/v1/printers/bare/diagnostics", opsKey, "")
	ws := body["warnings"].([]any)
	codes := map[any]bool{}
	for _, w := range ws {
		codes[w.(map[string]any)["code"]] = true
	}
	if len(ws) != 2 || !codes["media_undeclared"] || !codes["direction_undeclared"] {
		t.Fatalf("want media_undeclared and direction_undeclared, got %v", ws)
	}

	if res, _ := do(t, ts, "GET", "/api/v1/printers/p1/diagnostics", wmsKey, ""); res.StatusCode != http.StatusForbidden {
		t.Fatalf("diagnostics must be admin-only, got %d", res.StatusCode)
	}
}

func TestMaintenanceActions(t *testing.T) {
	ts := newTestServer(t, mockSpec("p1", &domain.MockCfg{}), mockSpec("busy", &domain.MockCfg{State: "printing"}))

	res, body := do(t, ts, "POST", "/api/v1/printers/p1/maintenance", opsKey, `{"action":"calibrate"}`)
	if res.StatusCode != http.StatusOK || body["sent"] != "~JC" {
		t.Fatalf("calibrate: %d %v", res.StatusCode, body)
	}
	res, body = do(t, ts, "POST", "/api/v1/printers/p1/maintenance", opsKey, `{"action":"label_top","dots":20}`)
	if res.StatusCode != http.StatusOK || body["sent"] != "^XA^LT20^JUS^XZ" {
		t.Fatalf("label_top: %d %v", res.StatusCode, body)
	}

	cases := []struct {
		printer, body, key string
		want               int
	}{
		{"p1", `{"action":"label_top","dots":500}`, opsKey, http.StatusUnprocessableEntity},
		{"p1", `{"action":"factory_reset"}`, opsKey, http.StatusUnprocessableEntity},
		{"p1", `{"action":"calibrate"}`, wmsKey, http.StatusForbidden},
		{"nope", `{"action":"calibrate"}`, opsKey, http.StatusNotFound},
	}
	for _, c := range cases {
		if res, body := do(t, ts, "POST", "/api/v1/printers/"+c.printer+"/maintenance", c.key, c.body); res.StatusCode != c.want {
			t.Errorf("%s %s: got %d %v, want %d", c.printer, c.body, res.StatusCode, body, c.want)
		}
	}

	// Calibration feeds labels, so it must not run while the printer is working.
	do(t, ts, "POST", "/api/v1/printers/probe", opsKey, "")
	if res, body := do(t, ts, "POST", "/api/v1/printers/busy/maintenance", opsKey, `{"action":"calibrate"}`); res.StatusCode != http.StatusConflict {
		t.Fatalf("busy printer: got %d %v, want 409", res.StatusCode, body)
	}
}

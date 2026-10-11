package httpapi

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kongkx/cloud-print-service/internal/domain"
)

// Helpers shared with server_test.go: newTestServer (panel + renderer enabled),
// newTestServerWith (custom web config), newTestServerNoRenderer, postWithKey, readAll.

func postWithKey(t *testing.T, ts *httpServer, key, path, body string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest("POST", ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	// Buffer the body so callers can still inspect it after we close the socket.
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	out := map[string]any{}
	_ = jsonUnmarshal(raw, &out)
	return resp, out
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The panel is served from the same binary and origin as the API (mihomo/clash
// pattern): no CORS, no second deployment, no node toolchain in the build.

func TestPanelRedirectsRootToUI(t *testing.T) {
	ts := newTestServer(t, mockSpec("p1", &domain.MockCfg{}))

	req, _ := http.NewRequest("GET", ts.URL+"/", nil)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("GET / status = %d, want 302", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/ui/" {
		t.Fatalf("Location = %q, want /ui/", loc)
	}
}

func TestPanelServesIndexAndAssets(t *testing.T) {
	ts := newTestServer(t, mockSpec("p1", &domain.MockCfg{}))

	resp, err := ts.Client().Get(ts.URL + "/ui/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /ui/ = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content type = %q", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("index must not be cached: %q", cc)
	}
	body := readAll(t, resp)
	for _, want := range []string{"<div id=\"app\">", "app.js", "app.css"} {
		if !strings.Contains(body, want) {
			t.Errorf("index.html missing %q", want)
		}
	}

	for path, wantCT := range map[string]string{
		"/ui/app.js":  "application/javascript",
		"/ui/app.css": "text/css",
	} {
		resp, err := ts.Client().Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body := readAll(t, resp)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d", path, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, wantCT) {
			t.Errorf("GET %s content-type = %q, want %s", path, ct, wantCT)
		}
		if len(body) == 0 {
			t.Errorf("GET %s returned an empty body", path)
		}
	}
}

// Deep links must resolve to the SPA, not 404: /ui/jobs is a client-side route.
func TestPanelServesSPAForUnknownRoutes(t *testing.T) {
	ts := newTestServer(t, mockSpec("p1", &domain.MockCfg{}))
	resp, err := ts.Client().Get(ts.URL + "/ui/jobs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SPA route status = %d, want 200", resp.StatusCode)
	}
	if body := readAll(t, resp); !strings.Contains(body, "<div id=\"app\">") {
		t.Error("SPA fallback must return index.html")
	}
}

// Serving the panel must not open a hole in the API's authentication.
func TestPanelDoesNotBypassAuth(t *testing.T) {
	ts := newTestServer(t, mockSpec("p1", &domain.MockCfg{}))
	if resp, _ := ts.Client().Get(ts.URL + "/api/v1/printers"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("API without a key = %d, want 401", resp.StatusCode)
	}
	if resp, _ := ts.Client().Get(ts.URL + "/api/v1/printers/status"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bulk status without a key = %d, want 401", resp.StatusCode)
	}
	// Ops endpoints are intentionally open (no business data).
	if resp, _ := ts.Client().Get(ts.URL + "/healthz"); resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz = %d, want 200", resp.StatusCode)
	}
}

func TestPanelCanBeDisabled(t *testing.T) {
	ts := newTestServerWith(t, WebConfig{Enabled: false}, mockSpec("p1", &domain.MockCfg{}))
	if resp, _ := ts.Client().Get(ts.URL + "/ui/"); resp.StatusCode == http.StatusOK {
		t.Fatal("panel must not be served when disabled")
	}
	if resp, _ := ts.Client().Get(ts.URL + "/healthz"); resp.StatusCode != http.StatusOK {
		t.Fatal("disabling the panel must not affect the API")
	}
}

func TestPanelCustomPathAndTitle(t *testing.T) {
	ts := newTestServerWith(t, WebConfig{Enabled: true, Path: "/dashboard", Title: "WH1 Print Ops"},
		mockSpec("p1", &domain.MockCfg{}))

	resp, err := ts.Client().Get(ts.URL + "/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("custom panel path = %d", resp.StatusCode)
	}
	if body := readAll(t, resp); !strings.Contains(body, "<title>WH1 Print Ops</title>") {
		t.Error("ui.title must be injected into index.html")
	}
	// The default path must not answer once the panel is mounted elsewhere.
	if resp, _ := ts.Client().Get(ts.URL + "/ui/"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("default path should be gone, got %d", resp.StatusCode)
	}
}

// ------------------------------------------------------------------ preview

func TestPreviewRendersZPL(t *testing.T) {
	ts := newTestServer(t, mockSpec("p1", &domain.MockCfg{}))

	resp, _ := postWithKey(t, ts, opsKey, "/api/v1/admin/preview",
		`{"zpl":"^XA^FO40,40^A0N,40,40^FDhello^FS^XZ","width_mm":101.6,"height_mm":152.4,"dpmm":8}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("preview = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("content type = %q, want image/png", ct)
	}
	if h := resp.Header.Get("X-Render-Backend"); h == "" {
		t.Error("render metadata headers must be present")
	}
	body := readAll(t, resp)
	if len(body) < 100 || !strings.HasPrefix(string(body[:4]), "\x89PNG") {
		t.Error("preview body is not a PNG")
	}
}

func TestPreviewIsAdminOnly(t *testing.T) {
	ts := newTestServer(t, mockSpec("p1", &domain.MockCfg{}))
	body := `{"zpl":"^XA^FO40,40^FDx^FS^XZ"}`

	// No credential.
	resp, _ := ts.Client().Post(ts.URL+"/api/v1/admin/preview", "application/json", strings.NewReader(body))
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no key = %d, want 401", resp.StatusCode)
	}
	// A client credential is not enough: preview can burn CPU rendering arbitrary ZPL.
	resp, _ = postWithKey(t, ts, erpKey, "/api/v1/admin/preview", body)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("client key = %d, want 403", resp.StatusCode)
	}
	resp, _ = postWithKey(t, ts, opsKey, "/api/v1/admin/preview", body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("admin key = %d, want 200", resp.StatusCode)
	}
}

func TestPreviewValidatesInput(t *testing.T) {
	ts := newTestServer(t, mockSpec("p1", &domain.MockCfg{}))
	cases := []struct{ name, body, code string }{
		{"empty zpl", `{"zpl":""}`, "invalid_request"},
		{"not zpl", `{"zpl":"hello world"}`, "render_failed"},
		{"bad json", `{`, "invalid_request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, out := postWithKey(t, ts, opsKey, "/api/v1/admin/preview", tc.body)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d (%v)", resp.StatusCode, out)
			}
			e, _ := out["error"].(map[string]any)
			if e["code"] != tc.code {
				t.Fatalf("code = %v, want %v", e["code"], tc.code)
			}
		})
	}
}

func TestPreviewReportsMissingRenderer(t *testing.T) {
	// A build without the render engine wired up must say so, not fail obscurely.
	plain := newTestServerNoRenderer(t)
	resp, out := postWithKey(t, plain, opsKey, "/api/v1/admin/preview", `{"zpl":"^XA^FDx^FS^XZ"}`)
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d (%v), want 501", resp.StatusCode, out)
	}
	e, _ := out["error"].(map[string]any)
	if e["code"] != "not_configured" {
		t.Fatalf("code = %v, want not_configured", e["code"])
	}
}

func TestPreviewSupportsOtherFormats(t *testing.T) {
	ts := newTestServer(t, mockSpec("p1", &domain.MockCfg{}))
	resp, _ := postWithKey(t, ts, opsKey, "/api/v1/admin/preview", `{"zpl":"^XA^FO40,40^FDx^FS^XZ","format":"svg"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("svg preview = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/svg+xml" {
		t.Fatalf("content type = %q", ct)
	}
	if resp.Header.Get("X-Render-Format") != "svg" {
		t.Error("format metadata header wrong")
	}
}

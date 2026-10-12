package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kongkx/cloud-print-service/internal/domain"
)

func TestOpenAPISpecsAndDocs(t *testing.T) {
	ts := newTestServer(t, mockSpec("p1", &domain.MockCfg{}))

	// 1. Raw OpenAPI JSON
	res, err := ts.Client().Get(ts.URL + "/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /openapi.json status = %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var doc map[string]any
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	info, ok := doc["info"].(map[string]any)
	if !ok || info["title"] != "Cloud Print Service API" {
		t.Fatalf("unexpected openapi doc: %+v", info)
	}

	// 2. Raw OpenAPI YAML
	resYAML, err := ts.Client().Get(ts.URL + "/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer resYAML.Body.Close()
	if resYAML.StatusCode != http.StatusOK {
		t.Fatalf("GET /openapi.yaml status = %d", resYAML.StatusCode)
	}
	if ct := resYAML.Header.Get("Content-Type"); !strings.Contains(ct, "yaml") {
		t.Fatalf("Content-Type = %q, want yaml", ct)
	}
	yamlBody := readAll(t, resYAML)
	if !strings.Contains(yamlBody, "openapi: 3.0.3") || !strings.Contains(yamlBody, "/printers:") {
		t.Fatalf("unexpected yaml: %s", yamlBody[:min(len(yamlBody), 200)])
	}

	// 3. Web Preview (/docs/)
	resDocs, err := ts.Client().Get(ts.URL + "/docs/")
	if err != nil {
		t.Fatal(err)
	}
	defer resDocs.Body.Close()
	if resDocs.StatusCode != http.StatusOK {
		t.Fatalf("GET /docs/ status = %d", resDocs.StatusCode)
	}
	docsBody := readAll(t, resDocs)
	if !strings.Contains(docsBody, "api-reference") || !strings.Contains(docsBody, "scalar") {
		t.Fatalf("docs page missing scalar bundle: %s", docsBody[:min(len(docsBody), 200)])
	}

	// 4. Redirect /docs -> /docs/
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resRedir, err := c.Get(ts.URL + "/docs")
	if err != nil {
		t.Fatal(err)
	}
	defer resRedir.Body.Close()
	if resRedir.StatusCode != http.StatusPermanentRedirect {
		t.Fatalf("GET /docs status = %d, want 308", resRedir.StatusCode)
	}
}

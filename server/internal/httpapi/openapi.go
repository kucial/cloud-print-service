package httpapi

import (
	_ "embed"
	"net/http"
	"strings"
)

//go:embed openapi.yaml
var openapiYAML []byte

//go:embed openapi.json
var openapiJSON []byte

const docsHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Cloud Print Service API Reference</title>
  <link rel="icon" href="data:image/svg+xml,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 32 32'><rect width='32' height='32' rx='7' fill='%230b1220'/><rect x='7' y='9' width='18' height='6' fill='%2338bdf8'/><rect x='7' y='18' width='12' height='5' fill='%2364748b'/></svg>">
  <style>
    body { margin: 0; background: #0f172a; color: #f8fafc; font-family: system-ui, sans-serif; }
    .offline-fallback { padding: 40px 20px; max-width: 800px; margin: 0 auto; line-height: 1.6; }
    .offline-fallback a { color: #38bdf8; text-decoration: none; }
    .offline-fallback a:hover { text-decoration: underline; }
    .badge { background: #1e293b; padding: 2px 8px; border-radius: 6px; font-family: monospace; font-size: 13px; }
  </style>
</head>
<body>
  <!-- Scalar Modern Interactive API Reference -->
  <script
    id="api-reference"
    data-url="/openapi.json"
    data-configuration='{"theme":"purple","layout":"modern","darkMode":true,"showSidebar":true}'></script>
  <script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference" onerror="this.onerror=null; document.getElementById('offline-notice').style.display='block';"></script>

  <div id="offline-notice" class="offline-fallback" style="display: none;">
    <h2>Cloud Print Service API Documentation</h2>
    <p>The interactive CDN documentation bundle could not be loaded (offline mode). You can access the raw specifications directly:</p>
    <ul>
      <li><a href="/openapi.json">OpenAPI Specification (JSON)</a> <span class="badge">/openapi.json</span></li>
      <li><a href="/openapi.yaml">OpenAPI Specification (YAML)</a> <span class="badge">/openapi.yaml</span></li>
    </ul>
    <p>To use offline with Swagger UI or Postman, import <a href="/openapi.yaml">/openapi.yaml</a>.</p>
  </div>
</body>
</html>
`

func (s *Server) mountOpenAPI(root *http.ServeMux) {
	// Raw OpenAPI JSON & YAML
	serveJSON := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(openapiJSON)
	}

	serveYAML := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(openapiYAML)
	}

	serveDocs := func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/") && r.URL.Path == "/docs" {
			http.Redirect(w, r, "/docs/", http.StatusPermanentRedirect)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(docsHTML))
	}

	// Mount endpoints on root
	root.HandleFunc("GET /openapi.json", serveJSON)
	root.HandleFunc("GET /openapi.yaml", serveYAML)
	root.HandleFunc("GET /openapi.yml", serveYAML)

	root.HandleFunc("GET /api/v1/openapi.json", serveJSON)
	root.HandleFunc("GET /api/v1/openapi.yaml", serveYAML)
	root.HandleFunc("GET /api/v1/openapi.yml", serveYAML)

	root.HandleFunc("GET /docs", serveDocs)
	root.HandleFunc("GET /docs/", serveDocs)
}

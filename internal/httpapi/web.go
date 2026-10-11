package httpapi

import (
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/kongkx/cloud-print-service/web"
)

// WebConfig controls the embedded operator panel.
//
// Pattern borrowed from mihomo/clash: the UI is served from the same binary and origin
// as the API, so there is no CORS, no second deployment, and no node toolchain in the
// build. Authentication is the API's own bearer token.
type WebConfig struct {
	Enabled bool   // serve the panel at all
	Path    string // mount point, default "/ui/"
	Title   string // injected into index.html
}

// DefaultWebPath is where the panel lives.
const DefaultWebPath = "/ui/"

type webHandler struct {
	fsys  fs.FS
	cfg   WebConfig
	index []byte
}

func newWebHandler(cfg WebConfig) (*webHandler, error) {
	if cfg.Path == "" {
		cfg.Path = DefaultWebPath
	}
	if !strings.HasPrefix(cfg.Path, "/") {
		cfg.Path = "/" + cfg.Path
	}
	if !strings.HasSuffix(cfg.Path, "/") {
		cfg.Path += "/"
	}
	f, err := web.FS()
	if err != nil {
		return nil, err
	}
	index, err := fs.ReadFile(f, "index.html")
	if err != nil {
		return nil, err
	}
	if cfg.Title != "" {
		index = []byte(strings.Replace(string(index), "<title>Cloud Print Service</title>",
			"<title>"+cfg.Title+"</title>", 1))
	}
	return &webHandler{fsys: f, cfg: cfg, index: index}, nil
}

func (w *webHandler) mount(root *http.ServeMux) {
	// `/` sends operators to the panel, the way mihomo serves its dashboard at the root.
	root.HandleFunc("GET /{$}", func(rw http.ResponseWriter, r *http.Request) {
		http.Redirect(rw, r, w.cfg.Path, http.StatusFound)
	})
	root.Handle("GET "+w.cfg.Path, http.StripPrefix(w.cfg.Path, http.HandlerFunc(w.serve)))
}

func (w *webHandler) serve(rw http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" || name == "." {
		w.serveIndex(rw, r)
		return
	}
	data, err := fs.ReadFile(w.fsys, name)
	if err != nil {
		// Unknown path: hand it back to the SPA so client-side routes deep-link.
		w.serveIndex(rw, r)
		return
	}
	rw.Header().Set("Content-Type", contentType(name))
	// Hashed asset names are not used here, so keep it simple and safe: revalidate.
	rw.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(rw, r, name, w.modTime(name), strings.NewReader(string(data)))
}

func (w *webHandler) serveIndex(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The panel must never be served stale after an upgrade.
	rw.Header().Set("Cache-Control", "no-store, must-revalidate")
	rw.WriteHeader(http.StatusOK)
	_, _ = rw.Write(w.index)
}

func (w *webHandler) modTime(name string) time.Time {
	// Deterministic zero time keeps responses cacheable-but-revalidating and makes
	// tests reproducible.
	return time.Time{}
}

func contentType(name string) string {
	switch path.Ext(name) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "application/javascript; charset=utf-8"
	case ".json":
		return "application/json"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	default:
		return "application/octet-stream"
	}
}

var errWebDisabled = errors.New("operator panel is disabled (set ui.enabled)")

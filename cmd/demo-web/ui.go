package main

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// The console UI is embedded like the server panel: one binary, nothing to deploy.
//
//go:embed all:web/dist
var uiAssets embed.FS

func (a *app) uiHandler() http.Handler {
	sub, err := fs.Sub(uiAssets, "web/dist")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "ui unavailable: "+err.Error(), http.StatusInternalServerError)
		})
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(sub, name); err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}

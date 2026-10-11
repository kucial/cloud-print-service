// Package web holds the operator panel: a dependency-free SPA embedded in the binary.
//
// Like mihomo/clash dashboards: the UI is served from the same origin as the API, so
// there is no CORS, no second deployment and no node toolchain in the build. The panel
// authenticates with the same bearer token the API uses (a bootstrap admin key or an
// issued admin token), held in localStorage.
//
//	go:embed dist keeps the built assets in the binary; there is nothing to install.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var assets embed.FS

// FS returns the panel asset tree rooted at dist/.
func FS() (fs.FS, error) { return fs.Sub(assets, "dist") }

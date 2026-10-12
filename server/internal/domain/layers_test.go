package domain

import (
	"go/build"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The architecture is only as good as the rule nobody checks. These tests enforce the
// dependency direction from docs/PLAN-agent-discovery.md §9.3, plus the component
// boundaries of the repo: server/, agent/, vprint/ and pkg/ are separately deployable.

const module = "github.com/kucial/cloud-print-service/"

// layers maps a package (module-relative path) to what it may import from this module.
var layers = map[string][]string{
	"server/internal/domain":    {"pkg/printer"},
	"server/internal/driver":    {"server/internal/domain"},
	"server/internal/store":     {"server/internal/domain"},
	"server/internal/observ":    {},
	"server/internal/config":    {"server/internal/domain"},
	"server/internal/token":     {"server/internal/domain"},
	"server/internal/fleet":     {"server/internal/domain", "server/internal/driver", "server/internal/store", "server/internal/observ", "pkg/agentapi"},
	"server/internal/job":       {"server/internal/domain", "server/internal/driver", "server/internal/store", "server/internal/fleet", "server/internal/observ"},
	"server/internal/discovery": {"server/internal/domain", "server/internal/fleet"},
	"server/internal/httpapi": {
		"server/internal/domain", "server/internal/driver", "server/internal/fleet", "server/internal/job",
		"server/internal/store", "server/internal/token", "server/internal/observ", "server/internal/config",
		"server/internal/discovery", "server/web", "pkg/render", "pkg/agentapi",
	},
	"server/web":     {},
	"pkg/render":     {},
	"pkg/printer":    {},
	"pkg/agentapi":   {"pkg/printer"},
	"vprint/virtual": {"pkg/render"},
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd() // server/internal/domain
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "..", "..", "..")
}

// imports returns the module-local, non-test imports of the package at rel.
func imports(t *testing.T, rel string) []string {
	t.Helper()
	p, err := build.ImportDir(filepath.Join(moduleRoot(t), rel), 0)
	if err != nil {
		t.Fatalf("import %s: %v", rel, err)
	}
	var out []string
	for _, imp := range p.Imports {
		if strings.HasPrefix(imp, module) {
			out = append(out, strings.TrimPrefix(imp, module))
		}
	}
	return out
}

// packagesUnder lists every Go package directory below rel (module-relative).
func packagesUnder(t *testing.T, rel string) []string {
	t.Helper()
	root := moduleRoot(t)
	var out []string
	_ = filepath.WalkDir(filepath.Join(root, rel), func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if name := d.Name(); name == "node_modules" || name == "dist" || strings.HasPrefix(name, ".") {
			return filepath.SkipDir
		}
		if _, err := build.ImportDir(path, 0); err == nil {
			r, _ := filepath.Rel(root, path)
			out = append(out, filepath.ToSlash(r))
		}
		return nil
	})
	return out
}

// domain may share leaf vocabulary from pkg/ (printer states) but nothing of the server.
func TestDomainHasNoInternalDependencies(t *testing.T) {
	for _, dep := range imports(t, "server/internal/domain") {
		if !strings.HasPrefix(dep, "pkg/") {
			t.Errorf("domain must import nothing internal, got %s", dep)
		}
	}
}

// Every layer may only reach downward, per the layer table.
func TestLayerDependencyDirection(t *testing.T) {
	for pkg, deps := range layers {
		allowed := map[string]bool{}
		for _, d := range deps {
			allowed[d] = true
		}
		for _, dep := range imports(t, pkg) {
			if !allowed[dep] {
				sort.Strings(deps)
				t.Errorf("%s must not import %s (allowed: %v)", pkg, dep, deps)
			}
		}
	}
}

// Every server/internal package must be in the layer table, so a new package cannot
// dodge the rules by simply not being listed.
func TestEveryServerPackageHasALayer(t *testing.T) {
	for _, pkg := range packagesUnder(t, "server/internal") {
		if _, ok := layers[pkg]; !ok {
			t.Errorf("%s is missing from the layer table", pkg)
		}
	}
}

// The fleet layer must not depend on the HTTP layer: discovery, agents and the panel are
// consumers of the fleet, not the other way round.
func TestFleetDoesNotDependOnHTTP(t *testing.T) {
	for _, dep := range imports(t, "server/internal/fleet") {
		switch dep {
		case "server/internal/httpapi", "server/internal/job", "server/web", "pkg/render":
			t.Errorf("fleet must not import %s", dep)
		}
	}
}

// Component boundaries: the server and the virtual printer are deployed independently.
// A virtual printer is a test double; nothing the server runs may import it, or the
// simulator would ship inside the service binary. The reverse keeps vprint a plain
// network printer that knows nothing about CPS.
func TestComponentsAreIndependent(t *testing.T) {
	for _, pkg := range append(packagesUnder(t, "server/internal"), packagesUnder(t, "server/cmd")...) {
		for _, dep := range imports(t, pkg) {
			if strings.HasPrefix(dep, "vprint/") {
				t.Errorf("%s must not import %s (the simulator is not part of the service)", pkg, dep)
			}
		}
	}
	for _, pkg := range packagesUnder(t, "vprint") {
		for _, dep := range imports(t, pkg) {
			if strings.HasPrefix(dep, "server/") {
				t.Errorf("%s must not import %s (vprint is standalone)", pkg, dep)
			}
		}
	}
	// The agent ships to packing PCs, the server to a container; they meet only on the
	// wire, through pkg/agentapi.
	for _, pkg := range packagesUnder(t, "agent") {
		for _, dep := range imports(t, pkg) {
			if !strings.HasPrefix(dep, "pkg/") {
				t.Errorf("%s must not import %s (the agent talks to the server only over HTTP)", pkg, dep)
			}
		}
	}
	for _, pkg := range append(packagesUnder(t, "server/internal"), packagesUnder(t, "server/cmd")...) {
		for _, dep := range imports(t, pkg) {
			if strings.HasPrefix(dep, "agent/") {
				t.Errorf("%s must not import %s (the agent is not part of the service)", pkg, dep)
			}
		}
	}
	for _, pkg := range packagesUnder(t, "pkg") {
		for _, dep := range imports(t, pkg) {
			if !strings.HasPrefix(dep, "pkg/") {
				t.Errorf("%s must not import %s (pkg/ is shared, leaf-only)", pkg, dep)
			}
		}
	}
}

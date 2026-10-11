package domain

import (
	"go/build"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The architecture is only as good as the rule nobody checks. These tests enforce the
// dependency direction from docs/PLAN-agent-discovery.md §9.3, so a well-meaning import
// cannot quietly turn the layers back inside out.

// internalPackages is the layer layout: which packages may import which.
var internalPackages = map[string][]string{
	"domain":  {}, // imports nothing internal
	"driver":  {"domain"},
	"store":   {"domain"},
	"fleet":   {"domain", "driver", "store", "observ"},
	"job":     {"domain", "driver", "store", "fleet", "observ"},
	"token":   {"domain"},
	"render":  {},
	"httpapi": {"domain", "driver", "fleet", "job", "store", "token", "render", "observ", "config", "web"},
	"virtual": {"domain", "render", "observ"},
	"config":  {"domain"},
	"observ":  {},
	"web":     {},
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd() // internal/domain
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(filepath.Dir(dir))
}

func internalImports(t *testing.T, pkg string) []string {
	t.Helper()
	root := moduleRoot(t)
	dir := filepath.Join(root, "internal", pkg)
	if pkg == "web" {
		dir = filepath.Join(root, "web")
	}
	pkgObj, err := build.ImportDir(dir, 0)
	if err != nil {
		t.Fatalf("import %s: %v", dir, err)
	}
	var out []string
	const prefix = "github.com/kongkx/cloud-print-service/"
	for _, imp := range pkgObj.Imports {
		if !strings.HasPrefix(imp, prefix) {
			continue
		}
		rest := strings.TrimPrefix(imp, prefix)
		if strings.HasPrefix(rest, "internal/") {
			out = append(out, strings.TrimSuffix(strings.TrimPrefix(rest, "internal/"), ""))
			continue
		}
		out = append(out, rest)
	}
	return out
}

func TestDomainHasNoInternalDependencies(t *testing.T) {
	if deps := internalImports(t, "domain"); len(deps) != 0 {
		t.Fatalf("domain must import nothing internal, got %v", deps)
	}
}

// Every layer may only reach downward, per the layer table.
func TestLayerDependencyDirection(t *testing.T) {
	allowed := map[string]map[string]bool{}
	for pkg, deps := range internalPackages {
		set := map[string]bool{}
		for _, d := range deps {
			set[d] = true
		}
		allowed[pkg] = set
	}

	for pkg, permitted := range internalPackages {
		_ = permitted
		for _, dep := range internalImports(t, pkg) {
			dep = strings.TrimSuffix(dep, "/")
			if pkg == "domain" {
				continue // covered by the test above
			}
			// A package may import itself indirectly (e.g. httpapi -> web) only if listed.
			if !allowed[pkg][dep] {
				t.Errorf("%s must not import %s (allowed: %v)", pkg, dep, keysOf(allowed[pkg]))
			}
		}
	}
}

// The fleet layer must not depend on the HTTP layer: discovery, agents and the panel are
// consumers of the fleet, not the other way round.
func TestFleetDoesNotDependOnHTTP(t *testing.T) {
	for _, dep := range internalImports(t, "fleet") {
		switch dep {
		case "httpapi", "job", "virtual", "web", "render":
			t.Errorf("fleet must not import %s", dep)
		}
	}
}

// A virtual printer is a test double; nothing the server runs may import it, or the
// simulator would ship inside the service binary.
func TestServerDoesNotDependOnVirtualPrinter(t *testing.T) {
	for _, pkg := range []string{"job", "fleet", "driver", "store", "httpapi", "domain"} {
		for _, dep := range internalImports(t, pkg) {
			if dep == "virtual" {
				t.Errorf("%s must not import virtual (the simulator is not part of the service)", pkg)
			}
		}
	}
}

// The agent toolkit belongs to cmd/cps-agent alone.
func TestAgentToolkitIsOnlyImportedByItsCommand(t *testing.T) {
	for pkg := range internalPackages {
		for _, dep := range internalImports(t, pkg) {
			if dep == "agent" {
				t.Errorf("%s must not import agent", pkg)
			}
		}
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

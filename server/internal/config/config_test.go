package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Until the Postgres store exists, asking for it must fail loudly rather than run in
// memory and lose tokens, agent credentials and jobs on the next restart.
func TestPostgresStoreIsRejectedUntilImplemented(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"store_driver":"postgres","postgres_dsn":"postgres://x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("want a not-implemented error, got %v", err)
	}
}

func TestStoreDefaultsToSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"api_keys":{"aaaaaaaaaaaaaaaaaaaaaaaa":{"client_id":"ops","role":"admin"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CPS_DATA_PATH", "")
	cfg, err := Load(path)
	if err != nil || cfg.StoreDriver != "sqlite" || cfg.DataPath != "cps.db" {
		t.Fatalf("want sqlite at cps.db by default, got %q %q (%v)", cfg.StoreDriver, cfg.DataPath, err)
	}
	t.Setenv("CPS_DATA_PATH", "/var/lib/cps/cps.db")
	if cfg, _ := Load(path); cfg.DataPath != "/var/lib/cps/cps.db" {
		t.Fatalf("CPS_DATA_PATH ignored: %q", cfg.DataPath)
	}
}

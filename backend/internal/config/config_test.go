package config

import (
	"path/filepath"
	"testing"
)

func TestLoadDefaultsToSQLiteWhenDatabaseURLMissing(t *testing.T) {
	t.Setenv("STORE_DRIVER", "")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("APPDATA", `C:\Users\demo\AppData\Roaming`)
	t.Setenv("AGT_DATA_DIR", "")
	t.Setenv("AGT_SQLITE_PATH", "")
	t.Setenv("SQLITE_PATH", "")

	cfg := Load()
	if cfg.StoreDriver != "sqlite" {
		t.Fatalf("expected sqlite store by default, got %q", cfg.StoreDriver)
	}
	want := filepath.Join(`C:\Users\demo\AppData\Roaming`, "AgentToolGate", "agenttoolgate.db")
	if cfg.SQLitePath != want {
		t.Fatalf("unexpected sqlite path: got %q want %q", cfg.SQLitePath, want)
	}
	if cfg.Host != "127.0.0.1" {
		t.Fatalf("expected localhost host, got %q", cfg.Host)
	}
}

func TestLoadKeepsPostgresWhenDatabaseURLExists(t *testing.T) {
	t.Setenv("STORE_DRIVER", "")
	t.Setenv("DATABASE_URL", "postgres://demo:demo@127.0.0.1:5432/demo?sslmode=disable")
	t.Setenv("AGT_SQLITE_PATH", "")
	t.Setenv("SQLITE_PATH", "")

	cfg := Load()
	if cfg.StoreDriver != "postgres" {
		t.Fatalf("expected postgres store, got %q", cfg.StoreDriver)
	}
}

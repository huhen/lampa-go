package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"lampa-go/internal/config"
)

func TestOpenSQLiteAndMigrate(t *testing.T) {
	cfg := config.Defaults().DB
	cfg.DSN = filepath.Join(t.TempDir(), "app.db")

	db, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if err := Migrate(context.Background(), db, cfg.Driver); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// Idempotent: second run must be a no-op.
	if err := Migrate(context.Background(), db, cfg.Driver); err != nil {
		t.Fatalf("Migrate (second run): %v", err)
	}

	var version int
	if err := db.QueryRow(`SELECT value FROM app_meta WHERE key = 'schema'`).Scan(&version); err != nil {
		t.Fatalf("select app_meta: %v", err)
	}
	if version != 1 {
		t.Errorf("schema version = %d, want 1", version)
	}

	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("pragma: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
}

func TestOpenSQLiteCreatesDirectory(t *testing.T) {
	cfg := config.Defaults().DB
	cfg.DSN = filepath.Join(t.TempDir(), "nested", "dir", "app.db")

	db, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if _, err := os.Stat(filepath.Dir(cfg.DSN)); err != nil {
		t.Fatalf("directory must be created: %v", err)
	}
}

func TestOpenUnknownDriver(t *testing.T) {
	cfg := config.Defaults().DB
	cfg.Driver = "mysql"
	if _, err := Open(cfg); err == nil {
		t.Fatal("expected error for unknown driver")
	}
}

func TestMigratePostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	cfg := config.Defaults().DB
	cfg.Driver = config.DriverPostgres
	cfg.DSN = dsn

	db, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if err := Migrate(context.Background(), db, cfg.Driver); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
}

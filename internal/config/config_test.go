package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultsAreValid(t *testing.T) {
	cfg := Defaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaults must be valid: %v", err)
	}
	if cfg.Server.Listen != ":8080" {
		t.Errorf("listen = %q, want :8080", cfg.Server.Listen)
	}
	if cfg.Cub.Timeout.Std() != 15*time.Second {
		t.Errorf("cub timeout = %v, want 15s", cfg.Cub.Timeout.Std())
	}
	if cfg.DB.Driver != DriverSQLite {
		t.Errorf("driver = %q, want %q", cfg.DB.Driver, DriverSQLite)
	}
}

func TestLoadOverridesAndDurationParsing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
server:
  listen: ":9000"
  static_dir: /srv/lampa
  base_domain: lampa.example.com
cub:
  upstream: https://cub.example
  timeout: 30s
db:
  driver: postgres
  dsn: postgres://localhost/lampa
  max_open_conns: 10
log:
  format: json
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Listen != ":9000" {
		t.Errorf("listen = %q, want :9000", cfg.Server.Listen)
	}
	if cfg.Cub.Timeout.Std() != 30*time.Second {
		t.Errorf("timeout = %v, want 30s", cfg.Cub.Timeout.Std())
	}
	if cfg.DB.Driver != DriverPostgres {
		t.Errorf("driver = %q, want %q", cfg.DB.Driver, DriverPostgres)
	}
	// Not overridden → default survives.
	if cfg.Cub.GeoDefault != "US" {
		t.Errorf("geo_default = %q, want US", cfg.Cub.GeoDefault)
	}
	if cfg.Log.Format != "json" {
		t.Errorf("log format = %q, want json", cfg.Log.Format)
	}
}

func TestLoadEmptyFile(t *testing.T) {
	cases := map[string]string{
		"empty":         "",
		"comments-only": "# just a comment\n",
	}
	for name, body := range cases {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("%s: Load: %v", name, err)
		}
		if cfg.Server.Listen != ":8080" {
			t.Errorf("%s: listen = %q, want :8080", name, cfg.Server.Listen)
		}
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadInvalidDuration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("cub:\n  timeout: soon\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for invalid duration")
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  listen: \":9000\"\n  unknown_key: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for unknown key")
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	cases := []func(*Config){
		func(c *Config) { c.Server.Listen = "" },
		func(c *Config) { c.Server.StaticDir = "" },
		func(c *Config) { c.DB.Driver = "mysql" },
		func(c *Config) { c.DB.DSN = "" },
		func(c *Config) { c.Cub.Upstream = "" },
		func(c *Config) { c.Cub.Timeout = Duration(0) },
		func(c *Config) { c.DB.MaxOpenConns = -5 },
		func(c *Config) { c.Log.Format = "csv" },
	}
	for i, breakFn := range cases {
		cfg := Defaults()
		breakFn(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Errorf("case %d: expected validation error", i)
		}
	}
}

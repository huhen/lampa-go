// Package config loads and validates the application configuration.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Database driver names.
const (
	DriverSQLite   = "sqlite"
	DriverPostgres = "postgres"
)

// Duration is a time.Duration that unmarshals from YAML strings like "15s".
type Duration time.Duration

// Std returns the underlying time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return fmt.Errorf("duration must be a string like \"15s\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

// Server holds HTTP server settings.
type Server struct {
	Listen     string `yaml:"listen"`
	StaticDir  string `yaml:"static_dir"`
	BaseDomain string `yaml:"base_domain"`
}

// Cub holds the cub proxy settings.
type Cub struct {
	Upstream         string   `yaml:"upstream"`
	Timeout          Duration `yaml:"timeout"`
	GeoHeader        string   `yaml:"geo_header"`
	GeoDefault       string   `yaml:"geo_default"`
	SubdomainMarkers []string `yaml:"subdomain_markers"`
}

// DB holds database settings.
type DB struct {
	Driver          string   `yaml:"driver"`
	DSN             string   `yaml:"dsn"`
	MaxOpenConns    int      `yaml:"max_open_conns"`
	MaxIdleConns    int      `yaml:"max_idle_conns"`
	ConnMaxLifetime Duration `yaml:"conn_max_lifetime"`
}

// OTel holds OpenTelemetry settings.
type OTel struct {
	Enable      bool   `yaml:"enable"`
	Endpoint    string `yaml:"endpoint"`
	ServiceName string `yaml:"service_name"`
	Insecure    bool   `yaml:"insecure"`
}

// Log holds logging settings.
type Log struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// Config is the root configuration.
type Config struct {
	Server Server `yaml:"server"`
	Cub    Cub    `yaml:"cub"`
	DB     DB     `yaml:"db"`
	OTel   OTel   `yaml:"otel"`
	Log    Log    `yaml:"log"`
}

// Defaults returns the built-in default configuration.
func Defaults() Config {
	return Config{
		Server: Server{Listen: ":8080", StaticDir: "./deploy/web"},
		Cub: Cub{
			Upstream:         "https://cub.best",
			Timeout:          Duration(15 * time.Second),
			GeoHeader:        "X-Geo-Country",
			GeoDefault:       "US",
			SubdomainMarkers: []string{"tmdb", "geo", "ws", "imagetmdb", "cdn", "ad"},
		},
		DB: DB{
			Driver:          DriverSQLite,
			DSN:             "./data/lampa-go.db",
			MaxOpenConns:    25,
			MaxIdleConns:    5,
			ConnMaxLifetime: Duration(30 * time.Minute),
		},
		OTel: OTel{Endpoint: "localhost:4317", ServiceName: "lampa-go", Insecure: true},
		Log:  Log{Level: "info", Format: "text"},
	}
}

// Load reads the YAML config from path (optional), applies defaults and validates.
func Load(path string) (Config, error) {
	cfg := Defaults()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read config: %w", err)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks the configuration for obvious errors.
func (c *Config) Validate() error {
	if c.Server.Listen == "" {
		return fmt.Errorf("server.listen is required")
	}
	if c.Server.StaticDir == "" {
		return fmt.Errorf("server.static_dir is required")
	}
	switch c.DB.Driver {
	case DriverSQLite, DriverPostgres:
	default:
		return fmt.Errorf("db.driver must be %q or %q", DriverSQLite, DriverPostgres)
	}
	if c.DB.DSN == "" {
		return fmt.Errorf("db.dsn is required")
	}
	if c.Cub.Upstream == "" {
		return fmt.Errorf("cub.upstream is required")
	}
	switch c.Log.Format {
	case "text", "json":
	default:
		return fmt.Errorf("log.format must be \"text\" or \"json\"")
	}
	return nil
}

# Lampa-Go First Iteration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Go-сервер: статика Lampa + скелет API cub (свои заглушки + прокси на настраиваемый upstream) + скрипт обновления фронта (fetch → diff-сводка → патчи → overlay → сборка → деплой).

**Architecture:** один бинарник на чистом stdlib (`net/http` Go 1.22+ routing, `httputil.ReverseProxy`), пакеты `internal/{config,obs,storage,web,api,cubproxy,server}`; фронтенд собирается из `yumata/lampa-source` скриптом, сервер раздаёт готовую папку. Схема API — сервис-первым: `/cub/{suffix}`, свои заглушки перехватывают конкретные пути внутри `/cub/`.

**Tech Stack:** Go 1.27.1 (stdlib-first), OpenTelemetry (OTLP gRPC → VictoriaMetrics, по умолчанию noop), SQLite (modernc, по умолчанию) / PostgreSQL (pgx v5 stdlib), goose-миграции, YAML-конфиг, bash+Makefile для фронта (npm + gulp).

**Spec:** `docs/superpowers/specs/2026-10-07-lampa-go-design.md`

**Conventions:** код и комментарии — только английский; git-коммиты — conventional commits (`feat:`, `test:`, `docs:`, `build:`, `chore:`). Тесты — stdlib `testing` + `net/http/httptest`, без сторонних assertion-библиотек.

---

### Task 1: Project bootstrap

**Files:**
- Create: `go.mod`, `.gitignore`, `Makefile`
- Modify: `docs/superpowers/specs/2026-10-07-lampa-go-design.md` (уточнение нотации маркеров)

- [ ] **Step 1: Verify toolchain**

Run: `go version`
Expected: `go1.27.1` или новее в ветке 1.27. Также проверить `node --version` (нужен ≥18 для Task 11) — если node отсутствует, продолжать (пометить в отчёте, Task 11 выполнится частично).

- [ ] **Step 2: Init module and layout**

```bash
cd /home/usr1/coding/lampa-go
go mod init lampa-go
go mod edit -go=1.27.1
mkdir -p cmd/lampa-go internal/config internal/obs internal/storage/migrations \
         internal/web internal/api internal/cubproxy internal/server \
         frontend/patches frontend/overlay scripts docs
```

- [ ] **Step 3: Write `.gitignore`**

```gitignore
/bin/
/data/
/deploy/
/frontend/sources/
*.db
*.db-wal
*.db-shm
```

- [ ] **Step 4: Write `Makefile` (skeleton, fe-цели добавляются в Task 11)**

```make
GO ?= go
BINARY := bin/lampa-go

.PHONY: all build run test vet fmt smoke clean

all: build

build:
	$(GO) build -o $(BINARY) ./cmd/lampa-go

run: build
	./$(BINARY) -config config.yaml

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

smoke: build
	./scripts/smoke.sh

clean:
	rm -rf bin
```

- [ ] **Step 5: Fix marker notation in the spec (точки убраны: маркер = первый сегмент пути без точки)**

В `docs/superpowers/specs/2026-10-07-lampa-go-design.md` заменить:
- строку таблицы §5.2: «`/cub/tmdb./...`, `/cub/geo./...` | прокси | маркер поддомена в первом сегменте → ...» на «`/cub/tmdb/...`, `/cub/geo/...` | прокси | маркер поддомена (первый сегмент пути) → `tmdb.<upstream-host>/...` (приём CubProxy `GetDomain`; работает на одном домене, без wildcard DNS)»;
- в §5.3: «первый сегмент пути из множества `{tmdb., geo., ws., imagetmdb., cdn., ad.}` → поддомен приписывается к хосту upstream (`/cub/tmdb./3/x` → `https://tmdb.cub.example/3/x`)» на «первый сегмент пути из множества `{tmdb, geo, ws, imagetmdb, cdn, ad}` → поддомен приписывается к хосту upstream (`/cub/tmdb/3/x` → `https://tmdb.cub.example/3/x`)»;
- в §7: `subdomain_markers: ["tmdb.", "geo.", "ws.", "imagetmdb.", "cdn.", "ad."]` → `subdomain_markers: ["tmdb", "geo", "ws", "imagetmdb", "cdn", "ad"]`.

- [ ] **Step 6: Verify build**

Run: `go build ./... && go vet ./...`
Expected: пустой вывод, код 0.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "chore: bootstrap go module, layout and Makefile"
```

---

### Task 2: Config package (internal/config)

**Files:**
- Create: `internal/config/config.go`, `internal/config/config_test.go`

- [ ] **Step 1: Add dependency**

Run: `go get gopkg.in/yaml.v3`

- [ ] **Step 2: Write the failing test**

`internal/config/config_test.go`:

```go
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

func TestValidateRejectsBadValues(t *testing.T) {
	cases := []func(*Config){
		func(c *Config) { c.Server.Listen = "" },
		func(c *Config) { c.Server.StaticDir = "" },
		func(c *Config) { c.DB.Driver = "mysql" },
		func(c *Config) { c.DB.DSN = "" },
		func(c *Config) { c.Cub.Upstream = "" },
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
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/config/`
Expected: FAIL (пакет не существует / типы не определены).

- [ ] **Step 4: Write the implementation**

`internal/config/config.go`:

```go
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
	Upstream         string        `yaml:"upstream"`
	Timeout          Duration      `yaml:"timeout"`
	GeoHeader        string        `yaml:"geo_header"`
	GeoDefault       string        `yaml:"geo_default"`
	SubdomainMarkers []string      `yaml:"subdomain_markers"`
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
			Upstream:         "https://cub.example",
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
```

- [ ] **Step 5: Run tests**

Run: `gofmt -w internal/config && go test ./internal/config/`
Expected: `ok  lampa-go/internal/config`

- [ ] **Step 6: Commit**

```bash
git add internal/config go.mod go.sum
git commit -m "feat: config package with yaml loading, defaults and validation"
```

---

### Task 3: Observability package (internal/obs)

**Files:**
- Create: `internal/obs/obs.go`, `internal/obs/obs_test.go`

- [ ] **Step 1: Add dependencies**

```bash
go get go.opentelemetry.io/otel \
       go.opentelemetry.io/otel/noop \
       go.opentelemetry.io/otel/trace \
       go.opentelemetry.io/otel/metric \
       go.opentelemetry.io/otel/sdk \
       go.opentelemetry.io/otel/sdk/metric \
       go.opentelemetry.io/otel/sdk/log \
       go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc \
       go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc \
       go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc \
       go.opentelemetry.io/otel/log \
       go.opentelemetry.io/contrib/bridges/otelslog
```

(Если `go get` ругается на несуществующую версию одного из пакетов — взять последнюю доступную; API стабилен.)

- [ ] **Step 2: Write the failing test**

`internal/obs/obs_test.go`:

```go
package obs

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"lampa-go/internal/config"
)

func TestSetupDisabledUsesStdoutAndNoop(t *testing.T) {
	cfg := config.Defaults()
	cfg.OTel.Enable = false

	core, err := Setup(context.Background(), cfg.OTel, cfg.Log)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if core.Logger == nil || core.Tracer == nil || core.Meter == nil {
		t.Fatal("core must expose logger, tracer and meter")
	}
	if err := core.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"info":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
		"bogus": slog.LevelInfo, // fallback
		"":      slog.LevelInfo,
	}
	for in, want := range cases {
		if got := parseLevel(in); got != want {
			t.Errorf("parseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestMultiHandlerFanOut(t *testing.T) {
	var a, b bytes.Buffer
	ha := slog.NewTextHandler(&a, &slog.HandlerOptions{Level: slog.LevelInfo})
	hb := slog.NewJSONHandler(&b, &slog.HandlerOptions{Level: slog.LevelDebug})

	m := multiHandler{ha, hb}
	if !m.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("enabled for info expected")
	}
	if err := m.Handle(context.Background(), slog.NewRecord(0, slog.LevelInfo, "hello", 0)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !strings.Contains(a.String(), "hello") || !strings.Contains(b.String(), "hello") {
		t.Fatalf("both handlers must receive the record, got %q and %q", a.String(), b.String())
	}
	withAttrs := m.WithAttrs([]slog.Attr{slog.String("k", "v")})
	if withAttrs == nil {
		t.Fatal("WithAttrs must return a handler")
	}
	if m.WithGroup("g") == nil {
		t.Fatal("WithGroup must return a handler")
	}
}

func TestDisabledLoggerWritesTextOrJson(t *testing.T) {
	cfg := config.Defaults()
	cfg.Log.Format = "json"
	core, err := Setup(context.Background(), cfg.OTel, cfg.Log)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	core.Logger.Info("startup", "version", "test")
	if err := core.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/obs/`
Expected: FAIL (типы не определены).

- [ ] **Step 4: Write the implementation**

`internal/obs/obs.go`:

```go
// Package obs wires OpenTelemetry providers for traces, metrics and logs.
// When telemetry is disabled it installs noop providers and a stdout logger.
package obs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	metrinop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/sdk/resource"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	tracenop "go.opentelemetry.io/otel/trace/noop"

	"lampa-go/internal/config"
)

const scopeName = "lampa-go"

// Core holds the runtime observability primitives.
type Core struct {
	Logger *slog.Logger
	Tracer trace.Tracer
	Meter  metric.Meter

	shutdown []func(context.Context) error
}

// Shutdown flushes and stops all providers (reverse registration order).
func (c *Core) Shutdown(ctx context.Context) error {
	var errs []error
	for i := len(c.shutdown) - 1; i >= 0; i-- {
		if err := c.shutdown[i](ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Setup initializes the observability stack.
func Setup(ctx context.Context, otelCfg config.OTel, logCfg config.Log) (*Core, error) {
	level := parseLevel(logCfg.Level)
	stdout := stdoutHandler(logCfg.Format, level)
	core := &Core{}

	if !otelCfg.Enable {
		core.Logger = slog.New(stdout)
		core.Tracer = tracenop.NewTracerProvider().Tracer(scopeName)
		meter, _ := metrinop.NewMeterProvider().Meter(scopeName)
		core.Meter = meter
		slog.SetDefault(core.Logger)
		return core, nil
	}

	if otelCfg.Endpoint == "" {
		return nil, fmt.Errorf("otel.endpoint is required when otel.enable is true")
	}

	res := newResource(otelCfg.ServiceName)

	traceExp, err := otlptracegrpc.New(ctx,
		append([]otlptracegrpc.Option{otlptracegrpc.WithEndpoint(otelCfg.Endpoint)},
			withInsecureTrace(otelCfg)...)...)
	if err != nil {
		return nil, fmt.Errorf("create trace exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)
	core.shutdown = append(core.shutdown, tp.Shutdown)
	core.Tracer = tp.Tracer(scopeName)
	otel.SetTracerProvider(tp)

	metricExp, err := otlpmetricgrpc.New(ctx,
		append([]otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(otelCfg.Endpoint)},
			withInsecureMetric(otelCfg)...)...)
	if err != nil {
		return nil, fmt.Errorf("create metric exporter: %w", err)
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
		sdkmetric.WithResource(res),
	)
	core.shutdown = append(core.shutdown, mp.Shutdown)
	meter, _ := mp.Meter(scopeName)
	core.Meter = meter
	otel.SetMeterProvider(mp)

	logExp, err := otlploggrpc.New(ctx,
		append([]otlploggrpc.Option{otlploggrpc.WithEndpoint(otelCfg.Endpoint)},
			withInsecureLog(otelCfg)...)...)
	if err != nil {
		return nil, fmt.Errorf("create log exporter: %w", err)
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
		sdklog.WithResource(res),
	)
	core.shutdown = append(core.shutdown, lp.Shutdown)
	core.Logger = slog.New(multiHandler{
		stdout,
		otelslog.NewHandler(lp),
	})
	slog.SetDefault(core.Logger)

	return core, nil
}

func newResource(serviceName string) *resource.Resource {
	return resource.NewWithAttributes("", attribute.String("service.name", serviceName))
}

func withInsecureTrace(cfg config.OTel) []otlptracegrpc.Option {
	if cfg.Insecure {
		return []otlptracegrpc.Option{otlptracegrpc.WithInsecure()}
	}
	return nil
}

func withInsecureMetric(cfg config.OTel) []otlpmetricgrpc.Option {
	if cfg.Insecure {
		return []otlpmetricgrpc.Option{otlpmetricgrpc.WithInsecure()}
	}
	return nil
}

func withInsecureLog(cfg config.OTel) []otlploggrpc.Option {
	if cfg.Insecure {
		return []otlploggrpc.Option{otlploggrpc.WithInsecure()}
	}
	return nil
}

func stdoutHandler(format string, level slog.Level) slog.Handler {
	opts := &slog.HandlerOptions{Level: level}
	if format == "json" {
		return slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.NewTextHandler(os.Stdout, opts)
}

func parseLevel(s string) slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(strings.TrimSpace(s))); err != nil {
		return slog.LevelInfo
	}
	return l
}

type multiHandler []slog.Handler

func (m multiHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range m {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (m multiHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range m {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r.Clone()); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (m multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(multiHandler, len(m))
	for i, h := range m {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (m multiHandler) WithGroup(name string) slog.Handler {
	out := make(multiHandler, len(m))
	for i, h := range m {
		out[i] = h.WithGroup(name)
	}
	return out
}
```

- [ ] **Step 5: Run tests**

Run: `gofmt -w internal/obs && go test ./internal/obs/`
Expected: `ok  lampa-go/internal/obs`

- [ ] **Step 6: Commit**

```bash
git add internal/obs go.mod go.sum
git commit -m "feat: OpenTelemetry setup with noop default and OTLP gRPC exporters"
```

---

### Task 4: Storage package (internal/storage)

**Files:**
- Create: `internal/storage/storage.go`, `internal/storage/storage_test.go`, `internal/storage/migrations/00001_init.sql`

- [ ] **Step 1: Add dependencies**

```bash
go get modernc.org/sqlite github.com/jackc/pgx/v5 github.com/pressly/goose/v3
```

- [ ] **Step 2: Write the migration**

`internal/storage/migrations/00001_init.sql`:

```sql
-- +goose Up
CREATE TABLE IF NOT EXISTS app_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

INSERT INTO app_meta (key, value) VALUES ('schema', '1');

-- +goose Down
DROP TABLE IF EXISTS app_meta;
```

- [ ] **Step 3: Write the failing test**

`internal/storage/storage_test.go`:

```go
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
```

- [ ] **Step 4: Run test to verify it fails**

Run: `go test ./internal/storage/`
Expected: FAIL (Open/Migrate не определены).

- [ ] **Step 5: Write the implementation**

`internal/storage/storage.go`:

```go
// Package storage opens the application database and runs migrations.
package storage

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx driver for database/sql
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // pure-Go sqlite driver

	"lampa-go/internal/config"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open connects to the database using the configured driver.
func Open(cfg config.DB) (*sql.DB, error) {
	switch cfg.Driver {
	case config.DriverSQLite, "":
		return openSQLite(cfg)
	case config.DriverPostgres:
		return openPostgres(cfg)
	default:
		return nil, fmt.Errorf("unknown db driver %q", cfg.Driver)
	}
}

func openSQLite(cfg config.DB) (*sql.DB, error) {
	if dir := filepath.Dir(cfg.DSN); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite has a single writer: one connection avoids SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON"} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("exec %s: %w", pragma, err)
		}
	}
	return db, nil
}

func openPostgres(cfg config.DB) (*sql.DB, error) {
	db, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	// database/sql is the connection pool; apply the configured limits.
	if cfg.MaxOpenConns > 0 {
		db.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		db.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if lt := cfg.ConnMaxLifetime.Std(); lt > 0 {
		db.SetConnMaxLifetime(lt)
	}
	return db, nil
}

// Migrate applies pending goose migrations embedded into the binary.
func Migrate(ctx context.Context, db *sql.DB, driver string) error {
	dialect := map[string]string{
		config.DriverSQLite:   "sqlite3",
		config.DriverPostgres: "postgres",
	}[driver]
	if dialect == "" {
		dialect = "sqlite3"
	}
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect(dialect); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
```

- [ ] **Step 6: Run tests**

Run: `gofmt -w internal/storage && go test ./internal/storage/`
Expected: `ok  lampa-go/internal/storage` (postgres-тест — SKIP).

- [ ] **Step 7: Commit**

```bash
git add internal/storage go.mod go.sum
git commit -m "feat: storage package with sqlite/postgres drivers and goose migrations"
```

---

### Task 5: Static web package (internal/web)

**Files:**
- Create: `internal/web/web.go`, `internal/web/web_test.go`

- [ ] **Step 1: Write the failing test**

`internal/web/web_test.go`:

```go
package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func newTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"index.html":              "<html>lampa-index</html>",
		"app.min.js":              "// app",
		"assembly.json":           "{}",
		"css/app.css":             "body{}",
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestHandler(t *testing.T) {
	handler := New(newTestDir(t))

	cases := []struct {
		name        string
		path        string
		wantCode    int
		wantBody    string
		wantCache   string
	}{
		{"root serves index", "/", http.StatusOK, "lampa-index", "no-cache"},
		{"index.html no-cache", "/index.html", http.StatusOK, "lampa-index", "no-cache"},
		{"assembly no-cache", "/assembly.json", http.StatusOK, "{}", "no-cache"},
		{"app.js long cache", "/app.min.js", http.StatusOK, "// app", "public, max-age=86400"},
		{"nested file", "/css/app.css", http.StatusOK, "body{}", "public, max-age=86400"},
		{"unknown is 404", "/missing.js", http.StatusNotFound, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
			if tc.wantBody != "" && rec.Body.String() != tc.wantBody {
				t.Errorf("body = %q, want %q", rec.Body.String(), tc.wantBody)
			}
			if tc.wantCache != "" && rec.Header().Get("Cache-Control") != tc.wantCache {
				t.Errorf("cache-control = %q, want %q", rec.Header().Get("Cache-Control"), tc.wantCache)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/web/`
Expected: FAIL (New не определён).

- [ ] **Step 3: Write the implementation**

`internal/web/web.go`:

```go
// Package web serves the built Lampa frontend from a static directory.
package web

import (
	"net/http"
	"os"
	"strings"
)

// New returns a handler serving files from dir.
// index.html and assembly.json get no-cache; everything else is cached for a day.
func New(dir string) http.Handler {
	files := http.FileServerFS(os.DirFS(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if name == "index.html" || name == "assembly.json" {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=86400")
		}
		files.ServeHTTP(w, r)
	})
}
```

- [ ] **Step 4: Run tests**

Run: `gofmt -w internal/web && go test ./internal/web/`
Expected: `ok  lampa-go/internal/web`

- [ ] **Step 5: Commit**

```bash
git add internal/web
git commit -m "feat: static file handler for the Lampa frontend"
```

---

### Task 6: API package (internal/api)

**Files:**
- Create: `internal/api/api.go`, `internal/api/stubs.go`, `internal/api/health.go`, `internal/api/api_test.go`

- [ ] **Step 1: Write the failing test**

`internal/api/api_test.go`:

```go
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"lampa-go/internal/config"
	"lampa-go/internal/storage"
)

func newTestMux(t *testing.T) (*http.ServeMux, func()) {
	t.Helper()
	cfg := config.Defaults().DB
	cfg.DSN = filepath.Join(t.TempDir(), "api.db")
	db, err := storage.Open(cfg)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := storage.Migrate(context.Background(), db, cfg.Driver); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	mux := http.NewServeMux()
	Register(mux, Deps{
		DB:         db,
		GeoHeader:  "X-Geo-Country",
		GeoDefault: "US",
	})
	return mux, func() { db.Close() }
}

func do(t *testing.T, mux *http.ServeMux, method, path, body, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestRegister(t *testing.T) {
	mux, closeDB := newTestMux(t)
	defer closeDB()

	t.Run("healthz", func(t *testing.T) {
		rec := do(t, mux, http.MethodGet, "/healthz", "", "")
		if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
			t.Fatalf("healthz: %d %q", rec.Code, rec.Body.String())
		}
	})

	t.Run("readyz with db", func(t *testing.T) {
		rec := do(t, mux, http.MethodGet, "/readyz", "", "")
		if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
			t.Fatalf("readyz: %d %q", rec.Code, rec.Body.String())
		}
	})

	t.Run("checker get", func(t *testing.T) {
		rec := do(t, mux, http.MethodGet, "/cub/api/checker", "", "")
		if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
			t.Fatalf("checker: %d %q", rec.Code, rec.Body.String())
		}
	})

	t.Run("checker post echoes first form value", func(t *testing.T) {
		rec := do(t, mux, http.MethodPost, "/cub/api/checker",
			"protocol=https&other=1", "application/x-www-form-urlencoded")
		if rec.Code != http.StatusOK || rec.Body.String() != "https" {
			t.Fatalf("checker post: %d %q", rec.Code, rec.Body.String())
		}
	})

	t.Run("checker post rejects non-form content type", func(t *testing.T) {
		rec := do(t, mux, http.MethodPost, "/cub/api/checker", "{}", "application/json")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("checker post json: %d", rec.Code)
		}
	})

	t.Run("blacklist is empty json array", func(t *testing.T) {
		rec := do(t, mux, http.MethodGet, "/cub/api/plugins/blacklist", "", "")
		if rec.Code != http.StatusOK || rec.Body.String() != "[]" {
			t.Fatalf("blacklist: %d %q", rec.Code, rec.Body.String())
		}
	})

	t.Run("metric stub", func(t *testing.T) {
		rec := do(t, mux, http.MethodGet, "/cub/api/metric/unic", "", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("metric: %d", rec.Code)
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("metric json: %v", err)
		}
		if got["secuses"] != true {
			t.Errorf("metric secuses = %v, want true", got["secuses"])
		}
	})

	t.Run("ads vast stub", func(t *testing.T) {
		rec := do(t, mux, http.MethodGet, "/cub/api/ad/vast", "", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("ads: %d", rec.Code)
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("ads json: %v", err)
		}
		if got["secuses"] != true {
			t.Errorf("secuses = %v, want true", got["secuses"])
		}
		if _, ok := got["ad"].([]any); !ok {
			t.Errorf("ad = %v, want empty array", got["ad"])
		}
	})

	t.Run("ads other stub", func(t *testing.T) {
		rec := do(t, mux, http.MethodGet, "/cub/api/ad/stat", "", "")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "secuses") {
			t.Fatalf("ads stat: %d %q", rec.Code, rec.Body.String())
		}
	})

	t.Run("geo from header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/cub/geo", nil)
		req.Header.Set("X-Geo-Country", "DE")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Body.String() != "DE" {
			t.Fatalf("geo: %d %q", rec.Code, rec.Body.String())
		}
	})

	t.Run("geo fallback", func(t *testing.T) {
		rec := do(t, mux, http.MethodGet, "/cub/geo", "", "")
		if rec.Code != http.StatusOK || rec.Body.String() != "US" {
			t.Fatalf("geo fallback: %d %q", rec.Code, rec.Body.String())
		}
	})
}

func TestReadyzWithoutDB(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux, Deps{DB: nil})
	rec := do(t, mux, http.MethodGet, "/readyz", "", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz without db: %d, want 503", rec.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api/`
Expected: FAIL (Register/Deps не определены).

- [ ] **Step 3: Write the implementation**

`internal/api/api.go`:

```go
// Package api implements the application's own HTTP endpoints:
// health probes and the local answers for the cub API.
package api

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
)

// Deps carries the dependencies the API endpoints need.
type Deps struct {
	DB         *sql.DB
	GeoHeader  string
	GeoDefault string
	Logger     *slog.Logger
}

// Register mounts the API endpoints on mux.
func Register(mux *http.ServeMux, d Deps) {
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /readyz", ready(d.DB))
	mux.HandleFunc("GET /cub/api/checker", checker)
	mux.HandleFunc("POST /cub/api/checker", checker)
	mux.HandleFunc("GET /cub/api/plugins/blacklist", blacklist)
	mux.HandleFunc("GET /cub/api/metric/{rest...}", metric)
	mux.HandleFunc("GET /cub/api/ad/{rest...}", ads)
	mux.HandleFunc("GET /cub/geo", geo(d.GeoHeader, d.GeoDefault))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Nothing sensible to do at this point: headers are already sent.
		return
	}
}
```

`internal/api/health.go`:

```go
package api

import (
	"context"
	"database/sql"
	"net/http"
	"time"
)

func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

func ready(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if db == nil {
			http.Error(w, "database is not configured", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.PingContext(ctx); err != nil {
			http.Error(w, "database is not ready", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	}
}
```

`internal/api/stubs.go`:

```go
package api

import (
	"io"
	"net/http"
	"strings"
	"time"
)

const maxCheckerBody = 1 << 20

// checker answers the Lampa mirror liveness probe locally:
// GET returns "ok", POST echoes back the first form value.
func checker(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.Method != http.MethodPost {
		_, _ = w.Write([]byte("ok"))
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
		http.Error(w, "error", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCheckerBody))
	if err != nil {
		http.Error(w, "error", http.StatusBadRequest)
		return
	}
	raw := string(body)
	i := strings.IndexByte(raw, '=')
	if i < 0 {
		http.Error(w, "error", http.StatusBadRequest)
		return
	}
	value := raw[i+1:]
	if j := strings.IndexByte(value, '&'); j >= 0 {
		value = value[:j]
	}
	_, _ = w.Write([]byte(value))
}

// blacklist returns an empty plugin blacklist.
func blacklist(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, []any{})
}

// metric accepts client metrics and returns an empty success.
func metric(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"secuses": true})
}

// ads serves empty advertisement payloads so the client shows nothing.
func ads(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("rest") == "vast" {
		now := time.Now()
		writeJSON(w, map[string]any{
			"secuses":       true,
			"ad":            []any{},
			"day_of_month":  now.Day(),
			"days_in_month": 31,
			"month":         int(now.Month()),
		})
		return
	}
	writeJSON(w, map[string]any{"secuses": true})
}

// geo returns the client country code taken from the reverse-proxy header.
func geo(header, fallback string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		country := strings.TrimSpace(r.Header.Get(header))
		if country == "" {
			country = fallback
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(country))
	}
}
```

- [ ] **Step 4: Run tests**

Run: `gofmt -w internal/api && go test ./internal/api/`
Expected: `ok  lampa-go/internal/api`

- [ ] **Step 5: Commit**

```bash
git add internal/api
git commit -m "feat: own API endpoints: health probes and cub stubs"
```

---

### Task 7: Cub proxy package (internal/cubproxy)

**Files:**
- Create: `internal/cubproxy/proxy.go`, `internal/cubproxy/proxy_test.go`

- [ ] **Step 1: Write the failing test**

`internal/cubproxy/proxy_test.go`:

```go
package cubproxy

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// newUpstream starts a fake upstream that records the last request
// and answers "upstream:<path>?<query>".
func newUpstream(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *url.URL) {
	t.Helper()
	if handler == nil {
		handler = func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "upstream:"+r.URL.Path+"?"+r.URL.RawQuery)
		}
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	return srv, u
}

func newProxy(t *testing.T, upstream *url.URL, timeout time.Duration) *Proxy {
	t.Helper()
	return New(Config{
		Upstream:         upstream,
		Timeout:          timeout,
		SubdomainMarkers: []string{"tmdb", "geo", "ws"},
	}, slog.New(slog.DiscardHandler), nil)
}

func TestPathPreservingProxy(t *testing.T) {
	var gotPath, gotQuery, gotXFF string
	_, upstream := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotXFF = r.URL.Path, r.URL.RawQuery, r.Header.Get("X-Forwarded-For")
		_, _ = io.WriteString(w, "upstream:"+r.URL.Path+"?"+r.URL.RawQuery)
	})
	p := newProxy(t, upstream, time.Second)

	req := httptest.NewRequest(http.MethodGet, "http://local/cub/api/users/get?email=abc", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if gotPath != "/api/users/get" || gotQuery != "email=abc" {
		t.Errorf("upstream saw %q?%q, want /api/users/get?email=abc", gotPath, gotQuery)
	}
	if gotXFF == "" {
		t.Error("X-Forwarded-For must be set")
	}
	if rec.Body.String() != "upstream:/api/users/get?email=abc" {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestSubdomainMarker(t *testing.T) {
	var gotHost, gotPath string
	_, upstream := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		gotHost, gotPath = r.Host, r.URL.Path
		_, _ = io.WriteString(w, "ok")
	})
	p := newProxy(t, upstream, time.Second)

	req := httptest.NewRequest(http.MethodGet, "http://local/cub/tmdb/3/movie/1", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if !strings.HasPrefix(gotHost, "tmdb.") {
		t.Errorf("upstream host = %q, want prefix tmdb.", gotHost)
	}
	if gotPath != "/3/movie/1" {
		t.Errorf("upstream path = %q, want /3/movie/1", gotPath)
	}
}

func TestResponseHeaderFiltering(t *testing.T) {
	_, upstream := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "secret")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		w.Header().Set("X-Trace-Id", "42")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ok")
	})
	p := newProxy(t, upstream, time.Second)

	req := httptest.NewRequest(http.MethodGet, "http://local/cub/api/x", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	for _, name := range []string{"Server", "Content-Security-Policy", "X-Trace-Id", "Access-Control-Allow-Origin"} {
		if rec.Header().Get(name) != "" {
			t.Errorf("header %s must be filtered, got %q", name, rec.Header().Get(name))
		}
	}
	if rec.Header().Get("Content-Type") == "" {
		t.Error("Content-Type must be preserved")
	}
}

func TestPostBodyForwarded(t *testing.T) {
	var gotBody string
	_, upstream := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = io.WriteString(w, "ok")
	})
	p := newProxy(t, upstream, time.Second)

	req := httptest.NewRequest(http.MethodPost, "http://local/cub/api/bookmarks/add",
		strings.NewReader(`{"type":"book"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if gotBody != `{"type":"book"}` {
		t.Errorf("upstream body = %q", gotBody)
	}
}

func TestTimeoutReturns502(t *testing.T) {
	_, upstream := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = io.WriteString(w, "ok")
	})
	p := newProxy(t, upstream, 50*time.Millisecond)

	req := httptest.NewRequest(http.MethodGet, "http://local/cub/api/slow", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cubproxy/`
Expected: FAIL (Config/New не определены).

- [ ] **Step 3: Write the implementation**

`internal/cubproxy/proxy.go`:

```go
// Package cubproxy forwards /cub/* requests to the configured upstream service.
package cubproxy

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// Config configures the proxy.
type Config struct {
	Upstream         *url.URL
	Timeout          time.Duration
	SubdomainMarkers []string
}

// Proxy forwards /cub/<suffix> to <upstream>/<suffix>.
type Proxy struct {
	upstream *url.URL
	markers  map[string]struct{}
	timeout  time.Duration
	rp       *httputil.ReverseProxy
}

// New builds the proxy. transport may be nil (http.DefaultTransport is used).
func New(cfg Config, logger *slog.Logger, transport http.RoundTripper) *Proxy {
	p := &Proxy{
		upstream: cfg.Upstream,
		markers:  make(map[string]struct{}, len(cfg.SubdomainMarkers)),
		timeout:  cfg.Timeout,
	}
	for _, m := range cfg.SubdomainMarkers {
		p.markers[strings.ToLower(m)] = struct{}{}
	}
	if transport == nil {
		transport = http.DefaultTransport
	}
	if logger == nil {
		logger = slog.Default()
	}
	p.rp = &httputil.ReverseProxy{
		Rewrite:   p.rewrite,
		Transport: headerFilter{next: transport},
		// Stream the body as it arrives (no buffering).
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.ErrorContext(r.Context(), "cub proxy request failed",
				slog.String("path", r.URL.Path), slog.Any("error", err))
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
		},
	}
	return p
}

// ServeHTTP forwards the request with the configured timeout.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if p.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.timeout)
		defer cancel()
	}
	p.rp.ServeHTTP(w, r.WithContext(ctx))
}

// rewrite maps /cub/<suffix> to <upstream>/<suffix>. A leading marker
// segment (for example "tmdb") selects the upstream subdomain:
// /cub/tmdb/3/movie/1 -> https://tmdb.<upstream-host>/3/movie/1.
func (p *Proxy) rewrite(pr *httputil.ProxyRequest) {
	suffix := strings.TrimPrefix(pr.In.URL.Path, "/cub/")
	host := p.upstream.Host
	if i := strings.IndexByte(suffix, '/'); i > 0 {
		if _, ok := p.markers[strings.ToLower(suffix[:i])]; ok {
			host = strings.ToLower(suffix[:i]) + "." + host
			suffix = suffix[i+1:]
		}
	}
	pr.SetXForwarded()
	pr.Out.URL.Scheme = p.upstream.Scheme
	pr.Out.URL.Host = host
	pr.Out.URL.Path = "/" + suffix
	pr.Out.URL.RawPath = ""
	pr.Out.URL.RawQuery = pr.In.URL.RawQuery
	pr.Out.Host = host
}

// filteredHeaderNames are dropped from upstream responses entirely.
var filteredHeaderNames = map[string]struct{}{
	"server":                  {},
	"content-security-policy": {},
	"content-disposition":     {},
}

// filteredHeaderPrefixes are dropped from upstream responses by prefix.
var filteredHeaderPrefixes = []string{"x-", "alt-", "access-control-"}

type headerFilter struct{ next http.RoundTripper }

func (f headerFilter) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := f.next.RoundTrip(req)
	if resp == nil {
		return resp, err
	}
	for name := range resp.Header {
		if filteredHeader(name) {
			resp.Header.Del(name)
		}
	}
	return resp, err
}

func filteredHeader(name string) bool {
	lower := strings.ToLower(name)
	if _, ok := filteredHeaderNames[lower]; ok {
		return true
	}
	for _, prefix := range filteredHeaderPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run tests**

Run: `gofmt -w internal/cubproxy && go test ./internal/cubproxy/`
Expected: `ok  lampa-go/internal/cubproxy`

- [ ] **Step 5: Commit**

```bash
git add internal/cubproxy
git commit -m "feat: cub reverse proxy with subdomain markers and header filtering"
```

---

### Task 8: Server assembly (internal/server)

**Files:**
- Create: `internal/server/server.go`, `internal/server/middleware.go`, `internal/server/server_test.go`

- [ ] **Step 1: Write the failing test**

`internal/server/server_test.go`:

```go
package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lampa-go/internal/config"
	"lampa-go/internal/storage"
)

func testConfig(t *testing.T, upstream string) config.Config {
	t.Helper()
	cfg := config.Defaults()
	cfg.Server.Listen = "127.0.0.1:0"
	cfg.Server.StaticDir = t.TempDir()
	cfg.Cub.Upstream = upstream
	cfg.Cub.Timeout = config.Duration(time.Second)
	cfg.DB.DSN = filepath.Join(t.TempDir(), "test.db")
	if err := os.WriteFile(filepath.Join(cfg.Server.StaticDir, "index.html"), []byte("<html>lampa</html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func newTestServer(t *testing.T, cfg config.Config) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)

	db, err := storage.Open(cfg.DB)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(context.Background(), db, cfg.DB.Driver); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	srv, err := New(Deps{
		Config: cfg,
		Logger: logger,
		DB:     db,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ts := httptest.NewServer(srv.Handler)
	t.Cleanup(ts.Close)
	return ts
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, string(body)
}

func TestRouting(t *testing.T) {
	_, upstreamURL := newUpstream(t, nil)
	cfg := testConfig(t, upstreamURL.String())
	ts := newTestServer(t, cfg)

	t.Run("healthz", func(t *testing.T) {
		resp, body := get(t, ts.URL+"/healthz")
		if resp.StatusCode != http.StatusOK || body != "ok" {
			t.Errorf("healthz: %d %q", resp.StatusCode, body)
		}
	})

	t.Run("readyz", func(t *testing.T) {
		resp, body := get(t, ts.URL+"/readyz")
		if resp.StatusCode != http.StatusOK || body != "ok" {
			t.Errorf("readyz: %d %q", resp.StatusCode, body)
		}
	})

	t.Run("stub wins over proxy", func(t *testing.T) {
		resp, body := get(t, ts.URL+"/cub/api/checker")
		if resp.StatusCode != http.StatusOK || body != "ok" {
			t.Errorf("checker: %d %q", resp.StatusCode, body)
		}
	})

	t.Run("cub proxy forwards", func(t *testing.T) {
		resp, body := get(t, ts.URL+"/cub/api/users/get")
		if resp.StatusCode != http.StatusOK || body != "upstream:/api/users/get?" {
			t.Errorf("proxy: %d %q", resp.StatusCode, body)
		}
	})

	t.Run("static index", func(t *testing.T) {
		resp, body := get(t, ts.URL+"/")
		if resp.StatusCode != http.StatusOK || body != "<html>lampa</html>" {
			t.Errorf("static: %d %q", resp.StatusCode, body)
		}
	})

	t.Run("unknown path is 404", func(t *testing.T) {
		resp, _ := get(t, ts.URL+"/definitely-missing.js")
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("unknown: %d, want 404", resp.StatusCode)
		}
	})

	t.Run("request id header", func(t *testing.T) {
		resp, _ := get(t, ts.URL+"/healthz")
		if resp.Header.Get("X-Request-ID") == "" {
			t.Error("X-Request-ID must be set")
		}
	})
}

func TestRecoverMiddleware(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	handler := recoverMiddleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestNewRejectsBadUpstream(t *testing.T) {
	cfg := config.Defaults()
	cfg.Cub.Upstream = "://broken"
	if _, err := New(Deps{Config: cfg, Logger: slog.New(slog.DiscardHandler)}); err == nil {
		t.Fatal("expected error for broken upstream url")
	}
}
```

Тест использует `newUpstream` из `internal/cubproxy` — но это другой пакет. Определить маленький helper локально в `server_test.go`: добавить в начало файла после импортов:

```go
// newUpstream starts a fake upstream answering "upstream:<path>?<query>".
func newUpstream(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *url.URL) {
	t.Helper()
	if handler == nil {
		handler = func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "upstream:"+r.URL.Path+"?"+r.URL.RawQuery)
		}
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	return srv, u
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/`
Expected: FAIL (New/Deps не определены).

- [ ] **Step 3: Write the implementation**

`internal/server/middleware.go`:

```go
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

type middleware func(http.Handler) http.Handler

// chain applies middlewares so that the first one is the outermost.
func chain(mws ...middleware) middleware {
	return func(next http.Handler) http.Handler {
		for i := len(mws) - 1; i >= 0; i-- {
			next = mws[i](next)
		}
		return next
	}
}

type requestIDKey struct{}

// statusWriter captures the response status code.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status = http.StatusOK
		w.wrote = true
	}
	return w.ResponseWriter.Write(b)
}

// requestID assigns an X-Request-ID to every request and response.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			var buf [8]byte
			if _, err := rand.Read(buf[:]); err == nil {
				id = hex.EncodeToString(buf[:])
			}
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// recoverMiddleware converts handler panics into 500 responses.
func recoverMiddleware(logger *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("panic in handler",
						slog.Any("panic", rec),
						slog.String("path", r.URL.Path))
					http.Error(w, "internal server error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// observe wires tracing, metrics and request logging into one middleware.
func observe(tracer trace.Tracer, meter metric.Meter, logger *slog.Logger) middleware {
	counter, _ := meter.Int64Counter("http.server.requests")
	duration, _ := meter.Float64Histogram("http.server.duration", metric.WithUnit("ms"))

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ctx, span := tracer.Start(r.Context(), r.Method+" "+r.URL.Path,
				trace.WithSpanKind(trace.SpanKindServer))
			defer span.End()

			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r.WithContext(ctx))

			route := r.Pattern
			if route != "" {
				span.SetName(r.Method + " " + route)
			}
			span.SetAttributes(attribute.Int("http.response.status_code", sw.status))

			attrs := []slog.Attr{
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", sw.status),
				slog.Duration("duration", time.Since(start)),
			}
			if route != "" {
				attrs = append(attrs, slog.String("route", route))
			}
			if id, _ := r.Context().Value(requestIDKey{}).(string); id != "" {
				attrs = append(attrs, slog.String("request_id", id))
			}
			if spanCtx := trace.SpanContextFromContext(ctx); spanCtx.HasTraceID() {
				attrs = append(attrs, slog.String("trace_id", spanCtx.TraceID().String()))
			}
			logger.LogAttrs(ctx, slog.LevelInfo, "http request", attrs...)

			stdAttrs := []attribute.KeyValue{
				attribute.String("http.request.method", r.Method),
				attribute.String("http.route", route),
			}
			if counter != nil {
				counter.Add(ctx, 1, metric.WithAttributes(append(stdAttrs,
					attribute.Int("http.response.status_code", sw.status))...))
			}
			if duration != nil {
				duration.Record(ctx, float64(time.Since(start).Milliseconds()),
					metric.WithAttributes(stdAttrs...))
			}
		})
	}
}
```

`internal/server/server.go`:

```go
// Package server assembles the HTTP server: middleware chain and routes.
package server

import (
	"database/sql"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"lampa-go/internal/api"
	"lampa-go/internal/cubproxy"
	"lampa-go/internal/config"
	"lampa-go/internal/web"
)

// Deps carries everything the server needs to run.
type Deps struct {
	Config config.Config
	Logger *slog.Logger
	Tracer trace.Tracer
	Meter  metric.Meter
	DB     *sql.DB
}

// New builds the fully wired (but not started) HTTP server.
func New(d Deps) (*http.Server, error) {
	if d.Tracer == nil || d.Meter == nil {
		// Tests may pass only the logger; fall back to noop instruments.
		tracer, meter := noopInstruments()
		d.Tracer, d.Meter = tracer, meter
	}

	upstream, err := url.Parse(d.Config.Cub.Upstream)
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()

	api.Register(mux, api.Deps{
		DB:         d.DB,
		GeoHeader:  d.Config.Cub.GeoHeader,
		GeoDefault: d.Config.Cub.GeoDefault,
		Logger:     d.Logger,
	})

	cub := cubproxy.New(cubproxy.Config{
		Upstream:         upstream,
		Timeout:          d.Config.Cub.Timeout.Std(),
		SubdomainMarkers: d.Config.Cub.SubdomainMarkers,
	}, d.Logger, nil)
	mux.Handle("/cub/{rest...}", cub)

	mux.Handle("/", web.New(d.Config.Server.StaticDir))

	handler := chain(
		requestID,
		observe(d.Tracer, d.Meter, d.Logger),
		recoverMiddleware(d.Logger),
	)(mux)

	return &http.Server{
		Addr:              d.Config.Server.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}, nil
}
```

`noopInstruments` — маленький helper (в `middleware.go` или отдельном файле `noop.go`), использующий `go.opentelemetry.io/otel/noop`:

```go
package server

import (
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// noopInstruments returns providers that discard everything;
// used when the caller does not supply observability primitives.
func noopInstruments() (trace.Tracer, metric.Meter) {
	tracer := tracenop.NewTracerProvider().Tracer("lampa-go")
	meter, _ := metrinop.NewMeterProvider().Meter("lampa-go")
	return tracer, meter
}
```

Внимание к коллизии имён пакетов `noop` (trace и metric) — использовать псевдонимы `tracenop` и `metrinop` в импортах:

```go
import (
	"go.opentelemetry.io/otel/metric"
	metrinop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	tracenop "go.opentelemetry.io/otel/trace/noop"
)
```

- [ ] **Step 4: Run tests**

Run: `gofmt -w internal/server && go test ./internal/server/`
Expected: `ok  lampa-go/internal/server`

- [ ] **Step 5: Commit**

```bash
git add internal/server
git commit -m "feat: server assembly with middleware chain and route table"
```

---

### Task 9: Main binary and smoke test (cmd/lampa-go)

**Files:**
- Create: `cmd/lampa-go/main.go`, `scripts/smoke.sh`
- Modify: `Makefile` (smoke уже есть), nothing else

- [ ] **Step 1: Write `cmd/lampa-go/main.go`**

```go
// Command lampa-go serves the Lampa web UI and the cub API skeleton.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"lampa-go/internal/config"
	"lampa-go/internal/obs"
	"lampa-go/internal/server"
	"lampa-go/internal/storage"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "lampa-go:", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "", "path to the YAML config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	core, err := obs.Setup(ctx, cfg.OTel, cfg.Log)
	if err != nil {
		return fmt.Errorf("init observability: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := core.Shutdown(shutdownCtx); err != nil {
			core.Logger.Error("observability shutdown", slog.Any("error", err))
		}
	}()

	db, err := storage.Open(cfg.DB)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	if err := storage.Migrate(ctx, db, cfg.DB.Driver); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	if err := os.MkdirAll(cfg.Server.StaticDir, 0o755); err != nil {
		return fmt.Errorf("prepare static dir: %w", err)
	}

	srv, err := server.New(server.Deps{
		Config: cfg,
		Logger: core.Logger,
		Tracer: core.Tracer,
		Meter:  core.Meter,
		DB:     db,
	})
	if err != nil {
		return fmt.Errorf("build server: %w", err)
	}

	errCh := make(chan error, 1)
	go func() {
		core.Logger.Info("http server listening", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	core.Logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
```

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: код 0.

- [ ] **Step 3: Write `scripts/smoke.sh`**

```bash
#!/usr/bin/env bash
# smoke.sh — boot the server with a temp config and check the key endpoints.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PORT="${SMOKE_PORT:-18091}"
UPSTREAM_PORT="${SMOKE_UPSTREAM_PORT:-18092}"
BASE="http://127.0.0.1:${PORT}"
TMP="$(mktemp -d)"
SERVER_PID=""
UPSTREAM_PID=""

cleanup() {
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
  [ -n "$UPSTREAM_PID" ] && kill "$UPSTREAM_PID" 2>/dev/null || true
  rm -rf "$TMP"
}
trap cleanup EXIT

fail() { echo "SMOKE FAIL: $1" >&2; exit 1; }

check() { # check <name> <expected-substr> <url>
  local name="$1" expected="$2" url="$3"
  local body
  body="$(curl -fsS "$url")" || fail "$name: request to $url failed"
  case "$body" in
    *"$expected"*) echo "ok   $name" ;;
    *) fail "$name: expected '$expected' got: $body" ;;
  esac
}

mkdir -p "$TMP/web" "$TMP/data"
printf '<html><body>lampa-smoke</body></html>' > "$TMP/web/index.html"

cat > "$TMP/upstream.py" <<PY
import http.server, socketserver

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = ("upstream:" + self.path).encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Server", "fake-upstream")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass

socketserver.TCPServer.allow_reuse_address = True
with socketserver.TCPServer(("127.0.0.1", ${UPSTREAM_PORT}), Handler) as srv:
    srv.serve_forever()
PY

cat > "$TMP/config.yaml" <<EOF
server:
  listen: "127.0.0.1:${PORT}"
  static_dir: ${TMP}/web
  base_domain: localhost
cub:
  upstream: http://127.0.0.1:${UPSTREAM_PORT}
  timeout: 5s
db:
  driver: sqlite
  dsn: ${TMP}/data/smoke.db
otel:
  enable: false
log:
  level: info
  format: text
EOF

python3 "$TMP/upstream.py" &
UPSTREAM_PID=$!

"$ROOT/bin/lampa-go" -config "$TMP/config.yaml" >"$TMP/server.log" 2>&1 &
SERVER_PID=$!

ready=0
for _ in $(seq 1 50); do
  if curl -fsS "$BASE/healthz" >/dev/null 2>&1; then ready=1; break; fi
  sleep 0.2
done
[ "$ready" = "1" ] || { cat "$TMP/server.log" >&2; fail "server did not start"; }

check "healthz" "ok" "$BASE/healthz"
check "readyz" "ok" "$BASE/readyz"
check "checker" "ok" "$BASE/cub/api/checker"
check "blacklist" "[]" "$BASE/cub/api/plugins/blacklist"
check "metric" "secuses" "$BASE/cub/api/metric/unic"
check "proxy-path" "upstream:/api/users/get" "$BASE/cub/api/users/get"
check "proxy-marker" "upstream:/3/movie/1" "$BASE/cub/tmdb/3/movie/1"
check "static-index" "lampa-smoke" "$BASE/"
check "geo-default" "US" "$BASE/cub/geo"

HDRS="$(curl -fsS -D - -o /dev/null "$BASE/cub/api/anything")"
case "$HDRS" in
  *Server:*fake-upstream*) fail "proxy headers: upstream Server header leaked" ;;
  *) echo "ok   proxy header filtering" ;;
esac

echo "SMOKE OK"
```

- [ ] **Step 4: Run smoke test**

```bash
chmod +x scripts/smoke.sh
make smoke
```
Expected: построчные `ok <name>` и финальное `SMOKE OK`.

- [ ] **Step 5: Commit**

```bash
git add cmd scripts/smoke.sh
git commit -m "feat: main binary with graceful shutdown and smoke test"
```

---

### Task 10: Frontend overlay and example config

**Files:**
- Create: `frontend/overlay/public/plugins/modification.js`, `config.example.yaml`

- [ ] **Step 1: Write `frontend/overlay/public/plugins/modification.js`**

```js
// modification.js — lampa-go runtime integration.
// Rewrites requests to the cub mirrors into our /cub/ proxy namespace.
// Lampa loads this file automatically from the hosting server
// (see src/core/plugins.js in lampa-source), so it needs no patching.
(function () {
	'use strict';

	var MIRRORS = ['cub.example', 'cub2.example', 'mirror1.example', 'mirror2.example'];
	var MARKERS = ['tmdb', 'geo', 'ws', 'imagetmdb', 'cdn', 'ad'];

	function ownHost(host) {
		return (
			host === location.hostname ||
			host.indexOf('.' + location.hostname, host.length - location.hostname.length - 1) !== -1
		);
	}

	Lampa.Listener.follow('request_before', function (e) {
		var url = e.params.url;
		if (typeof url !== 'string' || url.indexOf('/cub/') !== -1) return;

		var match = url.match(/^https?:\/\/([^\/?#]+)([^?#]*)(\?[^#]*)?/);
		if (!match) return;

		var host = match[1].toLowerCase();
		var path = match[2] || '/';
		var query = match[3] || '';

		if (!ownHost(host) && MIRRORS.indexOf(host) === -1) return;

		var marker = '';
		var labels = host.split('.');
		if (labels.length > 1 && MARKERS.indexOf(labels[0]) !== -1) {
			marker = labels[0] + '/';
		}

		if (path === '/') path = '';

		e.params.url =
			location.origin + '/cub/' + marker + path.replace(/^\//, '') + query;
	});
})();
```

- [ ] **Step 2: Note on the manifest.js patch (не создаётся в v1)**

Spec §5.5 описывает два механизма. В v1 работает только `modification.js`: фронтенд по умолчанию строит URL на зеркала из `MIRRORS`, и переписывание в `/cub/` не зависит от значения `cub_mirrors`. Патч `src/core/manifest.js` (`cub_mirrors = ['<боевой домен>']`) создаётся отдельно — когда известен реальный домен — штатной процедурой из Task 12 (`docs/frontend-update.md`): `make fe-update` → правка `frontend/sources/src/core/manifest.js` → `make fe-new-patch NAME=010-cub-domains` → повторный `make fe-update`. Не создавать патч с доменом-заглушкой.

- [ ] **Step 3: Write `config.example.yaml`**

```yaml
# lampa-go configuration example.
# Copy to config.yaml and adjust. All keys have built-in defaults.

server:
  listen: ":8080"            # address to listen on (behind a reverse proxy)
  static_dir: ./deploy/web   # directory with the built Lampa frontend
  base_domain: lampa.example.com  # our public domain (used by frontend patches)

cub:
  upstream: https://cub.example # upstream the /cub/* requests are forwarded to
  timeout: 15s
  geo_header: X-Geo-Country  # client country header set by the reverse proxy
  geo_default: US            # fallback when the header is missing
  subdomain_markers: ["tmdb", "geo", "ws", "imagetmdb", "cdn", "ad"]

db:
  driver: sqlite             # sqlite | postgres
  dsn: ./data/lampa-go.db    # or postgres://user:pass@host/lampa
  max_open_conns: 25         # postgres pool; sqlite forces 1 (single writer)
  max_idle_conns: 5
  conn_max_lifetime: 30m

otel:
  enable: false              # false -> noop providers, logs to stdout
  endpoint: localhost:4317   # OTLP gRPC (vmagent / collector -> VictoriaMetrics)
  service_name: lampa-go
  insecure: true             # no TLS on the OTLP connection

log:
  level: info                # debug | info | warn | error
  format: text               # text | json
```

- [ ] **Step 4: Commit**

```bash
git add frontend/overlay config.example.yaml
git commit -m "feat: modification.js overlay and example config"
```

---

### Task 11: Frontend update pipeline (scripts/update-frontend.sh)

**Files:**
- Create: `scripts/update-frontend.sh`
- Modify: `Makefile` (добавить fe-цели)
- Create (результат прогона): `frontend/ORIGIN_COMMIT`

- [ ] **Step 1: Write `scripts/update-frontend.sh`**

```bash
#!/usr/bin/env bash
# update-frontend.sh — update Lampa frontend sources, apply our patches,
# build and deploy.
#
# Usage:
#   update-frontend.sh diff             show what changed upstream since the last update
#   update-frontend.sh update           full pipeline: fetch, summary, patches, overlay, build, deploy
#   update-frontend.sh build            rebuild from the current sources
#   update-frontend.sh deploy           deploy the last build to the deploy directory
#   update-frontend.sh new-patch NAME   save current working-tree changes as patches/<NAME>.patch
#
# Environment:
#   FE_DEPLOY_DIR   deploy directory (default: <repo>/deploy/web)
#   FORCE=1         run the full pipeline even when the upstream commit is unchanged
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FRONTEND="$ROOT/frontend"
SOURCES="$FRONTEND/sources"
PATCH_DIR="$FRONTEND/patches"
OVERLAY_DIR="$FRONTEND/overlay"
ORIGIN_FILE="$FRONTEND/ORIGIN_COMMIT"
DEPLOY_DIR="${FE_DEPLOY_DIR:-$ROOT/deploy/web}"
UPSTREAM="https://github.com/yumata/lampa-source.git"
BRANCH="main"

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mWARN\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mERROR\033[0m %s\n' "$*" >&2; exit 1; }

origin_commit() {
  if [ -f "$ORIGIN_FILE" ]; then cat "$ORIGIN_FILE"; fi
}

require_sources() {
  [ -d "$SOURCES/.git" ] || die "no sources clone at $SOURCES — run 'update' first"
}

fetch_upstream() {
  if [ ! -d "$SOURCES/.git" ]; then
    log "cloning $UPSTREAM into $SOURCES"
    git clone -q "$UPSTREAM" "$SOURCES"
  fi
  git -C "$SOURCES" fetch -q origin "$BRANCH"
}

upstream_head() {
  git -C "$SOURCES" rev-parse FETCH_HEAD
}

# Files touched by our patches (from the '+++ b/<path>' header lines).
patch_files() {
  local patch
  for patch in "$PATCH_DIR"/*.patch; do
    [ -e "$patch" ] || return 0
    awk '/^\+\+\+ b\//{sub(/^\+\+\+ b\//, ""); print}' "$patch"
  done
}

warn_if_patches_conflict() {
  local base="$1" new="$2" pf cf
  ls "$PATCH_DIR"/*.patch >/dev/null 2>&1 || return 0
  pf="$(patch_files | sort -u)"
  [ -n "$pf" ] || return 0
  cf="$(comm -12 <(printf '%s\n' "$pf") <(git -C "$SOURCES" diff --name-only "$base..$new" | sort -u))"
  if [ -n "$cf" ]; then
    warn "upstream touched files covered by our patches — conflicts likely:"
    printf '%s\n' "$cf" | sed 's/^/  /' >&2
  fi
}

print_summary() {
  local base="$1" new="$2"
  log "changes ${base:0:12}..${new:0:12}:"
  git -C "$SOURCES" --no-pager diff --stat "$base..$new" | tail -n 1
  git -C "$SOURCES" --no-pager diff --name-status "$base..$new"
}

apply_patches() {
  ls "$PATCH_DIR"/*.patch >/dev/null 2>&1 || { log "no patches"; return 0; }
  local patch
  for patch in "$PATCH_DIR"/*.patch; do
    log "applying $(basename "$patch")"
    if ! git -C "$SOURCES" apply --check "$patch" 2>"$TMP_ERR"; then
      cat "$TMP_ERR" >&2
      die "patch $(basename "$patch") does not apply — resolve the conflict, refresh the patch, re-run"
    fi
    git -C "$SOURCES" apply "$patch"
  done
}

apply_overlay() {
  [ -d "$OVERLAY_DIR" ] || return 0
  log "copying overlay"
  cp -a "$OVERLAY_DIR/." "$SOURCES/"
}

build_frontend() {
  require_sources
  log "building frontend (gulp pack_github)"
  (
    cd "$SOURCES"
    if [ ! -d node_modules ]; then
      log "installing npm dependencies"
      npm ci
    fi
    npx gulp pack_github
  )
  [ -d "$SOURCES/build/github/lampa" ] || die "build produced no output at sources/build/github/lampa"
}

deploy_frontend() {
  [ -d "$SOURCES/build/github/lampa" ] || die "no build output — run 'build' first"
  log "deploying to $DEPLOY_DIR"
  rm -rf "$DEPLOY_DIR.tmp"
  cp -a "$SOURCES/build/github/lampa" "$DEPLOY_DIR.tmp"
  rm -rf "$DEPLOY_DIR"
  mv "$DEPLOY_DIR.tmp" "$DEPLOY_DIR"
}

cmd_diff() {
  fetch_upstream
  local base new
  base="$(origin_commit)"
  new="$(upstream_head)"
  if [ -z "$base" ]; then
    log "no previous update recorded; upstream head is ${new:0:12}"
    return 0
  fi
  if [ "$base" = "$new" ]; then
    log "up to date: ${base:0:12}"
    return 0
  fi
  print_summary "$base" "$new"
  warn_if_patches_conflict "$base" "$new"
}

cmd_update() {
  fetch_upstream
  local base new
  base="$(origin_commit)"
  new="$(upstream_head)"

  if [ -n "$base" ] && [ "$base" = "$new" ] && [ "${FORCE:-0}" != "1" ]; then
    log "up to date at ${new:0:12} (use FORCE=1 to rebuild)"
    return 0
  fi

  # Reset the working tree to the new upstream commit.
  # 'clean -fd' drops our overlay files (they are re-added below) but keeps
  # ignored paths like node_modules and build/.
  log "checking out ${new:0:12}"
  git -C "$SOURCES" checkout -q -f FETCH_HEAD
  git -C "$SOURCES" clean -qfd

  # Reinstall dependencies only when the lockfile changed.
  if [ -n "$base" ] && ! git -C "$SOURCES" diff --quiet "$base..$new" -- package-lock.json; then
    log "package-lock.json changed — running npm ci"
    (cd "$SOURCES" && npm ci)
  elif [ ! -d "$SOURCES/node_modules" ]; then
    log "installing npm dependencies"
    (cd "$SOURCES" && npm ci)
  fi

  TMP_ERR="$(mktemp)"
  trap 'rm -f "$TMP_ERR"' EXIT
  apply_patches
  apply_overlay
  build_frontend
  deploy_frontend

  echo "$new" > "$ORIGIN_FILE"
  if [ -n "$base" ]; then
    print_summary "$base" "$new" || true
  fi
  log "update complete: ${base:-none} -> ${new:0:12}"
}

cmd_new_patch() {
  local name="${1:-}"
  [ -n "$name" ] || die "usage: update-frontend.sh new-patch <NNN-short-name>"
  require_sources
  git -C "$SOURCES" diff --quiet && die "no uncommitted changes in sources to save as a patch"
  mkdir -p "$PATCH_DIR"
  git -C "$SOURCES" diff > "$PATCH_DIR/$name.patch"
  # Revert the tree so the next 'update' applies the patch cleanly.
  git -C "$SOURCES" checkout -q -- .
  log "saved $PATCH_DIR/$name.patch (working tree reverted to HEAD)"
}

case "${1:-update}" in
  diff)      cmd_diff ;;
  update)    cmd_update ;;
  build)     build_frontend ;;
  deploy)    deploy_frontend ;;
  new-patch) shift; cmd_new_patch "$@" ;;
  *)         die "unknown command: $1 (diff|update|build|deploy|new-patch)" ;;
esac
```

- [ ] **Step 2: Add fe-targets to `Makefile` (после цели `clean`)**

```make
FE_DEPLOY ?= deploy/web

.PHONY: fe-diff fe-update fe-build fe-deploy fe-new-patch

fe-diff:
	./scripts/update-frontend.sh diff

fe-update:
	./scripts/update-frontend.sh update

fe-build:
	./scripts/update-frontend.sh build

fe-deploy:
	./scripts/update-frontend.sh deploy

fe-new-patch:
	@test -n "$(NAME)" || { echo "usage: make fe-new-patch NAME=010-short-name"; exit 1; }
	./scripts/update-frontend.sh new-patch $(NAME)
```

(Вторую `.PHONY` строку объединить с существующей — одна `.PHONY` со всеми целями.)

- [ ] **Step 3: Run the real pipeline**

```bash
chmod +x scripts/update-frontend.sh
make fe-diff    # первый запуск: клонирует репозиторий, печатает head
make fe-update  # полный цикл: npm ci + gulp pack_github + deploy
ls deploy/web/  # ожидаемо: index.html, app.min.js, assembly.json, css/, img/, lang/, plugins/ ...
cat frontend/ORIGIN_COMMIT
```
Expected: сборка проходит, `deploy/web/index.html` существует, в `deploy/web/plugins/` лежит наш `modification.js` (скопирован из overlay). Если node/npm отсутствуют — зафиксировать в отчёте и завершить шаг после `make fe-diff`.

- [ ] **Step 4: Manual verification of serving**

```bash
go build -o bin/lampa-go ./cmd/lampa-go
./bin/lampa-go &        # без -config: используются дефолты (:8080, ./deploy/web)
sleep 1
curl -s http://127.0.0.1:8080/ | head -5
curl -s http://127.0.0.1:8080/plugins/modification.js | head -3
curl -s http://127.0.0.1:8080/assembly.json
kill %1
```
Expected: index.html отдаётся, modification.js отдаётся, assembly.json — JSON версии.

- [ ] **Step 5: Commit**

```bash
git add scripts/update-frontend.sh Makefile frontend/ORIGIN_COMMIT
git commit -m "build: frontend update pipeline with patches, overlay and deploy"
```

---

### Task 12: Documentation

**Files:**
- Create: `README.md`, `docs/architecture.md`, `docs/frontend-update.md`, `docs/configuration.md`, `docs/observability.md`, `docs/deploy.md`

Язык документации — русский, технические термины — английские. Ниже — обязательное содержание каждого файла (полный текст пишет исполнитель по этим тезисам, сохраняя структуру разделов):

- [ ] **Step 1: `README.md`**

Разделы: «Что это» (Go-сервер: хостинг Lampa + скелет API cub со схемой «свой ответ / прокси»; C#-проект lampac — образец конечного результата); «Возможности первой итерации» (статика из lampa-source, заглушки checker/blacklist/metric/ads/geo, прокси `/cub/*` на настраиваемый upstream, OTel, sqlite/postgres + goose); «Быстрый старт» (требования: Go 1.27+, node 18+; `make fe-update` → `make run` → `http://localhost:8080`; путь `config.example.yaml` → `config.yaml`); «Структура репозитория» (кратко, из spec §4); «Документация» (ссылки на docs/*.md); «Лицензии третьих компонентов» (упоминание AGPL lampa-source — ссылка).

- [ ] **Step 2: `docs/architecture.md`**

Разделы: «Обзор» (один домен, path-based маршрутизация, таблица маршрутов из spec §5.2); «Middleware chain» (requestID → observe (trace/metrics/log) → recover); «Прокси» (ReverseProxy, Rewrite, маркеры поддоменов, фильтрация response-заголовков, таймаут, стриминг); «Попадание фронтенда под /cub/» (патч `manifest.js` → `cub_mirrors`, `modification.js` → перехват `request_before`); «Хранилище» (sqlite single-writer + WAL, postgres pool, goose); «Принципы развития» (новый сервис = новый префикс-модуль или отдельный микросервис; spec §13).

- [ ] **Step 3: `docs/frontend-update.md`**

Разделы: «Как это работает» (pristine clone в `frontend/sources`, `ORIGIN_COMMIT`, патчи `patches/NNN-*.patch` через `git apply`, overlay `frontend/overlay/` копируется поверх, `gulp pack_github`, атомарный деплой в `deploy/web`); «Команды» (таблица `make fe-diff|fe-update|fe-build|fe-deploy|fe-new-patch NAME=...`); «Создание нового патча» (сценарий: `make fe-update` → правки в `frontend/sources` → проверка → `make fe-new-patch NAME=010-short-name` → tree reverted → повторный `fe-update` применяет патч; первый рекомендуемый патч — `src/core/manifest.js` с `cub_mirrors = ['<боевой домен>']`, когда домен известен); «Разрешение конфликтов» (скрипт предупреждает о пересечении изменённых upstream-файлов с файлами патчей; при отказе `git apply`: обновить патч вручную в sources и пересоздать через `fe-new-patch`); «Платформенные сборки (будущее)» (webOS/Tizen — доп. gulp-таски, общий пул патчей).

- [ ] **Step 4: `docs/configuration.md`**

Полная таблица всех ключей `config.yaml` (из `config.example.yaml`) с типами, дефолтами и описанием; разделы «SQLite» (single writer, WAL, dsn — путь файла) и «PostgreSQL» (dsn, параметры пула); «Переменные окружения» — нет, только YAML+флаг `-config`.

- [ ] **Step 5: `docs/observability.md`**

Разделы: «Сигналы» (traces/metrics/logs, middleware `observe`: span на запрос, `http.server.requests`/`http.server.duration`, slog с `request_id`/`route`); «Подключение к VictoriaMetrics» (пример: `vmagent` с `--remoteWrite.url`, endpoint OTLP gRPC `:4317`; включение `otel.enable: true` + `endpoint`); «VictoriaLogs» (опционально, тот же OTLP); «Выключенный режим» (noop, нулевой оверхед).

- [ ] **Step 6: `docs/deploy.md`**

Разделы: «Схема» (browser → reverse-proxy → lampa-go:8080); «Caddy» (сниппет: домен → `reverse_proxy 127.0.0.1:8080`, плюс заголовок `X-Geo-Country` не обязателен); «nginx» (сниппет: `proxy_pass http://127.0.0.1:8080; proxy_set_header Host $host;`, опционально `proxy_set_header X-Geo-Country $geoip2_data_country;`); «systemd unit» (пример с `ExecStart=/opt/lampa-go/bin/lampa-go -config /opt/lampa-go/config.yaml`, `Restart=on-failure`); «Обновление» (цикл `make fe-update` + `systemctl restart`, атомарность деплоя).

- [ ] **Step 7: Commit**

```bash
git add README.md docs/
git commit -m "docs: Russian documentation for architecture, frontend pipeline, config, observability and deploy"
```

---

### Task 13: Final verification

**Files:** нет новых; проверки и точечные фиксы.

- [ ] **Step 1: Full test suite and linters**

```bash
gofmt -l .          # пустой вывод
go vet ./...
go test ./...
```
Expected: все зелёное. Если что-то красное — исправить и перезапустить.

- [ ] **Step 2: Smoke**

```bash
make smoke
```
Expected: `SMOKE OK`.

- [ ] **Step 3: End-to-end check of the frontend update cycle**

```bash
make fe-diff   # "up to date" после Task 11
FORCE=1 make fe-update   # повторный полный прогон должен пройти чисто (патчей нет, overlay копируется)
```
Expected: оба шага без ошибок.

- [ ] **Step 4: Cleanup и финальный коммит (если есть изменения)**

```bash
git status --porcelain   # должен быть пуст или содержать только ожидаемые правки
git add -A && git commit -m "chore: final verification fixes" || true
```

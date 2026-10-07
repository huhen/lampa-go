# Builder Integration (issue #9) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** lampa-go сам обновляет фронтенд из lampa-web-builder: сверяет свой `deployed_commit` с `available_commit` билдера, заказывает сборку своего домена, скачивает архив и атомарно (symlink-swap) выкладывает его в статику.

**Architecture:** новый пакет `internal/builder` (клиент API, деплойер, воркер) + секция `builder` в конфиге + key-value в существующей таблице `app_meta`. Отдача статики не меняется: `os.DirFS` перечитывает путь на каждый запрос, поэтому атомарный rename symlink'а видим клиентам мгновенно (закрывает issue #7). Контракт API — spec билдера §3 (`docs/superpowers/specs/2026-10-07-lampa-web-builder-design.md` в huhen/lampa-web-builder).

**Tech Stack:** Go stdlib (`net/http`, `archive/tar`, `compress/gzip`, `log/slog`), `database/sql` (sqlite в тестах), `httptest` для фейка билдера.

**Ветка:** `feature/builder-integration`, стек на `feature/first-iteration` (PR #1 не смержен, `main` содержит только docs — вся реализация живёт в нём). PR открывать с base `feature/first-iteration`, после мержа PR #1 перетаргетировать на `main`.

**Отступление от issue #9 (намеренное):** вместо сеанса `deployed_commit` из `frontend/ORIGIN_COMMIT` — самовосстановление: пустой `deployed_commit` трактуется как «обновиться». Первое включение делает один лишний rebuild+deploy, зато не нужен ручной шаг и не будет сломано удалением `frontend/` (issue #10). При выполнении отметить это комментарием в issue #9.

---

### Task 1: Ветка

**Files:** — (только git)

- [ ] **Step 1: Создать ветку поверх feature/first-iteration**

```bash
cd /home/usr1/coding/lampa-go
git checkout feature/first-iteration
git checkout -b feature/builder-integration
```

- [ ] **Step 2: Убедиться, что тесты зелёные на базе**

Run: `go test ./... && go vet ./...`
Expected: `ok ...` по всем пакетам, vet молчит.

---

### Task 2: Конфиг — секция `builder`

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: failing-тесты в `internal/config/config_test.go`**

Добавить в конец файла:

```go
func TestBuilderDefaults(t *testing.T) {
	cfg := Defaults()
	if cfg.Builder.Enabled {
		t.Error("builder must be disabled by default")
	}
	if cfg.Builder.URL != "http://builder:8080" {
		t.Errorf("builder url = %q, want http://builder:8080", cfg.Builder.URL)
	}
	if cfg.Builder.PollInterval.Std() != 5*time.Minute {
		t.Errorf("poll_interval = %v, want 5m", cfg.Builder.PollInterval.Std())
	}
	if cfg.Builder.KeepVersions != 3 {
		t.Errorf("keep_versions = %d, want 3", cfg.Builder.KeepVersions)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaults must be valid: %v", err)
	}
}

func TestLoadBuilderSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := `
server:
  base_domain: lampa.example.com
builder:
  enabled: true
  url: http://127.0.0.1:8081
  api_key: secret
  poll_interval: 1m
  keep_versions: 2
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Builder.Enabled || cfg.Builder.APIKey != "secret" || cfg.Builder.KeepVersions != 2 {
		t.Errorf("builder section parsed wrong: %+v", cfg.Builder)
	}
	if cfg.Builder.PollInterval.Std() != time.Minute {
		t.Errorf("poll_interval = %v, want 1m", cfg.Builder.PollInterval.Std())
	}
}

func TestValidateBuilderRequiresFields(t *testing.T) {
	cases := []func(*Config){
		func(c *Config) { c.Builder.Enabled = true },                                             // no api_key
		func(c *Config) { c.Builder.Enabled = true; c.Builder.APIKey = "k" },                     // no base_domain
		func(c *Config) { c.Builder.Enabled = true; c.Builder.APIKey = "k"; c.Server.BaseDomain = "d"; c.Builder.URL = "builder:8080" }, // no scheme
		func(c *Config) {
			c.Builder.Enabled = true
			c.Builder.APIKey = "k"
			c.Server.BaseDomain = "d"
			c.Builder.PollInterval = Duration(0)
		},
		func(c *Config) {
			c.Builder.Enabled = true
			c.Builder.APIKey = "k"
			c.Server.BaseDomain = "d"
			c.Builder.KeepVersions = 1
		},
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

- [ ] **Step 2: Запустить, убедиться в падении**

Run: `go test ./internal/config/ -run 'TestBuilder|TestLoadBuilder|TestValidateBuilder' -v`
Expected: FAIL — `cfg.Builder undefined`.

- [ ] **Step 3: Реализация в `internal/config/config.go`**

После типа `Cub` добавить:

```go
// Builder holds the lampa-web-builder integration settings.
type Builder struct {
	Enabled      bool     `yaml:"enabled"`
	URL          string   `yaml:"url"`
	APIKey       string   `yaml:"api_key"`
	PollInterval Duration `yaml:"poll_interval"`
	KeepVersions int      `yaml:"keep_versions"`
}
```

В `Config` добавить поле (после `Cub`):

```go
	Builder Builder `yaml:"builder"`
```

В `Defaults()` после `Cub: Cub{...},` добавить:

```go
		Builder: Builder{
			URL:          "http://builder:8080",
			PollInterval: Duration(5 * time.Minute),
			KeepVersions: 3,
		},
```

В конец `Validate()` перед `return nil` добавить:

```go
	if c.Builder.Enabled {
		u, err := url.Parse(c.Builder.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("builder.url must be an http(s) URL, got %q", c.Builder.URL)
		}
		if c.Builder.APIKey == "" {
			return errors.New("builder.api_key is required when builder.enabled is true")
		}
		if c.Server.BaseDomain == "" {
			return errors.New("server.base_domain is required when builder.enabled is true (it is the build domain)")
		}
		if c.Builder.PollInterval.Std() <= 0 {
			return errors.New("builder.poll_interval must be positive")
		}
		// Rollback needs the previous version to stay on disk.
		if c.Builder.KeepVersions < 2 {
			return errors.New("builder.keep_versions must be at least 2")
		}
	}
```

- [ ] **Step 4: Запустить тесты пакета**

Run: `go test ./internal/config/ -v`
Expected: все PASS (включая старые: дефолты остаются валидными).

- [ ] **Step 5: Commit**

```bash
git add internal/config/
git commit -m "feat: builder integration config section"
```

---

### Task 3: Storage — MetaStore (key-value в `app_meta`)

**Files:**
- Modify: `internal/storage/storage.go`
- Test: `internal/storage/storage_test.go`

- [ ] **Step 1: failing-тест в `internal/storage/storage_test.go`**

Добавить в конец:

```go
func TestMetaStore(t *testing.T) {
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

	ctx := context.Background()
	meta, err := NewMetaStore(db, cfg.Driver)
	if err != nil {
		t.Fatalf("NewMetaStore: %v", err)
	}

	// Absent key reads as empty, without error.
	v, err := meta.Get(ctx, "builder.deployed_commit")
	if err != nil || v != "" {
		t.Fatalf("Get absent = (%q, %v), want (\"\", nil)", v, err)
	}

	if err := meta.Set(ctx, "builder.deployed_commit", "abc123"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := meta.Set(ctx, "builder.deployed_commit", "def456"); err != nil {
		t.Fatalf("Set (upsert): %v", err)
	}
	v, err = meta.Get(ctx, "builder.deployed_commit")
	if err != nil || v != "def456" {
		t.Fatalf("Get = (%q, %v), want (def456, nil)", v, err)
	}

	if _, err := NewMetaStore(db, "mysql"); err == nil {
		t.Fatal("expected error for unknown driver")
	}
}
```

- [ ] **Step 2: Запустить, убедиться в падении**

Run: `go test ./internal/storage/ -run TestMetaStore -v`
Expected: FAIL — `undefined: NewMetaStore`.

- [ ] **Step 3: Реализация в `internal/storage/storage.go`**

Добавить импорты `"strconv"` и `"errors"` (если ещё нет) к существующим. В конец файла:

```go
// MetaStore reads and writes the app_meta key-value table.
// It exists since migration 00001 and stores single-string app state
// (currently: builder.deployed_commit).
type MetaStore struct {
	db *sql.DB
	ph func(n int) string // positional placeholder: "?" or "$1"
}

// NewMetaStore builds a MetaStore for the given driver.
func NewMetaStore(db *sql.DB, driver string) (*MetaStore, error) {
	switch driver {
	case config.DriverSQLite, "":
		return &MetaStore{db: db, ph: func(int) string { return "?" }}, nil
	case config.DriverPostgres:
		return &MetaStore{db: db, ph: func(n int) string { return "$" + strconv.Itoa(n) }}, nil
	default:
		return nil, fmt.Errorf("unknown db driver %q", driver)
	}
}

// Get returns the value for key; an absent key reads as ("", nil).
func (m *MetaStore) Get(ctx context.Context, key string) (string, error) {
	q := "SELECT value FROM app_meta WHERE key = " + m.ph(1)
	var v string
	err := m.db.QueryRowContext(ctx, q, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get meta %q: %w", key, err)
	}
	return v, nil
}

// Set inserts or updates the value for key.
func (m *MetaStore) Set(ctx context.Context, key, value string) error {
	q := "INSERT INTO app_meta (key, value) VALUES (" + m.ph(1) + ", " + m.ph(2) + ")" +
		" ON CONFLICT(key) DO UPDATE SET value = excluded.value"
	if _, err := m.db.ExecContext(ctx, q, key, value); err != nil {
		return fmt.Errorf("set meta %q: %w", key, err)
	}
	return nil
}
```

- [ ] **Step 4: Запустить тесты пакета**

Run: `go test ./internal/storage/ -v`
Expected: все PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/storage/
git commit -m "feat: app_meta key-value store for deployed_commit"
```

---

### Task 4: Client — Status, StartBuild, Build + коды ошибок

**Files:**
- Create: `internal/builder/client.go`
- Test: `internal/builder/client_test.go`

- [ ] **Step 1: failing-тест `internal/builder/client_test.go`**

```go
package builder

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"errors"
)

// newFakeBuilder spins up an httptest server emulating the builder API
// (contract: lampa-web-builder spec §3) and returns the client aimed at it.
func newFakeBuilder(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL, "test-key")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestClientSendsAPIKey(t *testing.T) {
	var gotKey string
	c := newFakeBuilder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-API-Key")
		w.WriteHeader(http.StatusUnauthorized) // stop the flow early, key is what we check
	}))
	if _, err := c.Status(context.Background()); err == nil {
		t.Fatal("expected error for 401")
	}
	if gotKey != "test-key" {
		t.Errorf("X-API-Key = %q, want test-key", gotKey)
	}
}

func TestClientStatus(t *testing.T) {
	c := newFakeBuilder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/status" {
			t.Errorf("path = %q, want /api/v1/status", r.URL.Path)
		}
		w.Write([]byte(`{
			"version": "1.0.0",
			"available_commit": "aaa",
			"latest_seen_commit": "bbb",
			"bad_commit": "ccc",
			"poll": {"interval": "6h", "last_checked": "2026-10-07T00:00:00Z", "last_error": ""}
		}`))
	}))
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Version != "1.0.0" || st.AvailableCommit != "aaa" || st.LatestSeenCommit != "bbb" || st.BadCommit != "ccc" {
		t.Errorf("status = %+v", st)
	}
	if st.Poll.Interval != "6h" {
		t.Errorf("poll = %+v", st.Poll)
	}
}

func TestClientStartBuild(t *testing.T) {
	var gotBody StartBuild
	c := newFakeBuilder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/builds" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"build_id": "b-1", "cached": true}`))
	}))
	ref, err := c.StartBuild(context.Background(), "lampa.example.com")
	if err != nil {
		t.Fatalf("StartBuild: %v", err)
	}
	if gotBody.Domain != "lampa.example.com" {
		t.Errorf("sent domain = %q", gotBody.Domain)
	}
	if ref.BuildID != "b-1" || !ref.Cached {
		t.Errorf("ref = %+v", ref)
	}
}

func TestClientBuild(t *testing.T) {
	c := newFakeBuilder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/builds/b-1" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Write([]byte(`{"id": "b-1", "kind": "build", "domain": "d", "commit": "aaa", "status": "success", "error": ""}`))
	}))
	b, err := c.Build(context.Background(), "b-1")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if b.ID != "b-1" || b.Status != BuildSuccess || b.Commit != "aaa" {
		t.Errorf("build = %+v", b)
	}
}

func TestClientErrorMapping(t *testing.T) {
	cases := []struct {
		code int
		want error
	}{
		{http.StatusConflict, ErrBusy},
		{http.StatusTooManyRequests, ErrQueueFull},
		{http.StatusServiceUnavailable, ErrNoVersion},
	}
	for _, tc := range cases {
		c := newFakeBuilder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.code)
			w.Write([]byte(`{"error": "..."}`))
		}))
		_, err := c.StartBuild(context.Background(), "d")
		if !errors.Is(err, tc.want) {
			t.Errorf("code %d: err = %v, want %v", tc.code, err, tc.want)
		}
	}
}
```

- [ ] **Step 2: Запустить, убедиться в падении**

Run: `go test ./internal/builder/ -v`
Expected: пакет не компилируется (`NewClient undefined`).

- [ ] **Step 3: Реализация `internal/builder/client.go`**

```go
// Package builder integrates lampa-go with the lampa-web-builder service:
// the API client, the atomic frontend deployer and the update worker.
// The API contract is the builder spec §3
// (huhen/lampa-web-builder, docs/superpowers/specs/).
package builder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// Sentinel errors mapped from builder HTTP statuses: all mean "retry later".
var (
	ErrBusy      = errors.New("builder busy (test build in progress)") // 409
	ErrQueueFull = errors.New("builder queue is full")                 // 429
	ErrNoVersion = errors.New("builder has no available version yet")  // 503
)

// Frontend build statuses as reported by the builder.
const (
	BuildQueued  = "queued"
	BuildRunning = "running"
	BuildSuccess = "success"
	BuildFailed  = "failed"
)

// Status mirrors GET /api/v1/status.
type Status struct {
	Version          string `json:"version"`
	AvailableCommit  string `json:"available_commit"`
	LatestSeenCommit string `json:"latest_seen_commit"`
	BadCommit        string `json:"bad_commit"`
	Poll             struct {
		Interval    string `json:"interval"`
		LastChecked string `json:"last_checked"`
		LastError   string `json:"last_error"`
	} `json:"poll"`
}

// StartBuild is the POST /api/v1/builds request body.
type StartBuild struct {
	Domain string `json:"domain"`
}

// BuildRef is the POST /api/v1/builds response.
type BuildRef struct {
	BuildID string `json:"build_id"`
	Cached  bool   `json:"cached"`
}

// Build mirrors GET /api/v1/builds/{id}.
type Build struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Domain string `json:"domain"`
	Commit string `json:"commit"`
	Status string `json:"status"`
	Error  string `json:"error"`
}

// Client talks to the builder API. Safe for concurrent use.
type Client struct {
	base     *url.URL
	key      string
	hcAPI    *http.Client // JSON endpoints: bounded overall
	hcStream *http.Client // archive download: bounded by context only
}

// NewClient builds a client for the builder at rawURL using apiKey.
func NewClient(rawURL, apiKey string) (*Client, error) {
	base, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse builder url: %w", err)
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, fmt.Errorf("builder url must be http(s), got %q", rawURL)
	}
	return &Client{
		base: base,
		key:  apiKey,
		hcAPI: &http.Client{
			Timeout: 30 * time.Second,
		},
		hcStream: &http.Client{
			// The archive can take a while; the caller's context bounds it.
			ResponseHeaderTimeout: 30 * time.Second,
		},
	}, nil
}

func (c *Client) newRequest(ctx context.Context, hc *http.Client, method, path string, body io.Reader) (*http.Request, error) {
	u := c.base.JoinPath(path)
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", c.key)
	return req, nil
}

// statusError converts a non-2xx response into a sentinel or a descriptive error.
func statusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch resp.StatusCode {
	case http.StatusConflict:
		return ErrBusy
	case http.StatusTooManyRequests:
		return ErrQueueFull
	case http.StatusServiceUnavailable:
		return ErrNoVersion
	}
	return fmt.Errorf("builder returned %d: %s", resp.StatusCode, bytes.TrimSpace(body))
}

func decodeJSON(resp *http.Response, v any) error {
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("decode builder response: %w", err)
	}
	return nil
}

// Status returns the builder status snapshot.
func (c *Client) Status(ctx context.Context) (Status, error) {
	req, err := c.newRequest(ctx, c.hcAPI, http.MethodGet, "/api/v1/status", nil)
	if err != nil {
		return Status{}, err
	}
	resp, err := c.hcAPI.Do(req)
	if err != nil {
		return Status{}, fmt.Errorf("builder status: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Status{}, statusError(resp)
	}
	var st Status
	if err := decodeJSON(resp, &st); err != nil {
		return Status{}, err
	}
	return st, nil
}

// StartBuild orders a build for the domain. The builder always builds its
// latest available commit; the commit is not a parameter.
func (c *Client) StartBuild(ctx context.Context, domain string) (BuildRef, error) {
	body, err := json.Marshal(StartBuild{Domain: domain})
	if err != nil {
		return BuildRef{}, err
	}
	req, err := c.newRequest(ctx, c.hcAPI, http.MethodPost, "/api/v1/builds", bytes.NewReader(body))
	if err != nil {
		return BuildRef{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hcAPI.Do(req)
	if err != nil {
		return BuildRef{}, fmt.Errorf("builder start build: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		return BuildRef{}, statusError(resp)
	}
	var ref BuildRef
	if err := decodeJSON(resp, &ref); err != nil {
		return BuildRef{}, err
	}
	if ref.BuildID == "" {
		return BuildRef{}, errors.New("builder returned an empty build_id")
	}
	return ref, nil
}

// Build returns the status of a single build.
func (c *Client) Build(ctx context.Context, id string) (Build, error) {
	req, err := c.newRequest(ctx, c.hcAPI, http.MethodGet, "/api/v1/builds/"+url.PathEscape(id), nil)
	if err != nil {
		return Build{}, err
	}
	resp, err := c.hcAPI.Do(req)
	if err != nil {
		return Build{}, fmt.Errorf("builder build %s: %w", id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Build{}, statusError(resp)
	}
	var b Build
	if err := decodeJSON(resp, &b); err != nil {
		return Build{}, err
	}
	return b, nil
}

// DownloadArchive streams the build archive into dest (tmp file + rename).
// Caller bounds the transfer via ctx.
func (c *Client) DownloadArchive(ctx context.Context, id, dest string) error {
	req, err := c.newRequest(ctx, c.hcStream, http.MethodGet, "/api/v1/builds/"+url.PathEscape(id)+"/archive", nil)
	if err != nil {
		return err
	}
	resp, err := c.hcStream.Do(req)
	if err != nil {
		return fmt.Errorf("builder archive %s: %w", id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return statusError(resp)
	}
	f, err := os.CreateTemp(filepath.Dir(dest), ".archive-*")
	if err != nil {
		return fmt.Errorf("create temp archive: %w", err)
	}
	tmp := f.Name()
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("download archive: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write archive: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("place archive: %w", err)
	}
	return nil
}
```

Примечание: `os`/`filepath`/`time` импортируются уже сейчас — Task 5 расширит файл.

- [ ] **Step 4: Запустить тесты пакета**

Run: `go test ./internal/builder/ -v`
Expected: PASS. Внимание: `go vet` может ругаться на неиспользуемые… нет, пакет компилируется целиком — всё используется.

- [ ] **Step 5: Commit**

```bash
git add internal/builder/
git commit -m "feat: builder API client"
```

---

### Task 5: Deployer — безопасная распаковка архива

**Files:**
- Create: `internal/builder/deploy.go`
- Test: `internal/builder/deploy_test.go`

- [ ] **Step 1: failing-тест `internal/builder/deploy_test.go`**

```go
package builder

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeArchive builds a tar.gz with the given files (name → content) into dest.
func writeArchive(t *testing.T, dest string, files map[string]string) {
	t.Helper()
	f, err := os.Create(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     name,
			Size:     int64(len(content)),
			Mode:     0o644,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
}

func newTestDeployer(t *testing.T) *Deployer {
	t.Helper()
	root := t.TempDir()
	d, err := NewDeployer(filepath.Join(root, "current"), 3)
	if err != nil {
		t.Fatalf("NewDeployer: %v", err)
	}
	if err := d.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	return d
}

func readAndServe(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestExtract(t *testing.T) {
	d := newTestDeployer(t)
	archive := filepath.Join(t.TempDir(), "a.tar.gz")
	writeArchive(t, archive, map[string]string{
		"index.html":       "<html>v1</html>",
		"css/app.css":      "body{}",
		"plugins/mod.js":   "// mod",
	})

	if err := d.Extract(archive, "aaa"); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	// Files land in the version dir, archive root == deploy root (no wrapper).
	if got := readAndServe(t, filepath.Join(d.Root, "versions", "aaa", "index.html")); got != "<html>v1</html>" {
		t.Errorf("index.html = %q", got)
	}
	if got := readAndServe(t, filepath.Join(d.Root, "versions", "aaa", "css", "app.css")); got != "body{}" {
		t.Errorf("app.css = %q", got)
	}
	// No temp dirs left behind.
	entries, _ := os.ReadDir(filepath.Join(d.Root, "versions"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("temp dir leftover: %s", e.Name())
		}
	}
}

func TestExtractRejectsTraversal(t *testing.T) {
	d := newTestDeployer(t)
	archive := filepath.Join(t.TempDir(), "evil.tar.gz")
	writeArchive(t, archive, map[string]string{"../evil.txt": "pwn"})

	if err := d.Extract(archive, "bbb"); err == nil {
		t.Fatal("expected error for path traversal entry")
	}
	if _, err := os.Stat(filepath.Join(d.Root, "evil.txt")); err == nil {
		t.Error("file escaped the version dir")
	}
}

func TestExtractRejectsSymlink(t *testing.T) {
	d := newTestDeployer(t)
	archive := filepath.Join(t.TempDir(), "link.tar.gz")
	// Built by hand: the fixture helper only writes regular files.
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeSymlink, Name: "link", Linkname: "/etc/passwd"}); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	f.Close()

	if err := d.Extract(archive, "ccc"); err == nil {
		t.Fatal("expected error for symlink entry")
	}
}
```

- [ ] **Step 2: Запустить, убедиться в падении**

Run: `go test ./internal/builder/ -run TestExtract -v`
Expected: FAIL — `undefined: Deployer`/`NewDeployer`.

- [ ] **Step 3: Реализация Extract в `internal/builder/deploy.go`**

```go
package builder

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Deployer manages the versioned frontend layout:
//
//	<root>/versions/<commit>/   extracted builds
//	<root>/current              symlink → versions/<commit>, served by the web handler
//
// root is the parent of staticDir; staticDir itself must be the symlink path
// (os.DirFS re-resolves it on every open, so an atomic rename swaps the
// frontend for all clients — no 404 window).
type Deployer struct {
	StaticDir string // the symlink path served by the web handler
	Root      string // parent of StaticDir; holds versions/
	Keep      int    // how many version dirs to retain
}

// NewDeployer validates the static_dir layout and returns a Deployer.
func NewDeployer(staticDir string, keep int) (*Deployer, error) {
	if keep < 2 {
		return nil, fmt.Errorf("keep must be at least 2, got %d", keep)
	}
	if fi, err := os.Lstat(staticDir); err == nil {
		// A real directory under the static_dir path means the old flat
		// layout: the first Swap would fail (cannot rename a symlink over
		// a directory) and the config must be migrated first.
		if fi.IsDir() && fi.Mode()&os.ModeSymlink == 0 {
			return nil, fmt.Errorf(
				"static_dir %q is a real directory; with builder enabled it must be the symlink path (e.g. <root>/current), see docs/deploy.md",
				staticDir)
		}
	}
	return &Deployer{
		StaticDir: staticDir,
		Root:      filepath.Dir(staticDir),
		Keep:      keep,
	}, nil
}

// EnsureDirs creates the root and the versions directory. It deliberately
// does not create StaticDir: that is the symlink, swapped into place on
// the first deploy.
func (d *Deployer) EnsureDirs() error {
	if err := os.MkdirAll(filepath.Join(d.Root, "versions"), 0o755); err != nil {
		return fmt.Errorf("create versions dir: %w", err)
	}
	return nil
}

// Extract unpacks the tar.gz archive into versions/<commit>. Files are
// extracted safely: no absolute paths, no "..", no symlinks. The version
// dir appears atomically (extract into a temp dir, then rename).
func (d *Deployer) Extract(archivePath, commit string) error {
	versionsDir := filepath.Join(d.Root, "versions")
	dst := filepath.Join(versionsDir, commit)
	tmp, err := os.MkdirTemp(versionsDir, ".tmp-"+commit+"-")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)

	if err := untar(archivePath, tmp); err != nil {
		return err
	}
	if _, err := os.Lstat(dst); err == nil {
		// Leftover from an interrupted deploy of the same commit.
		if err := os.RemoveAll(dst); err != nil {
			return fmt.Errorf("remove stale version dir: %w", err)
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("place version dir: %w", err)
	}
	return nil
}

func untar(archivePath, dst string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		name := filepath.Clean(hdr.Name)
		// tar paths are slash-separated; validate the cleaned form.
		if !fs.ValidPath(name) || name == "." {
			return fmt.Errorf("archive entry %q: unsafe path", hdr.Name)
		}
		target := filepath.Join(dst, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", name, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", filepath.Dir(name), err)
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, hdr.FileInfo().Mode().Perm())
			if err != nil {
				return fmt.Errorf("create %s: %w", name, err)
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return fmt.Errorf("write %s: %w", name, err)
			}
			if err := out.Close(); err != nil {
				return fmt.Errorf("close %s: %w", name, err)
			}
		default:
			return fmt.Errorf("archive entry %q: unsupported type %q", hdr.Name, hdr.Typeflag)
		}
	}
}
```

- [ ] **Step 4: Запустить тесты пакета**

Run: `go test ./internal/builder/ -v`
Expected: все PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/builder/
git commit -m "feat: safe archive extraction into versioned dirs"
```

---

### Task 6: Deployer — Swap и Prune

**Files:**
- Modify: `internal/builder/deploy.go`
- Test: `internal/builder/deploy_test.go`

- [ ] **Step 1: failing-тесты в `internal/builder/deploy_test.go`**

```go
func TestSwapAndPrune(t *testing.T) {
	d := newTestDeployer(t)
	ctx := t.TempDir() // scratch for archives
	for _, commit := range []string{"aaa", "bbb", "ccc", "ddd"} {
		archive := filepath.Join(ctx, commit+".tar.gz")
		writeArchive(t, archive, map[string]string{"index.html": commit})
		if err := d.Extract(archive, commit); err != nil {
			t.Fatalf("Extract %s: %v", commit, err)
		}
	}

	// Swap is idempotent and works over an existing symlink or nothing.
	for range 2 {
		if err := d.Swap("ccc"); err != nil {
			t.Fatalf("Swap: %v", err)
		}
	}
	if got := readAndServe(t, d.StaticDir, "index.html"); got != "ccc" {
		t.Errorf("served = %q, want ccc", got)
	}
	// The symlink must be relative so the tree stays movable.
	target, err := os.Readlink(d.StaticDir)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != filepath.Join("versions", "ccc") {
		t.Errorf("symlink target = %q, want versions/ccc", target)
	}

	// Prune keeps Keep newest + the currently served one.
	if err := d.Prune(); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(d.Root, "versions"))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	// ddd, ccc, bbb are the three newest by mtime; ccc is protected as served.
	if len(names) != 3 {
		t.Errorf("versions after prune = %v, want 3 entries", names)
	}
	for _, gone := range []string{"aaa"} {
		for _, n := range names {
			if n == gone {
				t.Errorf("%s must be pruned, versions = %v", gone, names)
			}
		}
	}
}

// readAndServe reads path/name through nothing but the filesystem —
// helper distinct from readAndServe used for version dirs.
func readAndServe(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Join(dir, name), err)
	}
	return string(b)
}
```

Внимание: в Task 5 уже есть `readAndServe(t, path)` с одной сигнатурой — здесь добавляется второй хелпер с тем же именем, будет конфликт компиляции. **Переименовать хелпер из Task 5 в `readVersionFile`** (и его вызовы в `TestExtract`) — сделать это в этом же шаге:

В `TestExtract` заменить:

```go
	if got := readAndServe(t, filepath.Join(d.Root, "versions", "aaa", "index.html")); got != "<html>v1</html>" {
```

на:

```go
	if got := readVersionFile(t, filepath.Join(d.Root, "versions", "aaa"), "index.html"); got != "<html>v1</html>" {
```

и аналогично для `app.css`; определение из Task 5 переименовать:

```go
func readVersionFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Join(dir, name), err)
	}
	return string(b)
}
```

- [ ] **Step 2: Запустить, убедиться в падении**

Run: `go test ./internal/builder/ -run TestSwap -v`
Expected: FAIL — `d.Swap undefined`.

- [ ] **Step 3: Реализация Swap и Prune в `internal/builder/deploy.go`**

В конец файла:

```go
// Swap atomically repoints StaticDir at versions/<commit>: a temp symlink
// is created next to it and renamed over it. A rename replaces an existing
// symlink (or fills a missing path) in one step — readers never see the
// path absent (issue #7).
func (d *Deployer) Swap(commit string) error {
	tmp, err := os.MkdirTemp(d.Root, ".swap-")
	if err != nil {
		return fmt.Errorf("create swap temp: %w", err)
	}
	// MkdirTemp created a directory; a symlink needs the free path.
	if err := os.Remove(tmp); err != nil {
		return fmt.Errorf("clear swap temp: %w", err)
	}
	target := filepath.Join("versions", commit) // relative: the tree stays movable
	if err := os.Symlink(target, tmp); err != nil {
		return fmt.Errorf("symlink: %w", err)
	}
	if err := os.Rename(tmp, d.StaticDir); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("swap: %w", err)
	}
	return nil
}

// Prune removes version dirs beyond the Keep newest and sweeps temp
// leftovers. The currently served version is never removed.
func (d *Deployer) Prune() error {
	versionsDir := filepath.Join(d.Root, "versions")
	entries, err := os.ReadDir(versionsDir)
	if err != nil {
		return fmt.Errorf("read versions dir: %w", err)
	}

	// The served commit stays even if it is the oldest one.
	var served string
	if target, err := os.Readlink(d.StaticDir); err == nil {
		served = filepath.Base(target)
	}

	type version struct {
		name  string
		mtime int64
	}
	var versions []version
	for _, e := range entries {
		name := e.Name()
		// Temp leftovers from interrupted deploys are swept unconditionally.
		if strings.HasPrefix(name, ".tmp-") || strings.HasPrefix(name, ".swap-") {
			os.RemoveAll(filepath.Join(versionsDir, name))
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // raced with something else; skip
		}
		versions = append(versions, version{name: name, mtime: info.ModTime().UnixNano()})
	}
	// Newest first.
	for i := 0; i < len(versions); i++ {
		for j := i + 1; j < len(versions); j++ {
			if versions[j].mtime > versions[i].mtime {
				versions[i], versions[j] = versions[j], versions[i]
			}
		}
	}
	for i, v := range versions {
		if i < d.Keep || v.name == served {
			continue
		}
		if err := os.RemoveAll(filepath.Join(versionsDir, v.name)); err != nil {
			return fmt.Errorf("prune %s: %w", v.name, err)
		}
	}
	return nil
}
```

- [ ] **Step 4: Запустить тесты пакета**

Run: `go test ./internal/builder/ -v`
Expected: все PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/builder/
git commit -m "feat: atomic symlink swap and version pruning"
```

---

### Task 7: Worker — Reconcile (полный цикл)

**Files:**
- Create: `internal/builder/worker.go`
- Test: `internal/builder/worker_test.go`

- [ ] **Step 1: failing-тест `internal/builder/worker_test.go`**

```go
package builder

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"lampa-go/internal/config"
	"lampa-go/internal/storage"
)

// fakeBuilder emulates the builder API for worker tests.
type fakeBuilder struct {
	mu           sync.Mutex
	available    string
	buildStatus  string // what GET /builds/{id} reports
	buildErrCode int    // status code for POST /builds (0 = 202)
	starts       int
	t            *testing.T
}

func (f *fakeBuilder) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.URL.Path == "/api/v1/status":
			w.Write([]byte(`{"version":"1","available_commit":"` + f.available + `","latest_seen_commit":"` + f.available + `"}`))
		case r.URL.Path == "/api/v1/builds" && r.Method == http.MethodPost:
			f.starts++
			if f.buildErrCode != 0 {
				w.WriteHeader(f.buildErrCode)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"build_id":"b-1","cached":false}`))
		case r.URL.Path == "/api/v1/builds/b-1":
			w.Write([]byte(`{"id":"b-1","commit":"` + f.available + `","status":"` + f.buildStatus + `"}`))
		case r.URL.Path == "/api/v1/builds/b-1/archive":
			archive := filepath.Join(f.t.TempDir(), "a.tar.gz")
			f.writeArchive(archive)
			http.ServeFile(w, r, archive)
		default:
			http.NotFound(w, r)
		}
	})
}

func (f *fakeBuilder) writeArchive(dest string) {
	f2, err := os.Create(dest)
	if err != nil {
		f.t.Fatal(err)
	}
	defer f2.Close()
	gz := gzip.NewWriter(f2)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	content := "index for " + f.available
	tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "index.html", Size: int64(len(content)), Mode: 0o644})
	tw.Write([]byte(content))
}

// newTestWorker wires a worker against a fake builder and a fresh sqlite DB.
func newTestWorker(t *testing.T, f *fakeBuilder) (*Worker, *Deployer, *storage.MetaStore) {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)

	cfg := config.Defaults().DB
	cfg.DSN = filepath.Join(t.TempDir(), "app.db")
	db, err := storage.Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(context.Background(), db, cfg.Driver); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	meta, err := storage.NewMetaStore(db, cfg.Driver)
	if err != nil {
		t.Fatalf("NewMetaStore: %v", err)
	}

	client, err := NewClient(srv.URL, "k")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	d, err := NewDeployer(filepath.Join(t.TempDir(), "current"), 3)
	if err != nil {
		t.Fatalf("NewDeployer: %v", err)
	}
	if err := d.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	w := NewWorker(Deps{
		Client:   client,
		Deployer: d,
		Meta:     meta,
		Domain:   "lampa.example.com",
		Logger:   testLogger(),
	})
	return w, d, meta
}

func TestReconcileFirstDeploy(t *testing.T) {
	f := &fakeBuilder{available: "aaa", buildStatus: BuildSuccess, t: t}
	w, d, meta := newTestWorker(t, f)

	if err := w.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// Frontend is served through the symlink and carries the new content.
	b, err := os.ReadFile(filepath.Join(d.StaticDir, "index.html"))
	if err != nil || string(b) != "index for aaa" {
		t.Fatalf("served index = (%q, %v)", b, err)
	}
	// deployed_commit recorded.
	got, err := meta.Get(context.Background(), deployedCommitKey)
	if err != nil || got != "aaa" {
		t.Fatalf("deployed_commit = (%q, %v)", got, err)
	}
	// Second reconcile is a no-op: nothing new, no extra build ordered.
	if err := w.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile 2: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.starts != 1 {
		t.Errorf("starts = %d, want 1", f.starts)
	}
}

func TestReconcileUpdatesToNewCommit(t *testing.T) {
	f := &fakeBuilder{available: "aaa", buildStatus: BuildSuccess, t: t}
	w, d, meta := newTestWorker(t, f)
	if err := w.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	f.mu.Lock()
	f.available = "bbb"
	f.mu.Unlock()
	if err := w.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile 2: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(d.StaticDir, "index.html"))
	if string(b) != "index for bbb" {
		t.Errorf("served = %q, want index for bbb", b)
	}
	got, _ := meta.Get(context.Background(), deployedCommitKey)
	if got != "bbb" {
		t.Errorf("deployed_commit = %q, want bbb", got)
	}
}
```

Хелпер `testLogger()` — общий для тестов пакета, добавить в этот же файл:

```go
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
```

(импорты дополнить: `"io"`, `"log/slog"`).

- [ ] **Step 2: Запустить, убедиться в падении**

Run: `go test ./internal/builder/ -run TestReconcile -v`
Expected: FAIL — `undefined: Worker`, `deployedCommitKey`.

- [ ] **Step 3: Реализация `internal/builder/worker.go`**

```go
package builder

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"lampa-go/internal/storage"
)

const (
	// deployedCommitKey is the app_meta key holding the commit currently
	// served from static_dir.
	deployedCommitKey = "builder.deployed_commit"
	// buildPollInterval is how often the build status is polled.
	buildPollInterval = 5 * time.Second
	// buildTimeout caps one build wait; the builder itself times out builds.
	buildTimeout = 30 * time.Minute
	// downloadTimeout caps the archive transfer.
	downloadTimeout = 10 * time.Minute
)

// Worker keeps the served frontend in sync with the builder's
// available_commit. It is not safe for concurrent use; Run is the only
// intended driver.
type Worker struct {
	d Deps
}

// Deps wires the worker.
type Deps struct {
	Client   *Client
	Deployer *Deployer
	Meta     *storage.MetaStore
	Domain   string        // server.base_domain: the build domain
	Interval time.Duration // reconcile cadence
	Logger   *slog.Logger
}

// NewWorker builds a Worker; a nil logger falls back to slog.Default().
func NewWorker(d Deps) *Worker {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Worker{d: d}
}

// Run reconciles on every tick until ctx is done.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.d.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.Reconcile(ctx); err != nil {
				w.d.Logger.Warn("builder reconcile", slog.Any("error", err))
			}
		}
	}
}

// Reconcile performs one check-and-update cycle. Transient problems
// (builder unreachable, busy, queue full) return an error: the next tick
// retries. A successful cycle ends with the frontend swapped and
// deployed_commit recorded.
func (w *Worker) Reconcile(ctx context.Context) error {
	deployed, err := w.d.Meta.Get(ctx, deployedCommitKey)
	if err != nil {
		return fmt.Errorf("read deployed commit: %w", err)
	}
	st, err := w.d.Client.Status(ctx)
	if err != nil {
		return fmt.Errorf("builder status: %w", err)
	}
	// The builder publishes available_commit only after a successful test
	// build; empty means "nothing deployable yet". An empty deployed
	// commit (first run, or migrated install) means "deploy whatever is
	// available" — no seeding step needed.
	if st.AvailableCommit == "" {
		w.d.Logger.Debug("builder has no available commit yet")
		return nil
	}
	if deployed == st.AvailableCommit {
		return nil
	}
	w.d.Logger.Info("new frontend version available",
		slog.String("available", st.AvailableCommit),
		slog.String("deployed", deployed))

	ref, err := w.d.Client.StartBuild(ctx, w.d.Domain)
	if err != nil {
		return fmt.Errorf("order build: %w", err)
	}
	w.d.Logger.Info("build ordered", slog.String("id", ref.BuildID), slog.Bool("cached", ref.Cached))

	b, err := w.waitBuild(ctx, ref.BuildID)
	if err != nil {
		return err
	}
	if b.Status != BuildSuccess {
		return fmt.Errorf("build %s failed: %s", b.ID, b.Error)
	}

	if err := w.deploy(ctx, ref.BuildID, b.Commit); err != nil {
		return err
	}
	if err := w.d.Meta.Set(ctx, deployedCommitKey, b.Commit); err != nil {
		return fmt.Errorf("record deployed commit: %w", err)
	}
	w.d.Logger.Info("frontend deployed", slog.String("commit", b.Commit))
	return nil
}

// waitBuild polls the build status until a terminal state, the deadline
// or ctx cancellation. A vanished build (builder restarted, history
// pruned) surfaces as an error; the next reconcile re-orders the build.
func (w *Worker) waitBuild(ctx context.Context, id string) (Build, error) {
	deadline := time.Now().Add(buildTimeout)
	for {
		b, err := w.d.Client.Build(ctx, id)
		if err != nil {
			return Build{}, fmt.Errorf("build %s status: %w", id, err)
		}
		switch b.Status {
		case BuildSuccess, BuildFailed:
			return b, nil
		case BuildQueued, BuildRunning:
		default:
			return Build{}, fmt.Errorf("build %s: unknown status %q", id, b.Status)
		}
		if time.Now().After(deadline) {
			return Build{}, fmt.Errorf("build %s: timed out after %s", id, buildTimeout)
		}
		select {
		case <-ctx.Done():
			return Build{}, ctx.Err()
		case <-time.After(buildPollInterval):
		}
	}
}

// deploy downloads the archive, extracts it and swaps the symlink.
// Every step leaves the previous frontend serving on failure.
func (w *Worker) deploy(ctx context.Context, buildID, commit string) error {
	dctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	archive := filepath.Join(w.d.Deployer.Root, ".archive-"+buildID+".tar.gz")
	if err := w.d.Client.DownloadArchive(dctx, buildID, archive); err != nil {
		return fmt.Errorf("download archive: %w", err)
	}
	defer os.Remove(archive)

	if err := w.d.Deployer.Extract(archive, commit); err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	if err := w.d.Deployer.Swap(commit); err != nil {
		return fmt.Errorf("swap: %w", err)
	}
	if err := w.d.Deployer.Prune(); err != nil {
		// Non-fatal: stale versions only consume disk.
		w.d.Logger.Warn("prune versions", slog.Any("error", err))
	}
	return nil
}
```

Примечание: `errors` импорт здесь не нужен — убрать его, если линтер заметит (в этом файле не используется).

- [ ] **Step 4: Запустить тесты пакета**

Run: `go test ./internal/builder/ -v`
Expected: все PASS (waitBuild в тестах проходит мгновенно: fake всегда отвечает terminal status).

- [ ] **Step 5: Commit**

```bash
git add internal/builder/
git commit -m "feat: builder update worker"
```

---

### Task 8: Worker — краевые случаи

**Files:**
- Modify: `internal/builder/worker_test.go`

- [ ] **Step 1: failing-тесты (на самом деле они могут сразу пройти — но фиксируют контракт; прогнать обязательно)**

Добавить в `internal/builder/worker_test.go`:

```go
func TestReconcileNoAvailableCommit(t *testing.T) {
	f := &fakeBuilder{available: "", buildStatus: BuildSuccess, t: t}
	w, d, meta := newTestWorker(t, f)

	if err := w.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, err := os.Lstat(d.StaticDir); err == nil {
		t.Error("nothing must be deployed when available_commit is empty")
	}
	if got, _ := meta.Get(context.Background(), deployedCommitKey); got != "" {
		t.Errorf("deployed_commit = %q, want empty", got)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.starts != 0 {
		t.Errorf("starts = %d, want 0", f.starts)
	}
}

func TestReconcileFailedBuildLeavesState(t *testing.T) {
	f := &fakeBuilder{available: "aaa", buildStatus: BuildFailed, t: t}
	w, d, meta := newTestWorker(t, f)

	err := w.Reconcile(context.Background())
	if err == nil {
		t.Fatal("expected error for failed build")
	}
	if _, lstatErr := os.Lstat(d.StaticDir); lstatErr == nil {
		t.Error("nothing must be deployed on build failure")
	}
	if got, _ := meta.Get(context.Background(), deployedCommitKey); got != "" {
		t.Errorf("deployed_commit = %q, want empty", got)
	}
}

func TestReconcileBusyRetriesNextTick(t *testing.T) {
	f := &fakeBuilder{available: "aaa", buildStatus: BuildSuccess, buildErrCode: http.StatusConflict, t: t}
	w, d, meta := newTestWorker(t, f)

	err := w.Reconcile(context.Background())
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	if _, lstatErr := os.Lstat(d.StaticDir); lstatErr == nil {
		t.Error("nothing must be deployed when builder is busy")
	}
	if got, _ := meta.Get(context.Background(), deployedCommitKey); got != "" {
		t.Errorf("deployed_commit = %q, want empty", got)
	}
	// Builder frees up; the next tick succeeds.
	f.mu.Lock()
	f.buildErrCode = 0
	f.mu.Unlock()
	if err := w.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile after busy: %v", err)
	}
	got, _ := meta.Get(context.Background(), deployedCommitKey)
	if got != "aaa" {
		t.Errorf("deployed_commit = %q, want aaa", got)
	}
}
```

Импорты дополнить: `"errors"` (если нет) — `net/http` уже есть.

- [ ] **Step 2: Запустить тесты пакета**

Run: `go test ./internal/builder/ -v`
Expected: все PASS. Если что-то красное — это баг реализации Task 7, чинить до зелёного.

- [ ] **Step 3: Commit**

```bash
git add internal/builder/
git commit -m "test: worker edge cases (no version, failed build, busy)"
```

---

### Task 9: Отдача статики переживает symlink-swap (доказательство для #7)

**Files:**
- Modify: `internal/web/web_test.go`

- [ ] **Step 1: тест в `internal/web/web_test.go`**

```go
// The builder integration swaps the frontend by renaming a symlink that
// static_dir points at. os.DirFS must re-resolve it on every request so
// the swap is atomic for clients (issue #7).
func TestHandlerFollowsSymlinkSwap(t *testing.T) {
	root := t.TempDir()
	write := func(version, body string) {
		dir := filepath.Join(root, "versions", version)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("aaa", "<html>version-aaa</html>")
	write("bbb", "<html>version-bbb</html>")

	current := filepath.Join(root, "current")
	if err := os.Symlink(filepath.Join("versions", "aaa"), current); err != nil {
		t.Fatal(err)
	}
	handler := New(current)

	get := func() string {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		return rec.Body.String()
	}

	if got := get(); got != "<html>version-aaa</html>" {
		t.Fatalf("before swap = %q", got)
	}

	// Atomic swap, same technique as Deployer.Swap.
	tmp := filepath.Join(root, ".swap-tmp")
	if err := os.Symlink(filepath.Join("versions", "bbb"), tmp); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, current); err != nil {
		t.Fatal(err)
	}

	if got := get(); got != "<html>version-bbb</html>" {
		t.Fatalf("after swap = %q, want version-bbb", got)
	}
}
```

- [ ] **Step 2: Запустить**

Run: `go test ./internal/web/ -run TestHandlerFollowsSymlinkSwap -v`
Expected: PASS без изменений в `web.go` — это и есть доказательство (если упало, предположение `os.DirFS` неверно и дизайн надо пересматривать).

- [ ] **Step 3: Commit**

```bash
git add internal/web/web_test.go
git commit -m "test: static serving follows atomic symlink swap"
```

---

### Task 10: main.go — сборка всего вместе

**Files:**
- Modify: `cmd/lampa-go/main.go`

- [ ] **Step 1: правки `cmd/lampa-go/main.go`**

Импорты дополнить:

```go
	"lampa-go/internal/builder"
```

Блок подготовки статики (сейчас):

```go
	if err := os.MkdirAll(cfg.Server.StaticDir, 0o755); err != nil {
		return fmt.Errorf("prepare static dir: %w", err)
	}
```

заменить на:

```go
	var deployer *builder.Deployer
	if cfg.Builder.Enabled {
		// With the builder, static_dir is the symlink path; the real
		// directories are <root>/versions/<commit> and the symlink appears
		// on the first deploy.
		d, err := builder.NewDeployer(cfg.Server.StaticDir, cfg.Builder.KeepVersions)
		if err != nil {
			return err
		}
		if err := d.EnsureDirs(); err != nil {
			return fmt.Errorf("prepare static dirs: %w", err)
		}
		deployer = d
	} else if err := os.MkdirAll(cfg.Server.StaticDir, 0o755); err != nil {
		return fmt.Errorf("prepare static dir: %w", err)
	}
```

После успешного `server.New(...)` (перед запуском `errCh`-горутины) добавить:

```go
	if cfg.Builder.Enabled {
		client, err := builder.NewClient(cfg.Builder.URL, cfg.Builder.APIKey)
		if err != nil {
			return fmt.Errorf("builder client: %w", err)
		}
		meta, err := storage.NewMetaStore(db, cfg.DB.Driver)
		if err != nil {
			return err
		}
		worker := builder.NewWorker(builder.Deps{
			Client:   client,
			Deployer: deployer,
			Meta:     meta,
			Domain:   cfg.Server.BaseDomain,
			Interval: cfg.Builder.PollInterval.Std(),
			Logger:   core.Logger,
		})
		go worker.Run(ctx)
		core.Logger.Info("builder integration enabled",
			slog.String("url", cfg.Builder.URL),
			slog.Duration("interval", cfg.Builder.PollInterval.Std()))
	}
```

- [ ] **Step 2: Сборка и тесты всего репозитория**

Run: `go build ./... && go test ./... && go vet ./...`
Expected: всё зелёное. (`server_test.go` не затронут: дефолтный конфиг — builder disabled.)

- [ ] **Step 3: Ручная проверка негативного пути (опционально, но полезно)**

Run: `go run ./cmd/lampa-go -config /tmp/bad.yaml` после создания `/tmp/bad.yaml`:

```yaml
builder:
  enabled: true
```

Expected: ошибка валидации `builder.api_key is required...`, процесс не стартует.

- [ ] **Step 4: Commit**

```bash
git add cmd/lampa-go/main.go
git commit -m "feat: wire builder worker into main"
```

---

### Task 11: config.example.yaml, compose, .env.example

**Files:**
- Modify: `config.example.yaml`
- Create: `docker-compose.yml`
- Create: `.env.example`

- [ ] **Step 1: секция в `config.example.yaml`** — добавить после `cub:`-блока:

```yaml
builder:
  enabled: false             # enable the lampa-web-builder update worker
  url: http://builder:8080   # builder API (compose service name; loopback: http://127.0.0.1:8081)
  api_key: change-me         # X-API-Key for the builder (required when enabled)
  poll_interval: 5m          # how often to compare available_commit with ours
  keep_versions: 3           # extracted versions to keep under <static_dir>/../versions
```

и поправить комментарий над `static_dir`:

```yaml
  static_dir: ./deploy/web   # built frontend; with builder.enabled use ./deploy/web/current (a symlink path)
```

- [ ] **Step 2: `docker-compose.yml` в корне репозитория**

```yaml
# The lampa-web-builder side of the deployment (spec §12 of the builder repo).
# lampa-go itself is containerized separately; until then it reaches the
# builder via the published loopback port (commented out below) or, once
# containerized, via the compose network service name (builder.url: http://builder:8080).
services:
  builder:
    image: ghcr.io/huhen/lampa-web-builder:latest
    restart: unless-stopped
    volumes:
      - builder-data:/data
    environment:
      LISTEN: ":8080"
      API_KEY: ${BUILDER_API_KEY:?set BUILDER_API_KEY in .env}
      DEFAULT_DOMAIN: ${BUILDER_DEFAULT_DOMAIN:?set BUILDER_DEFAULT_DOMAIN in .env}
      POLL_INTERVAL: "6h"
    # Until lampa-go runs in this compose network, publish on loopback only:
    # ports:
    #   - "127.0.0.1:8081:8080"
volumes:
  builder-data:
```

- [ ] **Step 3: `.env.example`**

```bash
# cp .env.example .env and fill in
BUILDER_API_KEY=change-me-long-random-string
BUILDER_DEFAULT_DOMAIN=lampa.example.com
```

- [ ] **Step 4: Проверка compose-конфига (если установлен docker)**

Run: `docker compose config -q && echo OK`
Expected: `OK` (переменные берутся из .env; для проверки можно `BUILDER_API_KEY=x BUILDER_DEFAULT_DOMAIN=d docker compose config -q`).

- [ ] **Step 5: Commit**

```bash
git add config.example.yaml docker-compose.yml .env.example
git commit -m "build: builder compose service and config example"
```

---

### Task 12: Документация

**Files:**
- Modify: `docs/configuration.md`
- Modify: `docs/deploy.md`

Примечание: переписывание `docs/architecture.md`/`docs/frontend-update.md` (описание старого пайплайна) — задача issue #10, здесь их не трогаем.

- [ ] **Step 1: `docs/configuration.md` — секция `### builder`** после секции `### cub`:

```markdown
### builder

Интеграция с [lampa-web-builder](https://github.com/huhen/lampa-web-builder): lampa-go сам
сверяет свой задеплоенный коммит фронта с `available_commit` билдера, заказывает сборку,
скачивает архив и атомарно выкладывает его (symlink-swap).

| Ключ | По умолчанию | Значение |
|---|---|---|
| `enabled` | `false` | включить воркер обновления фронта |
| `url` | `http://builder:8080` | адрес API билдера; в compose — имя сервиса |
| `api_key` | — | ключ `X-API-Key`; обязателен при `enabled: true` |
| `poll_interval` | `5m` | как часто сверяться с билдером |
| `keep_versions` | `3` | сколько версий хранить в `<корень static_dir>/versions/` |

При `enabled: true` обязательны `server.base_domain` — им становится домен заказываемой
сборки. `server.static_dir` должен указывать на путь **symlink'а** (например,
`./deploy/web/current`): билдер-воркер кладёт версии в `<корень>/versions/<commit>/` и
атомарно переименовывает symlink; если `static_dir` — обычный каталог, старт падает с
подсказкой (см. deploy.md про миграцию).
```

- [ ] **Step 2: `docs/deploy.md` — раздел «Обновление фронта через билдер»** в конец файла:

```markdown
## Обновление фронта через билдер

При `builder.enabled: true` фронтенд обновляется автоматически из
[lampa-web-builder](https://github.com/huhen/lampa-web-builder): воркер lampa-go сверяет
`deployed_commit` (таблица `app_meta`) с `available_commit` билдера, при отличии заказывает
сборку домена `server.base_domain`, скачивает tar.gz и выкладывает атомарно.

Раскладка каталогов (`<корень>` — каталог, в котором лежит `static_dir`):

    <корень>/versions/<commit>/   распакованные сборки
    <корень>/current              symlink → versions/<commit>; путь из server.static_dir

Подмена — rename свежесозданного symlink'а поверх `current`: атомарная операция, окно 404
отсутствует (закрывает issue #7). Хранятся последние `builder.keep_versions` версий.

### compose

`docker-compose.yml` в корне репозитория поднимает билдер; ключи — в `.env`
(шаблон `.env.example`). Пока lampa-go работает бинарником на хосте, раскомментируйте
loopback-публикацию порта и укажите `builder.url: http://127.0.0.1:8081`; после
контейнеризации lampa-go — `builder.url: http://builder:8080` (общая сеть compose,
порты наружу не публикуются).

### Миграция с плоской раскладки

Старая раскладка (файлы прямо в `static_dir`) с билдером не работает — `static_dir` должен
указывать на symlink. Переезд:

1. Остановить lampa-go.
2. Перенести текущую статику в версионный каталог (`<commit>` — версия текущей сборки,
   например `b4a13b6…`; если неизвестна — любой маркер, первый цикл воркера всё равно
   задеплоит свежую версию):

   ```bash
   cd deploy/web
   mkdir -p versions/<commit>
   # всё верхнего уровня, кроме versions/, уезжает в версионный каталог
   find . -mindepth 1 -maxdepth 1 ! -name versions -exec mv {} versions/<commit>/ \;
   ln -s versions/<commit> .current-tmp && mv -T .current-tmp current
   ```

3. В конфиге: `server.static_dir: ./deploy/web/current`, секция `builder`.
4. Запустить lampa-go. Пустой `deployed_commit` означает «обновиться до доступного»:
   первый цикл воркера сам задеплоит актуальную версию из билдера — ручной сеанс коммита
   в БД не нужен.

Проще альтернатива, если старая статика не дорога: удалить `deploy/web` целиком, указать
`static_dir: ./deploy/web/current` и запустить lampa-go — воркер развернёт фронт с нуля.
```

- [ ] **Step 3: Проверить команды миграции из шага 2 в песочнице**

Run (в `/tmp`): воспроизвести раскладку `web/index.html`, `web/app.js`, выполнить блок из шага 2 с `<commit>=test`, убедиться, что `web/current/index.html` читается, `versions/test/` содержит оба файла, а на верхнем уровне `web/` остались только `versions/` и `current`.

Ожидание: команды рабочие как есть; при расхождении — починить блок в доке до зелёного результата.

- [ ] **Step 4: Commit**

```bash
git add docs/configuration.md docs/deploy.md
git commit -m "docs: builder integration config and deployment"
```

---

### Task 13: Финал — проверка, issue, PR

**Files:** — (git + GitHub)

- [ ] **Step 1: Полная проверка**

Run: `make build test vet && ./scripts/smoke.sh`
Expected: сборка ок, тесты ок, vet молчит, smoke проходит (замечание из памяти окружения: smoke требует systemd-resolved для `tmdb.localhost`; на этой машине доступен).

- [ ] **Step 2: Отметить отклонение от сеанса в issue #9**

```bash
gh issue comment 9 --body "Отклонение от текста issue: сеанс deployed_commit из frontend/ORIGIN_COMMIT заменён самовосстановлением — пустой deployed_commit трактуется как «обновиться до available_commit». Первое включение делает один лишний rebuild+deploy, зато не нужен ручной шаг и не ломается удаление frontend/ (#10). Реализация: feature/builder-integration."
```

- [ ] **Step 3: Push и PR**

```bash
git push -u origin feature/builder-integration
gh pr create --base feature/first-iteration \
  --title "feat: builder integration (auto frontend updates from lampa-web-builder)" \
  --body "Реализация huhen/lampa-go#9. Пакет internal/builder (клиент API, безопасная распаковка, symlink-swap деплой, воркер), секция builder в конфиге, app_meta key-value, compose + документация. Закрывает #7; #3 закрывается совместно с билдером. Base — feature/first-iteration (стек на PR #1), после его мержа перетаргетировать на main."
```

- [ ] **Step 4: Контроль ревью**

Дождаться ревью (coderabbitai), адресовать замечания отдельными коммитами; после мержа PR #1 — `gh pr edit --base main`.

---

## Self-review плана

- **Покрытие spec §13:** конфиг (`builder.*`) — Task 2; воркер-цикл (status → compare → order → poll → archive → swap → record) — Tasks 4, 7, 8; symlink-swap и атомарность — Tasks 6, 9; `deployed_commit` в БД — Tasks 3, 7; ретеншн 2–3 версии — Task 6 (`keep_versions`); статика без изменений (`os.DirFS`) — Task 9; compose — Task 11; документация — Task 12. Сеанс `deployed_commit` заменён самовосстановлением — отражено в шапке и Task 13.
- **Placeholder-скан:** все шаги содержат полный код/команды; «проверить в песочнице» в Task 12 — действие с критерием, не заглушка.
- **Согласованность типов:** `Deployer{StaticDir, Root, Keep}` и `NewDeployer(staticDir, keep)`; `Worker`/`Deps{Client, Deployer, Meta, Domain, Interval, Logger}`; `MetaStore.Get/Set`; статусы `BuildSuccess/BuildFailed/BuildQueued/BuildRunning`; `deployedCommitKey` — имена совпадают между тестами и реализацией. Хелпер чтения файла в тестах деплоера переименован при появлении второго (`readVersionFile` vs `readAndServe`) — конфликт исключён шагом в Task 6.

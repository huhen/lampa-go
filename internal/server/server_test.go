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

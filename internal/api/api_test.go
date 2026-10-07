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

	t.Run("checker post body without equals", func(t *testing.T) {
		rec := do(t, mux, http.MethodPost, "/cub/api/checker",
			"justtoken", "application/x-www-form-urlencoded")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("checker post no equals: %d, want 400", rec.Code)
		}
	})

	t.Run("checker post oversize body", func(t *testing.T) {
		rec := do(t, mux, http.MethodPost, "/cub/api/checker",
			strings.Repeat("x", (1<<20)+1), "application/x-www-form-urlencoded")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("checker post oversize: %d, want 400", rec.Code)
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

	// modification.js turns geo.<our-domain>/ into /cub/geo/ (trailing
	// slash); the stub must answer that too instead of letting the request
	// fall through to the cub proxy.
	t.Run("geo fallback trailing slash", func(t *testing.T) {
		rec := do(t, mux, http.MethodGet, "/cub/geo/", "", "")
		if rec.Code != http.StatusOK || rec.Body.String() != "US" {
			t.Fatalf("geo fallback trailing slash: %d %q", rec.Code, rec.Body.String())
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

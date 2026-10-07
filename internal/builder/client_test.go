package builder

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestClientDownloadArchive(t *testing.T) {
	c := newFakeBuilder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/builds/b-1/archive" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Write([]byte("archive-bytes"))
	}))
	dest := filepath.Join(t.TempDir(), "bundle.tar.gz")
	if err := c.DownloadArchive(context.Background(), "b-1", dest); err != nil {
		t.Fatalf("DownloadArchive: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != "archive-bytes" {
		t.Errorf("content = %q, want archive-bytes", got)
	}
}

func TestClientDownloadArchiveCleanup(t *testing.T) {
	c := newFakeBuilder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ab"))
		panic(http.ErrAbortHandler) // drop the connection mid-body
	}))
	dir := t.TempDir()
	dest := filepath.Join(dir, "bundle.tar.gz")
	if err := c.DownloadArchive(context.Background(), "b-1", dest); err == nil {
		t.Fatal("expected error for aborted body")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".archive-") {
			t.Errorf("leftover temp archive: %s", e.Name())
		}
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
			w.Write([]byte(`{"error": "fake reason"}`))
		}))
		_, err := c.StartBuild(context.Background(), "d")
		if !errors.Is(err, tc.want) {
			t.Errorf("code %d: err = %v, want %v", tc.code, err, tc.want)
		}
		if !strings.Contains(err.Error(), "fake reason") {
			t.Errorf("code %d: error lost builder detail: %v", tc.code, err)
		}
	}
}

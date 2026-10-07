package builder

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"lampa-go/internal/config"
	"lampa-go/internal/storage"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

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

func TestPruneSweepsStaleArchive(t *testing.T) {
	d := newTestDeployer(t)
	stale := filepath.Join(d.Root, ".archive-old.tar.gz")
	if err := os.WriteFile(stale, []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := d.Prune(); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if _, err := os.Lstat(stale); err == nil {
		t.Error("Prune must sweep stale .archive- files in Root")
	}
}

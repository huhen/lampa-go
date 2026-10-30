package builder

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
	badBuildID   string // when set, POST /builds returns it as build_id
	builtCommit  string // when set, GET /builds/{id} reports it as commit
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
			id := "b-1"
			if f.badBuildID != "" {
				id = f.badBuildID
			}
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"build_id":"` + id + `","cached":false}`))
		case r.URL.Path == "/api/v1/builds/b-1":
			commit := f.available
			if f.builtCommit != "" {
				commit = f.builtCommit
			}
			w.Write([]byte(`{"id":"b-1","commit":"` + commit + `","status":"` + f.buildStatus + `"}`))
		case r.URL.Path == "/api/v1/builds/b-1/archive":
			archive := filepath.Join(f.t.TempDir(), "a.tar.gz")
			if err := f.writeArchive(archive); err != nil {
				// t.Fatalf must not be called from a non-test goroutine;
				// report and fail the request instead.
				f.t.Errorf("create archive: %v", err)
				http.Error(w, "fixture failure", http.StatusInternalServerError)
				return
			}
			http.ServeFile(w, r, archive)
		default:
			http.NotFound(w, r)
		}
	})
}

func (f *fakeBuilder) writeArchive(dest string) error {
	f2, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f2.Close()
	gz := gzip.NewWriter(f2)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	content := "index for " + f.available
	tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "index.html", Size: int64(len(content)), Mode: 0o644})
	tw.Write([]byte(content))
	return nil
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
	w, err := NewWorker(Deps{
		Client:   client,
		Deployer: d,
		Meta:     meta,
		Domain:   "lampa.example.com",
		Interval: time.Minute,
		Logger:   testLogger(),
	})
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}
	return w, d, meta
}

// TestNewWorkerValidation covers the interval guard: a non-positive cadence
// would make Run's ticker panic, so it must be rejected at construction.
func TestNewWorkerValidation(t *testing.T) {
	if _, err := NewWorker(Deps{Interval: 0}); err == nil {
		t.Error("NewWorker with zero Interval must fail")
	}
	if _, err := NewWorker(Deps{Interval: -time.Minute}); err == nil {
		t.Error("NewWorker with negative Interval must fail")
	}
	if _, err := NewWorker(Deps{Interval: time.Minute}); err != nil {
		t.Errorf("NewWorker with valid Interval: %v", err)
	}
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

func TestReconcileRejectsBadBuildID(t *testing.T) {
	f := &fakeBuilder{
		available:   "aaa",
		buildStatus: BuildSuccess,
		badBuildID:  "../evil",
		t:           t,
	}
	w, d, meta := newTestWorker(t, f)

	err := w.Reconcile(context.Background())
	if err == nil {
		t.Fatal("Reconcile must reject a crafted build id")
	}
	if !strings.Contains(err.Error(), "bad build id") {
		t.Fatalf("error = %v, want it to mention bad build id", err)
	}
	if _, serr := os.Lstat(d.StaticDir); serr == nil {
		t.Error("nothing must be deployed for a bad build id")
	}
	got, gerr := meta.Get(context.Background(), deployedCommitKey)
	if gerr != nil || got != "" {
		t.Errorf("deployed_commit = (%q, %v), want empty", got, gerr)
	}
}

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

// TestWorkerRunFirstReconcile covers Run's startup behavior: the first
// reconcile happens immediately, not after a full interval (a fresh install
// would otherwise serve nothing for up to builder.poll_interval), and Run
// exits once the context is canceled.
func TestWorkerRunFirstReconcile(t *testing.T) {
	f := &fakeBuilder{available: "aaa", buildStatus: BuildSuccess, t: t}
	w, d, meta := newTestWorker(t, f) // Interval = 1m: only the startup reconcile can deploy

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		got, err := meta.Get(context.Background(), deployedCommitKey)
		if err == nil && got == "aaa" {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("no startup reconcile: deployed_commit = (%q, %v)", got, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if b, err := os.ReadFile(filepath.Join(d.StaticDir, "index.html")); err != nil || string(b) != "index for aaa" {
		t.Errorf("served = (%q, %v), want index for aaa", b, err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("Run did not return after ctx cancel")
	}
}

// TestReconcileRejectsCommitMismatch: a builder reporting a build commit
// other than its advertised available_commit must not be deployed — the
// mismatch would never converge (deployed ≠ available → rebuild forever).
func TestReconcileRejectsCommitMismatch(t *testing.T) {
	f := &fakeBuilder{
		available:   "aaa",
		buildStatus: BuildSuccess,
		builtCommit: "ccc",
		t:           t,
	}
	w, d, meta := newTestWorker(t, f)

	err := w.Reconcile(context.Background())
	if err == nil {
		t.Fatal("Reconcile must reject a build whose commit differs from available_commit")
	}
	if !strings.Contains(err.Error(), "but available is") {
		t.Fatalf("error = %v, want commit mismatch detail", err)
	}
	if _, serr := os.Lstat(d.StaticDir); serr == nil {
		t.Error("nothing must be deployed on commit mismatch")
	}
	if got, _ := meta.Get(context.Background(), deployedCommitKey); got != "" {
		t.Errorf("deployed_commit = %q, want empty", got)
	}
}

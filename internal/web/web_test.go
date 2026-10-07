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
		"index.html":    "<html>lampa-index</html>",
		"app.min.js":    "// app",
		"assembly.json": "{}",
		"css/app.css":   "body{}",
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
		name      string
		path      string
		wantCode  int
		wantBody  string
		wantCache string
	}{
		{"root serves index", "/", http.StatusOK, "<html>lampa-index</html>", "no-cache"},
		{"index.html no-cache", "/index.html", http.StatusOK, "<html>lampa-index</html>", "no-cache"},
		{"index rewrite keeps query", "/index.html?v=1", http.StatusOK, "<html>lampa-index</html>", "no-cache"},
		{"assembly no-cache", "/assembly.json", http.StatusOK, "{}", "no-cache"},
		{"app.js long cache", "/app.min.js", http.StatusOK, "// app", "public, max-age=86400"},
		{"nested file", "/css/app.css", http.StatusOK, "body{}", "public, max-age=86400"},
		{"dir not listed", "/css/", http.StatusNotFound, "", ""},
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

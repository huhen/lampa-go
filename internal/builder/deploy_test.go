package builder

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func readVersionFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Join(dir, name), err)
	}
	return string(b)
}

// pinMtimes sets distinct deterministic mtimes on version dirs so Prune's
// retention order cannot tie on filesystem timestamp granularity.
func pinMtimes(t *testing.T, versionsDir string, offsets map[string]time.Duration) {
	t.Helper()
	base := time.Now()
	for name, off := range offsets {
		when := base.Add(off)
		if err := os.Chtimes(filepath.Join(versionsDir, name), when, when); err != nil {
			t.Fatal(err)
		}
	}
}

func TestExtract(t *testing.T) {
	d := newTestDeployer(t)
	archive := filepath.Join(t.TempDir(), "a.tar.gz")
	writeArchive(t, archive, map[string]string{
		"index.html":     "<html>v1</html>",
		"css/app.css":    "body{}",
		"plugins/mod.js": "// mod",
	})

	if err := d.Extract(archive, "aaa"); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	// Files land in the version dir, archive root == deploy root (no wrapper).
	versionDir := filepath.Join(d.Root, "versions", "aaa")
	if got := readVersionFile(t, versionDir, "index.html"); got != "<html>v1</html>" {
		t.Errorf("index.html = %q", got)
	}
	if got := readVersionFile(t, versionDir, filepath.Join("css", "app.css")); got != "body{}" {
		t.Errorf("app.css = %q", got)
	}
	if got := readVersionFile(t, versionDir, filepath.Join("plugins", "mod.js")); got != "// mod" {
		t.Errorf("mod.js = %q", got)
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

// snapshotDir captures every file under dir (slash-relative path → content)
// so tests can assert the tree is unchanged.
func snapshotDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if e.IsDir() {
			out[rel+"/"] = ""
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", dir, err)
	}
	return out
}

func TestExtractRejectsBadCommit(t *testing.T) {
	for _, commit := range []string{"", ".", "..", "a/b", `a\b`} {
		t.Run(fmt.Sprintf("commit=%q", commit), func(t *testing.T) {
			d := newTestDeployer(t)
			archive := filepath.Join(t.TempDir(), "a.tar.gz")
			writeArchive(t, archive, map[string]string{"index.html": "<html>v1</html>"})
			if err := d.Extract(archive, "aaa"); err != nil {
				t.Fatalf("seed Extract(aaa): %v", err)
			}
			versionsDir := filepath.Join(d.Root, "versions")
			before := snapshotDir(t, versionsDir)

			if err := d.Extract(archive, commit); err == nil {
				t.Fatal("expected error for invalid commit")
			}
			if after := snapshotDir(t, versionsDir); !maps.Equal(before, after) {
				t.Errorf("versions dir changed: before %v, after %v", before, after)
			}
		})
	}
}

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
	if got := readVersionFile(t, d.StaticDir, "index.html"); got != "ccc" {
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

	// Pin distinct mtimes: equal timestamps could tie and flip the retention
	// order (aaa must be the oldest for the count assertion to hold).
	pinMtimes(t, filepath.Join(d.Root, "versions"), map[string]time.Duration{
		"aaa": -3 * time.Hour,
		"bbb": -2 * time.Hour,
		"ccc": -time.Hour,
		"ddd": 0,
	})

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

// TestPruneProtectsServedOldest covers the served-guard branch: the currently
// served version survives Prune even when it is the oldest one and falls
// outside the Keep window.
func TestPruneProtectsServedOldest(t *testing.T) {
	d := newTestDeployer(t) // Keep = 3
	ctx := t.TempDir()
	for _, commit := range []string{"aaa", "bbb", "ccc", "ddd"} {
		archive := filepath.Join(ctx, commit+".tar.gz")
		writeArchive(t, archive, map[string]string{"index.html": commit})
		if err := d.Extract(archive, commit); err != nil {
			t.Fatalf("Extract %s: %v", commit, err)
		}
	}
	pinMtimes(t, filepath.Join(d.Root, "versions"), map[string]time.Duration{
		"aaa": -3 * time.Hour, // oldest: retention alone would drop it
		"bbb": -2 * time.Hour,
		"ccc": -time.Hour,
		"ddd": 0,
	})
	if err := d.Swap("aaa"); err != nil {
		t.Fatalf("Swap: %v", err)
	}
	if err := d.Prune(); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(d.Root, "versions"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Errorf("versions after prune = %d, want 4 (retention keeps 3, served aaa is protected)", len(entries))
	}
	got := map[string]bool{}
	for _, e := range entries {
		got[e.Name()] = true
	}
	for _, want := range []string{"aaa", "bbb", "ccc", "ddd"} {
		if !got[want] {
			t.Errorf("served version %s missing after prune, versions = %v", want, got)
		}
	}
	if got := readVersionFile(t, d.StaticDir, "index.html"); got != "aaa" {
		t.Errorf("served = %q, want aaa", got)
	}
}

func TestSwapRejectsBadCommit(t *testing.T) {
	d := newTestDeployer(t)
	for _, bad := range []string{"", ".", "..", "a/b"} {
		if err := d.Swap(bad); err == nil {
			t.Errorf("Swap(%q) must fail", bad)
		}
	}
	if _, err := os.Lstat(d.StaticDir); err == nil {
		t.Error("Swap with a bad commit must not create the symlink")
	}
}

func TestSwapLeavesNoTempAndPruneSweepsRoot(t *testing.T) {
	d := newTestDeployer(t)
	archive := filepath.Join(t.TempDir(), "a.tar.gz")
	writeArchive(t, archive, map[string]string{"index.html": "aaa"})
	if err := d.Extract(archive, "aaa"); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if err := d.Swap("aaa"); err != nil {
		t.Fatalf("Swap: %v", err)
	}
	// No .swap- leftovers anywhere after a successful swap.
	for _, dir := range []string{d.Root, filepath.Join(d.Root, "versions")} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".swap-") {
				t.Errorf("%s leftover in %s", e.Name(), dir)
			}
		}
	}
	// A stale .swap- file in Root (crash leftover) is swept by Prune.
	stale := filepath.Join(d.Root, ".swap-stale")
	if err := os.Symlink("versions/aaa", stale); err != nil {
		t.Fatal(err)
	}
	if err := d.Prune(); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if _, err := os.Lstat(stale); err == nil {
		t.Error("Prune must sweep stale .swap- entries in Root")
	}
}

// TestPruneSweepsStaleArchive: the worker downloads archives into Root as
// .archive-<buildID>.tar.gz; a crash mid-download would leave one there
// forever, so Prune's Root sweep must cover the prefix too.
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

func TestNewDeployerValidation(t *testing.T) {
	t.Run("real directory under static_dir", func(t *testing.T) {
		staticDir := filepath.Join(t.TempDir(), "static")
		if err := os.MkdirAll(staticDir, 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := NewDeployer(staticDir, 3)
		if err == nil {
			t.Fatal("expected error for real directory static_dir")
		}
		if !strings.Contains(err.Error(), "docs/deploy.md") {
			t.Errorf("error should reference docs/deploy.md, got: %v", err)
		}
	})
	t.Run("keep too small", func(t *testing.T) {
		if _, err := NewDeployer(filepath.Join(t.TempDir(), "current"), 1); err == nil {
			t.Fatal("expected error for keep=1")
		}
	})
	t.Run("missing path is fine", func(t *testing.T) {
		staticDir := filepath.Join(t.TempDir(), "current")
		d, err := NewDeployer(staticDir, 3)
		if err != nil {
			t.Fatalf("NewDeployer: %v", err)
		}
		if d.Root != filepath.Dir(staticDir) {
			t.Errorf("Root = %q, want %q", d.Root, filepath.Dir(staticDir))
		}
	})
}

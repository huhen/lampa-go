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

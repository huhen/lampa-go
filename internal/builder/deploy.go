package builder

import (
	"archive/tar"
	"compress/gzip"
	"errors"
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
		if fi.IsDir() {
			return nil, fmt.Errorf(
				"static_dir %q is a real directory; with builder enabled it must be the symlink path (e.g. <root>/current), see docs/deploy.md",
				staticDir)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("stat static_dir: %w", err)
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
	if err := validVersionName(commit); err != nil {
		return err
	}
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

// validVersionName rejects commit values that are unsafe as a single
// path component under versions/.
func validVersionName(commit string) error {
	if commit == "" || commit == "." || commit == ".." ||
		strings.ContainsAny(commit, "/\\") {
		return fmt.Errorf("invalid version name %q", commit)
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

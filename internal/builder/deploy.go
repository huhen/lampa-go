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

// Swap atomically repoints StaticDir at versions/<commit>: a temp symlink
// is created next to it and renamed over it. A rename replaces an existing
// symlink (or fills a missing path) in one step — readers never see the
// path absent (issue #7).
func (d *Deployer) Swap(commit string) error {
	if err := validVersionName(commit); err != nil {
		return err
	}
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

	// Sweep .swap- leftovers in Root too: Swap creates its temp symlink
	// there, and a crash between symlink and rename leaves it behind.
	rootEntries, err := os.ReadDir(d.Root)
	if err != nil {
		return fmt.Errorf("read root dir: %w", err)
	}
	for _, e := range rootEntries {
		if strings.HasPrefix(e.Name(), ".swap-") {
			if err := os.RemoveAll(filepath.Join(d.Root, e.Name())); err != nil {
				return fmt.Errorf("prune %s: %w", e.Name(), err)
			}
		}
	}
	return nil
}

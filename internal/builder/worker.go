package builder

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"lampa-go/internal/storage"
)

const (
	// deployedCommitKey is the app_meta key holding the commit currently
	// served from static_dir.
	deployedCommitKey = "builder.deployed_commit"
	// buildPollInterval is how often the build status is polled.
	buildPollInterval = 5 * time.Second
	// buildTimeout caps one build wait; the builder itself times out builds.
	buildTimeout = 30 * time.Minute
	// downloadTimeout caps the archive transfer.
	downloadTimeout = 10 * time.Minute
)

// Worker keeps the served frontend in sync with the builder's
// available_commit. It is not safe for concurrent use; Run is the only
// intended driver.
type Worker struct {
	d Deps
}

// Deps wires the worker.
type Deps struct {
	Client   *Client
	Deployer *Deployer
	Meta     *storage.MetaStore
	Domain   string        // server.base_domain: the build domain
	Interval time.Duration // reconcile cadence
	Logger   *slog.Logger
}

// NewWorker builds a Worker; a nil logger falls back to slog.Default().
func NewWorker(d Deps) *Worker {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Worker{d: d}
}

// Run reconciles on every tick until ctx is done.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.d.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.Reconcile(ctx); err != nil {
				w.d.Logger.Warn("builder reconcile", slog.Any("error", err))
			}
		}
	}
}

// Reconcile performs one check-and-update cycle. Transient problems
// (builder unreachable, busy, queue full) return an error: the next tick
// retries. A successful cycle ends with the frontend swapped and
// deployed_commit recorded.
func (w *Worker) Reconcile(ctx context.Context) error {
	deployed, err := w.d.Meta.Get(ctx, deployedCommitKey)
	if err != nil {
		return fmt.Errorf("read deployed commit: %w", err)
	}
	st, err := w.d.Client.Status(ctx)
	if err != nil {
		return fmt.Errorf("builder status: %w", err)
	}
	// The builder publishes available_commit only after a successful test
	// build; empty means "nothing deployable yet". An empty deployed
	// commit (first run, or migrated install) means "deploy whatever is
	// available" — no seeding step needed.
	if st.AvailableCommit == "" {
		w.d.Logger.Debug("builder has no available commit yet")
		return nil
	}
	if deployed == st.AvailableCommit {
		return nil
	}
	w.d.Logger.Info("new frontend version available",
		slog.String("available", st.AvailableCommit),
		slog.String("deployed", deployed))

	ref, err := w.d.Client.StartBuild(ctx, w.d.Domain)
	if err != nil {
		return fmt.Errorf("order build: %w", err)
	}
	w.d.Logger.Info("build ordered", slog.String("id", ref.BuildID), slog.Bool("cached", ref.Cached))

	b, err := w.waitBuild(ctx, ref.BuildID)
	if err != nil {
		return err
	}
	if b.Status != BuildSuccess {
		return fmt.Errorf("build %s failed: %s", b.ID, b.Error)
	}

	if err := w.deploy(ctx, ref.BuildID, b.Commit); err != nil {
		return err
	}
	if err := w.d.Meta.Set(ctx, deployedCommitKey, b.Commit); err != nil {
		return fmt.Errorf("record deployed commit: %w", err)
	}
	w.d.Logger.Info("frontend deployed", slog.String("commit", b.Commit))
	return nil
}

// waitBuild polls the build status until a terminal state, the deadline
// or ctx cancellation. A vanished build (builder restarted, history
// pruned) surfaces as an error; the next reconcile re-orders the build.
func (w *Worker) waitBuild(ctx context.Context, id string) (Build, error) {
	deadline := time.Now().Add(buildTimeout)
	for {
		b, err := w.d.Client.Build(ctx, id)
		if err != nil {
			return Build{}, fmt.Errorf("build %s status: %w", id, err)
		}
		switch b.Status {
		case BuildSuccess, BuildFailed:
			return b, nil
		case BuildQueued, BuildRunning:
		default:
			return Build{}, fmt.Errorf("build %s: unknown status %q", id, b.Status)
		}
		if time.Now().After(deadline) {
			return Build{}, fmt.Errorf("build %s: timed out after %s", id, buildTimeout)
		}
		select {
		case <-ctx.Done():
			return Build{}, ctx.Err()
		case <-time.After(buildPollInterval):
		}
	}
}

// deploy downloads the archive, extracts it and swaps the symlink.
// Every step leaves the previous frontend serving on failure.
func (w *Worker) deploy(ctx context.Context, buildID, commit string) error {
	dctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	archive := filepath.Join(w.d.Deployer.Root, ".archive-"+buildID+".tar.gz")
	if err := w.d.Client.DownloadArchive(dctx, buildID, archive); err != nil {
		return fmt.Errorf("download archive: %w", err)
	}
	defer os.Remove(archive)

	if err := w.d.Deployer.Extract(archive, commit); err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	if err := w.d.Deployer.Swap(commit); err != nil {
		return fmt.Errorf("swap: %w", err)
	}
	if err := w.d.Deployer.Prune(); err != nil {
		// Non-fatal: stale versions only consume disk.
		w.d.Logger.Warn("prune versions", slog.Any("error", err))
	}
	return nil
}

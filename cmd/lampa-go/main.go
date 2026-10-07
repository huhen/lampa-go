// Command lampa-go serves the Lampa web UI and the cub API skeleton.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"lampa-go/internal/builder"
	"lampa-go/internal/config"
	"lampa-go/internal/obs"
	"lampa-go/internal/server"
	"lampa-go/internal/storage"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "lampa-go:", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "", "path to the YAML config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	core, err := obs.Setup(ctx, cfg.OTel, cfg.Log)
	if err != nil {
		return fmt.Errorf("init observability: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := core.Shutdown(shutdownCtx); err != nil {
			core.Logger.Error("observability shutdown", slog.Any("error", err))
		}
	}()

	core.Logger.Info("starting", slog.String("config", *configPath))

	db, err := storage.Open(cfg.DB)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			core.Logger.Warn("database close", slog.Any("error", err))
		}
	}()

	if err := storage.Migrate(ctx, db, cfg.DB.Driver); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	var deployer *builder.Deployer
	if cfg.Builder.Enabled {
		// With the builder, static_dir is the symlink path; the real
		// directories are <root>/versions/<commit> and the symlink appears
		// on the first deploy.
		d, err := builder.NewDeployer(cfg.Server.StaticDir, cfg.Builder.KeepVersions)
		if err != nil {
			return fmt.Errorf("builder deployer: %w", err)
		}
		if err := d.EnsureDirs(); err != nil {
			return fmt.Errorf("prepare static dirs: %w", err)
		}
		deployer = d
	} else if err := os.MkdirAll(cfg.Server.StaticDir, 0o755); err != nil {
		return fmt.Errorf("prepare static dir: %w", err)
	}

	srv, err := server.New(server.Deps{
		Config: cfg,
		Logger: core.Logger,
		Tracer: core.Tracer,
		Meter:  core.Meter,
		DB:     db,
	})
	if err != nil {
		return fmt.Errorf("build server: %w", err)
	}

	workerDone := make(chan struct{})
	if cfg.Builder.Enabled {
		client, err := builder.NewClient(cfg.Builder.URL, cfg.Builder.APIKey)
		if err != nil {
			return fmt.Errorf("builder client: %w", err)
		}
		meta, err := storage.NewMetaStore(db, cfg.DB.Driver)
		if err != nil {
			return fmt.Errorf("builder meta store: %w", err)
		}
		worker, err := builder.NewWorker(builder.Deps{
			Client:   client,
			Deployer: deployer,
			Meta:     meta,
			Domain:   cfg.Server.BaseDomain,
			Interval: cfg.Builder.PollInterval.Std(),
			Logger:   core.Logger,
		})
		if err != nil {
			return fmt.Errorf("builder worker: %w", err)
		}
		go func() {
			defer close(workerDone)
			worker.Run(ctx)
		}()
		core.Logger.Info("builder integration enabled",
			slog.String("url", cfg.Builder.URL),
			slog.Duration("interval", cfg.Builder.PollInterval.Std()))
	} else {
		// no worker to join: pre-close so the shutdown wait falls through
		close(workerDone)
	}

	errCh := make(chan error, 1)
	go func() {
		core.Logger.Info("http server listening", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	// restore default signal handling: a second signal force-quits
	stop()

	core.Logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	// Give a mid-reconcile worker a moment to notice the cancelled context
	// before the deferred db.Close() runs.
	select {
	case <-workerDone:
	case <-time.After(2 * time.Second):
		core.Logger.Warn("builder worker did not stop in time")
	}
	return nil
}

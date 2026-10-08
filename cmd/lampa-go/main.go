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

	if err := os.MkdirAll(cfg.Server.StaticDir, 0o755); err != nil {
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
	return nil
}

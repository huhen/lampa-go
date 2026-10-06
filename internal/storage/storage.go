// Package storage opens the application database and runs migrations.
package storage

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx driver for database/sql
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // pure-Go sqlite driver

	"lampa-go/internal/config"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open connects to the database using the configured driver.
func Open(cfg config.DB) (*sql.DB, error) {
	switch cfg.Driver {
	case config.DriverSQLite, "":
		return openSQLite(cfg)
	case config.DriverPostgres:
		return openPostgres(cfg)
	default:
		return nil, fmt.Errorf("unknown db driver %q", cfg.Driver)
	}
}

func openSQLite(cfg config.DB) (*sql.DB, error) {
	if dir := filepath.Dir(cfg.DSN); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite has a single writer: one connection avoids SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON"} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("exec %s: %w", pragma, err)
		}
	}
	return db, nil
}

func openPostgres(cfg config.DB) (*sql.DB, error) {
	db, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	// database/sql is the connection pool; apply the configured limits.
	if cfg.MaxOpenConns > 0 {
		db.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		db.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if lt := cfg.ConnMaxLifetime.Std(); lt > 0 {
		db.SetConnMaxLifetime(lt)
	}
	return db, nil
}

// Migrate applies pending goose migrations embedded into the binary.
// goose's global state (SetBaseFS/SetDialect) is acceptable here because the
// app opens the database once at startup.
func Migrate(ctx context.Context, db *sql.DB, driver string) error {
	dialect := map[string]string{
		config.DriverSQLite:   "sqlite3",
		config.DriverPostgres: "postgres",
	}[driver]
	if dialect == "" {
		dialect = "sqlite3"
	}
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect(dialect); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

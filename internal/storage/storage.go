// Package storage opens the application database and runs migrations.
package storage

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	if dir := sqliteDir(cfg.DSN); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db directory: %w", err)
		}
	}
	// Pragmas are passed as DSN params so the driver applies them on EVERY new
	// pooled connection: PRAGMA foreign_keys is connection-scoped and
	// database/sql may silently replace a connection after a driver error.
	sep := "?"
	if strings.Contains(cfg.DSN, "?") {
		sep = "&"
	}
	dsrc := cfg.DSN + sep + "_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsrc)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite has a single writer: one connection avoids SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	// Eager connectivity/path validation; the DSN params above carry the real,
	// per-connection pragma configuration.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("exec pragma journal_mode: %w", err)
	}
	return db, nil
}

// sqliteDir derives the on-disk directory of a sqlite DSN, ignoring a query
// part and an optional "file:" prefix so both "file:/data/app.db?x=1" and
// "/data/app.db" yield "/data".
func sqliteDir(dsn string) string {
	if base, _, found := strings.Cut(dsn, "?"); found {
		dsn = base
	}
	return filepath.Dir(strings.TrimPrefix(dsn, "file:"))
}

func openPostgres(cfg config.DB) (*sql.DB, error) {
	db, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	// database/sql is the connection pool; apply the configured limits as-is.
	// Zero semantics: 0 = unlimited open conns, 0 = keep no idle conns and
	// 0 lifetime = connections are reused forever.
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime.Std())
	return db, nil
}

// Migrate applies pending goose migrations embedded into the binary.
// goose's global state (SetBaseFS/SetDialect) is acceptable here because the
// app opens the database once at startup.
func Migrate(ctx context.Context, db *sql.DB, driver string) error {
	dialect, ok := map[string]string{
		config.DriverSQLite:   "sqlite3",
		config.DriverPostgres: "postgres",
	}[driver]
	if !ok {
		return fmt.Errorf("unknown db driver %q", driver)
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

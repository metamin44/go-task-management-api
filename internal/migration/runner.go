// Package migration applies versioned SQL schema migrations with
// golang-migrate.
//
// Responsibility: run migrations from an injected fs.FS (already rooted at
// the migrations directory) using golang-migrate's iofs source driver and
// its pgx/v5 database driver (the "pgx5" URL scheme). The migrate driver
// manages its own database/sql connection; the application's pgxpool is
// deliberately not used for migrations.
//
// LIMITATION: golang-migrate's Up() API is not context-aware, so Run takes
// no context parameter — an in-flight migration cannot be canceled through
// a context. Run migrations at startup, before serving traffic.
//
// SECURITY: only static, contextual errors are returned. Raw migration,
// driver, DSN, and SQL errors are never wrapped, printed, or logged because
// they can embed credentials, connection details, or schema data.
//
// No automatic rollback is performed: a failed migration leaves the schema
// version marked dirty by golang-migrate and requires manual resolution.
package migration

import (
	"errors"
	"io/fs"
	"net/url"

	"github.com/golang-migrate/migrate/v4"
	// Registers the "pgx5" database driver (pgx/v5 via database/sql).
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/metamin44/go-task-management-api/internal/config"
)

// Run applies all pending migrations from migrationsFS to the database
// described by cfg. migrationsFS must already be rooted at the migrations
// directory. Run is idempotent: migrate.ErrNoChange (nothing to apply) is
// treated as success.
//
// The PostgreSQL DSN from cfg.DSN() is converted to the "pgx5://" scheme
// required by the migrate driver, preserving all connection parameters.
// Migration resources are closed on every path where a migrate instance
// was created. All returned errors are static and secret-safe; the DSN,
// credentials, and underlying driver/SQL errors are never exposed.
func Run(cfg config.DBConfig, migrationsFS fs.FS) error {
	u, err := url.Parse(cfg.DSN())
	if err != nil {
		return errors.New("migration: invalid database configuration (details withheld for security)")
	}
	u.Scheme = "pgx5"
	databaseURL := u.String() // contains credentials: never log or expose

	src, err := iofs.New(migrationsFS, ".")
	if err != nil {
		return errors.New("migration: cannot initialize migration source (details withheld for security)")
	}

	m, err := migrate.NewWithSourceInstance("iofs", src, databaseURL)
	if err != nil {
		// NewWithSourceInstance does not close the source on failure.
		_ = src.Close()
		return errors.New("migration: cannot initialize migration runner (details withheld for security)")
	}

	upErr := m.Up()
	srcErr, dbErr := m.Close()

	if upErr != nil && !errors.Is(upErr, migrate.ErrNoChange) {
		return errors.New("migration: applying migrations failed (details withheld for security)")
	}
	if srcErr != nil || dbErr != nil {
		return errors.New("migration: closing migration resources failed (details withheld for security)")
	}
	return nil
}

// Package database bootstraps the PostgreSQL connection pool.
//
// Responsibility: translate validated config.DBConfig settings into a
// pgxpool.Pool (DSN parsing, pool sizing, connection lifetime) and verify
// initial connectivity with Ping at startup. The package never reads
// environment variables or .env files — configuration arrives exclusively
// through config.DBConfig from the composition root.
//
// SECURITY: errors returned by this package are safe, contextual messages
// only. Underlying pgx errors are deliberately not wrapped because they can
// embed the DSN, password, or raw connection details. Nothing here logs
// configuration, credentials, or connection errors.
package database

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metamin44/go-task-management-api/internal/config"
)

// pingTimeout bounds the initial connectivity check (derived from the
// caller's context) so startup fails fast when the server is unreachable.
const pingTimeout = 5 * time.Second

// NewPool creates a pgxpool.Pool from validated database configuration and
// verifies the initial connection with Ping.
//
// The caller-provided ctx governs pool creation via pgxpool.NewWithConfig;
// the Ping check derives a 5-second timeout from it, so caller cancellation
// or deadlines also bound the connectivity check.
//
// Pool settings (MaxConns, MinConns, MaxConnLifetime) are applied from
// DBConfig after DSN parsing. If Ping fails, the pool is closed before
// returning. On success the caller owns the pool and must Close it on
// shutdown.
//
// All returned errors are secret-safe: they never contain the DSN, password,
// or raw connection details, and no logging is performed.
func NewPool(ctx context.Context, cfg config.DBConfig) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN())
	if err != nil {
		// pgx parse errors can embed the full DSN including the password;
		// return a safe message instead of wrapping the underlying error.
		return nil, errors.New("database: connection settings rejected by pgx (details withheld for security)")
	}

	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MinConns = cfg.MinConns
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, errors.New("database: failed to create connection pool (details withheld for security)")
	}

	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, errors.New("database: initial connection check failed (details withheld for security)")
	}

	return pool, nil
}

// Package repository implements the domain repository interfaces with
// SQL-first PostgreSQL access via pgx/v5 (no ORM).
//
// Transactions: Transactor.WithTx stores a pgx.Tx in the context; every
// repository method picks it up automatically, otherwise it uses the pool.
// Services own transaction boundaries and row-locking order.
//
// SECURITY: errors are mapped to domain sentinels with operation context and
// (for constraint violations) the constraint name only. Raw driver errors,
// SQL text, and connection details are never wrapped or returned.
package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metamin44/go-task-management-api/internal/domain"
)

// querier is satisfied by both *pgxpool.Pool and pgx.Tx.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type txKey struct{}

func querierFrom(ctx context.Context, pool *pgxpool.Pool) querier {
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return tx
	}
	return pool
}

// Transactor implements domain.Transactor with a pgxpool.
type Transactor struct{ pool *pgxpool.Pool }

var _ domain.Transactor = (*Transactor)(nil)

// NewTransactor builds a Transactor over the given pool.
func NewTransactor(pool *pgxpool.Pool) *Transactor { return &Transactor{pool: pool} }

// WithTx runs fn in a READ COMMITTED transaction (pgx default). The deferred
// Rollback is a no-op after a successful Commit. Deadlock/serialization
// retries (40P01/40001) are the service layer's responsibility.
func (t *Transactor) WithTx(ctx context.Context, fn func(context.Context) error) error {
	tx, err := t.pool.Begin(ctx)
	if err != nil {
		return mapError("repository: begin transaction", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(context.WithValue(ctx, txKey{}, tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return mapError("repository: commit", err)
	}
	return nil
}

// mapError converts driver errors into safe, meaningful domain errors.
// Constraint names are included (they are schema metadata, not secrets and
// are needed for service-level mapping, e.g. tasks_project_assignee_fkey).
func mapError(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", op, domain.ErrNotFound)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case pgerrcode.UniqueViolation:
			return fmt.Errorf("%s: %w (constraint %s)", op, domain.ErrConflict, pgErr.ConstraintName)
		case pgerrcode.ForeignKeyViolation:
			return fmt.Errorf("%s: %w (constraint %s)", op, domain.ErrReference, pgErr.ConstraintName)
		case pgerrcode.CheckViolation, pgerrcode.NotNullViolation, pgerrcode.InvalidTextRepresentation:
			return fmt.Errorf("%s: %w", op, domain.ErrInvalid)
		case pgerrcode.DeadlockDetected, pgerrcode.SerializationFailure:
			// Retryable concurrency failures; services apply bounded retries.
			return fmt.Errorf("%s: %w", op, domain.ErrSerialization)
		}
	}
	// Unknown driver errors may embed SQL or connection details: withhold.
	return fmt.Errorf("%s: unexpected database error", op)
}

// requireRows maps zero affected rows (UPDATE/DELETE) to ErrNotFound.
func requireRows(op string, tag pgconn.CommandTag) error {
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%s: %w", op, domain.ErrNotFound)
	}
	return nil
}

// classifyUpdateMiss disambiguates a zero-row versioned UPDATE (the
// updated_at predicate failed). The row was either deleted since it was read
// (ErrNotFound) or concurrently modified (ErrConflict). existsSQL is a
// SELECT EXISTS(...) probe keyed by the row id; it runs only on the miss path.
// The probe is a best-effort snapshot: a delete racing between the UPDATE and
// the probe may surface as a conflict, which the client resolves on retry.
func classifyUpdateMiss(ctx context.Context, q querier, op, existsSQL string, id domain.UUID) error {
	var exists bool
	if err := q.QueryRow(ctx, existsSQL, id).Scan(&exists); err != nil {
		return mapError(op, err)
	}
	if exists {
		return fmt.Errorf("%s: %w", op, domain.ErrConflict)
	}
	return fmt.Errorf("%s: %w", op, domain.ErrNotFound)
}

// scanner is satisfied by pgx.Row and pgx.Rows.
type scanner interface {
	Scan(dest ...any) error
}

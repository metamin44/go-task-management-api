package repository

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metamin44/go-task-management-api/internal/domain"
)

// PasswordResetTokenRepository is the SQL-first implementation of
// domain.PasswordResetTokenRepository. Reset orchestration (FOR UPDATE lock,
// expiry/single-use checks, atomic password change + consumption + session
// revocation) lives in the service layer inside Transactor.WithTx.
type PasswordResetTokenRepository struct{ pool *pgxpool.Pool }

var _ domain.PasswordResetTokenRepository = (*PasswordResetTokenRepository)(nil)

// NewPasswordResetTokenRepository builds the repository over the given pool.
func NewPasswordResetTokenRepository(pool *pgxpool.Pool) *PasswordResetTokenRepository {
	return &PasswordResetTokenRepository{pool: pool}
}

const passwordResetCols = "id, user_id, token_hash, expires_at, created_at, consumed_at"

func scanPasswordReset(s scanner) (*domain.PasswordResetToken, error) {
	var t domain.PasswordResetToken
	if err := s.Scan(&t.ID, &t.UserID, &t.TokenHash, &t.ExpiresAt, &t.CreatedAt, &t.ConsumedAt); err != nil {
		return nil, err
	}
	return &t, nil
}

// Create inserts a new unconsumed token row; DB fills created_at.
func (r *PasswordResetTokenRepository) Create(ctx context.Context, t *domain.PasswordResetToken) error {
	q := querierFrom(ctx, r.pool)
	err := q.QueryRow(ctx,
		`INSERT INTO password_reset_tokens (id, user_id, token_hash, expires_at)
		 VALUES ($1, $2, $3, $4)
		 RETURNING created_at`,
		t.ID, t.UserID, t.TokenHash, t.ExpiresAt).Scan(&t.CreatedAt)
	return mapError("password_reset_tokens: create", err)
}

// GetByTokenHashForUpdate locks the token row for reset. Must run inside a
// transaction; concurrent presentations of the same token serialize here.
func (r *PasswordResetTokenRepository) GetByTokenHashForUpdate(ctx context.Context, tokenHash []byte) (*domain.PasswordResetToken, error) {
	q := querierFrom(ctx, r.pool)
	t, err := scanPasswordReset(q.QueryRow(ctx,
		`SELECT `+passwordResetCols+` FROM password_reset_tokens WHERE token_hash = $1 FOR UPDATE`,
		tokenHash))
	if err != nil {
		return nil, mapError("password_reset_tokens: get by token hash for update", err)
	}
	return t, nil
}

// MarkConsumed records successful use. The consumed_at IS NULL predicate makes
// consumption single-use even without the row lock; ErrNotFound means the row
// is missing or was already consumed.
func (r *PasswordResetTokenRepository) MarkConsumed(ctx context.Context, id domain.UUID) error {
	q := querierFrom(ctx, r.pool)
	tag, err := q.Exec(ctx,
		`UPDATE password_reset_tokens SET consumed_at = now()
		 WHERE id = $1 AND consumed_at IS NULL`,
		id)
	if err != nil {
		return mapError("password_reset_tokens: mark consumed", err)
	}
	return requireRows("password_reset_tokens: mark consumed", tag)
}

// DeleteUnconsumedByUser removes a user's outstanding unconsumed tokens so a
// fresh forgot-password request supersedes any previous link. Consumed rows
// are retained for audit until cleanup.
func (r *PasswordResetTokenRepository) DeleteUnconsumedByUser(ctx context.Context, userID domain.UUID) (int64, error) {
	q := querierFrom(ctx, r.pool)
	tag, err := q.Exec(ctx,
		`DELETE FROM password_reset_tokens WHERE user_id = $1 AND consumed_at IS NULL`,
		userID)
	if err != nil {
		return 0, mapError("password_reset_tokens: delete unconsumed by user", err)
	}
	return tag.RowsAffected(), nil
}

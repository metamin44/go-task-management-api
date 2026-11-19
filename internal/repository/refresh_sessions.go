package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metamin44/go-task-management-api/internal/domain"
)

// RefreshSessionRepository is the SQL-first implementation of
// domain.RefreshSessionRepository. Rotation orchestration (FOR UPDATE lock,
// reuse detection, revoke-before-insert ordering) lives in the service layer
// inside Transactor.WithTx; the deferred replaced_by FK validates at commit.
type RefreshSessionRepository struct{ pool *pgxpool.Pool }

var _ domain.RefreshSessionRepository = (*RefreshSessionRepository)(nil)

// NewRefreshSessionRepository builds the repository over the given pool.
func NewRefreshSessionRepository(pool *pgxpool.Pool) *RefreshSessionRepository {
	return &RefreshSessionRepository{pool: pool}
}

const refreshSessionCols = "id, session_id, user_id, token_hash, expires_at, created_at, revoked_at, replaced_by"

func scanRefreshSession(s scanner) (*domain.RefreshSession, error) {
	var rs domain.RefreshSession
	if err := s.Scan(&rs.ID, &rs.SessionID, &rs.UserID, &rs.TokenHash,
		&rs.ExpiresAt, &rs.CreatedAt, &rs.RevokedAt, &rs.ReplacedBy); err != nil {
		return nil, err
	}
	return &rs, nil
}

// Create inserts a new active token row; DB fills created_at.
func (r *RefreshSessionRepository) Create(ctx context.Context, s *domain.RefreshSession) error {
	q := querierFrom(ctx, r.pool)
	err := q.QueryRow(ctx,
		`INSERT INTO refresh_sessions (id, session_id, user_id, token_hash, expires_at)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING created_at`,
		s.ID, s.SessionID, s.UserID, s.TokenHash, s.ExpiresAt).Scan(&s.CreatedAt)
	return mapError("refresh_sessions: create", err)
}

// GetByTokenHashForUpdate locks the token row for rotation. Must run inside a
// transaction; concurrent presentations of the same token serialize here.
func (r *RefreshSessionRepository) GetByTokenHashForUpdate(ctx context.Context, tokenHash []byte) (*domain.RefreshSession, error) {
	q := querierFrom(ctx, r.pool)
	rs, err := scanRefreshSession(q.QueryRow(ctx,
		`SELECT `+refreshSessionCols+` FROM refresh_sessions WHERE token_hash = $1 FOR UPDATE`,
		tokenHash))
	if err != nil {
		return nil, mapError("refresh_sessions: get by token hash for update", err)
	}
	return rs, nil
}

// MarkReplaced revokes the old token and sets replaced_by in one statement.
// Run BEFORE inserting the replacement row (deferred FK checks at commit).
// ErrNotFound means no active row matched (missing or already revoked).
func (r *RefreshSessionRepository) MarkReplaced(ctx context.Context, id, replacedBy domain.UUID) error {
	q := querierFrom(ctx, r.pool)
	tag, err := q.Exec(ctx,
		`UPDATE refresh_sessions SET revoked_at = now(), replaced_by = $2
		 WHERE id = $1 AND revoked_at IS NULL`,
		id, replacedBy)
	if err != nil {
		return mapError("refresh_sessions: mark replaced", err)
	}
	return requireRows("refresh_sessions: mark replaced", tag)
}

// RevokeSession revokes all active tokens of a session (logout / reuse).
func (r *RefreshSessionRepository) RevokeSession(ctx context.Context, sessionID domain.UUID) (int64, error) {
	q := querierFrom(ctx, r.pool)
	tag, err := q.Exec(ctx,
		`UPDATE refresh_sessions SET revoked_at = now()
		 WHERE session_id = $1 AND revoked_at IS NULL`,
		sessionID)
	if err != nil {
		return 0, mapError("refresh_sessions: revoke session", err)
	}
	return tag.RowsAffected(), nil
}

// RevokeUserSessions revokes all active tokens of a user (logout-all).
func (r *RefreshSessionRepository) RevokeUserSessions(ctx context.Context, userID domain.UUID) (int64, error) {
	q := querierFrom(ctx, r.pool)
	tag, err := q.Exec(ctx,
		`UPDATE refresh_sessions SET revoked_at = now()
		 WHERE user_id = $1 AND revoked_at IS NULL`,
		userID)
	if err != nil {
		return 0, mapError("refresh_sessions: revoke user sessions", err)
	}
	return tag.RowsAffected(), nil
}

// DeleteExpiredBatch deletes one cleanup batch using the finalized CTE
// pattern (PostgreSQL DELETE has no LIMIT). SET NULL on the self-FK keeps
// deletion reference-safe. retention is the 30-day window per design.
func (r *RefreshSessionRepository) DeleteExpiredBatch(ctx context.Context, retention time.Duration, limit int) (int64, error) {
	if limit < 1 {
		return 0, fmt.Errorf("refresh_sessions: cleanup batch limit: %w", domain.ErrInvalid)
	}
	q := querierFrom(ctx, r.pool)
	tag, err := q.Exec(ctx,
		`WITH batch AS (
		     SELECT id FROM refresh_sessions
		     WHERE (revoked_at IS NOT NULL AND revoked_at < now() - $1::interval)
		        OR (revoked_at IS NULL     AND expires_at < now() - $1::interval)
		     ORDER BY COALESCE(revoked_at, expires_at), id
		     LIMIT $2
		 )
		 DELETE FROM refresh_sessions rs
		 USING batch b
		 WHERE rs.id = b.id`,
		retention, limit)
	if err != nil {
		return 0, mapError("refresh_sessions: cleanup batch", err)
	}
	return tag.RowsAffected(), nil
}

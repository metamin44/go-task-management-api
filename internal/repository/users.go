package repository

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metamin44/go-task-management-api/internal/domain"
)

// UserRepository is the SQL-first PostgreSQL implementation of
// domain.UserRepository.
type UserRepository struct{ pool *pgxpool.Pool }

var _ domain.UserRepository = (*UserRepository)(nil)

// NewUserRepository builds a UserRepository over the given pool.
func NewUserRepository(pool *pgxpool.Pool) *UserRepository { return &UserRepository{pool: pool} }

const userCols = "id, name, email, password_hash, created_at, updated_at"

func scanUser(s scanner) (*domain.User, error) {
	var u domain.User
	if err := s.Scan(&u.ID, &u.Name, &u.Email, &u.PasswordHash, &u.CreatedAt, &u.UpdatedAt); err != nil {
		return nil, err
	}
	return &u, nil
}

// Create inserts a user with a domain-generated UUID; DB fills timestamps.
func (r *UserRepository) Create(ctx context.Context, u *domain.User) error {
	q := querierFrom(ctx, r.pool)
	err := q.QueryRow(ctx,
		`INSERT INTO users (id, name, email, password_hash)
		 VALUES ($1, $2, $3, $4)
		 RETURNING created_at, updated_at`,
		u.ID, u.Name, u.Email, u.PasswordHash).Scan(&u.CreatedAt, &u.UpdatedAt)
	return mapError("users: create", err)
}

// GetByID returns the user or domain.ErrNotFound.
func (r *UserRepository) GetByID(ctx context.Context, id domain.UUID) (*domain.User, error) {
	q := querierFrom(ctx, r.pool)
	u, err := scanUser(q.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = $1`, id))
	if err != nil {
		return nil, mapError("users: get by id", err)
	}
	return u, nil
}

// GetByEmail matches via the lower(email) expression index.
func (r *UserRepository) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	q := querierFrom(ctx, r.pool)
	u, err := scanUser(q.QueryRow(ctx,
		`SELECT `+userCols+` FROM users WHERE lower(email) = lower($1)`, email))
	if err != nil {
		return nil, mapError("users: get by email", err)
	}
	return u, nil
}

// Update persists name/email with optimistic concurrency: the row is updated
// only when its stored updated_at still equals the value originally read
// (u.UpdatedAt). updated_at always advances strictly, even within the same
// clock tick, via GREATEST(now(), updated_at + 1µs). A zero-row result means
// the row was deleted (ErrNotFound) or concurrently modified (ErrConflict).
func (r *UserRepository) Update(ctx context.Context, u *domain.User) error {
	q := querierFrom(ctx, r.pool)
	err := q.QueryRow(ctx,
		`UPDATE users SET name = $2, email = $3,
		     updated_at = GREATEST(now(), updated_at + interval '1 microsecond')
		 WHERE id = $1 AND updated_at = $4
		 RETURNING updated_at`,
		u.ID, u.Name, u.Email, u.UpdatedAt).Scan(&u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return classifyUpdateMiss(ctx, q, "users: update",
			`SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, u.ID)
	}
	return mapError("users: update", err)
}

// UpdatePasswordHash replaces the stored hash. updated_at advances strictly
// (same GREATEST expression as the versioned updates) so the concurrency
// version never regresses; no version predicate is needed here.
func (r *UserRepository) UpdatePasswordHash(ctx context.Context, id domain.UUID, passwordHash string) error {
	q := querierFrom(ctx, r.pool)
	tag, err := q.Exec(ctx,
		`UPDATE users SET password_hash = $2,
		     updated_at = GREATEST(now(), updated_at + interval '1 microsecond')
		 WHERE id = $1`,
		id, passwordHash)
	if err != nil {
		return mapError("users: update password", err)
	}
	return requireRows("users: update password", tag)
}

// Delete removes the user; RESTRICT/CASCADE policies are enforced by the DB.
func (r *UserRepository) Delete(ctx context.Context, id domain.UUID) error {
	q := querierFrom(ctx, r.pool)
	tag, err := q.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	if err != nil {
		return mapError("users: delete", err)
	}
	return requireRows("users: delete", tag)
}

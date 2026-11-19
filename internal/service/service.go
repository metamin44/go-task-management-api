// Package service implements business logic: authentication, authorization,
// transaction orchestration, and concurrency handling. It depends only on
// domain interfaces (dependency inversion); repositories remain
// authorization-agnostic. HTTP handlers (later phase) map the errors here to
// transport responses.
//
// SECURITY: plaintext passwords never leave this package's hashing boundary;
// errors never echo passwords, tokens, or hashes. Access tokens are signed
// JWTs (typ=access); refresh tokens are opaque 256-bit random values stored
// only as SHA-256 hashes.
package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/metamin44/go-task-management-api/internal/domain"
)

// Service-level errors. Handlers map these to HTTP statuses (later phase):
// ErrUnauthorized -> 401, ErrForbidden -> 403, ErrTokenReused -> 401 (force
// re-login), domain sentinels -> 404/409/400 per PROJECT_CONTEXT.md.
var (
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
	ErrTokenReused  = errors.New("refresh token reuse detected")
)

// maxTxAttempts bounds retries on deadlock/serialization failures.
const maxTxAttempts = 3

// withRetry runs fn up to maxTxAttempts times while it fails with
// domain.ErrSerialization (40P01/40001). Any other error returns immediately.
func withRetry(ctx context.Context, fn func(context.Context) error) error {
	var err error
	for attempt := 0; attempt < maxTxAttempts; attempt++ {
		err = fn(ctx)
		if err == nil || !errors.Is(err, domain.ErrSerialization) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return err
}

// requireMembership returns the actor's membership or ErrForbidden.
func requireMembership(ctx context.Context, members domain.ProjectMemberRepository, projectID, userID domain.UUID) (*domain.ProjectMember, error) {
	m, err := members.Get(ctx, projectID, userID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, fmt.Errorf("project access: %w", ErrForbidden)
		}
		return nil, err
	}
	return m, nil
}

// requireRole checks the membership against allowed roles.
func requireRole(m *domain.ProjectMember, roles ...domain.Role) error {
	for _, r := range roles {
		if m.Role == r {
			return nil
		}
	}
	return fmt.Errorf("insufficient project role: %w", ErrForbidden)
}

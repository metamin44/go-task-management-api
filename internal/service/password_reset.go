package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/metamin44/go-task-management-api/internal/domain"
)

// resetDeliveryConcurrency bounds the number of concurrent asynchronous
// reset-token delivery workflows (S-07). Requests beyond the cap are dropped
// with a secret-safe warning (still a generic success) instead of spawning
// unbounded goroutines.
const resetDeliveryConcurrency = 32

// PasswordResetService implements forgot-password (issue a single-use reset
// token and email a reset code) and reset-password (consume the token, set a
// new password, revoke all sessions). It reuses the existing Argon2id hasher,
// session revocation, password-hash writer, transaction retry helper, and
// opaque-token crypto — no duplicated hashing or revocation logic.
//
// SECURITY: only the SHA-256 digest of a reset token is stored; the raw value
// appears solely in the emailed message body as a copy/paste code (never in a
// URL). Tokens, codes, links, credentials, and recipient addresses are never
// logged. Unknown, expired, and consumed tokens all map to the same
// ErrUnauthorized so the API never reveals token state or account existence.
type PasswordResetService struct {
	users        domain.UserRepository
	resets       domain.PasswordResetTokenRepository
	sessions     domain.RefreshSessionRepository
	tx           domain.Transactor
	hasher       PasswordHasher
	mailer       ResetMailer
	resetTTL     time.Duration
	resetBaseURL string
	log          *slog.Logger

	// spawn runs the delivery workflow. Production launches a goroutine; tests
	// inject a synchronous or capturing runner for determinism.
	spawn func(func())
	// sem bounds concurrent delivery workflows (S-07).
	sem chan struct{}
}

// NewPasswordResetService wires the service. resetTTL must be positive and
// resetBaseURL must be set (the clean reset-page URL; no token is appended).
// hasher and mailer are required. A nil logger uses slog.Default.
func NewPasswordResetService(
	users domain.UserRepository,
	resets domain.PasswordResetTokenRepository,
	sessions domain.RefreshSessionRepository,
	tx domain.Transactor,
	hasher PasswordHasher,
	mailer ResetMailer,
	resetTTL time.Duration,
	resetBaseURL string,
	log *slog.Logger,
) (*PasswordResetService, error) {
	if resetTTL <= 0 {
		return nil, fmt.Errorf("reset TTL must be positive: %w", domain.ErrInvalid)
	}
	if resetBaseURL == "" {
		return nil, fmt.Errorf("reset base URL must be set: %w", domain.ErrInvalid)
	}
	if hasher == nil || mailer == nil {
		return nil, fmt.Errorf("reset service requires a hasher and mailer: %w", domain.ErrInvalid)
	}
	if log == nil {
		log = slog.Default()
	}
	return &PasswordResetService{
		users: users, resets: resets, sessions: sessions, tx: tx,
		hasher: hasher, mailer: mailer, resetTTL: resetTTL, resetBaseURL: resetBaseURL, log: log,
		spawn: func(f func()) { go f() },
		sem:   make(chan struct{}, resetDeliveryConcurrency),
	}, nil
}

// ForgotPassword issues a reset token for email if (and only if) an account
// exists. For the existence dimension it always returns nil, so the handler
// emits an identical generic response whether or not the account exists (no
// enumeration).
//
// S-04: only the user lookup runs on the synchronous request path — identical
// work for existing and unknown emails — so DB-write latency cannot reveal
// existence. Token issuance and email delivery run in a controlled async
// workflow that is panic-recovered (S-02) and concurrency-bounded (S-07). Only
// infrastructure failures of the lookup itself propagate (as a generic 500).
func (s *PasswordResetService) ForgotPassword(ctx context.Context, email string) error {
	u, err := s.users.GetByEmail(ctx, domain.NormalizeEmail(email))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil // generic response: do not reveal non-existence
		}
		return err
	}
	userID, userEmail := u.ID, u.Email

	select {
	case s.sem <- struct{}{}: // acquire a delivery slot (non-blocking)
		// The request context is cancelled once the response is written, so
		// derive an independent, bounded context for the async workflow.
		sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		s.spawn(func() {
			defer func() { <-s.sem }() // release slot last, even on panic
			defer cancel()
			defer func() { // S-02: recover; log a static message, never the panic value or secrets
				if r := recover(); r != nil {
					s.log.Error("password reset delivery panicked")
				}
			}()
			s.issueAndDeliver(sendCtx, userID, userEmail)
		})
	default:
		// S-07: at capacity, drop the delivery rather than spawn unbounded
		// goroutines. The response stays generic (no existence signal).
		s.log.Warn("password reset delivery capacity reached; skipping send")
	}
	return nil
}

// issueAndDeliver runs off the request path: generate a single-use token, store
// only its hash (superseding any previous unconsumed token), then email the raw
// code in the message body. Errors are logged secret-safe; the raw code, link,
// and recipient are never logged.
func (s *PasswordResetService) issueAndDeliver(ctx context.Context, userID domain.UUID, email string) {
	// Reuse the same 256-bit opaque-token generation as refresh tokens; only
	// the SHA-256 digest is persisted.
	raw, hash, err := newRefreshToken()
	if err != nil {
		s.log.Warn("password reset token generation failed", "error", err)
		return
	}
	now := time.Now()
	err = withRetry(ctx, func(ctx context.Context) error {
		return s.tx.WithTx(ctx, func(ctx context.Context) error {
			// A fresh request supersedes any previous unconsumed code.
			if _, err := s.resets.DeleteUnconsumedByUser(ctx, userID); err != nil {
				return err
			}
			return s.resets.Create(ctx, &domain.PasswordResetToken{
				ID:        domain.NewUUID(),
				UserID:    userID,
				TokenHash: hash,
				ExpiresAt: now.Add(s.resetTTL),
			})
		})
	})
	if err != nil {
		s.log.Warn("password reset token issuance failed", "error", err)
		return
	}
	// S-05: the code travels in the email body; the page URL carries no token.
	if err := s.mailer.SendPasswordReset(ctx, email, s.resetBaseURL, raw); err != nil {
		s.log.Warn("password reset email delivery failed", "error", err)
	}
}

// ResetPassword consumes a valid reset token and sets a new password, revoking
// ALL of the user's refresh sessions atomically. Unknown, expired, and
// already-consumed tokens all return the same ErrUnauthorized (no oracle). The
// FOR UPDATE row lock serializes concurrent attempts: exactly one succeeds and
// the rest see the consumed row and fail. Any failure rolls back the whole
// transaction, leaving the password, token, and sessions unchanged.
func (s *PasswordResetService) ResetPassword(ctx context.Context, rawToken, newPassword string) error {
	hash := hashRefreshToken(rawToken)
	return withRetry(ctx, func(ctx context.Context) error {
		return s.tx.WithTx(ctx, func(ctx context.Context) error {
			rt, err := s.resets.GetByTokenHashForUpdate(ctx, hash)
			if err != nil {
				if errors.Is(err, domain.ErrNotFound) {
					return fmt.Errorf("reset password: %w", ErrUnauthorized)
				}
				return err
			}
			now := time.Now()
			if rt.IsConsumed() || rt.IsExpired(now) {
				return fmt.Errorf("reset password: %w", ErrUnauthorized)
			}
			if err := ValidatePassword(newPassword); err != nil {
				return err
			}
			newHash, err := s.hasher.Hash(newPassword)
			if err != nil {
				return err
			}
			if err := s.users.UpdatePasswordHash(ctx, rt.UserID, newHash); err != nil {
				return err
			}
			if err := s.resets.MarkConsumed(ctx, rt.ID); err != nil {
				return err
			}
			if _, err := s.sessions.RevokeUserSessions(ctx, rt.UserID); err != nil {
				return err
			}
			return nil
		})
	})
}

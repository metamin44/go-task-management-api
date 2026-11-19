package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/metamin44/go-task-management-api/internal/domain"
)

// dummyLoginPassword is a fixed, non-secret input used only to generate the
// dummy hash for login timing equalization (L-01). It is never a real
// credential and is never logged.
const dummyLoginPassword = "timing-equalization-dummy-password"

// AuthService implements registration, login, access-token validation,
// refresh rotation with replay detection, logout, and password change.
type AuthService struct {
	users      domain.UserRepository
	sessions   domain.RefreshSessionRepository
	tx         domain.Transactor
	hasher     PasswordHasher
	access     AccessTokenManager
	refreshTTL time.Duration

	// L-01: a dummy PHC hash generated exactly once (lazily) so unknown-email
	// logins perform the same Argon2id verification work as wrong-password
	// logins, preventing user enumeration via response timing.
	dummyOnce sync.Once
	dummyHash string
}

// NewAuthService wires the auth service; refreshTTL must be positive.
func NewAuthService(
	users domain.UserRepository,
	sessions domain.RefreshSessionRepository,
	tx domain.Transactor,
	hasher PasswordHasher,
	access AccessTokenManager,
	refreshTTL time.Duration,
) (*AuthService, error) {
	if refreshTTL <= 0 {
		return nil, fmt.Errorf("refresh TTL must be positive: %w", domain.ErrInvalid)
	}
	return &AuthService{users: users, sessions: sessions, tx: tx, hasher: hasher, access: access, refreshTTL: refreshTTL}, nil
}

// TokenPair is issued on login and rotation. RefreshToken is the raw opaque
// value shown to the client exactly once; only its SHA-256 hash is stored.
type TokenPair struct {
	AccessToken  string
	RefreshToken string
}

// Register creates a user with a hashed password. The returned user never
// contains the hash. Duplicate email surfaces domain.ErrConflict.
func (s *AuthService) Register(ctx context.Context, name, email, password string) (*domain.User, error) {
	name = strings.TrimSpace(name)
	if err := domain.ValidateUserName(name); err != nil {
		return nil, err
	}
	if err := domain.ValidateEmail(email); err != nil {
		return nil, err
	}
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	hash, err := s.hasher.Hash(password)
	if err != nil {
		return nil, err
	}
	u := &domain.User{
		ID:           domain.NewUUID(),
		Name:         name,
		Email:        domain.NormalizeEmail(email),
		PasswordHash: hash,
	}
	if err := s.users.Create(ctx, u); err != nil {
		return nil, err
	}
	u.PasswordHash = ""
	return u, nil
}

// Login verifies credentials and issues a token pair with a fresh session.
// Unknown email and wrong password return the same ErrUnauthorized.
func (s *AuthService) Login(ctx context.Context, email, password string) (*TokenPair, error) {
	u, err := s.users.GetByEmail(ctx, domain.NormalizeEmail(email))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			// L-01: equalize verification work so an unknown email costs the
			// same as a wrong password (no timing-based user enumeration).
			s.verifyDummy(password)
			return nil, fmt.Errorf("login: %w", ErrUnauthorized)
		}
		return nil, err
	}
	ok, err := s.hasher.Verify(u.PasswordHash, password)
	if err != nil || !ok {
		return nil, fmt.Errorf("login: %w", ErrUnauthorized)
	}
	return s.issueSession(ctx, u.ID)
}

// verifyDummy performs one Argon2id verification against a dummy PHC hash so
// the unknown-email path does the same work as the wrong-password path (L-01).
// The dummy hash is generated once via sync.Once — never per login. The result
// is intentionally discarded; the caller always returns ErrUnauthorized. No
// password, hash, or token is logged. If dummy generation fails (e.g. entropy
// failure), verification is skipped but the response stays ErrUnauthorized.
func (s *AuthService) verifyDummy(password string) {
	s.dummyOnce.Do(func() {
		if h, err := s.hasher.Hash(dummyLoginPassword); err == nil {
			s.dummyHash = h
		}
	})
	if s.dummyHash != "" {
		_, _ = s.hasher.Verify(s.dummyHash, password)
	}
}

func (s *AuthService) issueSession(ctx context.Context, userID domain.UUID) (*TokenPair, error) {
	now := time.Now()
	raw, hash, err := newRefreshToken()
	if err != nil {
		return nil, err
	}
	rs := &domain.RefreshSession{
		ID:        domain.NewUUID(),
		SessionID: domain.NewUUID(), // fresh session per login
		UserID:    userID,
		TokenHash: hash,
		ExpiresAt: now.Add(s.refreshTTL),
	}
	if err := s.sessions.Create(ctx, rs); err != nil {
		return nil, err
	}
	access, err := s.access.Issue(userID, now)
	if err != nil {
		return nil, err
	}
	return &TokenPair{AccessToken: access, RefreshToken: raw}, nil
}

// ValidateAccessToken validates an access token (signature, expiry, type).
func (s *AuthService) ValidateAccessToken(token string, now time.Time) (*TokenClaims, error) {
	return s.access.Validate(token, now)
}

// Refresh rotates a refresh token inside a row-locked transaction with
// bounded deadlock/serialization retries:
//   - unknown hash            -> ErrUnauthorized
//   - revoked hash (replay)   -> revoke the whole session, COMMIT, ErrTokenReused
//   - expired                 -> revoke the session, COMMIT, ErrUnauthorized
//   - valid                   -> MarkReplaced(old, new) BEFORE inserting the
//     new row (deferred FK validates at commit), same session_id, new pair.
//
// Revocation on replay/expiry is committed even though the request fails.
func (s *AuthService) Refresh(ctx context.Context, refreshToken string) (*TokenPair, error) {
	hash := hashRefreshToken(refreshToken)
	var outcome struct {
		pair    *TokenPair
		reused  bool
		expired bool
	}
	err := withRetry(ctx, func(ctx context.Context) error {
		return s.tx.WithTx(ctx, func(ctx context.Context) error {
			outcome = struct {
				pair    *TokenPair
				reused  bool
				expired bool
			}{}
			rs, err := s.sessions.GetByTokenHashForUpdate(ctx, hash)
			if err != nil {
				if errors.Is(err, domain.ErrNotFound) {
					return fmt.Errorf("refresh: %w", ErrUnauthorized)
				}
				return err
			}
			now := time.Now()
			if rs.IsRevoked() {
				if _, err := s.sessions.RevokeSession(ctx, rs.SessionID); err != nil {
					return err
				}
				outcome.reused = true
				return nil // commit the revocation, then fail the request
			}
			if rs.IsExpired(now) {
				if _, err := s.sessions.RevokeSession(ctx, rs.SessionID); err != nil {
					return err
				}
				outcome.expired = true
				return nil
			}
			raw, newHash, err := newRefreshToken()
			if err != nil {
				return err
			}
			newID := domain.NewUUID()
			if err := s.sessions.MarkReplaced(ctx, rs.ID, newID); err != nil {
				return err
			}
			ns := &domain.RefreshSession{
				ID:        newID,
				SessionID: rs.SessionID, // stable across rotation
				UserID:    rs.UserID,
				TokenHash: newHash,
				ExpiresAt: now.Add(s.refreshTTL),
			}
			if err := s.sessions.Create(ctx, ns); err != nil {
				return err
			}
			access, err := s.access.Issue(rs.UserID, now)
			if err != nil {
				return err
			}
			outcome.pair = &TokenPair{AccessToken: access, RefreshToken: raw}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	if outcome.reused {
		return nil, fmt.Errorf("refresh: %w", ErrTokenReused)
	}
	if outcome.expired {
		return nil, fmt.Errorf("refresh: %w", ErrUnauthorized)
	}
	return outcome.pair, nil
}

// Logout revokes the session of the presented refresh token (idempotent for
// already-revoked tokens; unknown tokens yield ErrUnauthorized).
func (s *AuthService) Logout(ctx context.Context, refreshToken string) error {
	rs, err := s.sessions.GetByTokenHashForUpdate(ctx, hashRefreshToken(refreshToken))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("logout: %w", ErrUnauthorized)
		}
		return err
	}
	if _, err := s.sessions.RevokeSession(ctx, rs.SessionID); err != nil {
		return err
	}
	return nil
}

// LogoutAll revokes every active session of the user.
func (s *AuthService) LogoutAll(ctx context.Context, userID domain.UUID) (int64, error) {
	return s.sessions.RevokeUserSessions(ctx, userID)
}

// ChangePassword verifies the current password, stores the new hash, and
// revokes ALL refresh sessions atomically (safe default; documented as
// proposed behavior in PROJECT_CONTEXT.md).
func (s *AuthService) ChangePassword(ctx context.Context, userID domain.UUID, currentPassword, newPassword string) error {
	u, err := s.users.GetByID(ctx, userID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("change password: %w", ErrUnauthorized)
		}
		return err
	}
	ok, err := s.hasher.Verify(u.PasswordHash, currentPassword)
	if err != nil || !ok {
		return fmt.Errorf("change password: %w", ErrUnauthorized)
	}
	if err := ValidatePassword(newPassword); err != nil {
		return err
	}
	hash, err := s.hasher.Hash(newPassword)
	if err != nil {
		return err
	}
	return withRetry(ctx, func(ctx context.Context) error {
		return s.tx.WithTx(ctx, func(ctx context.Context) error {
			if err := s.users.UpdatePasswordHash(ctx, userID, hash); err != nil {
				return err
			}
			if _, err := s.sessions.RevokeUserSessions(ctx, userID); err != nil {
				return err
			}
			return nil
		})
	})
}

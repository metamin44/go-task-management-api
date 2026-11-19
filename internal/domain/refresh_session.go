package domain

import (
	"fmt"
	"time"
)

// RefreshSession is one refresh-token row. SessionID is stable across the
// whole rotation chain of one login; ID identifies the individual token.
// TokenHash is the SHA-256 digest of the raw token (exactly 32 bytes).
type RefreshSession struct {
	ID         UUID
	SessionID  UUID
	UserID     UUID
	TokenHash  []byte
	ExpiresAt  time.Time
	CreatedAt  time.Time
	RevokedAt  *time.Time
	ReplacedBy *UUID
}

// TokenHashLen is the required SHA-256 digest length (DB CHECK).
const TokenHashLen = 32

// ValidateTokenHash enforces the 32-byte digest length.
func ValidateTokenHash(h []byte) error {
	if len(h) != TokenHashLen {
		return fmt.Errorf("token hash must be exactly %d bytes", TokenHashLen)
	}
	return nil
}

// IsRevoked reports whether the token has been revoked (rotation, logout,
// or reuse detection).
func (s *RefreshSession) IsRevoked() bool { return s.RevokedAt != nil }

// IsExpired reports whether the token is expired at now.
func (s *RefreshSession) IsExpired(now time.Time) bool { return !s.ExpiresAt.After(now) }

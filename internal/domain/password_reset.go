package domain

import (
	"fmt"
	"time"
)

// PasswordResetToken is one single-use password-reset row. Only the SHA-256
// digest of the raw token is stored (exactly 32 bytes); the raw value appears
// solely in the emailed reset link. ConsumedAt records successful use.
type PasswordResetToken struct {
	ID         UUID
	UserID     UUID
	TokenHash  []byte
	ExpiresAt  time.Time
	CreatedAt  time.Time
	ConsumedAt *time.Time
}

// ResetTokenHashLen is the required SHA-256 digest length (DB CHECK).
const ResetTokenHashLen = 32

// ValidateResetTokenHash enforces the 32-byte digest length.
func ValidateResetTokenHash(h []byte) error {
	if len(h) != ResetTokenHashLen {
		return fmt.Errorf("reset token hash must be exactly %d bytes", ResetTokenHashLen)
	}
	return nil
}

// IsConsumed reports whether the token has already been used.
func (t *PasswordResetToken) IsConsumed() bool { return t.ConsumedAt != nil }

// IsExpired reports whether the token is expired at now.
func (t *PasswordResetToken) IsExpired(now time.Time) bool { return !t.ExpiresAt.After(now) }

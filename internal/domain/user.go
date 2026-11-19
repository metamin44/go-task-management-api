package domain

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// User mirrors the users table. PasswordHash holds an encoded hash only;
// plaintext passwords never reach the domain or repository layers.
type User struct {
	ID           UUID
	Name         string
	Email        string
	PasswordHash string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Length limits identical to the applied migrations.
const (
	MaxUserNameLen = 100
	MaxEmailLen    = 254
)

// ValidateUserName enforces the users.name rules (non-blank, <= 100 chars).
func ValidateUserName(name string) error { return validateText("name", name, MaxUserNameLen) }

// NormalizeEmail trims and lowercases an email for storage and lookup
// (uniqueness is enforced by the lower(email) expression index).
func NormalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// ValidateEmail enforces length and a minimal address shape.
func ValidateEmail(email string) error {
	e := strings.TrimSpace(email)
	if e == "" || utf8.RuneCountInString(e) > MaxEmailLen {
		return fmt.Errorf("email must be 1-%d characters", MaxEmailLen)
	}
	if !strings.Contains(e, "@") || strings.ContainsAny(e, " \t\r\n") {
		return fmt.Errorf("email must be a valid address")
	}
	return nil
}

// ValidatePasswordHash rejects empty hashes.
func ValidatePasswordHash(hash string) error {
	if hash == "" {
		return fmt.Errorf("password hash must not be empty")
	}
	return nil
}

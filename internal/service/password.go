package service

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"

	"github.com/metamin44/go-task-management-api/internal/domain"
)

// Password policy (documented service decision; not a schema constraint).
const (
	MinPasswordLen = 8
	MaxPasswordLen = 128
)

// ValidatePassword enforces length bounds. Errors never echo the password.
func ValidatePassword(pw string) error {
	n := utf8.RuneCountInString(pw)
	if n < MinPasswordLen || n > MaxPasswordLen {
		return fmt.Errorf("password must be %d-%d characters: %w", MinPasswordLen, MaxPasswordLen, domain.ErrInvalid)
	}
	return nil
}

// PasswordHasher abstracts hashing for injectability/testability.
type PasswordHasher interface {
	Hash(password string) (encoded string, err error)
	Verify(encoded, password string) (bool, error)
}

// Argon2idHasher implements PasswordHasher with OWASP-recommended parameters
// (t=1, m=64MiB, p=4, 32-byte key, 16-byte salt) in PHC string format.
type Argon2idHasher struct {
	time, memory uint32
	threads      uint8
	keyLen       uint32
	saltLen      int
}

var _ PasswordHasher = (*Argon2idHasher)(nil)

// NewArgon2idHasher returns the production-parameter hasher.
func NewArgon2idHasher() *Argon2idHasher {
	return &Argon2idHasher{time: 1, memory: 64 * 1024, threads: 4, keyLen: 32, saltLen: 16}
}

// Hash produces a PHC-format argon2id encoded hash with a fresh random salt.
func (h *Argon2idHasher) Hash(password string) (string, error) {
	salt := make([]byte, h.saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("password hash: salt generation failed")
	}
	key := argon2.IDKey([]byte(password), salt, h.time, h.memory, h.threads, h.keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, h.memory, h.time, h.threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// Verify recomputes the key from the encoded parameters and compares in
// constant time. Malformed encodings return an error; mismatches return
// (false, nil). Error messages never contain hash material or the password.
func (h *Argon2idHasher) Verify(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, fmt.Errorf("password verify: unsupported hash format")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, fmt.Errorf("password verify: unsupported hash version")
	}
	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false, fmt.Errorf("password verify: invalid hash parameters")
	}
	if memory == 0 || time == 0 || threads == 0 {
		return false, fmt.Errorf("password verify: invalid hash parameters")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return false, fmt.Errorf("password verify: invalid hash encoding")
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) == 0 {
		return false, fmt.Errorf("password verify: invalid hash encoding")
	}
	other := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(key)))
	return subtle.ConstantTimeCompare(key, other) == 1, nil
}

package domain

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// UUID is an RFC 4122 UUID stored as 16 bytes. pgx/v5 encodes and decodes it
// natively through its underlying [16]byte type; no external UUID library is
// needed.
type UUID [16]byte

// NewUUID returns a random version-4 UUID. crypto/rand.Read is guaranteed to
// succeed on supported platforms (Go >= 1.24).
func NewUUID() UUID {
	var u UUID
	_, _ = rand.Read(u[:])
	u[6] = (u[6] & 0x0f) | 0x40 // version 4
	u[8] = (u[8] & 0x3f) | 0x80 // RFC 4122 variant
	return u
}

// ParseUUID parses the canonical 8-4-4-4-12 hexadecimal form. Error messages
// never echo the supplied input.
func ParseUUID(s string) (UUID, error) {
	var u UUID
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return u, fmt.Errorf("invalid UUID format")
	}
	b, err := hex.DecodeString(s[0:8] + s[9:13] + s[14:18] + s[19:23] + s[24:36])
	if err != nil {
		return u, fmt.Errorf("invalid UUID hexadecimal")
	}
	copy(u[:], b)
	return u, nil
}

// String returns the canonical 8-4-4-4-12 representation.
func (u UUID) String() string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

// IsZero reports whether u is the zero UUID.
func (u UUID) IsZero() bool { return u == UUID{} }

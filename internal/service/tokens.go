package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/metamin44/go-task-management-api/internal/domain"
)

// TokenClaims is the validated access-token payload.
type TokenClaims struct {
	UserID    domain.UUID
	ExpiresAt time.Time
}

// AccessTokenManager issues and validates access tokens (distinct type and
// rules from opaque refresh tokens). Injectable for tests.
type AccessTokenManager interface {
	Issue(userID domain.UUID, now time.Time) (string, error)
	Validate(token string, now time.Time) (*TokenClaims, error)
}

// JWTAccessTokens implements AccessTokenManager with HS256-signed JWTs
// carrying typ=access, sub (user UUID), iat, exp.
type JWTAccessTokens struct {
	secret []byte
	ttl    time.Duration
}

var _ AccessTokenManager = (*JWTAccessTokens)(nil)

// MinAccessSecretLen is the minimum signing-secret length (256 bits).
const MinAccessSecretLen = 32

// NewJWTAccessTokens validates the secret and TTL, then returns the manager.
// The secret is injected by the composition root; it is never logged.
func NewJWTAccessTokens(secret []byte, ttl time.Duration) (*JWTAccessTokens, error) {
	if len(secret) < MinAccessSecretLen {
		return nil, fmt.Errorf("access token secret must be at least %d bytes: %w", MinAccessSecretLen, domain.ErrInvalid)
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("access token TTL must be positive: %w", domain.ErrInvalid)
	}
	return &JWTAccessTokens{secret: secret, ttl: ttl}, nil
}

type accessClaims struct {
	Type string `json:"typ"`
	jwt.RegisteredClaims
}

// Issue signs a new access token for userID.
func (m *JWTAccessTokens) Issue(userID domain.UUID, now time.Time) (string, error) {
	claims := accessClaims{
		Type: "access",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(m.ttl)),
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
	if err != nil {
		return "", fmt.Errorf("access token: signing failed")
	}
	return signed, nil
}

// Validate enforces signature, HS256-only algorithm, expiry, and the access
// token type. Refresh tokens (opaque, unsigned) always fail here.
func (m *JWTAccessTokens) Validate(token string, now time.Time) (*TokenClaims, error) {
	var claims accessClaims
	parsed, err := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		return m.secret, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(func() time.Time { return now }),
	)
	if err != nil || !parsed.Valid {
		return nil, fmt.Errorf("access token invalid: %w", ErrUnauthorized)
	}
	if claims.Type != "access" {
		return nil, fmt.Errorf("wrong token type: %w", ErrUnauthorized)
	}
	uid, err := domain.ParseUUID(claims.Subject)
	if err != nil {
		return nil, fmt.Errorf("access token subject invalid: %w", ErrUnauthorized)
	}
	return &TokenClaims{UserID: uid, ExpiresAt: claims.ExpiresAt.Time}, nil
}

// newRefreshToken generates a 256-bit random opaque token (base64url) and its
// SHA-256 digest. Only the digest is ever persisted; the raw value is returned
// to the client once. Hash input is the encoded token string as presented.
func newRefreshToken() (raw string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("refresh token: generation failed")
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(raw))
	return raw, sum[:], nil
}

// hashRefreshToken computes the stored digest of a presented token.
func hashRefreshToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

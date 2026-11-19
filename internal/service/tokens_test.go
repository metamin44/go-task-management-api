package service

import (
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/metamin44/go-task-management-api/internal/domain"
)

var testSecret = []byte("test-secret-0123456789-0123456789-abcdef") // dummy, 38 bytes

func newTestJWT(t *testing.T, ttl time.Duration) *JWTAccessTokens {
	t.Helper()
	m, err := NewJWTAccessTokens(testSecret, ttl)
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	return m
}

func TestJWTRoundTrip(t *testing.T) {
	m := newTestJWT(t, 15*time.Minute)
	uid := domain.NewUUID()
	now := time.Now()
	token, err := m.Issue(uid, now)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	claims, err := m.Validate(token, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if claims.UserID != uid {
		t.Error("claims must carry the user ID")
	}
	// JWT NumericDate truncates to whole seconds.
	if !claims.ExpiresAt.Equal(now.Add(15 * time.Minute).Truncate(time.Second)) {
		t.Error("expiry must equal issue time + TTL (second precision)")
	}
}

func TestJWTRejections(t *testing.T) {
	m := newTestJWT(t, 15*time.Minute)
	uid := domain.NewUUID()
	now := time.Now()
	token, _ := m.Issue(uid, now)

	cases := []struct {
		name  string
		token string
		now   time.Time
	}{
		{"expired", token, now.Add(16 * time.Minute)},
		{"opaque_refresh_style", "some-opaque-refresh-token", now},
		{"garbage", "not.a.jwt", now},
		{"empty", "", now},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := m.Validate(tc.token, tc.now); !errors.Is(err, ErrUnauthorized) {
				t.Errorf("must reject with ErrUnauthorized, got %v", err)
			}
		})
	}

	t.Run("wrong_secret", func(t *testing.T) {
		other, err := NewJWTAccessTokens([]byte("another-secret-0123456789-0123456789-x"), 15*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := other.Validate(token, now); !errors.Is(err, ErrUnauthorized) {
			t.Error("token signed with a different secret must be rejected")
		}
	})

	t.Run("wrong_type_claim", func(t *testing.T) {
		claims := accessClaims{
			Type: "refresh",
			RegisteredClaims: jwt.RegisteredClaims{
				Subject:   uid.String(),
				ExpiresAt: jwt.NewNumericDate(now.Add(15 * time.Minute)),
			},
		}
		signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(testSecret)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.Validate(signed, now); !errors.Is(err, ErrUnauthorized) {
			t.Error("typ=refresh claim must be rejected by access validation")
		}
	})

	t.Run("alg_none", func(t *testing.T) {
		claims := accessClaims{
			Type: "access",
			RegisteredClaims: jwt.RegisteredClaims{
				Subject:   uid.String(),
				ExpiresAt: jwt.NewNumericDate(now.Add(15 * time.Minute)),
			},
		}
		signed, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).SignedString(jwt.UnsafeAllowNoneSignatureType)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.Validate(signed, now); !errors.Is(err, ErrUnauthorized) {
			t.Error("alg=none must be rejected")
		}
	})
}

func TestJWTConstructorValidation(t *testing.T) {
	if _, err := NewJWTAccessTokens([]byte("too-short"), 15*time.Minute); !errors.Is(err, domain.ErrInvalid) {
		t.Error("short secret must be rejected")
	}
	if _, err := NewJWTAccessTokens(testSecret, 0); !errors.Is(err, domain.ErrInvalid) {
		t.Error("non-positive TTL must be rejected")
	}
}

func TestRefreshTokenGeneration(t *testing.T) {
	raw1, hash1, err := newRefreshToken()
	if err != nil {
		t.Fatal(err)
	}
	raw2, hash2, err := newRefreshToken()
	if err != nil {
		t.Fatal(err)
	}
	if raw1 == raw2 {
		t.Error("tokens must be random")
	}
	if len(hash1) != 32 || len(hash2) != 32 {
		t.Error("hashes must be SHA-256 (32 bytes)")
	}
	if string(hash1) == raw1 || string(hash2) == raw2 {
		t.Error("hash must differ from the raw token")
	}
	if len(raw1) != 43 { // base64url of 32 bytes, no padding
		t.Errorf("raw token length = %d, want 43", len(raw1))
	}
}

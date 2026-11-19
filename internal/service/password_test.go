package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/metamin44/go-task-management-api/internal/domain"
)

func TestArgon2idRoundTrip(t *testing.T) {
	h := NewArgon2idHasher()
	encoded, err := h.Hash("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=65536,t=1,p=4$") {
		t.Errorf("unexpected PHC format: %s", encoded)
	}
	if strings.Contains(encoded, "correct horse") {
		t.Error("encoded hash must not contain the plaintext")
	}
	ok, err := h.Verify(encoded, "correct horse battery staple")
	if err != nil || !ok {
		t.Errorf("verify correct password: ok=%v err=%v", ok, err)
	}
	ok, err = h.Verify(encoded, "wrong password")
	if err != nil || ok {
		t.Errorf("verify wrong password must be (false, nil): ok=%v err=%v", ok, err)
	}
}

func TestArgon2idUniqueSalts(t *testing.T) {
	h := NewArgon2idHasher()
	a, _ := h.Hash("same-password-123")
	b, _ := h.Hash("same-password-123")
	if a == b {
		t.Error("identical passwords must produce different encodings (random salt)")
	}
}

func TestArgon2idMalformedEncodings(t *testing.T) {
	h := NewArgon2idHasher()
	for _, enc := range []string{
		"",
		"plaintext",
		"$argon2i$v=19$m=65536,t=1,p=4$c2FsdA$a2V5",
		"$argon2id$v=18$m=65536,t=1,p=4$c2FsdA$a2V5",
		"$argon2id$v=19$m=0,t=1,p=4$c2FsdA$a2V5",
		"$argon2id$v=19$m=65536,t=1,p=4$!!!$a2V5",
		"$argon2id$v=19$m=65536,t=1,p=4$c2FsdA$!!!",
	} {
		if _, err := h.Verify(enc, "password123"); err == nil {
			t.Error("malformed encoding must return an error")
		}
	}
}

func TestValidatePasswordBounds(t *testing.T) {
	cases := []struct {
		name    string
		pw      string
		wantErr bool
	}{
		{"7_chars", strings.Repeat("a", 7), true},
		{"8_chars", strings.Repeat("a", 8), false},
		{"128_chars", strings.Repeat("a", 128), false},
		{"129_chars", strings.Repeat("a", 129), true},
		{"empty", "", true},
		{"multibyte_8_runes", strings.Repeat("é", 8), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePassword(tc.pw)
			if tc.wantErr != (err != nil) {
				t.Errorf("ValidatePassword() err=%v, wantErr=%v", err, tc.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), tc.pw) && tc.pw != "" {
				t.Error("error must never echo the password")
			}
		})
	}
	if err := ValidatePassword("aaaaaaaa"); !errors.Is(err, domain.ErrInvalid) && err != nil {
		t.Errorf("validation errors should wrap domain.ErrInvalid, got %v", err)
	}
}

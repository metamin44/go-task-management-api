package domain

import (
	"strings"
	"testing"
)

func TestNewUUID(t *testing.T) {
	u := NewUUID()
	if u.IsZero() {
		t.Fatal("NewUUID returned the zero UUID")
	}
	if got := u[6] >> 4; got != 4 {
		t.Errorf("version nibble = %d, want 4", got)
	}
	if got := u[8] >> 6; got != 2 {
		t.Errorf("variant bits = %d, want 2 (RFC 4122)", got)
	}
	if NewUUID() == u {
		t.Error("two NewUUID calls returned the same value")
	}
	s := u.String()
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		t.Errorf("String() = %q, want canonical 8-4-4-4-12 form", s)
	}
}

func TestParseUUIDRoundTrip(t *testing.T) {
	for i := 0; i < 10; i++ {
		u := NewUUID()
		parsed, err := ParseUUID(u.String())
		if err != nil {
			t.Fatalf("ParseUUID(String()) failed: %v", err)
		}
		if parsed != u {
			t.Fatal("round trip changed the UUID")
		}
	}
}

func TestParseUUIDKnownValue(t *testing.T) {
	const canonical = "01234567-89ab-cdef-0123-456789abcdef"
	u, err := ParseUUID(canonical)
	if err != nil {
		t.Fatalf("parse known UUID: %v", err)
	}
	if u.String() != canonical {
		t.Errorf("String() = %q, want %q", u.String(), canonical)
	}
	if u[0] != 0x01 || u[15] != 0xef {
		t.Error("byte order mismatch after parse")
	}
}

func TestParseUUIDInvalid(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"too_short", "01234567-89ab-cdef-0123-456789abcde"},
		{"too_long", "01234567-89ab-cdef-0123-456789abcdef0"},
		{"no_hyphens", "0123456789abcdef0123456789abcdef"},
		{"misplaced_hyphens", "012345678-9ab-cdef-0123-456789abcdef"},
		{"bad_hex", "01234567-89ab-cdef-0123-456789abcdeg"},
		{"uppercase_hex_ok_check", "zzzzzzzz-zzzz-zzzz-zzzz-zzzzzzzzzzzz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseUUID(tc.input); err == nil {
				t.Errorf("expected error for invalid UUID input")
			}
		})
	}
}

func TestParseUUIDErrorsNeverEchoInput(t *testing.T) {
	secretish := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	_, err := ParseUUID(strings.Replace(secretish, "a", "g", 1))
	if err == nil {
		t.Fatal("expected parse error")
	}
	if strings.Contains(err.Error(), "g") || strings.Contains(err.Error(), secretish) {
		t.Error("parse error must not echo the supplied input")
	}
}

func TestUUIDIsZero(t *testing.T) {
	var zero UUID
	if !zero.IsZero() {
		t.Error("zero UUID must report IsZero")
	}
	if NewUUID().IsZero() {
		t.Error("random UUID must not report IsZero")
	}
}

package domain

import (
	"strings"
	"testing"
	"time"
)

func TestValidateTextLimits(t *testing.T) {
	cases := []struct {
		name    string
		fn      func(string) error
		value   string
		wantErr bool
	}{
		{"user_name_ok", ValidateUserName, "Alice", false},
		{"user_name_empty", ValidateUserName, "", true},
		{"user_name_spaces", ValidateUserName, "   ", true},
		{"user_name_tabs_newlines", ValidateUserName, " \t\n\r ", true},
		{"user_name_nbsp_only", ValidateUserName, "  ", true},
		{"user_name_max_100", ValidateUserName, strings.Repeat("a", 100), false},
		{"user_name_101", ValidateUserName, strings.Repeat("a", 101), true},
		{"user_name_100_runes_multibyte", ValidateUserName, strings.Repeat("é", 100), false},
		{"user_name_101_runes_multibyte", ValidateUserName, strings.Repeat("é", 101), true},
		{"user_name_padded_ok", ValidateUserName, " Alice ", false},
		{"project_name_max_150", ValidateProjectName, strings.Repeat("p", 150), false},
		{"project_name_151", ValidateProjectName, strings.Repeat("p", 151), true},
		{"project_name_blank", ValidateProjectName, "\t\n", true},
		{"task_title_max_200", ValidateTaskTitle, strings.Repeat("t", 200), false},
		{"task_title_201", ValidateTaskTitle, strings.Repeat("t", 201), true},
		{"task_title_blank", ValidateTaskTitle, " \t ", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.fn(tc.value)
			if tc.wantErr && err == nil {
				t.Error("expected validation error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected validation error: %v", err)
			}
		})
	}
}

func TestValidateEmail(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"valid", "user@example.com", false},
		{"valid_padded", "  user@example.com  ", false},
		{"empty", "", true},
		{"blank", "   ", true},
		{"no_at", "userexample.com", true},
		{"internal_space", "us er@example.com", true},
		{"internal_tab", "user\t@example.com", true},
		{"max_254", strings.Repeat("a", 242) + "@example.com", false},
		{"over_254", strings.Repeat("a", 243) + "@example.com", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateEmail(tc.value)
			if tc.wantErr != (err != nil) {
				t.Errorf("ValidateEmail() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestNormalizeEmail(t *testing.T) {
	if got := NormalizeEmail("  MiXeD@Example.COM "); got != "mixed@example.com" {
		t.Errorf("NormalizeEmail = %q, want mixed@example.com", got)
	}
}

func TestValidatePasswordHash(t *testing.T) {
	if err := ValidatePasswordHash(""); err == nil {
		t.Error("empty hash must be rejected")
	}
	if err := ValidatePasswordHash("$argon2id$v=19$encoded"); err != nil {
		t.Errorf("non-empty hash rejected: %v", err)
	}
}

func TestValidateTokenHash(t *testing.T) {
	cases := []struct {
		name    string
		n       int
		wantErr bool
	}{
		{"31_bytes", 31, true},
		{"32_bytes", 32, false},
		{"33_bytes", 33, true},
		{"empty", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTokenHash(make([]byte, tc.n))
			if tc.wantErr != (err != nil) {
				t.Errorf("ValidateTokenHash(len=%d) error = %v, wantErr %v", tc.n, err, tc.wantErr)
			}
		})
	}
}

func TestRoleValid(t *testing.T) {
	for _, r := range []Role{RoleOwner, RoleAdmin, RoleMember} {
		if !r.Valid() {
			t.Errorf("role %q must be valid", r)
		}
	}
	for _, r := range []Role{"", "superadmin", "OWNER", "guest"} {
		if r.Valid() {
			t.Errorf("role %q must be invalid", r)
		}
	}
}

func TestStatusValid(t *testing.T) {
	for _, s := range []Status{StatusTodo, StatusInProgress, StatusDone} {
		if !s.Valid() {
			t.Errorf("status %q must be valid", s)
		}
	}
	for _, s := range []Status{"", "blocked", "TODO", "archived"} {
		if s.Valid() {
			t.Errorf("status %q must be invalid", s)
		}
	}
}

func TestPriorityValid(t *testing.T) {
	for _, p := range []Priority{PriorityLow, PriorityMedium, PriorityHigh, PriorityUrgent} {
		if !p.Valid() {
			t.Errorf("priority %q must be valid", p)
		}
	}
	for _, p := range []Priority{"", "critical", "LOW", "none"} {
		if p.Valid() {
			t.Errorf("priority %q must be invalid", p)
		}
	}
}

func TestRefreshSessionStateHelpers(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	active := &RefreshSession{ExpiresAt: future}
	if active.IsRevoked() {
		t.Error("active session must not report revoked")
	}
	if active.IsExpired(now) {
		t.Error("future expiry must not report expired")
	}

	revoked := &RefreshSession{ExpiresAt: future, RevokedAt: &past}
	if !revoked.IsRevoked() {
		t.Error("revoked session must report revoked")
	}

	expired := &RefreshSession{ExpiresAt: past}
	if !expired.IsExpired(now) {
		t.Error("past expiry must report expired")
	}
	atNow := &RefreshSession{ExpiresAt: now}
	if !atNow.IsExpired(now) {
		t.Error("expiry exactly at now must count as expired")
	}
}

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/metamin44/go-task-management-api/internal/service"
)

// TestForgotPasswordGenericResponse proves the acknowledgement is identical
// whether the account exists or not (the service returns nil in both cases),
// so the response carries no account-existence signal.
func TestForgotPasswordGenericResponse(t *testing.T) {
	env := newTestEnv(t, Options{})
	var gotEmail string
	env.reset.forgotFn = func(_ context.Context, email string) error {
		gotEmail = email
		return nil // service contract: nil for both known and unknown emails
	}

	known := env.do(t, http.MethodPost, "/api/v1/auth/forgot-password", map[string]string{"email": "alice@example.com"}, "")
	unknown := env.do(t, http.MethodPost, "/api/v1/auth/forgot-password", map[string]string{"email": "nobody@example.com"}, "")
	assertStatus(t, known, http.StatusAccepted)
	assertStatus(t, unknown, http.StatusAccepted)
	if known.Body.String() != unknown.Body.String() {
		t.Errorf("responses must be identical for known/unknown email:\nknown=%s\nunknown=%s", known.Body.String(), unknown.Body.String())
	}
	if !strings.Contains(known.Body.String(), "If an account exists") {
		t.Errorf("expected generic acknowledgement, got %s", known.Body.String())
	}
	if gotEmail != "nobody@example.com" {
		t.Errorf("service must receive the submitted email, got %q", gotEmail)
	}
}

func TestForgotPasswordValidation(t *testing.T) {
	env := newTestEnv(t, Options{})
	env.reset.forgotFn = func(context.Context, string) error {
		t.Error("service must not be called on invalid input")
		return nil
	}
	cases := []struct {
		name string
		body any
		raw  string
		ct   string
		want int
	}{
		{"missing_email", map[string]string{}, "", "", http.StatusBadRequest},
		{"blank_email", map[string]string{"email": "   "}, "", "", http.StatusBadRequest},
		{"bad_email", map[string]string{"email": "not-an-email"}, "", "", http.StatusBadRequest},
		{"unknown_field", nil, `{"email":"a@b.co","x":"y"}`, "", http.StatusBadRequest},
		{"malformed_json", nil, `{"email":`, "", http.StatusBadRequest},
		{"empty_body", nil, "", "", http.StatusBadRequest},
		{"wrong_content_type", nil, `{"email":"a@b.co"}`, "text/plain", http.StatusUnsupportedMediaType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var rec *httptest.ResponseRecorder
			if tc.body != nil {
				rec = env.do(t, http.MethodPost, "/api/v1/auth/forgot-password", tc.body, "")
			} else {
				ct := tc.ct
				if ct == "" {
					ct = "application/json"
				}
				rec = env.doRaw(t, http.MethodPost, "/api/v1/auth/forgot-password", strings.NewReader(tc.raw), ct, "")
			}
			assertStatus(t, rec, tc.want)
		})
	}
}

func TestForgotPasswordRateLimited(t *testing.T) {
	env := newTestEnv(t, Options{}).withRateLimit(time.Second)
	env.reset.forgotFn = func(context.Context, string) error { return nil }
	body := map[string]string{"email": "alice@example.com"}

	// Burst of 5 allowed, then the shared per-IP bucket denies with 429.
	for i := 0; i < authRateBurst; i++ {
		rec := env.do(t, http.MethodPost, "/api/v1/auth/forgot-password", body, "")
		assertStatus(t, rec, http.StatusAccepted)
	}
	rec := env.do(t, http.MethodPost, "/api/v1/auth/forgot-password", body, "")
	assertStatus(t, rec, http.StatusTooManyRequests)
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 must include a Retry-After header")
	}
	if decodeEnvelope(t, rec).Error.Code != "rate_limited" {
		t.Error("expected rate_limited code")
	}
}

func TestResetPasswordSuccess(t *testing.T) {
	env := newTestEnv(t, Options{})
	var gotToken, gotPassword string
	env.reset.resetFn = func(_ context.Context, token, newPassword string) error {
		gotToken, gotPassword = token, newPassword
		return nil
	}
	rec := env.do(t, http.MethodPost, "/api/v1/auth/reset-password", map[string]string{
		"token": "reset-token-value", "new_password": "newpassword456",
	}, "")
	assertStatus(t, rec, http.StatusNoContent)
	if gotToken != "reset-token-value" || gotPassword != "newpassword456" {
		t.Error("service must receive the submitted token and new password")
	}
	assertNoLeak(t, rec.Body.String(), env.logs.String(), "reset-token-value", "newpassword456")
}

func TestResetPasswordUnauthorized(t *testing.T) {
	env := newTestEnv(t, Options{})
	env.reset.resetFn = func(context.Context, string, string) error {
		return service.ErrUnauthorized // unknown/expired/reused all map here
	}
	rec := env.do(t, http.MethodPost, "/api/v1/auth/reset-password", map[string]string{
		"token": "bad-token", "new_password": "newpassword456",
	}, "")
	assertStatus(t, rec, http.StatusUnauthorized)
	if decodeEnvelope(t, rec).Error.Code != "unauthorized" {
		t.Error("expected unauthorized code")
	}
}

func TestResetPasswordValidation(t *testing.T) {
	env := newTestEnv(t, Options{})
	env.reset.resetFn = func(context.Context, string, string) error {
		t.Error("service must not be called on invalid input")
		return nil
	}
	cases := []struct {
		name string
		body any
		want int
	}{
		{"missing_token", map[string]string{"new_password": "newpassword456"}, http.StatusBadRequest},
		{"missing_password", map[string]string{"token": "t"}, http.StatusBadRequest},
		{"weak_password", map[string]string{"token": "t", "new_password": "short"}, http.StatusBadRequest},
		{"unknown_field", map[string]string{"token": "t", "new_password": "newpassword456", "x": "y"}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.do(t, http.MethodPost, "/api/v1/auth/reset-password", tc.body, "")
			assertStatus(t, rec, tc.want)
		})
	}
}

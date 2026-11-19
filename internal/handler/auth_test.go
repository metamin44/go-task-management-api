package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/metamin44/go-task-management-api/internal/domain"
	"github.com/metamin44/go-task-management-api/internal/service"
)

func TestRegisterSuccess(t *testing.T) {
	env := newTestEnv(t, Options{})
	var gotName, gotEmail, gotPassword string
	env.auth.registerFn = func(_ context.Context, name, email, password string) (*domain.User, error) {
		gotName, gotEmail, gotPassword = name, email, password
		return sampleUser(), nil
	}
	rec := env.do(t, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"name": "Test User", "email": "test@example.com", "password": "password123",
	}, "")
	assertStatus(t, rec, http.StatusCreated)
	if gotName != "Test User" || gotEmail != "test@example.com" || gotPassword != "password123" {
		t.Error("service must receive the submitted credentials")
	}
	body := rec.Body.String()
	if strings.Contains(body, "password") {
		t.Errorf("register response must not contain password fields: %s", body)
	}
	if !strings.Contains(body, `"id"`) || !strings.Contains(body, `"email"`) {
		t.Errorf("unexpected user response: %s", body)
	}
}

func TestRegisterValidation(t *testing.T) {
	env := newTestEnv(t, Options{})
	env.auth.registerFn = func(context.Context, string, string, string) (*domain.User, error) {
		t.Error("service must not be called on invalid input")
		return nil, nil
	}
	cases := []struct {
		name string
		body any
		raw  string
		ct   string
		want int
	}{
		{"missing_name", map[string]string{"email": "a@b.co", "password": "password123"}, "", "", http.StatusBadRequest},
		{"missing_email", map[string]string{"name": "A", "password": "password123"}, "", "", http.StatusBadRequest},
		{"missing_password", map[string]string{"name": "A", "email": "a@b.co"}, "", "", http.StatusBadRequest},
		{"blank_name", map[string]string{"name": "   ", "email": "a@b.co", "password": "password123"}, "", "", http.StatusBadRequest},
		{"bad_email", map[string]string{"name": "A", "email": "not-an-email", "password": "password123"}, "", "", http.StatusBadRequest},
		{"short_password", map[string]string{"name": "A", "email": "a@b.co", "password": "short"}, "", "", http.StatusBadRequest},
		{"unknown_field", nil, `{"name":"A","email":"a@b.co","password":"password123","role":"admin"}`, "", http.StatusBadRequest},
		{"malformed_json", nil, `{"name":`, "", http.StatusBadRequest},
		{"trailing_data", nil, `{"name":"A","email":"a@b.co","password":"password123"}{}`, "", http.StatusBadRequest},
		{"empty_body", nil, "", "", http.StatusBadRequest},
		{"wrong_content_type", nil, `{"name":"A","email":"a@b.co","password":"password123"}`, "text/plain", http.StatusUnsupportedMediaType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var rec *httptest.ResponseRecorder
			if tc.body != nil {
				rec = env.do(t, http.MethodPost, "/api/v1/auth/register", tc.body, "")
			} else {
				ct := tc.ct
				if ct == "" {
					ct = "application/json"
				}
				rec = env.doRaw(t, http.MethodPost, "/api/v1/auth/register", strings.NewReader(tc.raw), ct, "")
			}
			assertStatus(t, rec, tc.want)
			envelope := decodeEnvelope(t, rec)
			if envelope.RequestID == "" {
				t.Error("error envelope must carry request_id")
			}
		})
	}
}

func TestRegisterConflictIsSafe(t *testing.T) {
	env := newTestEnv(t, Options{})
	env.auth.registerFn = func(context.Context, string, string, string) (*domain.User, error) {
		return nil, fmt.Errorf("users: create: %w (constraint users_email_lower_uidx)", domain.ErrConflict)
	}
	rec := env.do(t, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"name": "A", "email": "a@b.co", "password": "password123",
	}, "")
	assertStatus(t, rec, http.StatusConflict)
	envelope := decodeEnvelope(t, rec)
	if envelope.Error.Code != "conflict" {
		t.Errorf("code = %s, want conflict", envelope.Error.Code)
	}
	assertNoLeak(t, rec.Body.String(), env.logs.String(), "users_email_lower_uidx", "constraint", "password123")
}

func TestLogin(t *testing.T) {
	env := newTestEnv(t, Options{})
	env.auth.loginFn = func(_ context.Context, email, password string) (*service.TokenPair, error) {
		if email == "test@example.com" && password == "password123" {
			return &service.TokenPair{AccessToken: "access-xyz", RefreshToken: "refresh-xyz"}, nil
		}
		return nil, fmt.Errorf("login: %w", service.ErrUnauthorized)
	}

	rec := env.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": "test@example.com", "password": "password123",
	}, "")
	assertStatus(t, rec, http.StatusOK)
	body := rec.Body.String()
	if !strings.Contains(body, `"access_token":"access-xyz"`) || !strings.Contains(body, `"refresh_token":"refresh-xyz"`) || !strings.Contains(body, `"token_type":"Bearer"`) {
		t.Errorf("unexpected token response: %s", body)
	}

	rec = env.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": "test@example.com", "password": "wrong-password",
	}, "")
	assertStatus(t, rec, http.StatusUnauthorized)
	assertNoLeak(t, rec.Body.String(), env.logs.String(), "wrong-password", "test@example.com")

	rec = env.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"email": "test@example.com"}, "")
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestRefreshOutcomes(t *testing.T) {
	env := newTestEnv(t, Options{})
	env.auth.refreshFn = func(_ context.Context, token string) (*service.TokenPair, error) {
		switch token {
		case "good":
			return &service.TokenPair{AccessToken: "a2", RefreshToken: "r2"}, nil
		case "replayed-secret-value":
			return nil, fmt.Errorf("refresh: %w", service.ErrTokenReused)
		default:
			return nil, fmt.Errorf("refresh: %w", service.ErrUnauthorized)
		}
	}

	rec := env.do(t, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refresh_token": "good"}, "")
	assertStatus(t, rec, http.StatusOK)

	rec = env.do(t, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refresh_token": "replayed-secret-value"}, "")
	assertStatus(t, rec, http.StatusUnauthorized)
	if decodeEnvelope(t, rec).Error.Code != "token_reused" {
		t.Error("reuse must surface code token_reused")
	}
	assertNoLeak(t, rec.Body.String(), env.logs.String(), "replayed-secret-value")

	rec = env.do(t, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refresh_token": "unknown"}, "")
	assertStatus(t, rec, http.StatusUnauthorized)

	rec = env.do(t, http.MethodPost, "/api/v1/auth/refresh", map[string]string{}, "")
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestLogout(t *testing.T) {
	env := newTestEnv(t, Options{})
	env.auth.logoutFn = func(_ context.Context, token string) error {
		if token == "known" {
			return nil
		}
		return fmt.Errorf("logout: %w", service.ErrUnauthorized)
	}
	rec := env.do(t, http.MethodPost, "/api/v1/auth/logout", map[string]string{"refresh_token": "known"}, "")
	assertStatus(t, rec, http.StatusNoContent)
	rec = env.do(t, http.MethodPost, "/api/v1/auth/logout", map[string]string{"refresh_token": "unknown"}, "")
	assertStatus(t, rec, http.StatusUnauthorized)
	rec = env.do(t, http.MethodPost, "/api/v1/auth/logout", map[string]string{}, "")
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestLogoutAll(t *testing.T) {
	env := newTestEnv(t, Options{})
	var gotUID domain.UUID
	env.auth.logoutAllFn = func(_ context.Context, uid domain.UUID) (int64, error) {
		gotUID = uid
		return 3, nil
	}
	rec := env.do(t, http.MethodPost, "/api/v1/auth/logout-all", nil, "")
	assertStatus(t, rec, http.StatusUnauthorized)

	rec = env.do(t, http.MethodPost, "/api/v1/auth/logout-all", nil, testToken)
	assertStatus(t, rec, http.StatusOK)
	if gotUID != testUID {
		t.Error("actor must come from the token, not the request")
	}
	if !strings.Contains(rec.Body.String(), `"revoked_sessions":3`) {
		t.Errorf("unexpected body: %s", rec.Body.String())
	}
}

func TestInternalErrorNeverLeaks(t *testing.T) {
	env := newTestEnv(t, Options{})
	env.auth.loginFn = func(context.Context, string, string) (*service.TokenPair, error) {
		return nil, errors.New("dial tcp 127.0.0.1:5432: password=supersecret sql: SELECT password_hash FROM users")
	}
	rec := env.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": "test@example.com", "password": "password123",
	}, "")
	assertStatus(t, rec, http.StatusInternalServerError)
	envelope := decodeEnvelope(t, rec)
	if envelope.Error.Code != "internal_error" || envelope.Error.Message != "internal server error" {
		t.Errorf("unexpected 500 envelope: %+v", envelope)
	}
	assertNoLeak(t, rec.Body.String(), env.logs.String(), "supersecret", "SELECT", "password_hash", "5432", "password123")
}

func TestSerializationExhaustionMapsTo503(t *testing.T) {
	env := newTestEnv(t, Options{})
	env.auth.refreshFn = func(context.Context, string) (*service.TokenPair, error) {
		return nil, fmt.Errorf("refresh: %w", domain.ErrSerialization)
	}
	rec := env.do(t, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refresh_token": "x"}, "")
	assertStatus(t, rec, http.StatusServiceUnavailable)
}

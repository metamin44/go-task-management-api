package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"

	"github.com/metamin44/go-task-management-api/internal/domain"
	"github.com/metamin44/go-task-management-api/internal/service"
)

func TestAuthHeaderRejection(t *testing.T) {
	env := newTestEnv(t, Options{})
	env.users.profileFn = func(context.Context, domain.UUID) (*domain.User, error) {
		t.Error("profile must not load for rejected tokens")
		return nil, nil
	}
	cases := []struct{ name, header string }{
		{"missing", ""},
		{"wrong_scheme", "Basic dXNlcjpwYXNz"},
		{"bare_bearer", "Bearer"},
		{"empty_token", "Bearer   "},
		{"invalid_token", "Bearer not-a-jwt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me", nil)
			if tc.header != "" {
				req.Header.Set(echo.HeaderAuthorization, tc.header)
			}
			rec := httptest.NewRecorder()
			env.e.ServeHTTP(rec, req)
			assertStatus(t, rec, http.StatusUnauthorized)
			if decodeEnvelope(t, rec).Error.Code != "unauthorized" {
				t.Error("expected code unauthorized")
			}
			assertNoLeak(t, rec.Body.String(), env.logs.String(), tc.header)
		})
	}
}

// realJWTEnv wires the middleware to the production JWT manager.
func realJWTEnv(t *testing.T, secret []byte, ttl time.Duration) (*testEnv, *service.JWTAccessTokens) {
	t.Helper()
	mgr, err := service.NewJWTAccessTokens(secret, ttl)
	if err != nil {
		t.Fatal(err)
	}
	env := newTestEnv(t, Options{})
	env.auth.validateFn = mgr.Validate
	return env, mgr
}

func TestRealJWTAcceptedAndActorLoaded(t *testing.T) {
	secret := bytes.Repeat([]byte("k"), 32)
	env, mgr := realJWTEnv(t, secret, 15*time.Minute)
	var gotUID domain.UUID
	env.users.profileFn = func(_ context.Context, uid domain.UUID) (*domain.User, error) {
		gotUID = uid
		return &domain.User{ID: uid, Name: "N", Email: "e@x.y"}, nil
	}
	token, err := mgr.Issue(testUID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rec := env.do(t, http.MethodGet, "/api/v1/users/me", nil, token)
	assertStatus(t, rec, http.StatusOK)
	if gotUID != testUID {
		t.Error("identity must be loaded from the validated token subject")
	}
	if strings.Contains(rec.Body.String(), "password_hash") {
		t.Error("profile response must never expose the hash field")
	}
}

func TestExpiredAndForeignAndWrongTypeJWTs(t *testing.T) {
	secret := bytes.Repeat([]byte("k"), 32)
	env, mgr := realJWTEnv(t, secret, 15*time.Minute)

	expired, err := mgr.Issue(testUID, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	foreignMgr, err := service.NewJWTAccessTokens(bytes.Repeat([]byte("z"), 32), 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := foreignMgr.Issue(testUID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Same secret, but typ=refresh: must be rejected as an access token.
	wrongType, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"typ": "refresh", "sub": testUID.String(),
		"exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString(secret)
	if err != nil {
		t.Fatal(err)
	}
	// alg=none must never be accepted.
	none := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
		"typ": "access", "sub": testUID.String(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	noneToken, err := none.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}

	for name, token := range map[string]string{
		"expired": expired, "foreign_secret": foreign, "wrong_type": wrongType, "alg_none": noneToken,
	} {
		t.Run(name, func(t *testing.T) {
			rec := env.do(t, http.MethodGet, "/api/v1/users/me", nil, token)
			assertStatus(t, rec, http.StatusUnauthorized)
			assertNoLeak(t, rec.Body.String(), env.logs.String(), token)
		})
	}
}

func TestDeletedAccountRejected(t *testing.T) {
	env := newTestEnv(t, Options{})
	env.users.profileFn = func(context.Context, domain.UUID) (*domain.User, error) {
		return nil, fmt.Errorf("users: get: %w", domain.ErrNotFound)
	}
	rec := env.do(t, http.MethodGet, "/api/v1/users/me", nil, testToken)
	assertStatus(t, rec, http.StatusUnauthorized)
}

func TestMassAssignmentRejected(t *testing.T) {
	env := newTestEnv(t, Options{})
	rec := env.doRaw(t, http.MethodPatch, "/api/v1/users/me",
		strings.NewReader(`{"name":"N","id":"`+domain.NewUUID().String()+`"}`), "application/json", testToken)
	assertStatus(t, rec, http.StatusBadRequest)
	rec = env.doRaw(t, http.MethodPost, "/api/v1/projects",
		strings.NewReader(`{"name":"P","created_by":"`+domain.NewUUID().String()+`"}`), "application/json", testToken)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestRequestID(t *testing.T) {
	env := newTestEnv(t, Options{})
	rec1 := env.do(t, http.MethodGet, "/health", nil, "")
	rec2 := env.do(t, http.MethodGet, "/health", nil, "")
	id1 := rec1.Header().Get(headerRequestID)
	id2 := rec2.Header().Get(headerRequestID)
	if id1 == "" || id2 == "" || id1 == id2 {
		t.Fatalf("request IDs must be present and unique: %q %q", id1, id2)
	}
	rec := env.do(t, http.MethodGet, "/api/v1/users/me", nil, "")
	assertStatus(t, rec, http.StatusUnauthorized)
	if decodeEnvelope(t, rec).RequestID != rec.Header().Get(headerRequestID) {
		t.Error("envelope request_id must match the response header")
	}
}

func TestPanicRecoveryIsSafe(t *testing.T) {
	env := newTestEnv(t, Options{})
	env.users.profileFn = func(context.Context, domain.UUID) (*domain.User, error) {
		panic("secret-panic-value password123")
	}
	rec := env.do(t, http.MethodGet, "/api/v1/users/me", nil, testToken)
	assertStatus(t, rec, http.StatusInternalServerError)
	envelope := decodeEnvelope(t, rec)
	if envelope.Error.Message != "internal server error" {
		t.Errorf("panic response must be static: %+v", envelope)
	}
	assertNoLeak(t, rec.Body.String(), env.logs.String(), "secret-panic-value", "password123", "goroutine", ".go:")
}

func TestCORSConservative(t *testing.T) {
	// Default: no CORS middleware at all.
	env := newTestEnv(t, Options{})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set(echo.HeaderOrigin, "https://app.example")
	rec := httptest.NewRecorder()
	env.e.ServeHTTP(rec, req)
	if rec.Header().Get(echo.HeaderAccessControlAllowOrigin) != "" {
		t.Error("no CORS headers may be sent without an explicit whitelist")
	}

	// Whitelisted origin only.
	env = newTestEnv(t, Options{AllowedOrigins: []string{"https://app.example"}})
	for _, origin := range []string{"https://app.example", "https://evil.example"} {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.Header.Set(echo.HeaderOrigin, origin)
		rec := httptest.NewRecorder()
		env.e.ServeHTTP(rec, req)
		got := rec.Header().Get(echo.HeaderAccessControlAllowOrigin)
		if origin == "https://app.example" && got != origin {
			t.Errorf("allowed origin must receive ACAO, got %q", got)
		}
		if origin == "https://evil.example" && got != "" {
			t.Error("disallowed origin must not receive ACAO")
		}
	}

	// Preflight from an allowed origin is answered without authentication.
	req = httptest.NewRequest(http.MethodOptions, "/api/v1/projects", nil)
	req.Header.Set(echo.HeaderOrigin, "https://app.example")
	req.Header.Set(echo.HeaderAccessControlRequestMethod, http.MethodPost)
	rec = httptest.NewRecorder()
	env.e.ServeHTTP(rec, req)
	assertStatus(t, rec, http.StatusNoContent)
	if rec.Header().Get(echo.HeaderAccessControlAllowOrigin) != "https://app.example" {
		t.Error("preflight must carry ACAO for the allowed origin")
	}
	if rec.Header().Get(echo.HeaderAccessControlAllowCredentials) == "true" {
		t.Error("credentials must never be allowed")
	}
}

func TestRouteErrorsUseEnvelope(t *testing.T) {
	env := newTestEnv(t, Options{})
	rec := env.do(t, http.MethodGet, "/no-such-route", nil, "")
	assertStatus(t, rec, http.StatusNotFound)
	if decodeEnvelope(t, rec).Error.Code != "not_found" {
		t.Error("unmatched route must use the error envelope")
	}
	// Wrong method outside the authenticated subtree: plain 405 envelope.
	rec = env.do(t, http.MethodDelete, "/health", nil, "")
	assertStatus(t, rec, http.StatusMethodNotAllowed)
	decodeEnvelope(t, rec)
	// Under /api/v1 the auth middleware guards unmatched requests too:
	// unauthenticated wrong-method calls get 401 (no route probing).
	// Authenticated ones resolve to a 404 envelope (Echo collapses method
	// mismatches to not-found inside middleware-bearing groups).
	rec = env.do(t, http.MethodPut, "/api/v1/auth/login", nil, "")
	assertStatus(t, rec, http.StatusUnauthorized)
	rec = env.do(t, http.MethodPut, "/api/v1/auth/login", nil, testToken)
	assertStatus(t, rec, http.StatusNotFound)
	decodeEnvelope(t, rec)
}

func TestBodyTooLarge(t *testing.T) {
	env := newTestEnv(t, Options{})
	huge := `{"name":"` + strings.Repeat("x", (1<<20)+100) + `"}`
	rec := env.doRaw(t, http.MethodPost, "/api/v1/auth/register", strings.NewReader(huge), "application/json", "")
	assertStatus(t, rec, http.StatusRequestEntityTooLarge)
}

func TestLogsContainMetadataOnly(t *testing.T) {
	env := newTestEnv(t, Options{})
	env.auth.loginFn = func(context.Context, string, string) (*service.TokenPair, error) {
		return &service.TokenPair{AccessToken: "secret-access-token", RefreshToken: "secret-refresh-token"}, nil
	}
	rec := env.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": "user@example.com", "password": "SuperSecret123!",
	}, "")
	assertStatus(t, rec, http.StatusOK)
	env.do(t, http.MethodGet, "/api/v1/users/me", nil, testToken)

	logs := env.logs.String()
	assertNoLeak(t, "", logs, "SuperSecret123!", "secret-access-token", "secret-refresh-token", testToken, "Authorization", "user@example.com")
	if !strings.Contains(logs, `"request_id"`) || !strings.Contains(logs, `"status"`) || !strings.Contains(logs, "/api/v1/auth/login") {
		t.Errorf("logs must carry request metadata: %s", logs)
	}
}

func TestHealthAndReady(t *testing.T) {
	env := newTestEnv(t, Options{})
	rec := env.do(t, http.MethodGet, "/health", nil, "")
	assertStatus(t, rec, http.StatusOK)
	rec = env.do(t, http.MethodGet, "/ready", nil, "")
	assertStatus(t, rec, http.StatusNotFound) // not registered without a check

	env = newTestEnv(t, Options{ReadinessCheck: func(context.Context) error { return nil }})
	rec = env.do(t, http.MethodGet, "/ready", nil, "")
	assertStatus(t, rec, http.StatusOK)

	env = newTestEnv(t, Options{ReadinessCheck: func(context.Context) error { return errors.New("db down") }})
	rec = env.do(t, http.MethodGet, "/ready", nil, "")
	assertStatus(t, rec, http.StatusServiceUnavailable)
	assertNoLeak(t, rec.Body.String(), env.logs.String(), "db down")
}

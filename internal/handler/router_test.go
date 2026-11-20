package handler

import (
	"context"
	"net/http"
	"sort"
	"testing"

	"github.com/metamin44/go-task-management-api/internal/service"
)

// TestRouteTable guards the router refactor: the exact method/path set must
// stay stable, with no duplicates and no accidental public exposure of
// authenticated routes.
func TestRouteTable(t *testing.T) {
	env := newTestEnv(t, Options{})

	want := []string{
		"GET /health",
		"POST /api/v1/auth/register",
		"POST /api/v1/auth/login",
		"POST /api/v1/auth/refresh",
		"POST /api/v1/auth/logout",
		"POST /api/v1/auth/logout-all",
		"POST /api/v1/auth/forgot-password",
		"POST /api/v1/auth/reset-password",
		"GET /api/v1/users/me",
		"PATCH /api/v1/users/me",
		"DELETE /api/v1/users/me",
		"PATCH /api/v1/users/me/password",
		"GET /api/v1/users/me/tasks",
		"POST /api/v1/projects",
		"GET /api/v1/projects",
		"GET /api/v1/projects/:id",
		"PATCH /api/v1/projects/:id",
		"DELETE /api/v1/projects/:id",
		"GET /api/v1/projects/:id/members",
		"POST /api/v1/projects/:id/members",
		"PATCH /api/v1/projects/:id/members/:userId",
		"DELETE /api/v1/projects/:id/members/:userId",
		"POST /api/v1/projects/:id/transfer-ownership",
		"POST /api/v1/projects/:id/tasks",
		"GET /api/v1/projects/:id/tasks",
		"GET /api/v1/projects/:id/tasks/:taskId",
		"PATCH /api/v1/projects/:id/tasks/:taskId",
		"DELETE /api/v1/projects/:id/tasks/:taskId",
	}

	got := make([]string, 0, len(want))
	seen := map[string]bool{}
	for _, r := range env.e.Routes() {
		// Echo registers internal not-found sentinels for groups carrying
		// middleware; they are framework plumbing, not API endpoints.
		if r.Method == "echo_route_not_found" {
			continue
		}
		key := r.Method + " " + r.Path
		if seen[key] {
			t.Errorf("duplicate route registration: %s", key)
		}
		seen[key] = true
		got = append(got, key)
	}
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("route count = %d, want %d\ngot: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("route[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// /ready is registered only when a readiness check is injected.
	env = newTestEnv(t, Options{ReadinessCheck: func(context.Context) error { return nil }})
	found := false
	for _, r := range env.e.Routes() {
		if r.Method == http.MethodGet && r.Path == "/ready" {
			found = true
		}
	}
	if !found {
		t.Error("/ready must be registered when ReadinessCheck is set")
	}
}

// TestPublicVsAuthenticatedRoutes verifies the auth boundary survived the
// refactor: public auth endpoints work without a token; everything else
// under /api/v1 rejects missing tokens.
func TestPublicVsAuthenticatedRoutes(t *testing.T) {
	env := newTestEnv(t, Options{})
	env.auth.loginFn = func(context.Context, string, string) (*service.TokenPair, error) {
		return &service.TokenPair{AccessToken: "a", RefreshToken: "r"}, nil
	}

	public := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/auth/register"},
		{http.MethodPost, "/api/v1/auth/login"},
		{http.MethodPost, "/api/v1/auth/refresh"},
		{http.MethodPost, "/api/v1/auth/logout"},
		{http.MethodPost, "/api/v1/auth/forgot-password"},
		{http.MethodPost, "/api/v1/auth/reset-password"},
		{http.MethodGet, "/health"},
	}
	for _, rt := range public {
		rec := env.do(t, rt.method, rt.path, map[string]string{
			"name": "A", "email": "a@b.co", "password": "password123", "refresh_token": "x",
		}, "")
		if rec.Code == http.StatusUnauthorized {
			t.Errorf("%s %s must be public, got 401", rt.method, rt.path)
		}
	}

	authed := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/auth/logout-all"},
		{http.MethodGet, "/api/v1/users/me"},
		{http.MethodGet, "/api/v1/users/me/tasks"},
		{http.MethodGet, "/api/v1/projects"},
		{http.MethodGet, "/api/v1/projects/" + testUID.String()},
		{http.MethodGet, "/api/v1/projects/" + testUID.String() + "/members"},
		{http.MethodGet, "/api/v1/projects/" + testUID.String() + "/tasks"},
	}
	for _, rt := range authed {
		rec := env.do(t, rt.method, rt.path, nil, "")
		assertStatus(t, rec, http.StatusUnauthorized)
	}
}

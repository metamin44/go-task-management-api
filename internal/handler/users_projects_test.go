package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/metamin44/go-task-management-api/internal/domain"
	"github.com/metamin44/go-task-management-api/internal/service"
)

func TestGetProfile(t *testing.T) {
	env := newTestEnv(t, Options{})
	rec := env.do(t, http.MethodGet, "/api/v1/users/me", nil, testToken)
	assertStatus(t, rec, http.StatusOK)
	body := rec.Body.String()
	if !strings.Contains(body, `"email":"test@example.com"`) || strings.Contains(body, "password_hash") {
		t.Errorf("unexpected profile body: %s", body)
	}
}

func TestUpdateProfilePartialFields(t *testing.T) {
	env := newTestEnv(t, Options{})
	var gotName, gotEmail *string
	env.users.updateProfileFn = func(_ context.Context, _ domain.UUID, name, email *string) (*domain.User, error) {
		gotName, gotEmail = name, email
		return sampleUser(), nil
	}

	// Only name supplied: email must reach the service as nil. The handler no
	// longer pre-reads and merges omitted fields (R-01); the service merges on
	// its freshest read.
	rec := env.do(t, http.MethodPatch, "/api/v1/users/me", map[string]string{"name": "New Name"}, testToken)
	assertStatus(t, rec, http.StatusOK)
	if gotName == nil || *gotName != "New Name" || gotEmail != nil {
		t.Errorf("omitted email must be nil (no handler merge): name=%v email=%v", gotName, gotEmail)
	}

	rec = env.do(t, http.MethodPatch, "/api/v1/users/me", map[string]string{"email": "New@Example.com"}, testToken)
	assertStatus(t, rec, http.StatusOK)
	if gotName != nil || gotEmail == nil || *gotEmail != "New@Example.com" {
		t.Errorf("omitted name must be nil (no handler merge): name=%v email=%v", gotName, gotEmail)
	}

	rec = env.do(t, http.MethodPatch, "/api/v1/users/me", map[string]string{}, testToken)
	assertStatus(t, rec, http.StatusBadRequest)
	rec = env.do(t, http.MethodPatch, "/api/v1/users/me", map[string]string{"name": "   "}, testToken)
	assertStatus(t, rec, http.StatusBadRequest)

	env.users.updateProfileFn = func(context.Context, domain.UUID, *string, *string) (*domain.User, error) {
		return nil, fmt.Errorf("users: update: %w (constraint users_email_lower_uidx)", domain.ErrConflict)
	}
	rec = env.do(t, http.MethodPatch, "/api/v1/users/me", map[string]string{"email": "taken@example.com"}, testToken)
	assertStatus(t, rec, http.StatusConflict)
	assertNoLeak(t, rec.Body.String(), env.logs.String(), "users_email_lower_uidx")
}

// R-01 regression: the handler must not issue a second profile read to merge
// omitted fields. requireAuth loads identity once; updateProfile adds no read.
func TestUpdateProfileNoHandlerPreRead(t *testing.T) {
	env := newTestEnv(t, Options{})
	var profileCalls int
	env.users.profileFn = func(_ context.Context, uid domain.UUID) (*domain.User, error) {
		profileCalls++
		return &domain.User{ID: uid, Name: "Stale", Email: "stale@example.com"}, nil
	}
	env.users.updateProfileFn = func(context.Context, domain.UUID, *string, *string) (*domain.User, error) {
		return sampleUser(), nil
	}
	rec := env.do(t, http.MethodPatch, "/api/v1/users/me", map[string]string{"email": "new@example.com"}, testToken)
	assertStatus(t, rec, http.StatusOK)
	if profileCalls != 1 {
		t.Errorf("Profile called %d times, want 1 (middleware only; no handler merge read)", profileCalls)
	}
}

// Q-01: explicit JSON null on user PATCH fields follows the current documented
// semantics — null decodes to a nil pointer, indistinguishable from omission,
// so it means "no change" (the service merges on its freshest read). The
// at-least-one-field rule still rejects bodies that supply no actual value.
func TestUpdateProfileExplicitNullFields(t *testing.T) {
	env := newTestEnv(t, Options{})
	var gotName, gotEmail *string
	env.users.updateProfileFn = func(_ context.Context, _ domain.UUID, name, email *string) (*domain.User, error) {
		gotName, gotEmail = name, email
		return sampleUser(), nil
	}

	// null name + real email: name reaches the service as nil (omitted).
	rec := env.do(t, http.MethodPatch, "/api/v1/users/me", map[string]any{"name": nil, "email": "new@example.com"}, testToken)
	assertStatus(t, rec, http.StatusOK)
	if gotName != nil || gotEmail == nil || *gotEmail != "new@example.com" {
		t.Errorf("null name must be omitted (nil), email applied: name=%v email=%v", gotName, gotEmail)
	}

	// real name + null email: email reaches the service as nil (omitted).
	rec = env.do(t, http.MethodPatch, "/api/v1/users/me", map[string]any{"name": "New Name", "email": nil}, testToken)
	assertStatus(t, rec, http.StatusOK)
	if gotName == nil || *gotName != "New Name" || gotEmail != nil {
		t.Errorf("null email must be omitted (nil), name applied: name=%v email=%v", gotName, gotEmail)
	}

	// Null-only / all-null bodies supply no field -> 400 (unchanged rule).
	rec = env.do(t, http.MethodPatch, "/api/v1/users/me", map[string]any{"name": nil}, testToken)
	assertStatus(t, rec, http.StatusBadRequest)
	rec = env.do(t, http.MethodPatch, "/api/v1/users/me", map[string]any{"name": nil, "email": nil}, testToken)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestChangePasswordEndpoint(t *testing.T) {
	env := newTestEnv(t, Options{})
	var gotUID domain.UUID
	var gotCur, gotNew string
	env.auth.changePasswordFn = func(_ context.Context, uid domain.UUID, cur, next string) error {
		gotUID, gotCur, gotNew = uid, cur, next
		if cur != "current-pass1" {
			return fmt.Errorf("change password: %w", service.ErrUnauthorized)
		}
		return nil
	}
	rec := env.do(t, http.MethodPatch, "/api/v1/users/me/password", map[string]string{
		"current_password": "current-pass1", "new_password": "new-password1",
	}, testToken)
	assertStatus(t, rec, http.StatusNoContent)
	if gotUID != testUID || gotCur != "current-pass1" || gotNew != "new-password1" {
		t.Error("service must receive actor from token and both passwords")
	}

	rec = env.do(t, http.MethodPatch, "/api/v1/users/me/password", map[string]string{
		"current_password": "wrong-pass12", "new_password": "new-password1",
	}, testToken)
	assertStatus(t, rec, http.StatusUnauthorized)
	assertNoLeak(t, rec.Body.String(), env.logs.String(), "wrong-pass12", "new-password1", "current-pass1")

	rec = env.do(t, http.MethodPatch, "/api/v1/users/me/password", map[string]string{
		"current_password": "current-pass1", "new_password": "short",
	}, testToken)
	assertStatus(t, rec, http.StatusBadRequest)
	rec = env.do(t, http.MethodPatch, "/api/v1/users/me/password", map[string]string{}, testToken)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestDeleteAccountEndpoint(t *testing.T) {
	env := newTestEnv(t, Options{})
	env.users.deleteAccountFn = func(context.Context, domain.UUID) error { return nil }
	rec := env.do(t, http.MethodDelete, "/api/v1/users/me", nil, testToken)
	assertStatus(t, rec, http.StatusNoContent)

	env.users.deleteAccountFn = func(context.Context, domain.UUID) error {
		return fmt.Errorf("users: delete: %w (constraint projects_created_by_fkey)", domain.ErrReference)
	}
	rec = env.do(t, http.MethodDelete, "/api/v1/users/me", nil, testToken)
	assertStatus(t, rec, http.StatusConflict)
	if decodeEnvelope(t, rec).Error.Code != "reference_violation" {
		t.Error("RESTRICT chain must map to reference_violation")
	}
	assertNoLeak(t, rec.Body.String(), env.logs.String(), "projects_created_by_fkey")
}

func TestProjectCreateAndList(t *testing.T) {
	env := newTestEnv(t, Options{})
	pid := domain.NewUUID()
	var gotName string
	var gotDesc *string
	env.projects.createFn = func(_ context.Context, uid domain.UUID, name string, desc *string) (*domain.Project, error) {
		if uid != testUID {
			t.Error("creator must be the authenticated actor")
		}
		gotName, gotDesc = name, desc
		return sampleProject(pid), nil
	}
	rec := env.do(t, http.MethodPost, "/api/v1/projects", map[string]any{"name": "  My Project  ", "description": "d"}, testToken)
	assertStatus(t, rec, http.StatusCreated)
	if gotName != "My Project" || gotDesc == nil || *gotDesc != "d" {
		t.Errorf("unexpected create args: %q %v", gotName, gotDesc)
	}
	rec = env.do(t, http.MethodPost, "/api/v1/projects", map[string]any{"name": "   "}, testToken)
	assertStatus(t, rec, http.StatusBadRequest)
	rec = env.do(t, http.MethodPost, "/api/v1/projects", map[string]any{}, testToken)
	assertStatus(t, rec, http.StatusBadRequest)

	env.projects.listFn = func(context.Context, domain.UUID) ([]domain.Project, error) {
		return []domain.Project{*sampleProject(pid)}, nil
	}
	rec = env.do(t, http.MethodGet, "/api/v1/projects", nil, testToken)
	assertStatus(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), `"data":[`) {
		t.Errorf("list must wrap data: %s", rec.Body.String())
	}
}

func TestProjectGetUpdateDelete(t *testing.T) {
	env := newTestEnv(t, Options{})
	pid := domain.NewUUID()
	desc := "original"
	current := sampleProject(pid)
	current.Description = &desc
	env.projects.getFn = func(_ context.Context, _, p domain.UUID) (*domain.Project, error) {
		if p != pid {
			return nil, fmt.Errorf("projects: get: %w", domain.ErrNotFound)
		}
		cp := *current
		return &cp, nil
	}

	rec := env.do(t, http.MethodGet, "/api/v1/projects/"+pid.String(), nil, testToken)
	assertStatus(t, rec, http.StatusOK)
	rec = env.do(t, http.MethodGet, "/api/v1/projects/not-a-uuid", nil, testToken)
	assertStatus(t, rec, http.StatusBadRequest)

	env.projects.getFn = func(context.Context, domain.UUID, domain.UUID) (*domain.Project, error) {
		return nil, fmt.Errorf("project access: %w", service.ErrForbidden)
	}
	rec = env.do(t, http.MethodGet, "/api/v1/projects/"+pid.String(), nil, testToken)
	assertStatus(t, rec, http.StatusForbidden)

	// PATCH partial semantics (R-01): the handler passes a patch and does NOT
	// pre-read the project to merge omitted fields; the service merges on its
	// freshest read.
	var gotPatch service.ProjectPatch
	var getDuringPatch bool
	env.projects.getFn = func(context.Context, domain.UUID, domain.UUID) (*domain.Project, error) {
		getDuringPatch = true
		cp := *current
		return &cp, nil
	}
	env.projects.updateFn = func(_ context.Context, _, _ domain.UUID, patch service.ProjectPatch) (*domain.Project, error) {
		gotPatch = patch
		return sampleProject(pid), nil
	}
	getDuringPatch = false
	rec = env.doRaw(t, http.MethodPatch, "/api/v1/projects/"+pid.String(),
		strings.NewReader(`{"description":null}`), "application/json", testToken)
	assertStatus(t, rec, http.StatusOK)
	if !gotPatch.DescriptionSet || gotPatch.Description != nil || gotPatch.Name != nil {
		t.Errorf("null description must clear with name omitted: %+v", gotPatch)
	}
	if getDuringPatch {
		t.Error("handler must not pre-read the project to merge omitted fields")
	}
	getDuringPatch = false
	rec = env.do(t, http.MethodPatch, "/api/v1/projects/"+pid.String(), map[string]any{"name": "Renamed"}, testToken)
	assertStatus(t, rec, http.StatusOK)
	if gotPatch.Name == nil || *gotPatch.Name != "Renamed" || gotPatch.DescriptionSet {
		t.Errorf("omitted description must not be set: %+v", gotPatch)
	}
	if getDuringPatch {
		t.Error("handler must not pre-read the project to merge omitted fields")
	}
	rec = env.do(t, http.MethodPatch, "/api/v1/projects/"+pid.String(), map[string]any{}, testToken)
	assertStatus(t, rec, http.StatusBadRequest)

	env.projects.deleteFn = func(context.Context, domain.UUID, domain.UUID) error { return nil }
	rec = env.do(t, http.MethodDelete, "/api/v1/projects/"+pid.String(), nil, testToken)
	assertStatus(t, rec, http.StatusNoContent)
	env.projects.deleteFn = func(context.Context, domain.UUID, domain.UUID) error {
		return fmt.Errorf("insufficient project role: %w", service.ErrForbidden)
	}
	rec = env.do(t, http.MethodDelete, "/api/v1/projects/"+pid.String(), nil, testToken)
	assertStatus(t, rec, http.StatusForbidden)
}

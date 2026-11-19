package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/metamin44/go-task-management-api/internal/domain"
	"github.com/metamin44/go-task-management-api/internal/service"
)

// F-02 transport mapping: a concurrent modification surfaces as 409 conflict;
// a missing record keeps the existing 404 behavior. Authorization, validation,
// and PATCH merge are unchanged (covered by the other handler tests).

func TestPatchConcurrentModificationIs409(t *testing.T) {
	pid, tid := domain.NewUUID(), domain.NewUUID()

	t.Run("user_profile", func(t *testing.T) {
		env := newTestEnv(t, Options{})
		env.users.updateProfileFn = func(context.Context, domain.UUID, *string, *string) (*domain.User, error) {
			return nil, fmt.Errorf("users: update: %w", domain.ErrConflict)
		}
		rec := env.do(t, http.MethodPatch, "/api/v1/users/me", map[string]any{"name": "New"}, testToken)
		assertStatus(t, rec, http.StatusConflict)
		if decodeEnvelope(t, rec).Error.Code != "conflict" {
			t.Error("expected code conflict")
		}
	})

	t.Run("project", func(t *testing.T) {
		env := newTestEnv(t, Options{})
		env.projects.getFn = func(context.Context, domain.UUID, domain.UUID) (*domain.Project, error) {
			return sampleProject(pid), nil
		}
		env.projects.updateFn = func(context.Context, domain.UUID, domain.UUID, service.ProjectPatch) (*domain.Project, error) {
			return nil, fmt.Errorf("projects: update: %w", domain.ErrConflict)
		}
		rec := env.do(t, http.MethodPatch, "/api/v1/projects/"+pid.String(), map[string]any{"name": "New"}, testToken)
		assertStatus(t, rec, http.StatusConflict)
		if decodeEnvelope(t, rec).Error.Code != "conflict" {
			t.Error("expected code conflict")
		}
	})

	t.Run("task", func(t *testing.T) {
		env := newTestEnv(t, Options{})
		env.tasks.getFn = func(context.Context, domain.UUID, domain.UUID) (*domain.Task, error) {
			return sampleTask(tid, pid), nil
		}
		env.tasks.updateFn = func(context.Context, domain.UUID, domain.UUID, service.TaskPatch) (*domain.Task, error) {
			return nil, fmt.Errorf("tasks: update: %w", domain.ErrConflict)
		}
		rec := env.do(t, http.MethodPatch, "/api/v1/projects/"+pid.String()+"/tasks/"+tid.String(), map[string]any{"title": "New"}, testToken)
		assertStatus(t, rec, http.StatusConflict)
		if decodeEnvelope(t, rec).Error.Code != "conflict" {
			t.Error("expected code conflict")
		}
	})
}

func TestPatchMissingRecordStays404(t *testing.T) {
	pid, tid := domain.NewUUID(), domain.NewUUID()

	t.Run("user_profile", func(t *testing.T) {
		env := newTestEnv(t, Options{})
		env.users.updateProfileFn = func(context.Context, domain.UUID, *string, *string) (*domain.User, error) {
			return nil, fmt.Errorf("users: update: %w", domain.ErrNotFound)
		}
		rec := env.do(t, http.MethodPatch, "/api/v1/users/me", map[string]any{"name": "New"}, testToken)
		assertStatus(t, rec, http.StatusNotFound)
	})

	t.Run("project", func(t *testing.T) {
		env := newTestEnv(t, Options{})
		env.projects.getFn = func(context.Context, domain.UUID, domain.UUID) (*domain.Project, error) {
			return sampleProject(pid), nil
		}
		env.projects.updateFn = func(context.Context, domain.UUID, domain.UUID, service.ProjectPatch) (*domain.Project, error) {
			return nil, fmt.Errorf("projects: update: %w", domain.ErrNotFound)
		}
		rec := env.do(t, http.MethodPatch, "/api/v1/projects/"+pid.String(), map[string]any{"name": "New"}, testToken)
		assertStatus(t, rec, http.StatusNotFound)
	})

	t.Run("task", func(t *testing.T) {
		env := newTestEnv(t, Options{})
		env.tasks.getFn = func(context.Context, domain.UUID, domain.UUID) (*domain.Task, error) {
			return sampleTask(tid, pid), nil
		}
		env.tasks.updateFn = func(context.Context, domain.UUID, domain.UUID, service.TaskPatch) (*domain.Task, error) {
			return nil, fmt.Errorf("tasks: update: %w", domain.ErrNotFound)
		}
		rec := env.do(t, http.MethodPatch, "/api/v1/projects/"+pid.String()+"/tasks/"+tid.String(), map[string]any{"title": "New"}, testToken)
		assertStatus(t, rec, http.StatusNotFound)
	})
}

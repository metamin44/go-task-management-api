package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/metamin44/go-task-management-api/internal/domain"
	"github.com/metamin44/go-task-management-api/internal/service"
)

func TestMemberEndpoints(t *testing.T) {
	env := newTestEnv(t, Options{})
	pid, target := domain.NewUUID(), domain.NewUUID()

	env.members.listFn = func(context.Context, domain.UUID, domain.UUID) ([]domain.ProjectMember, error) {
		return []domain.ProjectMember{{ProjectID: pid, UserID: testUID, Role: domain.RoleOwner, JoinedAt: time.Now()}}, nil
	}
	rec := env.do(t, http.MethodGet, "/api/v1/projects/"+pid.String()+"/members", nil, testToken)
	assertStatus(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), `"role":"owner"`) {
		t.Errorf("unexpected member list: %s", rec.Body.String())
	}

	var gotTarget domain.UUID
	env.members.addFn = func(_ context.Context, _, _, u domain.UUID) (*domain.ProjectMember, error) {
		gotTarget = u
		return &domain.ProjectMember{ProjectID: pid, UserID: u, Role: domain.RoleMember}, nil
	}
	rec = env.do(t, http.MethodPost, "/api/v1/projects/"+pid.String()+"/members", map[string]string{"user_id": target.String()}, testToken)
	assertStatus(t, rec, http.StatusCreated)
	if gotTarget != target {
		t.Error("target user must be parsed from the body")
	}
	rec = env.do(t, http.MethodPost, "/api/v1/projects/"+pid.String()+"/members", map[string]string{"user_id": "bad"}, testToken)
	assertStatus(t, rec, http.StatusBadRequest)
	rec = env.do(t, http.MethodPost, "/api/v1/projects/"+pid.String()+"/members", map[string]string{}, testToken)
	assertStatus(t, rec, http.StatusBadRequest)

	env.members.addFn = func(context.Context, domain.UUID, domain.UUID, domain.UUID) (*domain.ProjectMember, error) {
		return nil, fmt.Errorf("members: add: %w (constraint project_members_pkey)", domain.ErrConflict)
	}
	rec = env.do(t, http.MethodPost, "/api/v1/projects/"+pid.String()+"/members", map[string]string{"user_id": target.String()}, testToken)
	assertStatus(t, rec, http.StatusConflict)
	assertNoLeak(t, rec.Body.String(), env.logs.String(), "project_members_pkey")

	env.members.addFn = func(context.Context, domain.UUID, domain.UUID, domain.UUID) (*domain.ProjectMember, error) {
		return nil, fmt.Errorf("target user: %w", domain.ErrNotFound)
	}
	rec = env.do(t, http.MethodPost, "/api/v1/projects/"+pid.String()+"/members", map[string]string{"user_id": target.String()}, testToken)
	assertStatus(t, rec, http.StatusNotFound)
}

func TestMemberRoleAndRemoveAndTransfer(t *testing.T) {
	env := newTestEnv(t, Options{})
	pid, target := domain.NewUUID(), domain.NewUUID()
	base := "/api/v1/projects/" + pid.String() + "/members/" + target.String()

	var gotRole domain.Role
	env.members.roleFn = func(_ context.Context, _, _, _ domain.UUID, role domain.Role) error {
		gotRole = role
		return nil
	}
	rec := env.do(t, http.MethodPatch, base, map[string]string{"role": "admin"}, testToken)
	assertStatus(t, rec, http.StatusNoContent)
	if gotRole != domain.RoleAdmin {
		t.Error("role must be forwarded")
	}
	for _, role := range []string{"owner", "superuser", ""} {
		rec = env.do(t, http.MethodPatch, base, map[string]string{"role": role}, testToken)
		assertStatus(t, rec, http.StatusBadRequest)
	}
	env.members.roleFn = func(context.Context, domain.UUID, domain.UUID, domain.UUID, domain.Role) error {
		return fmt.Errorf("owner role changes only via ownership transfer: %w", service.ErrForbidden)
	}
	rec = env.do(t, http.MethodPatch, base, map[string]string{"role": "member"}, testToken)
	assertStatus(t, rec, http.StatusForbidden)

	env.members.removeFn = func(context.Context, domain.UUID, domain.UUID, domain.UUID) error { return nil }
	rec = env.do(t, http.MethodDelete, base, nil, testToken)
	assertStatus(t, rec, http.StatusNoContent)
	env.members.removeFn = func(context.Context, domain.UUID, domain.UUID, domain.UUID) error {
		return fmt.Errorf("member has assigned tasks; unassign or reassign first: %w", domain.ErrConflict)
	}
	rec = env.do(t, http.MethodDelete, base, nil, testToken)
	assertStatus(t, rec, http.StatusConflict)

	env.members.transferFn = func(context.Context, domain.UUID, domain.UUID, domain.UUID) error { return nil }
	rec = env.do(t, http.MethodPost, "/api/v1/projects/"+pid.String()+"/transfer-ownership",
		map[string]string{"new_owner_id": target.String()}, testToken)
	assertStatus(t, rec, http.StatusNoContent)
	rec = env.do(t, http.MethodPost, "/api/v1/projects/"+pid.String()+"/transfer-ownership",
		map[string]string{"new_owner_id": "bad"}, testToken)
	assertStatus(t, rec, http.StatusBadRequest)
	env.members.transferFn = func(context.Context, domain.UUID, domain.UUID, domain.UUID) error {
		return fmt.Errorf("new owner must be an existing project member: %w", domain.ErrNotFound)
	}
	rec = env.do(t, http.MethodPost, "/api/v1/projects/"+pid.String()+"/transfer-ownership",
		map[string]string{"new_owner_id": target.String()}, testToken)
	assertStatus(t, rec, http.StatusNotFound)
}

func TestTaskCreateEndpoint(t *testing.T) {
	env := newTestEnv(t, Options{})
	pid, assignee := domain.NewUUID(), domain.NewUUID()
	var gotIn service.TaskInput
	env.tasks.createFn = func(_ context.Context, uid, p domain.UUID, in service.TaskInput) (*domain.Task, error) {
		if uid != testUID || p != pid {
			t.Error("actor/project must come from token and path")
		}
		gotIn = in
		return sampleTask(domain.NewUUID(), pid), nil
	}
	rec := env.do(t, http.MethodPost, "/api/v1/projects/"+pid.String()+"/tasks", map[string]any{
		"title": "Write tests", "assigned_to": assignee.String(),
	}, testToken)
	assertStatus(t, rec, http.StatusCreated)
	if gotIn.Title != "Write tests" || gotIn.AssignedTo == nil || *gotIn.AssignedTo != assignee {
		t.Errorf("unexpected TaskInput: %+v", gotIn)
	}
	if gotIn.Status != "" || gotIn.Priority != "" {
		t.Error("omitted enums must pass through empty so the service applies defaults")
	}

	for _, body := range []map[string]any{
		{},
		{"title": "   "},
		{"title": "T", "status": "blocked"},
		{"title": "T", "priority": "critical"},
		{"title": "T", "assigned_to": "not-a-uuid"},
		{"title": "T", "due_at": "yesterday"},
	} {
		rec = env.do(t, http.MethodPost, "/api/v1/projects/"+pid.String()+"/tasks", body, testToken)
		assertStatus(t, rec, http.StatusBadRequest)
	}

	env.tasks.createFn = func(context.Context, domain.UUID, domain.UUID, service.TaskInput) (*domain.Task, error) {
		return nil, fmt.Errorf("assignee must be a member of the same project: %w", domain.ErrInvalid)
	}
	rec = env.do(t, http.MethodPost, "/api/v1/projects/"+pid.String()+"/tasks", map[string]any{
		"title": "T", "assigned_to": assignee.String(),
	}, testToken)
	assertStatus(t, rec, http.StatusBadRequest)

	env.tasks.createFn = func(context.Context, domain.UUID, domain.UUID, service.TaskInput) (*domain.Task, error) {
		return nil, fmt.Errorf("project access: %w", service.ErrForbidden)
	}
	rec = env.do(t, http.MethodPost, "/api/v1/projects/"+pid.String()+"/tasks", map[string]any{"title": "T"}, testToken)
	assertStatus(t, rec, http.StatusForbidden)
}

func TestTaskListQueryParsing(t *testing.T) {
	env := newTestEnv(t, Options{})
	pid, assignee := domain.NewUUID(), domain.NewUUID()
	var gotF domain.TaskFilter
	env.tasks.listFn = func(_ context.Context, _, _ domain.UUID, f domain.TaskFilter) ([]domain.Task, error) {
		gotF = f
		return []domain.Task{}, nil
	}
	base := "/api/v1/projects/" + pid.String() + "/tasks"

	rec := env.do(t, http.MethodGet, base, nil, testToken)
	assertStatus(t, rec, http.StatusOK)
	if gotF.Limit != 50 || gotF.Offset != 0 || gotF.SortBy != domain.TaskSortCreatedAt || gotF.SortDesc {
		t.Errorf("unexpected defaults: %+v", gotF)
	}
	if !strings.Contains(rec.Body.String(), `"limit":50`) || !strings.Contains(rec.Body.String(), `"offset":0`) {
		t.Errorf("list must echo pagination: %s", rec.Body.String())
	}

	query := "?limit=10&offset=5&status=todo&priority=high&assigned_to=" + assignee.String() +
		"&due_after=2026-01-01T00:00:00Z&due_before=2026-12-31T23:59:59Z&sort_by=priority&sort_order=desc"
	rec = env.do(t, http.MethodGet, base+query, nil, testToken)
	assertStatus(t, rec, http.StatusOK)
	if gotF.Limit != 10 || gotF.Offset != 5 || gotF.Status == nil || *gotF.Status != domain.StatusTodo ||
		gotF.Priority == nil || *gotF.Priority != domain.PriorityHigh ||
		gotF.AssignedTo == nil || *gotF.AssignedTo != assignee ||
		gotF.DueAfter == nil || gotF.DueBefore == nil ||
		gotF.SortBy != domain.TaskSortPriority || !gotF.SortDesc {
		t.Errorf("filter not parsed correctly: %+v", gotF)
	}

	bad := []string{
		"?limit=0", "?limit=201", "?limit=abc", "?offset=-1", "?offset=x",
		"?status=blocked", "?priority=critical", "?assigned_to=bad",
		"?due_after=yesterday", "?due_before=2026-13-99",
		"?sort_by=title", "?sort_order=sideways", "?bogus=1", "?limit=1&limit=2",
	}
	for _, q := range bad {
		rec = env.do(t, http.MethodGet, base+q, nil, testToken)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("query %q: status %d, want 400", q, rec.Code)
		}
	}

	env.tasks.listFn = func(context.Context, domain.UUID, domain.UUID, domain.TaskFilter) ([]domain.Task, error) {
		return nil, fmt.Errorf("project access: %w", service.ErrForbidden)
	}
	rec = env.do(t, http.MethodGet, base, nil, testToken)
	assertStatus(t, rec, http.StatusForbidden)
}

func TestMyTasksEndpoint(t *testing.T) {
	env := newTestEnv(t, Options{})
	pid := domain.NewUUID()
	var gotF domain.TaskFilter
	env.tasks.listMineFn = func(_ context.Context, uid domain.UUID, f domain.TaskFilter) ([]domain.Task, error) {
		if uid != testUID {
			t.Error("actor must come from the token")
		}
		gotF = f
		return []domain.Task{}, nil
	}
	rec := env.do(t, http.MethodGet, "/api/v1/users/me/tasks?project_id="+pid.String()+"&status=done&limit=25", nil, testToken)
	assertStatus(t, rec, http.StatusOK)
	if gotF.ProjectID == nil || *gotF.ProjectID != pid || gotF.Status == nil || *gotF.Status != domain.StatusDone || gotF.Limit != 25 {
		t.Errorf("unexpected filter: %+v", gotF)
	}
	rec = env.do(t, http.MethodGet, "/api/v1/users/me/tasks?project_id=bad", nil, testToken)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestTaskGetAndProjectMismatch(t *testing.T) {
	env := newTestEnv(t, Options{})
	pid, other, tid := domain.NewUUID(), domain.NewUUID(), domain.NewUUID()
	env.tasks.getFn = func(context.Context, domain.UUID, domain.UUID) (*domain.Task, error) {
		return sampleTask(tid, other), nil // belongs to a different project
	}
	rec := env.do(t, http.MethodGet, "/api/v1/projects/"+pid.String()+"/tasks/"+tid.String(), nil, testToken)
	assertStatus(t, rec, http.StatusNotFound)

	env.tasks.getFn = func(context.Context, domain.UUID, domain.UUID) (*domain.Task, error) {
		return sampleTask(tid, pid), nil
	}
	rec = env.do(t, http.MethodGet, "/api/v1/projects/"+pid.String()+"/tasks/"+tid.String(), nil, testToken)
	assertStatus(t, rec, http.StatusOK)
	rec = env.do(t, http.MethodGet, "/api/v1/projects/"+pid.String()+"/tasks/bad-uuid", nil, testToken)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestTaskPatchSemantics(t *testing.T) {
	env := newTestEnv(t, Options{})
	pid, tid, assignee := domain.NewUUID(), domain.NewUUID(), domain.NewUUID()
	env.tasks.getFn = func(context.Context, domain.UUID, domain.UUID) (*domain.Task, error) {
		return sampleTask(tid, pid), nil
	}
	var gotPatch service.TaskPatch
	env.tasks.updateFn = func(_ context.Context, _, _ domain.UUID, patch service.TaskPatch) (*domain.Task, error) {
		gotPatch = patch
		return sampleTask(tid, pid), nil
	}
	base := "/api/v1/projects/" + pid.String() + "/tasks/" + tid.String()

	patch := func(body string) *httptest.ResponseRecorder {
		return env.doRaw(t, http.MethodPatch, base, strings.NewReader(body), "application/json", testToken)
	}

	rec := patch(`{"assigned_to":null}`)
	assertStatus(t, rec, http.StatusOK)
	if !gotPatch.Unassign || gotPatch.AssignedTo != nil {
		t.Errorf("null assigned_to must unassign: %+v", gotPatch)
	}
	rec = patch(`{"due_at":null}`)
	assertStatus(t, rec, http.StatusOK)
	if !gotPatch.ClearDueAt || gotPatch.DueAt != nil {
		t.Errorf("null due_at must clear: %+v", gotPatch)
	}
	rec = patch(`{"assigned_to":"` + assignee.String() + `","due_at":"2026-06-01T12:00:00Z","status":"done","priority":"urgent","title":"New","description":"d"}`)
	assertStatus(t, rec, http.StatusOK)
	if gotPatch.AssignedTo == nil || *gotPatch.AssignedTo != assignee || gotPatch.DueAt == nil ||
		gotPatch.Status == nil || *gotPatch.Status != domain.StatusDone ||
		gotPatch.Priority == nil || *gotPatch.Priority != domain.PriorityUrgent ||
		gotPatch.Title == nil || gotPatch.Description == nil {
		t.Errorf("patch fields not forwarded: %+v", gotPatch)
	}

	for _, body := range []string{
		`{"description":null}`,
		`{"title":"   "}`,
		`{"status":"blocked"}`,
		`{"priority":"critical"}`,
		`{"assigned_to":"bad"}`,
		`{"due_at":"tomorrow"}`,
		`{"assigned_to":123}`,
		`{"unknown":true}`,
	} {
		rec := patch(body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: status %d, want 400", body, rec.Code)
		}
	}

	env.tasks.updateFn = func(context.Context, domain.UUID, domain.UUID, service.TaskPatch) (*domain.Task, error) {
		return nil, fmt.Errorf("task write: %w", service.ErrForbidden)
	}
	rec = patch(`{"title":"X"}`)
	assertStatus(t, rec, http.StatusForbidden)
}

func TestTaskDeleteEndpoint(t *testing.T) {
	env := newTestEnv(t, Options{})
	pid, tid := domain.NewUUID(), domain.NewUUID()
	env.tasks.getFn = func(context.Context, domain.UUID, domain.UUID) (*domain.Task, error) {
		return sampleTask(tid, pid), nil
	}
	env.tasks.deleteFn = func(context.Context, domain.UUID, domain.UUID) error { return nil }
	rec := env.do(t, http.MethodDelete, "/api/v1/projects/"+pid.String()+"/tasks/"+tid.String(), nil, testToken)
	assertStatus(t, rec, http.StatusNoContent)

	env.tasks.deleteFn = func(context.Context, domain.UUID, domain.UUID) error {
		return fmt.Errorf("task write: %w", service.ErrForbidden)
	}
	rec = env.do(t, http.MethodDelete, "/api/v1/projects/"+pid.String()+"/tasks/"+tid.String(), nil, testToken)
	assertStatus(t, rec, http.StatusForbidden)

	env.tasks.getFn = func(context.Context, domain.UUID, domain.UUID) (*domain.Task, error) {
		return nil, fmt.Errorf("tasks: get: %w", domain.ErrNotFound)
	}
	rec = env.do(t, http.MethodDelete, "/api/v1/projects/"+pid.String()+"/tasks/"+tid.String(), nil, testToken)
	assertStatus(t, rec, http.StatusNotFound)
}

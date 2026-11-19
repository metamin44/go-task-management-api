//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/metamin44/go-task-management-api/internal/domain"
	"github.com/metamin44/go-task-management-api/internal/service"
)

// T1 (PROJECT_CONTEXT.md §8): delete a project with assigned tasks (multiple
// members, incl. assignee=owner) → succeeds; memberships and tasks gone;
// users untouched. Verifies the real NO ACTION composite FK + cascade order
// that fakes cannot reproduce.
func TestT1ProjectDeleteWithAssignedTasks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	owner := seedUser(t, f, "owner@example.com")
	admin := seedUser(t, f, "admin@example.com")
	member := seedUser(t, f, "member@example.com")

	p, err := f.projectSvc.Create(ctx, owner, "Project", nil)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := f.memberSvc.Add(ctx, owner, p.ID, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := f.memberSvc.Add(ctx, owner, p.ID, member); err != nil {
		t.Fatal(err)
	}

	// Assigned tasks including assignee=owner, plus an unassigned one.
	if _, err := f.taskSvc.Create(ctx, member, p.ID, service.TaskInput{Title: "Owner task", AssignedTo: &owner}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.taskSvc.Create(ctx, admin, p.ID, service.TaskInput{Title: "Member task", AssignedTo: &member}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.taskSvc.Create(ctx, owner, p.ID, service.TaskInput{Title: "Unassigned"}); err != nil {
		t.Fatal(err)
	}

	// Non-owner cannot delete.
	if err := f.projectSvc.Delete(ctx, admin, p.ID); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("admin delete must be forbidden, got %v", err)
	}

	// Owner delete must succeed despite assigned tasks: the composite FK is
	// NO ACTION (not RESTRICT), so its check queues behind the cascades.
	if err := f.projectSvc.Delete(ctx, owner, p.ID); err != nil {
		t.Fatalf("project delete with assigned tasks must succeed: %v", err)
	}

	if n := countRows(t, f, "projects"); n != 0 {
		t.Errorf("projects remaining: %d", n)
	}
	if n := countRows(t, f, "project_members"); n != 0 {
		t.Errorf("memberships remaining: %d", n)
	}
	if n := countRows(t, f, "tasks"); n != 0 {
		t.Errorf("tasks remaining: %d", n)
	}
	if n := countRows(t, f, "users"); n != 3 {
		t.Errorf("users must be untouched, got %d", n)
	}
	for _, uid := range []domain.UUID{owner, admin, member} {
		if _, err := f.users.GetByID(ctx, uid); err != nil {
			t.Errorf("user %s must survive project deletion: %v", uid, err)
		}
	}
}

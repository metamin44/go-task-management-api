//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/metamin44/go-task-management-api/internal/domain"
	"github.com/metamin44/go-task-management-api/internal/service"
)

// T2 (PROJECT_CONTEXT.md §8): remove a member with assigned tasks → fails
// 23503 (tasks_project_assignee_fkey); after unassign/reassign → succeeds;
// removing a member who only created tasks succeeds (attribution preserved);
// owner removal requires transfer.
func TestT2MemberRemovalFKBehavior(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	owner := seedUser(t, f, "owner@example.com")
	admin := seedUser(t, f, "admin@example.com")
	member := seedUser(t, f, "member@example.com")
	creator := seedUser(t, f, "creator@example.com")

	p, err := f.projectSvc.Create(ctx, owner, "Project", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, uid := range []domain.UUID{admin, member, creator} {
		if _, err := f.memberSvc.Add(ctx, owner, p.ID, uid); err != nil {
			t.Fatal(err)
		}
	}
	// New members start as member; elevate admin for the admin-actor steps.
	if err := f.memberSvc.UpdateRole(ctx, owner, p.ID, admin, domain.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	task, err := f.taskSvc.Create(ctx, member, p.ID, service.TaskInput{Title: "Assigned", AssignedTo: &member})
	if err != nil {
		t.Fatal(err)
	}

	// Repository level: the real composite FK fires 23503, mapped to
	// ErrReference with the constraint name.
	err = f.members.Remove(ctx, p.ID, member)
	if !errors.Is(err, domain.ErrReference) {
		t.Fatalf("raw removal with assigned tasks must be ErrReference, got %v", err)
	}
	if !strings.Contains(err.Error(), "tasks_project_assignee_fkey") {
		t.Errorf("error must name the composite FK, got %v", err)
	}

	// Service level: pre-check surfaces a clear conflict; membership kept.
	if err := f.memberSvc.Remove(ctx, owner, p.ID, member); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("service removal must conflict, got %v", err)
	}
	if _, err := f.members.Get(ctx, p.ID, member); err != nil {
		t.Errorf("failed removal must keep the membership: %v", err)
	}

	// Creator-only member is removable; created_by attribution survives.
	created, err := f.taskSvc.Create(ctx, creator, p.ID, service.TaskInput{Title: "Created only"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.memberSvc.Remove(ctx, owner, p.ID, creator); err != nil {
		t.Fatalf("creator-only member removal must succeed: %v", err)
	}
	got, err := f.tasks.GetByID(ctx, created.ID)
	if err != nil || got.CreatedBy != creator {
		t.Fatalf("attribution must survive member removal: %v", err)
	}

	// Reassign, then removal succeeds.
	if _, err := f.taskSvc.Update(ctx, owner, task.ID, service.TaskPatch{AssignedTo: &admin}); err != nil {
		t.Fatal(err)
	}
	if err := f.memberSvc.Remove(ctx, owner, p.ID, member); err != nil {
		t.Fatalf("removal after reassignment must succeed: %v", err)
	}

	// Unassign variant via the repository batch operation.
	member2 := seedUser(t, f, "member2@example.com")
	if _, err := f.memberSvc.Add(ctx, owner, p.ID, member2); err != nil {
		t.Fatal(err)
	}
	if _, err := f.taskSvc.Create(ctx, admin, p.ID, service.TaskInput{Title: "Second", AssignedTo: &member2}); err != nil {
		t.Fatal(err)
	}
	n, err := f.tasks.UnassignMemberTasks(ctx, p.ID, member2)
	if err != nil || n != 1 {
		t.Fatalf("unassign: n=%d err=%v", n, err)
	}
	if err := f.memberSvc.Remove(ctx, admin, p.ID, member2); err != nil {
		t.Fatalf("removal after unassignment must succeed: %v", err)
	}

	// Owner removal requires prior transfer.
	if err := f.memberSvc.Remove(ctx, owner, p.ID, owner); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("owner removal must be forbidden before transfer, got %v", err)
	}
	if err := f.memberSvc.TransferOwnership(ctx, owner, p.ID, admin); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if err := f.memberSvc.Remove(ctx, admin, p.ID, owner); err != nil {
		t.Fatalf("old owner removal after transfer must succeed: %v", err)
	}
	// Exactly one owner remains (partial unique index + app invariant).
	var owners int64
	if err := f.pool.QueryRow(ctx,
		"SELECT count(*) FROM project_members WHERE project_id = $1 AND role = 'owner'", p.ID).Scan(&owners); err != nil {
		t.Fatal(err)
	}
	if owners != 1 {
		t.Errorf("exactly one owner required, got %d", owners)
	}
}

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/metamin44/go-task-management-api/internal/domain"
)

// fixture: owner, admin, member, outsider users; one project; one task
// created by member and assigned to member.
type authzFixture struct {
	f                           *fakeStore
	owner, admin, member, other domain.UUID
	pid, taskID                 domain.UUID
	projects                    *ProjectService
	members                     *MemberService
	tasks                       *TaskService
}

func newAuthzFixture(t *testing.T) *authzFixture {
	t.Helper()
	f := newFakeStore()
	fx := &authzFixture{f: f}
	fx.owner = seedUser(f, "owner@example.com")
	fx.admin = seedUser(f, "admin@example.com")
	fx.member = seedUser(f, "member@example.com")
	fx.other = seedUser(f, "other@example.com")
	fx.pid = seedProject(f, fx.owner)
	seedMember(f, fx.pid, fx.admin, domain.RoleAdmin)
	seedMember(f, fx.pid, fx.member, domain.RoleMember)
	fx.projects = NewProjectService(&f.projects, &f.members, f)
	fx.members = NewMemberService(&f.projects, &f.members, &f.users, &f.tasks, f)
	fx.tasks = NewTaskService(&f.tasks, &f.members)

	task, err := fx.tasks.Create(context.Background(), fx.member, fx.pid, TaskInput{
		Title:      "Task",
		AssignedTo: &fx.member,
	})
	if err != nil {
		t.Fatalf("seed task: %v", err)
	}
	fx.taskID = task.ID
	return fx
}

func TestProjectCreateAtomicWithOwner(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	svc := NewProjectService(&f.projects, &f.members, f)
	uid := seedUser(f, "owner@example.com")

	p, err := svc.Create(ctx, uid, "New Project", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	m, err := f.members.Get(ctx, p.ID, uid)
	if err != nil || m.Role != domain.RoleOwner {
		t.Fatalf("creator must hold owner membership: %v", err)
	}

	// Rollback: membership failure must leave no project behind.
	f.addMemberErr = errors.New("injected membership failure")
	if _, err := svc.Create(ctx, uid, "Doomed Project", nil); err == nil {
		t.Fatal("injected failure must propagate")
	}
	for _, p := range f.db.projects {
		if p.Name == "Doomed Project" {
			t.Error("failed create must roll back the project insert")
		}
	}
}

func TestProjectRolePolicy(t *testing.T) {
	ctx := context.Background()
	fx := newAuthzFixture(t)

	if _, err := fx.projects.Get(ctx, fx.member, fx.pid); err != nil {
		t.Errorf("member must read project: %v", err)
	}
	if _, err := fx.projects.Get(ctx, fx.other, fx.pid); !errors.Is(err, ErrForbidden) {
		t.Errorf("outsider must be forbidden, got %v", err)
	}
	renamed := "Renamed"
	if _, err := fx.projects.Update(ctx, fx.admin, fx.pid, ProjectPatch{Name: &renamed}); err != nil {
		t.Errorf("admin must update project: %v", err)
	}
	nope := "Nope"
	if _, err := fx.projects.Update(ctx, fx.member, fx.pid, ProjectPatch{Name: &nope}); !errors.Is(err, ErrForbidden) {
		t.Errorf("member must not update project, got %v", err)
	}
	if err := fx.projects.Delete(ctx, fx.admin, fx.pid); !errors.Is(err, ErrForbidden) {
		t.Errorf("admin must not delete project, got %v", err)
	}
	if err := fx.projects.Delete(ctx, fx.owner, fx.pid); err != nil {
		t.Errorf("owner must delete project: %v", err)
	}
	if len(fx.f.db.tasks) != 0 || len(fx.f.db.members) != 0 {
		t.Error("project delete must cascade tasks and memberships")
	}
}

func TestMemberAddPolicy(t *testing.T) {
	ctx := context.Background()
	fx := newAuthzFixture(t)

	if _, err := fx.members.Add(ctx, fx.admin, fx.pid, fx.other); err != nil {
		t.Errorf("admin must add members: %v", err)
	}
	m, _ := fx.f.members.Get(ctx, fx.pid, fx.other)
	if m.Role != domain.RoleMember {
		t.Error("added members must start with role member")
	}
	if _, err := fx.members.Add(ctx, fx.member, fx.pid, fx.other); !errors.Is(err, ErrForbidden) {
		t.Errorf("plain member must not add, got %v", err)
	}
	if _, err := fx.members.Add(ctx, fx.owner, fx.pid, fx.other); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("duplicate membership must conflict, got %v", err)
	}
	if _, err := fx.members.Add(ctx, fx.owner, fx.pid, domain.NewUUID()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown target user must be not found, got %v", err)
	}
}

func TestMemberListPolicy(t *testing.T) {
	ctx := context.Background()
	fx := newAuthzFixture(t)

	list, err := fx.members.List(ctx, fx.member, fx.pid)
	if err != nil || len(list) != 3 {
		t.Fatalf("member must list memberships: n=%d err=%v", len(list), err)
	}
	if _, err := fx.members.List(ctx, fx.other, fx.pid); !errors.Is(err, ErrForbidden) {
		t.Errorf("outsider must not list members, got %v", err)
	}
}

func TestMemberRoleChangeProtectsOwner(t *testing.T) {
	ctx := context.Background()
	fx := newAuthzFixture(t)

	if err := fx.members.UpdateRole(ctx, fx.owner, fx.pid, fx.member, domain.RoleAdmin); err != nil {
		t.Errorf("owner must promote member: %v", err)
	}
	if err := fx.members.UpdateRole(ctx, fx.admin, fx.pid, fx.member, domain.RoleMember); err != nil {
		t.Errorf("admin must demote admin: %v", err)
	}
	if err := fx.members.UpdateRole(ctx, fx.admin, fx.pid, fx.owner, domain.RoleMember); !errors.Is(err, ErrForbidden) {
		t.Errorf("owner must never be demoted via role change, got %v", err)
	}
	if err := fx.members.UpdateRole(ctx, fx.member, fx.pid, fx.admin, domain.RoleMember); !errors.Is(err, ErrForbidden) {
		t.Errorf("plain member must not change roles, got %v", err)
	}
	if err := fx.members.UpdateRole(ctx, fx.owner, fx.pid, fx.member, domain.RoleOwner); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("owner role via UpdateRole must be invalid, got %v", err)
	}
}

func TestMemberRemovePolicy(t *testing.T) {
	ctx := context.Background()
	fx := newAuthzFixture(t)

	// member still has the assigned fixture task.
	err := fx.members.Remove(ctx, fx.owner, fx.pid, fx.member)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("removal with assigned tasks must conflict, got %v", err)
	}
	if _, err := fx.f.members.Get(ctx, fx.pid, fx.member); err != nil {
		t.Error("failed removal must keep the membership")
	}

	if err := fx.members.Remove(ctx, fx.owner, fx.pid, fx.owner); !errors.Is(err, ErrForbidden) {
		t.Errorf("owner removal must require transfer first, got %v", err)
	}
	if err := fx.members.Remove(ctx, fx.member, fx.pid, fx.admin); !errors.Is(err, ErrForbidden) {
		t.Errorf("plain member must not remove others, got %v", err)
	}

	// Unassign, then removal succeeds.
	if _, err := fx.f.tasks.UnassignMemberTasks(ctx, fx.pid, fx.member); err != nil {
		t.Fatal(err)
	}
	if err := fx.members.Remove(ctx, fx.admin, fx.pid, fx.member); err != nil {
		t.Errorf("removal after unassignment must succeed: %v", err)
	}

	// Self-leave for non-owners.
	if err := fx.members.Remove(ctx, fx.admin, fx.pid, fx.admin); err != nil {
		t.Errorf("admin self-leave must succeed: %v", err)
	}
}

func TestTransferOwnership(t *testing.T) {
	ctx := context.Background()
	fx := newAuthzFixture(t)

	if err := fx.members.TransferOwnership(ctx, fx.admin, fx.pid, fx.member); !errors.Is(err, ErrForbidden) {
		t.Errorf("only the owner may transfer, got %v", err)
	}
	if err := fx.members.TransferOwnership(ctx, fx.owner, fx.pid, fx.other); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("target must be an existing member, got %v", err)
	}

	fx.f.log = nil
	if err := fx.members.TransferOwnership(ctx, fx.owner, fx.pid, fx.member); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	oldOwner, _ := fx.f.members.Get(ctx, fx.pid, fx.owner)
	newOwner, _ := fx.f.members.Get(ctx, fx.pid, fx.member)
	if oldOwner.Role != domain.RoleMember || newOwner.Role != domain.RoleOwner {
		t.Error("transfer must demote old owner and promote target")
	}
	owners := 0
	for _, m := range fx.f.db.members {
		if m.ProjectID == fx.pid && m.Role == domain.RoleOwner {
			owners++
		}
	}
	if owners != 1 {
		t.Errorf("exactly one owner required, got %d", owners)
	}
	// Demote-before-promote: exactly two UpdateRole ops. (Promote-first would
	// violate the one-owner partial unique index, so success proves ordering.)
	roleOps := 0
	for _, op := range fx.f.log {
		if op == "members.UpdateRole" {
			roleOps++
		}
	}
	if roleOps != 2 {
		t.Errorf("expected two UpdateRole ops, log=%v", fx.f.log)
	}

	// Self-transfer is a no-op.
	if err := fx.members.TransferOwnership(ctx, fx.member, fx.pid, fx.member); err != nil {
		t.Errorf("self-transfer must be a no-op: %v", err)
	}

	// Rollback: promotion failure must restore the original owner.
	fx.f.updateRoleErr = errors.New("injected promotion failure")
	fx.f.updateRoleSkip = 1 // demote succeeds, promote fails
	if err := fx.members.TransferOwnership(ctx, fx.member, fx.pid, fx.admin); err == nil {
		t.Fatal("injected failure must propagate")
	}
	m, _ := fx.f.members.Get(ctx, fx.pid, fx.member)
	if m.Role != domain.RoleOwner {
		t.Error("failed transfer must roll back completely (original owner restored)")
	}
}

func TestTaskCreatePolicy(t *testing.T) {
	ctx := context.Background()
	fx := newAuthzFixture(t)

	if _, err := fx.tasks.Create(ctx, fx.other, fx.pid, TaskInput{Title: "X"}); !errors.Is(err, ErrForbidden) {
		t.Errorf("non-member must not create tasks, got %v", err)
	}
	if _, err := fx.tasks.Create(ctx, fx.member, fx.pid, TaskInput{Title: "X", AssignedTo: &fx.other}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("non-member assignee must be invalid, got %v", err)
	}
	if _, err := fx.tasks.Create(ctx, fx.member, fx.pid, TaskInput{Title: "   "}); err == nil {
		t.Error("blank title must be rejected")
	}
	task, err := fx.tasks.Create(ctx, fx.member, fx.pid, TaskInput{Title: "Defaults"})
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.StatusTodo || task.Priority != domain.PriorityMedium {
		t.Error("defaults must be todo/medium")
	}
}

func TestTaskWritePolicy(t *testing.T) {
	ctx := context.Background()
	fx := newAuthzFixture(t)
	// Fixture task: created by member, assigned to member.
	assigneeCreator := fx.member

	cases := []struct {
		name    string
		actor   domain.UUID
		allowed bool
	}{
		{"owner", fx.owner, true},
		{"admin", fx.admin, true},
		{"creator_assignee", assigneeCreator, true},
		{"plain_other_member", fx.other, false}, // not a member at all
	}
	for _, tc := range cases {
		t.Run("update_"+tc.name, func(t *testing.T) {
			// Give 'other' a plain membership for the non-creator member case.
			if tc.actor == fx.other {
				seedMember(fx.f, fx.pid, fx.other, domain.RoleMember)
			}
			title := "Updated"
			_, err := fx.tasks.Update(ctx, tc.actor, fx.taskID, TaskPatch{Title: &title})
			if tc.allowed && err != nil {
				t.Errorf("allowed actor rejected: %v", err)
			}
			if !tc.allowed && !errors.Is(err, ErrForbidden) {
				t.Errorf("disallowed actor must be forbidden, got %v", err)
			}
		})
	}

	// Assignee-only permission: task created by owner, assigned to member.
	task2, err := fx.tasks.Create(ctx, fx.owner, fx.pid, TaskInput{Title: "Second", AssignedTo: &fx.member})
	if err != nil {
		t.Fatal(err)
	}
	status := domain.StatusDone
	if _, err := fx.tasks.Update(ctx, fx.member, task2.ID, TaskPatch{Status: &status}); err != nil {
		t.Errorf("assignee must update status: %v", err)
	}
	seedMember(fx.f, fx.pid, fx.other, domain.RoleMember)
	if err := fx.tasks.Delete(ctx, fx.other, task2.ID); !errors.Is(err, ErrForbidden) {
		t.Errorf("plain member must not delete others' tasks, got %v", err)
	}
	if err := fx.tasks.Delete(ctx, fx.member, task2.ID); err != nil {
		t.Errorf("assignee must delete: %v", err)
	}
}

func TestTaskReadAndListScoping(t *testing.T) {
	ctx := context.Background()
	fx := newAuthzFixture(t)

	if _, err := fx.tasks.Get(ctx, fx.other, fx.taskID); !errors.Is(err, ErrForbidden) {
		t.Errorf("non-member must not read tasks, got %v", err)
	}
	if _, err := fx.tasks.List(ctx, fx.other, fx.pid, domain.TaskFilter{}); !errors.Is(err, ErrForbidden) {
		t.Errorf("non-member must not list, got %v", err)
	}
	tasks, err := fx.tasks.List(ctx, fx.member, fx.pid, domain.TaskFilter{})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("member list: n=%d err=%v", len(tasks), err)
	}

	// ListMine forces the assignee filter.
	mine, err := fx.tasks.ListMine(ctx, fx.member, domain.TaskFilter{})
	if err != nil || len(mine) != 1 {
		t.Fatalf("ListMine: n=%d err=%v", len(mine), err)
	}
	none, err := fx.tasks.ListMine(ctx, fx.admin, domain.TaskFilter{})
	if err != nil || len(none) != 0 {
		t.Fatalf("admin has no assigned tasks: n=%d err=%v", len(none), err)
	}
}

func TestTaskPatchValidation(t *testing.T) {
	ctx := context.Background()
	fx := newAuthzFixture(t)
	uid := fx.member

	if _, err := fx.tasks.Update(ctx, uid, fx.taskID, TaskPatch{Unassign: true, AssignedTo: &fx.admin}); !errors.Is(err, domain.ErrInvalid) {
		t.Error("assign+unassign conflict must be invalid")
	}
	if _, err := fx.tasks.Update(ctx, uid, fx.taskID, TaskPatch{ClearDueAt: true, DueAt: ptrTime()}); !errors.Is(err, domain.ErrInvalid) {
		t.Error("set+clear due conflict must be invalid")
	}
	bad := domain.Status("blocked")
	if _, err := fx.tasks.Update(ctx, uid, fx.taskID, TaskPatch{Status: &bad}); !errors.Is(err, domain.ErrInvalid) {
		t.Error("invalid status must be rejected")
	}
	if _, err := fx.tasks.Update(ctx, uid, fx.taskID, TaskPatch{AssignedTo: &fx.other}); !errors.Is(err, domain.ErrInvalid) {
		t.Error("non-member assignee must be rejected")
	}
	updated, err := fx.tasks.Update(ctx, uid, fx.taskID, TaskPatch{Unassign: true, ClearDueAt: true})
	if err != nil {
		t.Fatal(err)
	}
	if updated.AssignedTo != nil || updated.DueAt != nil {
		t.Error("unassign and clear-due must null the fields")
	}
}

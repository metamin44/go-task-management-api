//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/metamin44/go-task-management-api/internal/service"
)

// R-03 against real PostgreSQL: the non-PATCH writers (UpdatePasswordHash,
// UnassignMemberTasks) use the same GREATEST(now(), updated_at + 1µs) expression,
// so updated_at strictly advances even when a prior value is in the future and
// the concurrency version never regresses.

func TestT7PasswordHashAdvancesUpdatedAt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	uid := seedUser(t, f, "user@example.com")

	if _, err := f.pool.Exec(ctx,
		`UPDATE users SET updated_at = now() + interval '1 hour' WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	u, _ := f.users.GetByID(ctx, uid)
	future := u.UpdatedAt
	if err := f.users.UpdatePasswordHash(ctx, uid, "new-hash-value"); err != nil {
		t.Fatal(err)
	}
	got, _ := f.users.GetByID(ctx, uid)
	if !got.UpdatedAt.After(future) {
		t.Fatalf("password change must advance updated_at past a future stamp: %v -> %v", future, got.UpdatedAt)
	}
	if got.PasswordHash != "new-hash-value" {
		t.Error("password hash must be updated")
	}
}

func TestT7UnassignAdvancesUpdatedAt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner := seedUser(t, f, "owner@example.com")
	member := seedUser(t, f, "member@example.com")
	p, err := f.projectSvc.Create(ctx, owner, "Project", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.memberSvc.Add(ctx, owner, p.ID, member); err != nil {
		t.Fatal(err)
	}
	task, err := f.taskSvc.Create(ctx, owner, p.ID, service.TaskInput{Title: "T", AssignedTo: &member})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx,
		`UPDATE tasks SET updated_at = now() + interval '1 hour' WHERE id = $1`, task.ID); err != nil {
		t.Fatal(err)
	}
	before, _ := f.tasks.GetByID(ctx, task.ID)
	future := before.UpdatedAt

	n, err := f.tasks.UnassignMemberTasks(ctx, p.ID, member)
	if err != nil || n != 1 {
		t.Fatalf("unassign: n=%d err=%v", n, err)
	}
	got, _ := f.tasks.GetByID(ctx, task.ID)
	if got.AssignedTo != nil {
		t.Error("task must be unassigned")
	}
	if !got.UpdatedAt.After(future) {
		t.Fatalf("unassign must advance updated_at past a future stamp: %v -> %v", future, got.UpdatedAt)
	}
}

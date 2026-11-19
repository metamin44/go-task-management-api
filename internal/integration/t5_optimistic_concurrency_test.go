//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/metamin44/go-task-management-api/internal/domain"
	"github.com/metamin44/go-task-management-api/internal/service"
)

// F-02 (optimistic concurrency) against real PostgreSQL: the updated_at version
// predicate rejects stale writes (ErrConflict), deleted rows stay ErrNotFound,
// concurrent updates to different fields cannot silently overwrite each other,
// and updated_at strictly advances even within one clock tick. These exercise
// the actual SQL (GREATEST + version predicate) that fakes cannot prove.

func TestT5UserOptimisticConcurrency(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	uid := seedUser(t, f, "user@example.com")

	base, err := f.users.GetByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	// A concurrent writer commits a name change on a fresh read.
	w, _ := f.users.GetByID(ctx, uid)
	w.Name = "Writer A"
	if err := f.users.Update(ctx, w); err != nil {
		t.Fatalf("concurrent writer: %v", err)
	}
	// A stale update (base version) must conflict, not overwrite.
	stale := *base
	stale.Email = "stale@example.com"
	if err := f.users.Update(ctx, &stale); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale update must conflict, got %v", err)
	}
	got, _ := f.users.GetByID(ctx, uid)
	if got.Name != "Writer A" || got.Email == "stale@example.com" {
		t.Errorf("lost update: name=%q email=%q", got.Name, got.Email)
	}
	// After deletion the same stale update is not-found, not conflict.
	if err := f.users.Delete(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := f.users.Update(ctx, &stale); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("update of deleted row must be not-found, got %v", err)
	}
}

func TestT5ProjectOptimisticConcurrency(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner := seedUser(t, f, "owner@example.com")
	p, err := f.projectSvc.Create(ctx, owner, "Project", nil)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := f.projects.GetByID(ctx, p.ID)
	w, _ := f.projects.GetByID(ctx, p.ID)
	w.Name = "Writer A"
	if err := f.projects.Update(ctx, w); err != nil {
		t.Fatalf("concurrent writer: %v", err)
	}
	stale := *base
	stale.Name = "Stale"
	if err := f.projects.Update(ctx, &stale); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale update must conflict, got %v", err)
	}
	got, _ := f.projects.GetByID(ctx, p.ID)
	if got.Name != "Writer A" {
		t.Errorf("lost update: name=%q", got.Name)
	}
	if err := f.projects.Delete(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.projects.Update(ctx, &stale); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("update of deleted project must be not-found, got %v", err)
	}
}

func TestT5TaskConcurrentDifferentFields(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner := seedUser(t, f, "owner@example.com")
	p, err := f.projectSvc.Create(ctx, owner, "Project", nil)
	if err != nil {
		t.Fatal(err)
	}
	task, err := f.taskSvc.Create(ctx, owner, p.ID, service.TaskInput{Title: "Original"})
	if err != nil {
		t.Fatal(err)
	}
	base, _ := f.tasks.GetByID(ctx, task.ID) // title Original, status todo

	// Writer A changes the title on a fresh read.
	wa, _ := f.tasks.GetByID(ctx, task.ID)
	wa.Title = "Title A"
	if err := f.tasks.Update(ctx, wa); err != nil {
		t.Fatalf("writer A: %v", err)
	}
	// Writer B changes a DIFFERENT field (status) from the stale base.
	wb := *base
	wb.Status = domain.StatusDone
	if err := f.tasks.Update(ctx, &wb); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale different-field update must conflict, got %v", err)
	}
	// No lost update: A's title kept, B's stale status rejected.
	got, _ := f.tasks.GetByID(ctx, task.ID)
	if got.Title != "Title A" {
		t.Errorf("title lost: %q", got.Title)
	}
	if got.Status != domain.StatusTodo {
		t.Errorf("stale status leaked: %q", got.Status)
	}
	// B retries on a fresh base: both changes now coexist.
	wb2, _ := f.tasks.GetByID(ctx, task.ID)
	wb2.Status = domain.StatusDone
	if err := f.tasks.Update(ctx, wb2); err != nil {
		t.Fatalf("fresh retry: %v", err)
	}
	got, _ = f.tasks.GetByID(ctx, task.ID)
	if got.Title != "Title A" || got.Status != domain.StatusDone {
		t.Errorf("retry must merge both changes: title=%q status=%q", got.Title, got.Status)
	}
}

func TestT5UpdatedAtStrictlyAdvances(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	uid := seedUser(t, f, "user@example.com")

	// Force a FUTURE updated_at to exercise GREATEST(now(), updated_at + 1µs).
	if _, err := f.pool.Exec(ctx,
		`UPDATE users SET updated_at = now() + interval '1 hour' WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	u, _ := f.users.GetByID(ctx, uid)
	future := u.UpdatedAt
	u.Name = "Advanced"
	if err := f.users.Update(ctx, u); err != nil {
		t.Fatal(err)
	}
	if !u.UpdatedAt.After(future) {
		t.Fatalf("updated_at must advance past a future stamp: %v -> %v", future, u.UpdatedAt)
	}
	// Two rapid sequential updates must also strictly advance.
	prev := u.UpdatedAt
	u.Name = "Again"
	if err := f.users.Update(ctx, u); err != nil {
		t.Fatal(err)
	}
	if !u.UpdatedAt.After(prev) {
		t.Fatalf("sequential update must advance updated_at: %v -> %v", prev, u.UpdatedAt)
	}
}

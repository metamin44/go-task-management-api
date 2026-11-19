package service

import (
	"context"
	"errors"
	"testing"

	"github.com/metamin44/go-task-management-api/internal/domain"
)

// F-02 / R-01: optimistic concurrency and service-side partial merging. The
// fakes mirror the real updated_at version predicate; the updateConflict hook
// simulates a concurrent writer committing between the service's read and its
// update. Omitted fields must keep their latest committed value because merging
// happens on the service's freshest read (no stale handler-side merge).

func TestUpdateProfileConflictThenRetry(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	uid := seedUser(f, "user@example.com")
	svc := NewUserService(&f.users)
	name, email := "New Name", "user@example.com"

	f.updateConflict = true
	if _, err := svc.UpdateProfile(ctx, uid, &name, &email); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("concurrent modification must conflict, got %v", err)
	}
	u, err := svc.UpdateProfile(ctx, uid, &name, &email)
	if err != nil {
		t.Fatalf("fresh retry must succeed: %v", err)
	}
	if u.Name != "New Name" {
		t.Error("retry must apply the change")
	}
}

func TestUpdateProfileMissingUser(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	svc := NewUserService(&f.users)
	n, e := "N", "e@x.y"
	if _, err := svc.UpdateProfile(ctx, domain.NewUUID(), &n, &e); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing user must be not-found, got %v", err)
	}
}

// R-01 regression: a partial update that omits name must preserve the latest
// committed name (the value a concurrent writer set), not revert it.
func TestUpdateProfilePreservesLatestOmittedField(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	uid := seedUser(f, "user@example.com")
	svc := NewUserService(&f.users)

	b := "B"
	if _, err := svc.UpdateProfile(ctx, uid, &b, nil); err != nil {
		t.Fatal(err)
	}
	e := "new@example.com"
	u, err := svc.UpdateProfile(ctx, uid, nil, &e)
	if err != nil {
		t.Fatal(err)
	}
	if u.Name != "B" {
		t.Errorf("omitted name must keep latest value B, got %q", u.Name)
	}
	if u.Email != "new@example.com" {
		t.Errorf("email not applied: %q", u.Email)
	}
}

func TestProjectUpdateConflictThenRetry(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	owner := seedUser(f, "owner@example.com")
	pid := seedProject(f, owner)
	svc := NewProjectService(&f.projects, &f.members, f)
	renamed := "Renamed"

	f.updateConflict = true
	if _, err := svc.Update(ctx, owner, pid, ProjectPatch{Name: &renamed}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("concurrent modification must conflict, got %v", err)
	}
	p, err := svc.Update(ctx, owner, pid, ProjectPatch{Name: &renamed})
	if err != nil {
		t.Fatalf("fresh retry must succeed: %v", err)
	}
	if p.Name != "Renamed" {
		t.Error("retry must apply the change")
	}
}

// R-01 regression: a partial update that omits name must preserve the latest
// committed name while applying the description change.
func TestProjectUpdatePreservesLatestOmittedField(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	owner := seedUser(f, "owner@example.com")
	pid := seedProject(f, owner)
	svc := NewProjectService(&f.projects, &f.members, f)

	b := "B"
	if _, err := svc.Update(ctx, owner, pid, ProjectPatch{Name: &b}); err != nil {
		t.Fatal(err)
	}
	d := "new description"
	p, err := svc.Update(ctx, owner, pid, ProjectPatch{Description: &d, DescriptionSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "B" {
		t.Errorf("omitted name must keep latest value B, got %q", p.Name)
	}
	if p.Description == nil || *p.Description != "new description" {
		t.Errorf("description not applied: %v", p.Description)
	}
}

func TestTaskUpdateConflictThenRetry(t *testing.T) {
	ctx := context.Background()
	fx := newAuthzFixture(t)
	title := "New Title"

	fx.f.updateConflict = true
	if _, err := fx.tasks.Update(ctx, fx.member, fx.taskID, TaskPatch{Title: &title}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale update must conflict, got %v", err)
	}
	got, err := fx.tasks.Update(ctx, fx.member, fx.taskID, TaskPatch{Title: &title})
	if err != nil {
		t.Fatalf("fresh retry must succeed: %v", err)
	}
	if got.Title != title {
		t.Error("retry must apply the change")
	}
}

func TestTaskUpdateMissingTask(t *testing.T) {
	ctx := context.Background()
	fx := newAuthzFixture(t)
	title := "X"
	if _, err := fx.tasks.Update(ctx, fx.member, domain.NewUUID(), TaskPatch{Title: &title}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing task must be not-found, got %v", err)
	}
}

// Q-01: two writers updating DIFFERENT fields concurrently. Writer A commits an
// email change; writer B's update carries the pre-A version and only touches
// name. The version predicate must reject B's stale write (no silent lost
// update of A's email); after a fresh service retry both changes are present
// because merging happens on the service's freshest read. Deterministic: the
// stale version is captured explicitly, no goroutines or timing involved.
func TestUpdateProfileConcurrentDifferentFieldsNoLostUpdate(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	uid := seedUser(f, "user@example.com")
	svc := NewUserService(&f.users)

	// Writer B's client read the row before A committed (stale version v0).
	v0, err := f.users.GetByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}

	// Writer A concurrently commits an EMAIL change (advances the version).
	emailA := "a@example.com"
	if _, err := svc.UpdateProfile(ctx, uid, nil, &emailA); err != nil {
		t.Fatal(err)
	}

	// B's stale name-only update must conflict, not overwrite A's email.
	nameB := "B Name"
	stale := *v0
	stale.Name = nameB
	if err := f.users.Update(ctx, &stale); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale different-field update must conflict, got %v", err)
	}

	// B retries through the service (fresh read): name applied, A's email kept.
	u, err := svc.UpdateProfile(ctx, uid, &nameB, nil)
	if err != nil {
		t.Fatalf("fresh retry must succeed: %v", err)
	}
	if u.Name != nameB {
		t.Errorf("retry must apply name %q, got %q", nameB, u.Name)
	}
	if u.Email != emailA {
		t.Errorf("concurrent email change lost: want %q, got %q", emailA, u.Email)
	}
}

// Q-01: project analogue — a concurrent description change must survive a
// stale name-only update (conflict) and a fresh retry (preservation).
func TestProjectUpdateConcurrentDifferentFieldsNoLostUpdate(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	owner := seedUser(f, "owner@example.com")
	pid := seedProject(f, owner)
	svc := NewProjectService(&f.projects, &f.members, f)

	v0, err := f.projects.GetByID(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}

	// Writer A commits a DESCRIPTION change.
	descA := "A description"
	if _, err := svc.Update(ctx, owner, pid, ProjectPatch{Description: &descA, DescriptionSet: true}); err != nil {
		t.Fatal(err)
	}

	// Writer B's stale name-only update (pre-A version) must conflict.
	nameB := "B Project"
	stale := *v0
	stale.Name = nameB
	if err := f.projects.Update(ctx, &stale); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale different-field update must conflict, got %v", err)
	}

	// Fresh retry: name applied, A's description preserved.
	p, err := svc.Update(ctx, owner, pid, ProjectPatch{Name: &nameB})
	if err != nil {
		t.Fatalf("fresh retry must succeed: %v", err)
	}
	if p.Name != nameB {
		t.Errorf("retry must apply name %q, got %q", nameB, p.Name)
	}
	if p.Description == nil || *p.Description != descA {
		t.Errorf("concurrent description change lost: %v", p.Description)
	}
}

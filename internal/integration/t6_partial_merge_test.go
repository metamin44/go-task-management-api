//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/metamin44/go-task-management-api/internal/service"
)

// R-01 against real PostgreSQL: partial updates merge on the service's freshest
// read, so a field omitted by a later update keeps the latest committed value
// (set here by an earlier "concurrent" writer) instead of being reverted.

func TestT6UserPartialUpdatePreservesLatestName(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	uid := seedUser(t, f, "user@example.com")

	// A concurrent writer commits a new name.
	concurrent := "ConcurrentName"
	if _, err := f.userSvc.UpdateProfile(ctx, uid, &concurrent, nil); err != nil {
		t.Fatal(err)
	}
	// A later partial update changes only email; name is omitted.
	email := "new@example.com"
	u, err := f.userSvc.UpdateProfile(ctx, uid, nil, &email)
	if err != nil {
		t.Fatal(err)
	}
	if u.Name != "ConcurrentName" {
		t.Errorf("omitted name must keep latest value, got %q", u.Name)
	}
	got, _ := f.users.GetByID(ctx, uid)
	if got.Name != "ConcurrentName" || got.Email != "new@example.com" {
		t.Errorf("lost update in DB: name=%q email=%q", got.Name, got.Email)
	}
}

func TestT6ProjectPartialUpdatePreservesLatestName(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner := seedUser(t, f, "owner@example.com")
	p, err := f.projectSvc.Create(ctx, owner, "Project", nil)
	if err != nil {
		t.Fatal(err)
	}

	concurrent := "ConcurrentName"
	if _, err := f.projectSvc.Update(ctx, owner, p.ID, service.ProjectPatch{Name: &concurrent}); err != nil {
		t.Fatal(err)
	}
	// Later partial update changes only description; name omitted.
	desc := "new description"
	got, err := f.projectSvc.Update(ctx, owner, p.ID, service.ProjectPatch{Description: &desc, DescriptionSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "ConcurrentName" {
		t.Errorf("omitted name must keep latest value, got %q", got.Name)
	}
	db, _ := f.projects.GetByID(ctx, p.ID)
	if db.Name != "ConcurrentName" || db.Description == nil || *db.Description != "new description" {
		t.Errorf("lost update in DB: name=%q desc=%v", db.Name, db.Description)
	}
}

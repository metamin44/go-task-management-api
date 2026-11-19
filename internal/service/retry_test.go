package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/metamin44/go-task-management-api/internal/domain"
)

func ptrTime() *time.Time {
	t := time.Now().Add(24 * time.Hour)
	return &t
}

func TestRetryOnSerializationFailure(t *testing.T) {
	ctx := context.Background()
	fx := newAuthzFixture(t)

	// One transient serialization failure, then success.
	fx.f.lockSerialize = 1
	if err := fx.members.TransferOwnership(ctx, fx.owner, fx.pid, fx.member); err != nil {
		t.Fatalf("must retry and succeed: %v", err)
	}
	begins := 0
	for _, op := range fx.f.log {
		if op == "begin" {
			begins++
		}
	}
	if begins != 2 {
		t.Errorf("expected 2 transaction attempts, got %d", begins)
	}
}

func TestRetryBounded(t *testing.T) {
	ctx := context.Background()
	fx := newAuthzFixture(t)

	fx.f.lockSerialize = 10 // more failures than the retry budget
	err := fx.members.TransferOwnership(ctx, fx.owner, fx.pid, fx.member)
	if !errors.Is(err, domain.ErrSerialization) {
		t.Fatalf("must surface serialization error after bounded retries, got %v", err)
	}
	begins := 0
	for _, op := range fx.f.log {
		if op == "begin" {
			begins++
		}
	}
	if begins != maxTxAttempts {
		t.Errorf("expected exactly %d attempts, got %d", maxTxAttempts, begins)
	}
	// State untouched after exhausted retries.
	m, _ := fx.f.members.Get(ctx, fx.pid, fx.owner)
	if m.Role != domain.RoleOwner {
		t.Error("exhausted retries must leave state unchanged")
	}
}

func TestNoRetryOnNonSerializationErrors(t *testing.T) {
	ctx := context.Background()
	fx := newAuthzFixture(t)
	fx.f.addMemberErr = errors.New("permanent failure")
	svc := NewProjectService(&fx.f.projects, &fx.f.members, fx.f)
	if _, err := svc.Create(ctx, fx.owner, "X", nil); err == nil {
		t.Fatal("expected failure")
	}
	begins := strings.Count(strings.Join(fx.f.log, ","), "begin")
	if begins != 1 {
		t.Errorf("non-serialization errors must not retry, attempts=%d", begins)
	}
}

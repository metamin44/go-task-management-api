package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/metamin44/go-task-management-api/internal/domain"
)

// MemberService implements membership management. Every mutation runs in a
// transaction under the project row lock (finalized serialization rule).
// Policy: add/role-change/remove = owner/admin; the owner can never be
// demoted or removed except via atomic ownership transfer (owner-only).
// Non-owners may remove themselves (leave). New members are added with role
// member; elevation happens via UpdateRole.
type MemberService struct {
	projects domain.ProjectRepository
	members  domain.ProjectMemberRepository
	users    domain.UserRepository
	tasks    domain.TaskRepository
	tx       domain.Transactor
}

// NewMemberService wires the membership service.
func NewMemberService(
	projects domain.ProjectRepository,
	members domain.ProjectMemberRepository,
	users domain.UserRepository,
	tasks domain.TaskRepository,
	tx domain.Transactor,
) *MemberService {
	return &MemberService{projects: projects, members: members, users: users, tasks: tasks, tx: tx}
}

// List returns the project's memberships (members only).
func (s *MemberService) List(ctx context.Context, actorID, projectID domain.UUID) ([]domain.ProjectMember, error) {
	if _, err := requireMembership(ctx, s.members, projectID, actorID); err != nil {
		return nil, err
	}
	return s.members.ListByProject(ctx, projectID)
}

// Add invites an existing user into the project as a member (owner/admin).
func (s *MemberService) Add(ctx context.Context, actorID, projectID, targetUserID domain.UUID) (*domain.ProjectMember, error) {
	var m *domain.ProjectMember
	err := withRetry(ctx, func(ctx context.Context) error {
		return s.tx.WithTx(ctx, func(ctx context.Context) error {
			if err := s.projects.LockForUpdate(ctx, projectID); err != nil {
				return err
			}
			actor, err := requireMembership(ctx, s.members, projectID, actorID)
			if err != nil {
				return err
			}
			if err := requireRole(actor, domain.RoleOwner, domain.RoleAdmin); err != nil {
				return err
			}
			if _, err := s.users.GetByID(ctx, targetUserID); err != nil {
				return fmt.Errorf("target user: %w", err)
			}
			m = &domain.ProjectMember{ProjectID: projectID, UserID: targetUserID, Role: domain.RoleMember}
			return s.members.Add(ctx, m)
		})
	})
	if err != nil {
		return nil, err
	}
	return m, nil
}

// UpdateRole toggles a non-owner member between admin and member
// (owner/admin only). The owner role changes exclusively via transfer.
func (s *MemberService) UpdateRole(ctx context.Context, actorID, projectID, targetUserID domain.UUID, newRole domain.Role) error {
	if newRole != domain.RoleAdmin && newRole != domain.RoleMember {
		return fmt.Errorf("role must be admin or member: %w", domain.ErrInvalid)
	}
	return withRetry(ctx, func(ctx context.Context) error {
		return s.tx.WithTx(ctx, func(ctx context.Context) error {
			if err := s.projects.LockForUpdate(ctx, projectID); err != nil {
				return err
			}
			actor, err := requireMembership(ctx, s.members, projectID, actorID)
			if err != nil {
				return err
			}
			if err := requireRole(actor, domain.RoleOwner, domain.RoleAdmin); err != nil {
				return err
			}
			target, err := s.members.Get(ctx, projectID, targetUserID)
			if err != nil {
				return err
			}
			if target.Role == domain.RoleOwner {
				return fmt.Errorf("owner role changes only via ownership transfer: %w", ErrForbidden)
			}
			return s.members.UpdateRole(ctx, projectID, targetUserID, newRole)
		})
	})
}

// Remove deletes a membership (owner/admin, or self-leave for non-owners).
// The owner cannot be removed (transfer first). Members with assigned tasks
// cannot be removed until tasks are unassigned/reassigned: a pre-check gives
// a clear conflict, and the composite FK is the concurrency backstop.
func (s *MemberService) Remove(ctx context.Context, actorID, projectID, targetUserID domain.UUID) error {
	return withRetry(ctx, func(ctx context.Context) error {
		return s.tx.WithTx(ctx, func(ctx context.Context) error {
			if err := s.projects.LockForUpdate(ctx, projectID); err != nil {
				return err
			}
			actor, err := requireMembership(ctx, s.members, projectID, actorID)
			if err != nil {
				return err
			}
			if actorID != targetUserID {
				if err := requireRole(actor, domain.RoleOwner, domain.RoleAdmin); err != nil {
					return err
				}
			}
			target, err := s.members.Get(ctx, projectID, targetUserID)
			if err != nil {
				return err
			}
			if target.Role == domain.RoleOwner {
				return fmt.Errorf("owner must transfer ownership before removal: %w", ErrForbidden)
			}
			assigned, err := s.tasks.List(ctx, domain.TaskFilter{
				ProjectID:  &projectID,
				AssignedTo: &targetUserID,
				Limit:      1,
			})
			if err != nil {
				return err
			}
			if len(assigned) > 0 {
				return fmt.Errorf("member has assigned tasks; unassign or reassign first: %w", domain.ErrConflict)
			}
			if err := s.members.Remove(ctx, projectID, targetUserID); err != nil {
				if errors.Is(err, domain.ErrReference) {
					// Concurrent assignment between pre-check and delete.
					return fmt.Errorf("member has assigned tasks; unassign or reassign first: %w", domain.ErrConflict)
				}
				return err
			}
			return nil
		})
	})
}

// TransferOwnership atomically demotes the current owner to member and
// promotes an existing member to owner (owner-only, demote-before-promote,
// project row lock). Transferring to self is a no-op.
func (s *MemberService) TransferOwnership(ctx context.Context, actorID, projectID, newOwnerID domain.UUID) error {
	return withRetry(ctx, func(ctx context.Context) error {
		return s.tx.WithTx(ctx, func(ctx context.Context) error {
			if err := s.projects.LockForUpdate(ctx, projectID); err != nil {
				return err
			}
			actor, err := requireMembership(ctx, s.members, projectID, actorID)
			if err != nil {
				return err
			}
			if actor.Role != domain.RoleOwner {
				return fmt.Errorf("only the owner may transfer ownership: %w", ErrForbidden)
			}
			if newOwnerID == actorID {
				return nil
			}
			if _, err := s.members.Get(ctx, projectID, newOwnerID); err != nil {
				if errors.Is(err, domain.ErrNotFound) {
					return fmt.Errorf("new owner must be an existing project member: %w", domain.ErrNotFound)
				}
				return err
			}
			if err := s.members.UpdateRole(ctx, projectID, actorID, domain.RoleMember); err != nil {
				return err
			}
			return s.members.UpdateRole(ctx, projectID, newOwnerID, domain.RoleOwner)
		})
	})
}

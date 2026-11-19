package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/metamin44/go-task-management-api/internal/domain"
)

// ProjectService implements project CRUD with the documented role policy:
// create = any authenticated user; read = members; update = owner/admin;
// delete = owner only.
type ProjectService struct {
	projects domain.ProjectRepository
	members  domain.ProjectMemberRepository
	tx       domain.Transactor
}

// NewProjectService wires the project service.
func NewProjectService(projects domain.ProjectRepository, members domain.ProjectMemberRepository, tx domain.Transactor) *ProjectService {
	return &ProjectService{projects: projects, members: members, tx: tx}
}

// Create atomically creates the project and the creator's owner membership
// in one transaction (app-enforced invariant per finalized design).
func (s *ProjectService) Create(ctx context.Context, actorID domain.UUID, name string, description *string) (*domain.Project, error) {
	name = strings.TrimSpace(name)
	if err := domain.ValidateProjectName(name); err != nil {
		return nil, err
	}
	var p *domain.Project
	err := withRetry(ctx, func(ctx context.Context) error {
		return s.tx.WithTx(ctx, func(ctx context.Context) error {
			p = &domain.Project{
				ID:          domain.NewUUID(),
				Name:        name,
				Description: description,
				CreatedBy:   actorID,
			}
			if err := s.projects.Create(ctx, p); err != nil {
				return err
			}
			return s.members.Add(ctx, &domain.ProjectMember{
				ProjectID: p.ID,
				UserID:    actorID,
				Role:      domain.RoleOwner,
			})
		})
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

// Get returns the project; non-members get ErrForbidden.
func (s *ProjectService) Get(ctx context.Context, actorID, projectID domain.UUID) (*domain.Project, error) {
	if _, err := requireMembership(ctx, s.members, projectID, actorID); err != nil {
		return nil, err
	}
	return s.projects.GetByID(ctx, projectID)
}

// List returns projects where the actor is a member.
func (s *ProjectService) List(ctx context.Context, actorID domain.UUID) ([]domain.Project, error) {
	return s.projects.ListByMember(ctx, actorID)
}

// ProjectPatch carries optional project update fields. A nil Name means "no
// change". Description is applied only when DescriptionSet is true; a nil
// Description with DescriptionSet clears it (JSON null), preserving the
// omitted/null/value distinction from the HTTP layer.
type ProjectPatch struct {
	Name           *string
	Description    *string
	DescriptionSet bool
}

// Update applies a partial patch (owner/admin only). Merging happens on the
// freshest read so omitted fields keep their latest committed values; the
// repository's updated_at predicate rejects concurrent modifications.
func (s *ProjectService) Update(ctx context.Context, actorID, projectID domain.UUID, patch ProjectPatch) (*domain.Project, error) {
	m, err := requireMembership(ctx, s.members, projectID, actorID)
	if err != nil {
		return nil, err
	}
	if err := requireRole(m, domain.RoleOwner, domain.RoleAdmin); err != nil {
		return nil, err
	}
	p, err := s.projects.GetByID(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if patch.Name != nil {
		n := strings.TrimSpace(*patch.Name)
		if err := domain.ValidateProjectName(n); err != nil {
			return nil, fmt.Errorf("%w: %s", domain.ErrInvalid, err)
		}
		p.Name = n
	}
	if patch.DescriptionSet {
		p.Description = patch.Description // nil clears; string replaces
	}
	if err := s.projects.Update(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// Delete removes the project (owner only); memberships and tasks cascade.
func (s *ProjectService) Delete(ctx context.Context, actorID, projectID domain.UUID) error {
	m, err := requireMembership(ctx, s.members, projectID, actorID)
	if err != nil {
		return err
	}
	if err := requireRole(m, domain.RoleOwner); err != nil {
		return err
	}
	return s.projects.Delete(ctx, projectID)
}

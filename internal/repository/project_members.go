package repository

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metamin44/go-task-management-api/internal/domain"
)

// ProjectMemberRepository is the SQL-first implementation of
// domain.ProjectMemberRepository. The one-owner partial unique index and the
// composite assignee FK enforce integrity; the >=1-owner invariant and
// transfer ordering are service-layer responsibilities.
type ProjectMemberRepository struct{ pool *pgxpool.Pool }

var _ domain.ProjectMemberRepository = (*ProjectMemberRepository)(nil)

// NewProjectMemberRepository builds the repository over the given pool.
func NewProjectMemberRepository(pool *pgxpool.Pool) *ProjectMemberRepository {
	return &ProjectMemberRepository{pool: pool}
}

const memberCols = "project_id, user_id, role, joined_at"

func scanMember(s scanner) (*domain.ProjectMember, error) {
	var m domain.ProjectMember
	if err := s.Scan(&m.ProjectID, &m.UserID, &m.Role, &m.JoinedAt); err != nil {
		return nil, err
	}
	return &m, nil
}

// Add inserts a membership; DB fills joined_at.
func (r *ProjectMemberRepository) Add(ctx context.Context, m *domain.ProjectMember) error {
	q := querierFrom(ctx, r.pool)
	err := q.QueryRow(ctx,
		`INSERT INTO project_members (project_id, user_id, role)
		 VALUES ($1, $2, $3)
		 RETURNING joined_at`,
		m.ProjectID, m.UserID, string(m.Role)).Scan(&m.JoinedAt)
	return mapError("project_members: add", err)
}

// Get returns one membership or domain.ErrNotFound.
func (r *ProjectMemberRepository) Get(ctx context.Context, projectID, userID domain.UUID) (*domain.ProjectMember, error) {
	q := querierFrom(ctx, r.pool)
	m, err := scanMember(q.QueryRow(ctx,
		`SELECT `+memberCols+` FROM project_members WHERE project_id = $1 AND user_id = $2`,
		projectID, userID))
	if err != nil {
		return nil, mapError("project_members: get", err)
	}
	return m, nil
}

// GetOwner returns the owner membership (partial unique index guarantees at
// most one) or domain.ErrNotFound when the project has no owner.
func (r *ProjectMemberRepository) GetOwner(ctx context.Context, projectID domain.UUID) (*domain.ProjectMember, error) {
	q := querierFrom(ctx, r.pool)
	m, err := scanMember(q.QueryRow(ctx,
		`SELECT `+memberCols+` FROM project_members WHERE project_id = $1 AND role = 'owner'`,
		projectID))
	if err != nil {
		return nil, mapError("project_members: get owner", err)
	}
	return m, nil
}

// ListByProject lists all memberships of a project.
func (r *ProjectMemberRepository) ListByProject(ctx context.Context, projectID domain.UUID) ([]domain.ProjectMember, error) {
	q := querierFrom(ctx, r.pool)
	rows, err := q.Query(ctx,
		`SELECT `+memberCols+` FROM project_members WHERE project_id = $1 ORDER BY joined_at, user_id`,
		projectID)
	if err != nil {
		return nil, mapError("project_members: list by project", err)
	}
	defer rows.Close()

	members := make([]domain.ProjectMember, 0)
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, mapError("project_members: list by project scan", err)
		}
		members = append(members, *m)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError("project_members: list by project", err)
	}
	return members, nil
}

// UpdateRole changes a member's role. Ownership transfer = demote old owner
// then promote successor, in one transaction under the project row lock
// (service-orchestrated; demote-first avoids transient unique violations).
func (r *ProjectMemberRepository) UpdateRole(ctx context.Context, projectID, userID domain.UUID, role domain.Role) error {
	q := querierFrom(ctx, r.pool)
	tag, err := q.Exec(ctx,
		`UPDATE project_members SET role = $3 WHERE project_id = $1 AND user_id = $2`,
		projectID, userID, string(role))
	if err != nil {
		return mapError("project_members: update role", err)
	}
	return requireRows("project_members: update role", tag)
}

// Remove deletes a membership. Fails with domain.ErrReference (constraint
// tasks_project_assignee_fkey) while assigned tasks exist — unassign or
// reassign them first.
func (r *ProjectMemberRepository) Remove(ctx context.Context, projectID, userID domain.UUID) error {
	q := querierFrom(ctx, r.pool)
	tag, err := q.Exec(ctx,
		`DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`,
		projectID, userID)
	if err != nil {
		return mapError("project_members: remove", err)
	}
	return requireRows("project_members: remove", tag)
}

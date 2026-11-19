package repository

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metamin44/go-task-management-api/internal/domain"
)

// ProjectRepository is the SQL-first implementation of
// domain.ProjectRepository.
type ProjectRepository struct{ pool *pgxpool.Pool }

var _ domain.ProjectRepository = (*ProjectRepository)(nil)

// NewProjectRepository builds the repository over the given pool.
func NewProjectRepository(pool *pgxpool.Pool) *ProjectRepository {
	return &ProjectRepository{pool: pool}
}

const projectCols = "id, name, description, created_by, created_at, updated_at"

func scanProject(s scanner) (*domain.Project, error) {
	var p domain.Project
	if err := s.Scan(&p.ID, &p.Name, &p.Description, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	return &p, nil
}

// Create inserts a project; the service adds the owner membership in the
// same transaction (app-enforced atomicity per finalized design).
func (r *ProjectRepository) Create(ctx context.Context, p *domain.Project) error {
	q := querierFrom(ctx, r.pool)
	err := q.QueryRow(ctx,
		`INSERT INTO projects (id, name, description, created_by)
		 VALUES ($1, $2, $3, $4)
		 RETURNING created_at, updated_at`,
		p.ID, p.Name, p.Description, p.CreatedBy).Scan(&p.CreatedAt, &p.UpdatedAt)
	return mapError("projects: create", err)
}

// GetByID returns the project or domain.ErrNotFound.
func (r *ProjectRepository) GetByID(ctx context.Context, id domain.UUID) (*domain.Project, error) {
	q := querierFrom(ctx, r.pool)
	p, err := scanProject(q.QueryRow(ctx, `SELECT `+projectCols+` FROM projects WHERE id = $1`, id))
	if err != nil {
		return nil, mapError("projects: get by id", err)
	}
	return p, nil
}

// LockForUpdate takes the per-project row lock used to serialize membership
// mutations (owner transfer, member removal). Must run inside a transaction.
func (r *ProjectRepository) LockForUpdate(ctx context.Context, id domain.UUID) error {
	q := querierFrom(ctx, r.pool)
	var locked domain.UUID
	err := q.QueryRow(ctx, `SELECT id FROM projects WHERE id = $1 FOR UPDATE`, id).Scan(&locked)
	return mapError("projects: lock for update", err)
}

// ListByMember lists projects where userID holds a membership.
func (r *ProjectRepository) ListByMember(ctx context.Context, userID domain.UUID) ([]domain.Project, error) {
	q := querierFrom(ctx, r.pool)
	rows, err := q.Query(ctx,
		`SELECT p.id, p.name, p.description, p.created_by, p.created_at, p.updated_at
		 FROM projects p
		 JOIN project_members pm ON pm.project_id = p.id
		 WHERE pm.user_id = $1
		 ORDER BY p.created_at DESC, p.id`,
		userID)
	if err != nil {
		return nil, mapError("projects: list by member", err)
	}
	defer rows.Close()

	projects := make([]domain.Project, 0)
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, mapError("projects: list by member scan", err)
		}
		projects = append(projects, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError("projects: list by member", err)
	}
	return projects, nil
}

// Update persists name/description with optimistic concurrency on updated_at
// (see UserRepository.Update). created_by is immutable. A zero-row result is
// ErrNotFound (deleted) or ErrConflict (concurrently modified).
func (r *ProjectRepository) Update(ctx context.Context, p *domain.Project) error {
	q := querierFrom(ctx, r.pool)
	err := q.QueryRow(ctx,
		`UPDATE projects SET name = $2, description = $3,
		     updated_at = GREATEST(now(), updated_at + interval '1 microsecond')
		 WHERE id = $1 AND updated_at = $4
		 RETURNING updated_at`,
		p.ID, p.Name, p.Description, p.UpdatedAt).Scan(&p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return classifyUpdateMiss(ctx, q, "projects: update",
			`SELECT EXISTS (SELECT 1 FROM projects WHERE id = $1)`, p.ID)
	}
	return mapError("projects: update", err)
}

// Delete removes the project; memberships and tasks cascade via the DB.
func (r *ProjectRepository) Delete(ctx context.Context, id domain.UUID) error {
	q := querierFrom(ctx, r.pool)
	tag, err := q.Exec(ctx, `DELETE FROM projects WHERE id = $1`, id)
	if err != nil {
		return mapError("projects: delete", err)
	}
	return requireRows("projects: delete", tag)
}

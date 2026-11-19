package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metamin44/go-task-management-api/internal/domain"
)

// TaskRepository is the SQL-first implementation of domain.TaskRepository.
type TaskRepository struct{ pool *pgxpool.Pool }

var _ domain.TaskRepository = (*TaskRepository)(nil)

// NewTaskRepository builds the repository over the given pool.
func NewTaskRepository(pool *pgxpool.Pool) *TaskRepository { return &TaskRepository{pool: pool} }

const taskCols = "id, project_id, created_by, assigned_to, title, description, status, priority, due_at, created_at, updated_at"

func scanTask(s scanner) (*domain.Task, error) {
	var t domain.Task
	if err := s.Scan(&t.ID, &t.ProjectID, &t.CreatedBy, &t.AssignedTo, &t.Title,
		&t.Description, &t.Status, &t.Priority, &t.DueAt, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	return &t, nil
}

// Create inserts a task; the composite FK validates same-project membership
// of the assignee at statement time. DB fills timestamps.
func (r *TaskRepository) Create(ctx context.Context, t *domain.Task) error {
	q := querierFrom(ctx, r.pool)
	err := q.QueryRow(ctx,
		`INSERT INTO tasks (id, project_id, created_by, assigned_to, title, description, status, priority, due_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 RETURNING created_at, updated_at`,
		t.ID, t.ProjectID, t.CreatedBy, t.AssignedTo, t.Title, t.Description,
		string(t.Status), string(t.Priority), t.DueAt).Scan(&t.CreatedAt, &t.UpdatedAt)
	return mapError("tasks: create", err)
}

// GetByID returns the task or domain.ErrNotFound.
func (r *TaskRepository) GetByID(ctx context.Context, id domain.UUID) (*domain.Task, error) {
	q := querierFrom(ctx, r.pool)
	t, err := scanTask(q.QueryRow(ctx, `SELECT `+taskCols+` FROM tasks WHERE id = $1`, id))
	if err != nil {
		return nil, mapError("tasks: get by id", err)
	}
	return t, nil
}

// Update persists mutable fields (assignment included) with optimistic
// concurrency on updated_at (see UserRepository.Update). project_id and
// created_by are immutable. A zero-row result is ErrNotFound (deleted) or
// ErrConflict (concurrently modified).
func (r *TaskRepository) Update(ctx context.Context, t *domain.Task) error {
	q := querierFrom(ctx, r.pool)
	err := q.QueryRow(ctx,
		`UPDATE tasks
		 SET assigned_to = $2, title = $3, description = $4, status = $5,
		     priority = $6, due_at = $7,
		     updated_at = GREATEST(now(), updated_at + interval '1 microsecond')
		 WHERE id = $1 AND updated_at = $8
		 RETURNING updated_at`,
		t.ID, t.AssignedTo, t.Title, t.Description,
		string(t.Status), string(t.Priority), t.DueAt, t.UpdatedAt).Scan(&t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return classifyUpdateMiss(ctx, q, "tasks: update",
			`SELECT EXISTS (SELECT 1 FROM tasks WHERE id = $1)`, t.ID)
	}
	return mapError("tasks: update", err)
}

// Delete removes a task.
func (r *TaskRepository) Delete(ctx context.Context, id domain.UUID) error {
	q := querierFrom(ctx, r.pool)
	tag, err := q.Exec(ctx, `DELETE FROM tasks WHERE id = $1`, id)
	if err != nil {
		return mapError("tasks: delete", err)
	}
	return requireRows("tasks: delete", tag)
}

// List returns tasks matching the filter with deterministic ordering and
// parameterized pagination.
func (r *TaskRepository) List(ctx context.Context, f domain.TaskFilter) ([]domain.Task, error) {
	query, args, err := buildTaskListQuery(f)
	if err != nil {
		return nil, err
	}
	q := querierFrom(ctx, r.pool)
	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, mapError("tasks: list", err)
	}
	defer rows.Close()

	tasks := make([]domain.Task, 0)
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, mapError("tasks: list scan", err)
		}
		tasks = append(tasks, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError("tasks: list", err)
	}
	return tasks, nil
}

// UnassignMemberTasks bulk-unassigns a member's tasks within one project
// (required before member removal). updated_at advances strictly per row so a
// concurrent versioned PATCH cannot silently overwrite the unassignment.
func (r *TaskRepository) UnassignMemberTasks(ctx context.Context, projectID, userID domain.UUID) (int64, error) {
	q := querierFrom(ctx, r.pool)
	tag, err := q.Exec(ctx,
		`UPDATE tasks SET assigned_to = NULL,
		     updated_at = GREATEST(now(), updated_at + interval '1 microsecond')
		 WHERE project_id = $1 AND assigned_to = $2`,
		projectID, userID)
	if err != nil {
		return 0, mapError("tasks: unassign member tasks", err)
	}
	return tag.RowsAffected(), nil
}

// Pagination bounds (documented defaults; not schema decisions).
const (
	defaultTaskLimit = 50
	maxTaskLimit     = 200
)

// taskSortExprs whitelists sort fields. Status/priority use semantic CASE
// ranking (alphabetical order would be misleading); due_at sorts NULLS LAST.
var taskSortExprs = map[domain.TaskSortField]string{
	domain.TaskSortCreatedAt: "created_at",
	domain.TaskSortDueAt:     "due_at",
	domain.TaskSortPriority:  "CASE priority WHEN 'low' THEN 1 WHEN 'medium' THEN 2 WHEN 'high' THEN 3 WHEN 'urgent' THEN 4 END",
	domain.TaskSortStatus:    "CASE status WHEN 'todo' THEN 1 WHEN 'in_progress' THEN 2 WHEN 'done' THEN 3 END",
}

// buildTaskListQuery assembles the filtered, sorted, paginated SELECT with
// fully parameterized values. Sort fields come only from the whitelist;
// user input never reaches SQL text.
func buildTaskListQuery(f domain.TaskFilter) (string, []any, error) {
	sortBy := f.SortBy
	if sortBy == "" {
		sortBy = domain.TaskSortCreatedAt
	}
	expr, ok := taskSortExprs[sortBy]
	if !ok {
		return "", nil, fmt.Errorf("tasks: list sort field: %w", domain.ErrInvalid)
	}

	var conds []string
	var args []any
	add := func(cond string, val any) {
		args = append(args, val)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if f.ProjectID != nil {
		add("project_id = $%d", *f.ProjectID)
	}
	if f.AssignedTo != nil {
		add("assigned_to = $%d", *f.AssignedTo)
	}
	if f.Status != nil {
		add("status = $%d", string(*f.Status))
	}
	if f.Priority != nil {
		add("priority = $%d", string(*f.Priority))
	}
	if f.DueAfter != nil {
		add("due_at >= $%d", *f.DueAfter)
	}
	if f.DueBefore != nil {
		add("due_at <= $%d", *f.DueBefore)
	}

	limit := f.Limit
	if limit <= 0 {
		limit = defaultTaskLimit
	}
	if limit > maxTaskLimit {
		limit = maxTaskLimit
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	dir := "ASC"
	if f.SortDesc {
		dir = "DESC"
	}
	order := expr + " " + dir
	if sortBy == domain.TaskSortDueAt {
		order += " NULLS LAST"
	}
	order += ", id ASC" // stable tiebreaker for offset pagination

	args = append(args, limit, offset)
	var b strings.Builder
	b.WriteString("SELECT " + taskCols + " FROM tasks")
	if len(conds) > 0 {
		b.WriteString(" WHERE " + strings.Join(conds, " AND "))
	}
	fmt.Fprintf(&b, " ORDER BY %s LIMIT $%d OFFSET $%d", order, len(args)-1, len(args))
	return b.String(), args, nil
}

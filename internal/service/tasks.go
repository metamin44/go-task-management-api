package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/metamin44/go-task-management-api/internal/domain"
)

// TaskService implements task CRUD, assignment, and filtered listing.
// Access policy: all project members may create/list/view; update/delete is
// restricted to the task creator, current assignee, or project owner/admin
// (approved Phase 2 decision). The composite FK is the DB backstop for
// same-project assignment.
type TaskService struct {
	tasks   domain.TaskRepository
	members domain.ProjectMemberRepository
}

// NewTaskService wires the task service.
func NewTaskService(tasks domain.TaskRepository, members domain.ProjectMemberRepository) *TaskService {
	return &TaskService{tasks: tasks, members: members}
}

// TaskInput carries creation fields; zero Status/Priority default to
// todo/medium.
type TaskInput struct {
	Title       string
	Description *string
	Status      domain.Status
	Priority    domain.Priority
	AssignedTo  *domain.UUID
	DueAt       *time.Time
}

// Create adds a task to a project (creator must be a member; assignee, when
// set, must be a member of the same project).
func (s *TaskService) Create(ctx context.Context, actorID, projectID domain.UUID, in TaskInput) (*domain.Task, error) {
	if _, err := requireMembership(ctx, s.members, projectID, actorID); err != nil {
		return nil, err
	}
	if err := domain.ValidateTaskTitle(in.Title); err != nil {
		return nil, err
	}
	status := in.Status
	if status == "" {
		status = domain.StatusTodo
	}
	if !status.Valid() {
		return nil, fmt.Errorf("invalid status: %w", domain.ErrInvalid)
	}
	priority := in.Priority
	if priority == "" {
		priority = domain.PriorityMedium
	}
	if !priority.Valid() {
		return nil, fmt.Errorf("invalid priority: %w", domain.ErrInvalid)
	}
	if in.AssignedTo != nil {
		if _, err := s.members.Get(ctx, projectID, *in.AssignedTo); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return nil, fmt.Errorf("assignee must be a member of the same project: %w", domain.ErrInvalid)
			}
			return nil, err
		}
	}
	t := &domain.Task{
		ID:          domain.NewUUID(),
		ProjectID:   projectID,
		CreatedBy:   actorID,
		AssignedTo:  in.AssignedTo,
		Title:       in.Title,
		Description: in.Description,
		Status:      status,
		Priority:    priority,
		DueAt:       in.DueAt,
	}
	if err := s.tasks.Create(ctx, t); err != nil {
		return nil, err
	}
	return t, nil
}

// Get returns a task; only members of its project may view it.
func (s *TaskService) Get(ctx context.Context, actorID, taskID domain.UUID) (*domain.Task, error) {
	t, err := s.tasks.GetByID(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if _, err := requireMembership(ctx, s.members, t.ProjectID, actorID); err != nil {
		return nil, err
	}
	return t, nil
}

// List returns project tasks with filtering/pagination (members only). The
// project scope is forced regardless of the caller-supplied filter.
func (s *TaskService) List(ctx context.Context, actorID, projectID domain.UUID, f domain.TaskFilter) ([]domain.Task, error) {
	if _, err := requireMembership(ctx, s.members, projectID, actorID); err != nil {
		return nil, err
	}
	f.ProjectID = &projectID
	return s.tasks.List(ctx, f)
}

// ListMine returns the actor's assigned tasks across projects. An optional
// ProjectID filter still requires membership in that project.
func (s *TaskService) ListMine(ctx context.Context, actorID domain.UUID, f domain.TaskFilter) ([]domain.Task, error) {
	if f.ProjectID != nil {
		if _, err := requireMembership(ctx, s.members, *f.ProjectID, actorID); err != nil {
			return nil, err
		}
	}
	f.AssignedTo = &actorID
	return s.tasks.List(ctx, f)
}

// TaskPatch carries optional update fields. Nil means "no change".
// Unassign clears the assignee; ClearDueAt clears due_at. Conflicting
// combinations are rejected.
type TaskPatch struct {
	Title       *string
	Description *string
	Status      *domain.Status
	Priority    *domain.Priority
	AssignedTo  *domain.UUID
	Unassign    bool
	DueAt       *time.Time
	ClearDueAt  bool
}

// Update applies a patch under the write policy (creator/assignee/owner/
// admin). project_id and created_by are immutable.
func (s *TaskService) Update(ctx context.Context, actorID, taskID domain.UUID, patch TaskPatch) (*domain.Task, error) {
	t, err := s.tasks.GetByID(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeWrite(ctx, t, actorID); err != nil {
		return nil, err
	}
	if patch.Title != nil {
		if err := domain.ValidateTaskTitle(*patch.Title); err != nil {
			return nil, err
		}
		t.Title = *patch.Title
	}
	if patch.Description != nil {
		t.Description = patch.Description
	}
	if patch.Status != nil {
		if !patch.Status.Valid() {
			return nil, fmt.Errorf("invalid status: %w", domain.ErrInvalid)
		}
		t.Status = *patch.Status
	}
	if patch.Priority != nil {
		if !patch.Priority.Valid() {
			return nil, fmt.Errorf("invalid priority: %w", domain.ErrInvalid)
		}
		t.Priority = *patch.Priority
	}
	if patch.AssignedTo != nil && patch.Unassign {
		return nil, fmt.Errorf("cannot assign and unassign simultaneously: %w", domain.ErrInvalid)
	}
	if patch.Unassign {
		t.AssignedTo = nil
	} else if patch.AssignedTo != nil {
		if _, err := s.members.Get(ctx, t.ProjectID, *patch.AssignedTo); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return nil, fmt.Errorf("assignee must be a member of the same project: %w", domain.ErrInvalid)
			}
			return nil, err
		}
		t.AssignedTo = patch.AssignedTo
	}
	if patch.DueAt != nil && patch.ClearDueAt {
		return nil, fmt.Errorf("cannot set and clear due date simultaneously: %w", domain.ErrInvalid)
	}
	if patch.ClearDueAt {
		t.DueAt = nil
	} else if patch.DueAt != nil {
		t.DueAt = patch.DueAt
	}
	if err := s.tasks.Update(ctx, t); err != nil {
		return nil, err
	}
	return t, nil
}

// Delete removes a task under the same write policy as Update.
func (s *TaskService) Delete(ctx context.Context, actorID, taskID domain.UUID) error {
	t, err := s.tasks.GetByID(ctx, taskID)
	if err != nil {
		return err
	}
	if err := s.authorizeWrite(ctx, t, actorID); err != nil {
		return err
	}
	return s.tasks.Delete(ctx, taskID)
}

// authorizeWrite enforces creator/assignee/owner/admin (approved decision).
func (s *TaskService) authorizeWrite(ctx context.Context, t *domain.Task, actorID domain.UUID) error {
	m, err := requireMembership(ctx, s.members, t.ProjectID, actorID)
	if err != nil {
		return err
	}
	if m.Role == domain.RoleOwner || m.Role == domain.RoleAdmin ||
		t.CreatedBy == actorID || (t.AssignedTo != nil && *t.AssignedTo == actorID) {
		return nil
	}
	return fmt.Errorf("task write: %w", ErrForbidden)
}
